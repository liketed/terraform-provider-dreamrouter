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
	_ resource.ResourceWithConfigure   = &reservationResource{}
	_ resource.ResourceWithImportState = &reservationResource{}
)

type reservationResource struct {
	data *providerData
}

type reservationModel struct {
	ID              types.String `tfsdk:"id"`
	MAC             types.String `tfsdk:"mac"`
	IP              types.String `tfsdk:"ip"`
	Name            types.String `tfsdk:"name"`
	NetworkID       types.String `tfsdk:"network_id"`
	ForgetOnDestroy types.Bool   `tfsdk:"forget_on_destroy"`
}

func newReservationResource() resource.Resource { return &reservationResource{} }

func (r *reservationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dhcp_reservation"
}

func (r *reservationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A DHCP reservation: a fixed IP address for a device, identified by its MAC address. " +
			"To also give the device a DNS name, use dreamrouter_host instead.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "The device's client ID on the router.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"mac": schema.StringAttribute{
				Required:      true,
				Description:   "The device's MAC address, in lower-case colon form (aa:bb:cc:dd:ee:ff). Changing it replaces the reservation.",
				Validators:    []validator.String{macValidator{}},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"ip": schema.StringAttribute{
				Required:    true,
				Description: "The fixed IPv4 address. It must be in one of the router's networks, and not reserved for another device.",
				Validators:  []validator.String{ipv4Validator{}},
			},
			"name": schema.StringAttribute{
				Optional: true, Computed: true,
				Description: "The device name shown in the web UI. If not set, a device the router already knows " +
					"keeps its name.",
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
					"instead of only removing the reservation. Defaults to false.",
			},
		},
	}
}

func (r *reservationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func reservationRequest(m reservationModel) fixedIPRequest {
	req := fixedIPRequest{mac: m.MAC.ValueString(), ip: m.IP.ValueString()}
	if !m.Name.IsUnknown() {
		req.name = m.Name.ValueString()
	}
	if !m.NetworkID.IsUnknown() {
		req.networkID = m.NetworkID.ValueString()
	}
	return req
}

func reservationFromDevice(d unifi.ClientDevice, forget types.Bool) reservationModel {
	if forget.IsNull() || forget.IsUnknown() {
		forget = types.BoolValue(false)
	}
	return reservationModel{
		ID:              types.StringValue(d.ID),
		MAC:             types.StringValue(d.MAC),
		IP:              types.StringValue(d.FixedIP),
		Name:            types.StringValue(d.Name),
		NetworkID:       types.StringValue(d.NetworkID),
		ForgetOnDestroy: forget,
	}
}

func (r *reservationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan reservationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, ok := createFixedIP(ctx, r.data.client, reservationRequest(plan), "DHCP reservation", &resp.Diagnostics)
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if !ok {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, reservationFromDevice(d, plan.ForgetOnDestroy))...)
}

func (r *reservationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state reservationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, err := r.data.client.GetClient(ctx, state.ID.ValueString())
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if unifi.IsNotFound(err) || (err == nil && !d.UseFixedIP) {
		resp.State.RemoveResource(ctx) // removed outside Terraform; plan will recreate it
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading DHCP reservation", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, reservationFromDevice(d, state.ForgetOnDestroy))...)
}

func (r *reservationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state reservationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, ok := updateFixedIP(ctx, r.data.client, state.ID.ValueString(), reservationRequest(plan), "DHCP reservation", &resp.Diagnostics)
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if !ok {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, reservationFromDevice(d, plan.ForgetOnDestroy))...)
}

func (r *reservationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state reservationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	removeFixedIP(ctx, r.data.client, state.ID.ValueString(), state.MAC.ValueString(), state.ForgetOnDestroy.ValueBool(),
		"DHCP reservation", &resp.Diagnostics)
	r.data.warnIfLoginWaited(&resp.Diagnostics)
}

// ImportState accepts the device's MAC address (any common format) or its client ID.
func (r *reservationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	clients, err := r.data.client.ListClients(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Error listing devices", err.Error())
		return
	}
	mac, macErr := check.MAC(req.ID)
	for _, d := range clients {
		if d.ID == req.ID || (macErr == nil && d.MAC == mac) {
			if !d.UseFixedIP {
				resp.Diagnostics.AddError("No DHCP reservation", fmt.Sprintf("%s has no DHCP reservation to import.", describeDevice(&d)))
				return
			}
			resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), d.ID)...)
			resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("forget_on_destroy"), false)...)
			return
		}
	}
	resp.Diagnostics.AddError("Device not found", fmt.Sprintf("No device matches %q. Use the device's MAC address.", req.ID))
}
