package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/liketed/dreamrouter-go/check"
	"github.com/liketed/dreamrouter-go/unifi"
)

var (
	_ resource.ResourceWithConfigure      = &networkDHCPResource{}
	_ resource.ResourceWithImportState    = &networkDHCPResource{}
	_ resource.ResourceWithValidateConfig = &networkDHCPResource{}
)

// networkDHCPResource manages DHCP settings of an existing network: network
// boot (PXE) and the TFTP server (DHCP option 66). It never creates or
// deletes networks.
type networkDHCPResource struct {
	data *providerData
}

type networkBootModel struct {
	Server types.String `tfsdk:"server"`
	File   types.String `tfsdk:"file"`
}

type networkDHCPModel struct {
	ID         types.String      `tfsdk:"id"`
	Network    types.String      `tfsdk:"network"`
	Boot       *networkBootModel `tfsdk:"boot"`
	TFTPServer types.String      `tfsdk:"tftp_server"`
}

func newNetworkDHCPResource() resource.Resource { return &networkDHCPResource{} }

func (r *networkDHCPResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_network_dhcp"
}

func (r *networkDHCPResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "DHCP settings of an existing network (Settings → Networks): network boot (PXE) and the " +
			"TFTP server. The resource never creates or deletes networks; destroying it turns network boot " +
			"off, clears the boot server and stops handing out the TFTP server. Manage each network from one resource only.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "The network's ID on the router.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"network": schema.StringAttribute{
				Required:      true,
				Description:   "Name of the network, e.g. \"Default\" (case-insensitive). Changing it replaces the resource.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"boot": schema.SingleNestedAttribute{
				Optional: true,
				Description: "Network boot (PXE): which server and file a network-booting machine should use. " +
					"Omit it to keep network boot off. When turned off, the router keeps the last server and file " +
					"stored (it doesn't allow clearing a boot file), as the web UI does.",
				Attributes: map[string]schema.Attribute{
					"server": schema.StringAttribute{
						Required:    true,
						Description: "IPv4 address of the boot server.",
					},
					"file": schema.StringAttribute{
						Required:    true,
						Description: "File the machine should load, e.g. \"netboot.xyz.efi\"; may include a path. No spaces or commas.",
					},
				},
			},
			"tftp_server": schema.StringAttribute{
				Optional: true,
				Description: "TFTP server host name or IP address handed out as DHCP option 66, e.g. for IP phones. " +
					"Independent of boot: it is handed out whenever set. Omit it to hand out none.",
			},
		},
	}
}

func (r *networkDHCPResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *networkDHCPResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg networkDHCPModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if b := cfg.Boot; b != nil {
		if s := knownString(b.Server); s != nil {
			if _, err := check.IPv4(*s); err != nil {
				resp.Diagnostics.AddAttributeError(path.Root("boot").AtName("server"), "Invalid boot server",
					fmt.Sprintf("boot server %q must be an IPv4 address", *s))
			}
		}
		if f := knownString(b.File); f != nil {
			if err := check.Boot("0.0.0.0", *f); err != nil {
				resp.Diagnostics.AddAttributeError(path.Root("boot").AtName("file"), "Invalid boot file", err.Error())
			}
		}
	}
	if t := knownString(cfg.TFTPServer); t != nil {
		if err := check.TFTPServer(*t); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("tftp_server"), "Invalid TFTP server", err.Error())
		}
	}
}

// findNetwork looks a network up by name (case-insensitive) or ID.
func (r *networkDHCPResource) findNetwork(ctx context.Context, nameOrID string) (unifi.Network, error) {
	networks, err := r.data.client.ListNetworks(ctx)
	if err != nil {
		return unifi.Network{}, err
	}
	var lans []unifi.Network
	for _, n := range networks {
		if n.Subnet == "" {
			continue
		}
		lans = append(lans, n)
		if strings.EqualFold(n.Name, nameOrID) || n.ID == nameOrID {
			return n, nil
		}
	}
	return unifi.Network{}, fmt.Errorf("the router has no network named %q (networks: %s)", nameOrID, check.NetworkNames(lans))
}

// fieldsFor returns the API fields that make network n match the plan.
func fieldsFor(n unifi.Network, plan networkDHCPModel) map[string]any {
	f := map[string]any{}
	if plan.Boot != nil {
		server, file := plan.Boot.Server.ValueString(), plan.Boot.File.ValueString()
		if !n.BootEnabled {
			f["dhcpd_boot_enabled"] = true
		}
		if n.BootServer != server {
			f["dhcpd_boot_server"] = server
		}
		if n.BootFilename != file {
			f["dhcpd_boot_filename"] = file
		}
	} else if n.BootEnabled {
		f["dhcpd_boot_enabled"] = false
	}
	if tftp := plan.TFTPServer.ValueString(); n.TFTPServer != tftp {
		f["dhcpd_tftp_server"] = tftp
	}
	return f
}

// stateFrom describes network n in the resource's terms, keeping the
// configured network name.
func stateFrom(n unifi.Network, network types.String) networkDHCPModel {
	m := networkDHCPModel{ID: types.StringValue(n.ID), Network: network, TFTPServer: types.StringNull()}
	if n.BootEnabled {
		m.Boot = &networkBootModel{Server: types.StringValue(n.BootServer), File: types.StringValue(n.BootFilename)}
	}
	if n.TFTPServer != "" {
		m.TFTPServer = types.StringValue(n.TFTPServer)
	}
	return m
}

func (r *networkDHCPResource) apply(ctx context.Context, plan networkDHCPModel) (networkDHCPModel, error) {
	n, err := r.findNetwork(ctx, plan.Network.ValueString())
	if err != nil {
		return networkDHCPModel{}, err
	}
	if f := fieldsFor(n, plan); len(f) > 0 {
		if n, err = r.data.client.UpdateNetwork(ctx, n.ID, f); err != nil {
			return networkDHCPModel{}, err
		}
	}
	return stateFrom(n, plan.Network), nil
}

func (r *networkDHCPResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan networkDHCPModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state, err := r.apply(ctx, plan)
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error setting network DHCP settings", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *networkDHCPResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state networkDHCPModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	networks, err := r.data.client.ListNetworks(ctx)
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error reading networks", err.Error())
		return
	}
	for _, n := range networks {
		if n.ID == state.ID.ValueString() {
			resp.Diagnostics.Append(resp.State.Set(ctx, stateFrom(n, state.Network))...)
			return
		}
	}
	resp.State.RemoveResource(ctx) // the network was deleted outside Terraform
}

func (r *networkDHCPResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan networkDHCPModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state, err := r.apply(ctx, plan)
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error updating network DHCP settings", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Delete turns network boot off, clears the boot server and stops handing
// out the TFTP server. (The router doesn't allow clearing a stored boot file,
// so that stays, inactive.) The network itself is left alone.
func (r *networkDHCPResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state networkDHCPModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	n, err := r.findNetwork(ctx, state.ID.ValueString())
	if err != nil {
		r.data.warnIfLoginWaited(&resp.Diagnostics)
		if !strings.Contains(err.Error(), "has no network") {
			resp.Diagnostics.AddError("Error resetting network DHCP settings", err.Error())
		}
		return // the network is gone; nothing to reset
	}
	fields := fieldsFor(n, networkDHCPModel{TFTPServer: types.StringValue("")})
	if n.BootServer != "" {
		fields["dhcpd_boot_server"] = ""
	}
	if len(fields) > 0 {
		_, err = r.data.client.UpdateNetwork(ctx, n.ID, fields)
	}
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error resetting network DHCP settings", err.Error())
	}
}

// ImportState accepts the network's name or ID.
func (r *networkDHCPResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	n, err := r.findNetwork(ctx, req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Network not found", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), n.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("network"), n.Name)...)
}
