package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/liketed/dreamrouter-go/check"
	"github.com/liketed/dreamrouter-go/unifi"
)

var (
	_ resource.ResourceWithConfigure      = &networkResource{}
	_ resource.ResourceWithImportState    = &networkResource{}
	_ resource.ResourceWithValidateConfig = &networkResource{}
)

// networkResource creates a VLAN network with its own subnet and DHCP.
type networkResource struct {
	data *providerData
}

type networkResModel struct {
	ID         types.String `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	VLAN       types.Int64  `tfsdk:"vlan"`
	Subnet     types.String `tfsdk:"subnet"`
	DHCPStart  types.String `tfsdk:"dhcp_start"`
	DHCPStop   types.String `tfsdk:"dhcp_stop"`
	DNSServers types.List   `tfsdk:"dns_servers"`
}

func newNetworkResource() resource.Resource { return &networkResource{} }

func (r *networkResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_network"
}

func (r *networkResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A network (Settings → Networks): a VLAN with its own subnet and DHCP, e.g. for children's or " +
			"guest devices. Attach Wi-Fi networks to it with dreamrouter_wifi. The provider checks what the router " +
			"doesn't: a unique name and a private subnet. Set the DNS servers here or with dreamrouter_network_dhcp, " +
			"not both.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "The network's ID on the router.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Name of the network, unique among networks (case-insensitive).",
			},
			"vlan": schema.Int64Attribute{
				Required:      true,
				Description:   "VLAN ID, 2 to 4094, not used by another network. Changing it replaces the network.",
				Validators:    []validator.Int64{int64validator.Between(2, 4094)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()},
			},
			"subnet": schema.StringAttribute{
				Required: true,
				Description: `The router's address on the network with the prefix, e.g. "192.168.30.1/24": a private ` +
					`range that overlaps no other network. Changing it replaces the network.`,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"dhcp_start": schema.StringAttribute{
				Optional: true, Computed: true,
				Description:   "First address handed out by DHCP. Defaults to the subnet's .6 (as in the web UI).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"dhcp_stop": schema.StringAttribute{
				Optional: true, Computed: true,
				Description:   "Last address handed out by DHCP. Defaults to the last address but one.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"dns_servers": schema.ListAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "DNS servers handed out, up to four IPv4 addresses; [] gives devices the router itself. " +
					"Left out, a new network starts with the router itself and the setting isn't managed here (e.g. " +
					"to manage it with dreamrouter_network_dhcp instead).",
				Validators: []validator.List{listvalidator.SizeAtMost(4)},
			},
		},
	}
}

func (r *networkResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	data, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("got %T", req.ProviderData))
		return
	}
	r.data = data
}

// ValidateConfig checks what can be checked without the router.
func (r *networkResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg networkResModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	spec, ok := cfg.spec()
	if !ok {
		return // something is unknown until apply
	}
	if err := check.NewNetwork(&spec, nil); err != nil {
		resp.Diagnostics.AddError("Invalid network", err.Error())
	}
}

// spec returns the configuration as a NetworkSpec; ok is false if a value
// is unknown. An unknown (not yet computed) DHCP range means the default.
func (m networkResModel) spec() (unifi.NetworkSpec, bool) {
	if m.Name.IsUnknown() || m.VLAN.IsUnknown() || m.Subnet.IsUnknown() || m.DNSServers.IsUnknown() {
		return unifi.NetworkSpec{}, false
	}
	dns, _ := stringList(m.DNSServers)
	return unifi.NetworkSpec{Name: m.Name.ValueString(), VLAN: int(m.VLAN.ValueInt64()), Subnet: m.Subnet.ValueString(),
		DHCPStart: m.DHCPStart.ValueString(), DHCPStop: m.DHCPStop.ValueString(), DNS: dns}, true
}

func networkResState(n unifi.Network, prev networkResModel) networkResModel {
	m := networkResModel{ID: types.StringValue(n.ID), Name: types.StringValue(n.Name), VLAN: types.Int64Value(int64(n.VLAN)),
		Subnet: types.StringValue(n.Subnet), DHCPStart: types.StringValue(n.DHCPStart), DHCPStop: types.StringValue(n.DHCPStop),
		DNSServers: types.ListNull(types.StringType)}
	if !prev.DNSServers.IsNull() {
		m.DNSServers = listOf(n.DNSServers())
	}
	return m
}

func (r *networkResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan networkResModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	spec, ok := plan.spec()
	if !ok {
		resp.Diagnostics.AddError("Error creating network", "a value is still unknown at apply time")
		return
	}
	existing, err := r.data.client.ListNetworks(ctx)
	if err == nil {
		err = check.NewNetwork(&spec, existing)
	}
	var n unifi.Network
	if err == nil {
		n, err = r.data.client.CreateNetwork(ctx, spec)
	}
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error creating network", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, networkResState(n, plan))...)
}

func (r *networkResource) find(ctx context.Context, id string) (unifi.Network, bool, error) {
	nets, err := r.data.client.ListNetworks(ctx)
	if err != nil {
		return unifi.Network{}, false, err
	}
	for _, n := range nets {
		if n.ID == id {
			return n, true, nil
		}
	}
	return unifi.Network{}, false, nil
}

func (r *networkResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state networkResModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	n, found, err := r.find(ctx, state.ID.ValueString())
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error reading networks", err.Error())
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, networkResState(n, state))...)
}

func (r *networkResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state networkResModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	nets, err := r.data.client.ListNetworks(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Error reading networks", err.Error())
		return
	}
	var others []unifi.Network
	for _, n := range nets {
		if n.ID != state.ID.ValueString() {
			others = append(others, n)
		}
	}
	spec, _ := plan.spec()
	if err := check.NewNetwork(&spec, others); err != nil {
		resp.Diagnostics.AddError("Invalid network", err.Error())
		return
	}
	fields := map[string]any{"name": spec.Name, "dhcpd_start": spec.DHCPStart, "dhcpd_stop": spec.DHCPStop}
	if !plan.DNSServers.IsNull() {
		for k, v := range unifi.DNSFields(spec.DNS) {
			fields[k] = v
		}
	}
	n, err := r.data.client.UpdateNetwork(ctx, state.ID.ValueString(), fields)
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error updating network", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, networkResState(n, plan))...)
}

func (r *networkResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state networkResModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	wifis, err := r.data.client.ListWiFi(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Error reading Wi-Fi networks", err.Error())
		return
	}
	var using []string
	for _, w := range wifis {
		if w.NetworkID == state.ID.ValueString() {
			using = append(using, w.Name)
		}
	}
	if len(using) > 0 {
		resp.Diagnostics.AddError("Network in use", fmt.Sprintf("Wi-Fi network %s uses network %s; delete or move it first.",
			strings.Join(using, ", "), state.Name.ValueString()))
		return
	}
	err = r.data.client.DeleteNetwork(ctx, state.ID.ValueString())
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil && !unifi.IsNotFound(err) {
		resp.Diagnostics.AddError("Error deleting network", err.Error())
	}
}

// ImportState accepts the network's name (case-insensitive) or ID; the
// router's own networks (e.g. Default) can't be imported.
func (r *networkResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	nets, err := r.data.client.ListNetworks(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Error reading networks", err.Error())
		return
	}
	for _, n := range nets {
		if n.ID == req.ID || strings.EqualFold(n.Name, req.ID) {
			if n.NoDelete || !n.VLANEnabled {
				resp.Diagnostics.AddError("Can't import this network", fmt.Sprintf("%s is the router's own network; manage its DHCP settings with dreamrouter_network_dhcp.", n.Name))
				return
			}
			resp.Diagnostics.Append(resp.State.Set(ctx, networkResState(n, networkResModel{DNSServers: types.ListNull(types.StringType)}))...)
			return
		}
	}
	resp.Diagnostics.AddError("Network not found", fmt.Sprintf("No network is named %q or has that ID.", req.ID))
}
