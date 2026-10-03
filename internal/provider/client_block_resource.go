package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/liketed/dreamrouter-go/check"
	"github.com/liketed/dreamrouter-go/unifi"
)

var (
	_ resource.ResourceWithConfigure   = &clientBlockResource{}
	_ resource.ResourceWithImportState = &clientBlockResource{}
)

// clientBlockResource blocks one device by MAC address.
type clientBlockResource struct {
	data *providerData
}

type clientBlockModel struct {
	ID  types.String `tfsdk:"id"`
	MAC types.String `tfsdk:"mac"`
}

func newClientBlockResource() resource.Resource { return &clientBlockResource{} }

func (r *clientBlockResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_client_block"
}

func (r *clientBlockResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Blocks a device (client) by MAC address: it is disconnected and can't connect again, on Wi-Fi " +
			"or wired ports, until unblocked. Destroying the resource unblocks it. The MAC address may be one the " +
			"router doesn't know yet (a device that has never connected), to block it before it joins; the router " +
			"then creates a record for it, which is removed again on destroy. The provider refuses to block the " +
			"machine Terraform runs on. If the device is unblocked outside Terraform, the next plan blocks it again.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "The device's MAC address.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"mac": schema.StringAttribute{
				Required:      true,
				Description:   "MAC address of the device to block, in lower-case colon form. Changing it replaces the resource.",
				Validators:    []validator.String{macValidator{}},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
		},
	}
}

func (r *clientBlockResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// find returns what the router knows about the device; found is false if
// it has no record of the MAC address.
func (r *clientBlockResource) find(ctx context.Context, mac string) (ci clientInfo, found bool, err error) {
	clients, err := loadClients(ctx, r.data.client, 30*24, true)
	if err != nil {
		return clientInfo{}, false, err
	}
	for _, c := range clients {
		if c.mac() == mac {
			return c, true, nil
		}
	}
	return clientInfo{}, false, nil
}

func (r *clientBlockResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan clientBlockModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	mac := plan.MAC.ValueString()
	ci, found, err := r.find(ctx, mac)
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error reading devices", err.Error())
		return
	}
	if !found {
		ci = clientInfo{status: &unifi.ClientStatus{MAC: mac}}
		resp.Diagnostics.AddAttributeWarning(path.Root("mac"), "Unknown device",
			fmt.Sprintf("The router doesn't know a device with MAC %s (it has never connected, or was forgotten). "+
				"Blocking it anyway, so it can't connect when it appears.", mac))
	}
	if ci.isLocal() {
		resp.Diagnostics.AddAttributeError(path.Root("mac"), "Refusing to block this machine",
			fmt.Sprintf("%s is the machine Terraform is running on; blocking it would cut it off from the network.", mac))
		return
	}
	if !found || !ci.blocked() {
		if err := r.data.client.BlockClient(ctx, mac); err != nil {
			resp.Diagnostics.AddError("Error blocking device", err.Error())
			return
		}
	}
	plan.ID = types.StringValue(mac)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *clientBlockResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state clientBlockModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ci, found, err := r.find(ctx, state.MAC.ValueString())
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error reading devices", err.Error())
		return
	}
	if !found || !ci.blocked() {
		resp.State.RemoveResource(ctx) // unblocked or forgotten outside Terraform: block again
	}
}

// Update is never called: the only attribute, mac, forces replacement.
func (r *clientBlockResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan clientBlockModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// Delete unblocks the device. A record that only exists because of the
// block (a device that never connected, with no name, note or reservation,
// as the router creates when blocking an unknown MAC) is removed too.
func (r *clientBlockResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state clientBlockModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	mac := state.MAC.ValueString()
	ci, found, err := r.find(ctx, mac)
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error reading devices", err.Error())
		return
	}
	if !found {
		return // forgotten outside Terraform; nothing is blocked
	}
	if ci.blocked() {
		if err := r.data.client.UnblockClient(ctx, mac); err != nil {
			resp.Diagnostics.AddError("Error unblocking device", err.Error())
			return
		}
	}
	if d := ci.device; d != nil && ci.status == nil && d.Name == "" && d.Note == "" && !d.UseFixedIP &&
		d.Hostname == "" && d.FirstSeen == 0 && d.LastSeen == 0 {
		if err := r.data.client.ForgetClient(ctx, mac); err != nil {
			resp.Diagnostics.AddWarning("Device unblocked, but its empty record was not removed", err.Error())
		}
	}
}

// ImportState accepts the MAC address (any common format) of a blocked device.
func (r *clientBlockResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	mac, err := check.MAC(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid MAC address", err.Error())
		return
	}
	ci, found, err := r.find(ctx, mac)
	if err != nil {
		resp.Diagnostics.AddError("Error reading devices", err.Error())
		return
	}
	if !found || !ci.blocked() {
		resp.Diagnostics.AddError("Device not blocked", fmt.Sprintf("No blocked device has MAC %s; only blocked devices can be imported.", mac))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), mac)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("mac"), mac)...)
}
