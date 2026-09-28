package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/liketed/dreamrouter-go/check"
	"github.com/liketed/dreamrouter-go/unifi"
)

var (
	_ resource.ResourceWithConfigure      = &hostResource{}
	_ resource.ResourceWithImportState    = &hostResource{}
	_ resource.ResourceWithValidateConfig = &hostResource{}
)

// A host is a device with a DHCP reservation and a DNS name. The router
// stores the name on the device (its local DNS record) and only serves it
// while the device has a fixed IP, so the two are managed together.
type hostResource struct {
	data *providerData
}

type hostModel struct {
	ID              types.String `tfsdk:"id"`
	Name            types.String `tfsdk:"name"`
	IP              types.String `tfsdk:"ip"`
	MAC             types.String `tfsdk:"mac"`
	DeviceName      types.String `tfsdk:"device_name"`
	NetworkID       types.String `tfsdk:"network_id"`
	ForgetOnDestroy types.Bool   `tfsdk:"forget_on_destroy"`
}

func newHostResource() resource.Resource { return &hostResource{} }

func (r *hostResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_host"
}

func (r *hostResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A device with a fixed IP address and a DNS name. The router serves the name like an A record, " +
			"but stores it on the device, and only while the device has a fixed IP, so both are managed together.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "The device's client ID on the router.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required: true,
				Description: "The device's DNS name, e.g. \"nas.home.internal\". It can't also be a static DNS " +
					"record or another device's name. Changing it renames the host in place.",
			},
			"ip": schema.StringAttribute{
				Required:    true,
				Description: "The fixed IPv4 address. It must be in one of the router's networks, and not reserved for another device.",
				Validators:  []validator.String{ipv4Validator{}},
			},
			"mac": schema.StringAttribute{
				Required:      true,
				Description:   "The device's MAC address, in lower-case colon form (aa:bb:cc:dd:ee:ff). Changing it replaces the host.",
				Validators:    []validator.String{macValidator{}},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"device_name": schema.StringAttribute{
				Optional: true, Computed: true,
				Description: "The device name shown in the web UI. If not set, a device the router already knows " +
					"keeps its name, and a new device is labelled with the host name.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"network_id": schema.StringAttribute{
				Optional: true, Computed: true,
				Description: "ID of the network the IP belongs to (see the dreamrouter_networks data source). " +
					"Defaults to the network whose subnet contains ip.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"forget_on_destroy": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "On destroy, remove the device from the router entirely (its name and history), " +
					"instead of only removing its fixed IP and DNS name. Defaults to false.",
			},
		},
	}
}

func (r *hostResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *hostResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg hostModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || cfg.Name.IsNull() || cfg.Name.IsUnknown() {
		return
	}
	name := cfg.Name.ValueString()
	if err := check.Name(name); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Invalid host name", err.Error())
	} else if check.NormalizeName(name) != name {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Invalid host name",
			fmt.Sprintf("write the name as %q, without surrounding spaces or a trailing dot", check.NormalizeName(name)))
	}
}

func hostRequest(m hostModel) fixedIPRequest {
	req := fixedIPRequest{mac: m.MAC.ValueString(), ip: m.IP.ValueString(), dnsName: m.Name.ValueString()}
	if !m.DeviceName.IsUnknown() {
		req.name = m.DeviceName.ValueString()
	}
	if !m.NetworkID.IsUnknown() {
		req.networkID = m.NetworkID.ValueString()
	}
	return req
}

func hostFromDevice(d unifi.ClientDevice, forget types.Bool) hostModel {
	if forget.IsNull() || forget.IsUnknown() {
		forget = types.BoolValue(false)
	}
	name := d.LocalDNSRecord
	if !d.LocalDNSRecordEnabled {
		name = "" // turned off outside Terraform: shows as drift, and apply turns it back on
	}
	return hostModel{
		ID:              types.StringValue(d.ID),
		Name:            types.StringValue(name),
		IP:              types.StringValue(d.FixedIP),
		MAC:             types.StringValue(d.MAC),
		DeviceName:      types.StringValue(d.Name),
		NetworkID:       types.StringValue(d.NetworkID),
		ForgetOnDestroy: forget,
	}
}

// checkNameFree fails if name is a static DNS record or another device's name.
func (r *hostResource) checkNameFree(ctx context.Context, name, mac string) error {
	records, err := r.data.client.ListDNS(ctx)
	if err != nil {
		return err
	}
	for _, rec := range records {
		if sameName(rec.Key, name) {
			return fmt.Errorf("%s is already a static DNS record (%s %s -> %s); remove that record "+
				"(dreamrouter_dns_record, drctl dns delete or the web UI) or choose another name", name, rec.RecordType, rec.Key, rec.Value)
		}
	}
	clients, err := r.data.client.ListClients(ctx)
	if err != nil {
		return err
	}
	for i := range clients {
		d := &clients[i]
		if d.MAC != mac && d.HasDNSName() && sameName(d.LocalDNSRecord, name) {
			return fmt.Errorf("%s is already the DNS name of %s (%s)", name, describeDevice(d), d.FixedIP)
		}
	}
	return nil
}

func (r *hostResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan hostModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.checkNameFree(ctx, plan.Name.ValueString(), plan.MAC.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Host name already in use", err.Error())
		return
	}
	d, ok := createFixedIP(ctx, r.data.client, hostRequest(plan), "host", &resp.Diagnostics)
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if !ok {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, hostFromDevice(d, plan.ForgetOnDestroy))...)
}

func (r *hostResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state hostModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, err := r.data.client.GetClient(ctx, state.ID.ValueString())
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if unifi.IsNotFound(err) || (err == nil && !d.UseFixedIP) {
		resp.State.RemoveResource(ctx) // device or reservation removed outside Terraform; plan will recreate it
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading host", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, hostFromDevice(d, state.ForgetOnDestroy))...)
}

func (r *hostResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state hostModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !sameName(plan.Name.ValueString(), state.Name.ValueString()) {
		if err := r.checkNameFree(ctx, plan.Name.ValueString(), plan.MAC.ValueString()); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("name"), "Host name already in use", err.Error())
			return
		}
	}
	d, ok := updateFixedIP(ctx, r.data.client, state.ID.ValueString(), hostRequest(plan), "host", &resp.Diagnostics)
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if !ok {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, hostFromDevice(d, plan.ForgetOnDestroy))...)
}

func (r *hostResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state hostModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	removeFixedIP(ctx, r.data.client, state.ID.ValueString(), state.MAC.ValueString(), state.ForgetOnDestroy.ValueBool(),
		"host", &resp.Diagnostics)
	r.data.warnIfLoginWaited(&resp.Diagnostics)
}

// ImportState accepts the host name, the device's MAC address or its client ID.
func (r *hostResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	clients, err := r.data.client.ListClients(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Error listing devices", err.Error())
		return
	}
	mac, macErr := check.MAC(req.ID)
	for _, d := range clients {
		if d.ID == req.ID || (macErr == nil && d.MAC == mac) || (d.HasDNSName() && sameName(d.LocalDNSRecord, req.ID)) {
			if !d.HasDNSName() {
				resp.Diagnostics.AddError("Not a host", fmt.Sprintf("%s has no fixed IP with a DNS name; "+
					"import a plain reservation as dreamrouter_dhcp_reservation instead.", describeDevice(&d)))
				return
			}
			resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), d.ID)...)
			resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("forget_on_destroy"), false)...)
			return
		}
	}
	resp.Diagnostics.AddError("Host not found", fmt.Sprintf("No host matches %q. Use the host name or the device's MAC address.", req.ID))
}
