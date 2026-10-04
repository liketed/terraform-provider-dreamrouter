package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/liketed/dreamrouter-go/check"
	"github.com/liketed/dreamrouter-go/unifi"
)

var (
	_ resource.ResourceWithConfigure      = &wifiResource{}
	_ resource.ResourceWithImportState    = &wifiResource{}
	_ resource.ResourceWithValidateConfig = &wifiResource{}
)

// wifiNote is added as a warning to every change.
const wifiNote = "Wi-Fi devices on every Wi-Fi network disconnect for about 15-30 seconds while the access points apply this change."

type wifiResource struct {
	data *providerData
}

type wifiModel struct {
	ID        types.String `tfsdk:"id"`
	Name      types.String `tfsdk:"name"`
	Password  types.String `tfsdk:"password"`
	NetworkID types.String `tfsdk:"network_id"`
	Bands     types.List   `tfsdk:"bands"`
	Hidden    types.Bool   `tfsdk:"hidden"`
	Enabled   types.Bool   `tfsdk:"enabled"`
}

func newWiFiResource() resource.Resource { return &wifiResource{} }

func (r *wifiResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_wifi"
}

func (r *wifiResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A Wi-Fi network (SSID) secured with a password (WPA2/WPA3), broadcast by all access points, " +
			"whose devices join network_id. Every change makes the access points re-apply their settings, which " +
			"disconnects Wi-Fi devices on all Wi-Fi networks for about 15-30 seconds. The provider refuses to " +
			"disable or delete the Wi-Fi network the machine running Terraform is connected through.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "The Wi-Fi network's ID on the router.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "The network name (SSID), 1 to 32 bytes, used by no other Wi-Fi network.",
			},
			"password": schema.StringAttribute{
				Required:    true,
				Sensitive:   true,
				Description: "WPA password: 8 to 63 printable ASCII characters, or 64 hex digits. Changed in the web UI, it is set back.",
			},
			"network_id": schema.StringAttribute{
				Required:    true,
				Description: "ID of the network the devices join, e.g. dreamrouter_network.kids.id.",
			},
			"bands": schema.ListAttribute{
				Optional: true, Computed: true,
				ElementType: types.StringType,
				Default:     listdefault.StaticValue(listOf([]string{"2g", "5g"})),
				Description: `Bands to broadcast on: "2g", "5g", "6g". Defaults to ["2g", "5g"].`,
			},
			"hidden": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "Whether the name is hidden (not broadcast). Defaults to false.",
			},
			"enabled": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				Description: "Whether the Wi-Fi network is on. Defaults to true.",
			},
		},
	}
}

func (r *wifiResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *wifiResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg wifiModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if n := knownString(cfg.Name); n != nil && (*n == "" || len(*n) > 32) {
		resp.Diagnostics.AddError("Invalid Wi-Fi network name", fmt.Sprintf("a Wi-Fi network name must be 1 to 32 bytes, not %d", len(*n)))
	}
	if p := knownString(cfg.Password); p != nil {
		if err := check.WiFiPassword(*p); err != nil {
			resp.Diagnostics.AddError("Invalid Wi-Fi password", err.Error())
		}
	}
	if b, ok := stringList(cfg.Bands); ok {
		if err := check.WiFiBands(b); err != nil {
			resp.Diagnostics.AddError("Invalid bands", err.Error())
		}
	}
}

func wifiState(w unifi.WiFi) wifiModel {
	return wifiModel{ID: types.StringValue(w.ID), Name: types.StringValue(w.Name), Password: types.StringValue(w.Password),
		NetworkID: types.StringValue(w.NetworkID), Bands: listOf(w.Bands), Hidden: types.BoolValue(w.Hidden), Enabled: types.BoolValue(w.Enabled)}
}

// connectedVia returns the Wi-Fi network the machine running Terraform is
// connected through, if any.
func (r *wifiResource) connectedVia(ctx context.Context) string {
	active, err := r.data.client.ListActiveClients(ctx)
	if err != nil {
		return ""
	}
	macs, ips := localAddrs()
	for _, s := range active {
		for _, m := range macs {
			if strings.EqualFold(m, s.MAC) && s.ESSID != "" {
				return s.ESSID
			}
		}
		for _, ip := range ips {
			if ip == s.IP && s.ESSID != "" {
				return s.ESSID
			}
		}
	}
	return ""
}

func (r *wifiResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan wifiModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	bands, _ := stringList(plan.Bands)
	spec := unifi.WiFiSpec{Name: plan.Name.ValueString(), Password: plan.Password.ValueString(), NetworkID: plan.NetworkID.ValueString(),
		Bands: bands, Hidden: plan.Hidden.ValueBool(), Disabled: !plan.Enabled.ValueBool()}
	existing, err := r.data.client.ListWiFi(ctx)
	var nets []unifi.Network
	if err == nil {
		nets, err = r.data.client.ListNetworks(ctx)
	}
	if err == nil {
		err = check.NewWiFi(&spec, existing, nets)
	}
	var w unifi.WiFi
	if err == nil {
		w, err = r.data.client.CreateWiFi(ctx, spec)
	}
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error creating Wi-Fi network", err.Error())
		return
	}
	resp.Diagnostics.AddWarning("Wi-Fi briefly interrupted", wifiNote)
	resp.Diagnostics.Append(resp.State.Set(ctx, wifiState(w))...)
}

func (r *wifiResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state wifiModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	list, err := r.data.client.ListWiFi(ctx)
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error reading Wi-Fi networks", err.Error())
		return
	}
	for _, w := range list {
		if w.ID == state.ID.ValueString() {
			resp.Diagnostics.Append(resp.State.Set(ctx, wifiState(w))...)
			return
		}
	}
	resp.State.RemoveResource(ctx)
}

func (r *wifiResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state wifiModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	list, err := r.data.client.ListWiFi(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Error reading Wi-Fi networks", err.Error())
		return
	}
	for _, w := range list {
		if w.ID != state.ID.ValueString() && w.Name == plan.Name.ValueString() {
			resp.Diagnostics.AddError("Wi-Fi network name in use", fmt.Sprintf("a Wi-Fi network named %q already exists", w.Name))
			return
		}
	}
	if !plan.Enabled.ValueBool() && state.Enabled.ValueBool() && r.connectedVia(ctx) == state.Name.ValueString() {
		resp.Diagnostics.AddError("Refusing to disable this Wi-Fi network",
			fmt.Sprintf("The machine running Terraform is connected through %q; disabling it would cut it off.", state.Name.ValueString()))
		return
	}
	bands, _ := stringList(plan.Bands)
	fields := map[string]any{"name": plan.Name.ValueString(), "x_passphrase": plan.Password.ValueString(),
		"networkconf_id": plan.NetworkID.ValueString(), "wlan_bands": bands, "hide_ssid": plan.Hidden.ValueBool(), "enabled": plan.Enabled.ValueBool()}
	w, err := r.data.client.UpdateWiFi(ctx, state.ID.ValueString(), fields)
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error updating Wi-Fi network", err.Error())
		return
	}
	resp.Diagnostics.AddWarning("Wi-Fi briefly interrupted", wifiNote)
	resp.Diagnostics.Append(resp.State.Set(ctx, wifiState(w))...)
}

func (r *wifiResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state wifiModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.connectedVia(ctx) == state.Name.ValueString() {
		resp.Diagnostics.AddError("Refusing to delete this Wi-Fi network",
			fmt.Sprintf("The machine running Terraform is connected through %q; deleting it would cut it off.", state.Name.ValueString()))
		return
	}
	err := r.data.client.DeleteWiFi(ctx, state.ID.ValueString())
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil && !unifi.IsNotFound(err) {
		resp.Diagnostics.AddError("Error deleting Wi-Fi network", err.Error())
		return
	}
	resp.Diagnostics.AddWarning("Wi-Fi briefly interrupted", wifiNote)
}

// ImportState accepts the Wi-Fi network's name or ID.
func (r *wifiResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	list, err := r.data.client.ListWiFi(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Error reading Wi-Fi networks", err.Error())
		return
	}
	for _, w := range list {
		if w.ID == req.ID || w.Name == req.ID {
			resp.Diagnostics.Append(resp.State.Set(ctx, wifiState(w))...)
			return
		}
	}
	resp.Diagnostics.AddError("Wi-Fi network not found", fmt.Sprintf("No Wi-Fi network is named %q or has that ID.", req.ID))
}
