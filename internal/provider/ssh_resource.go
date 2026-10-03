package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/liketed/dreamrouter-go/unifi"
)

var (
	_ resource.ResourceWithConfigure      = &sshResource{}
	_ resource.ResourceWithImportState    = &sshResource{}
	_ resource.ResourceWithValidateConfig = &sshResource{}
)

// sshResource manages the router's two SSH settings. There is one per router.
type sshResource struct {
	data *providerData
}

type sshModel struct {
	ID      types.String `tfsdk:"id"`
	Router  types.Bool   `tfsdk:"router"`
	Devices types.Bool   `tfsdk:"devices"`
}

func newSSHResource() resource.Resource { return &sshResource{} }

func (r *sshResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ssh"
}

func (r *sshResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The router's SSH settings: SSH to the router itself, and SSH to adopted devices such as access " +
			"points. Leave an attribute out to leave that setting alone. There is one of these per router; " +
			"destroying the resource leaves SSH as it is. Passwords are not managed here: turning router SSH on " +
			"keeps the root password set before.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   `Always "ssh".`,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"router": schema.BoolAttribute{
				Optional:    true,
				Description: "Whether SSH to the router itself is on (UniFi OS: Control Plane → Console → SSH).",
			},
			"devices": schema.BoolAttribute{
				Optional: true,
				Description: "Whether SSH to adopted devices is on (Network: Device SSH Authentication). Every change " +
					"makes the router issue its devices a new internal API token, so it is only written when it changes.",
			},
		},
	}
}

func (r *sshResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *sshResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg sshModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if !resp.Diagnostics.HasError() && cfg.Router.IsNull() && cfg.Devices.IsNull() {
		resp.Diagnostics.AddError("Nothing to manage", `Set "router", "devices" or both.`)
	}
}

// apply changes the settings the plan manages, writing only what differs.
func (r *sshResource) apply(ctx context.Context, plan sshModel) (sshModel, error) {
	cur, err := r.data.client.GetSSH(ctx)
	if err != nil {
		return sshModel{}, err
	}
	if !plan.Router.IsNull() && plan.Router.ValueBool() != cur.Router {
		if err := r.data.client.SetRouterSSH(ctx, plan.Router.ValueBool()); err != nil {
			return sshModel{}, err
		}
	}
	if !plan.Devices.IsNull() && plan.Devices.ValueBool() != cur.Devices {
		if err := r.data.client.SetDevicesSSH(ctx, plan.Devices.ValueBool()); err != nil {
			return sshModel{}, err
		}
	}
	cur, err = r.data.client.GetSSH(ctx)
	if err != nil {
		return sshModel{}, err
	}
	return sshState(cur, plan), nil
}

// sshState describes the settings, keeping unmanaged attributes null.
func sshState(s unifi.SSHSettings, managed sshModel) sshModel {
	m := sshModel{ID: types.StringValue("ssh"), Router: types.BoolNull(), Devices: types.BoolNull()}
	if !managed.Router.IsNull() {
		m.Router = types.BoolValue(s.Router)
	}
	if !managed.Devices.IsNull() {
		m.Devices = types.BoolValue(s.Devices)
	}
	return m
}

func (r *sshResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan sshModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state, err := r.apply(ctx, plan)
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error setting SSH", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *sshResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state sshModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	cur, err := r.data.client.GetSSH(ctx)
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error reading SSH settings", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, sshState(cur, state))...)
}

func (r *sshResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan sshModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state, err := r.apply(ctx, plan)
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error setting SSH", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Delete leaves SSH as it is; the settings just stop being managed.
func (r *sshResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {}

// ImportState takes "ssh" and manages both settings.
func (r *sshResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if req.ID != "ssh" {
		resp.Diagnostics.AddError("Invalid import ID", `Import the SSH settings with the ID "ssh".`)
		return
	}
	cur, err := r.data.client.GetSSH(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Error reading SSH settings", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, sshModel{ID: types.StringValue("ssh"),
		Router: types.BoolValue(cur.Router), Devices: types.BoolValue(cur.Devices)})...)
}
