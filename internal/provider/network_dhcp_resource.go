package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"

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
	DNSServers types.List        `tfsdk:"dns_servers"`
	LeaseTime  types.Int64       `tfsdk:"lease_time"`
	NTPServers types.List        `tfsdk:"ntp_servers"`
	DomainName types.String      `tfsdk:"domain_name"`
}

// stringList reads a known list of strings; ok is false if it is null or unknown.
func stringList(l types.List) (out []string, ok bool) {
	if l.IsNull() || l.IsUnknown() {
		return nil, false
	}
	for _, e := range l.Elements() {
		if s, isStr := e.(types.String); isStr && !s.IsNull() && !s.IsUnknown() {
			out = append(out, s.ValueString())
		} else {
			return nil, false
		}
	}
	return out, true
}

func listOf(values []string) types.List {
	elems := make([]attr.Value, 0, len(values))
	for _, v := range values {
		elems = append(elems, types.StringValue(v))
	}
	return types.ListValueMust(types.StringType, elems)
}

func newNetworkDHCPResource() resource.Resource { return &networkDHCPResource{} }

func (r *networkDHCPResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_network_dhcp"
}

func (r *networkDHCPResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "DHCP settings of an existing network (Settings → Networks): network boot (PXE), the " +
			"TFTP server, and the DNS servers, lease time, NTP servers and domain name handed out. The resource " +
			"never creates or deletes networks. dns_servers, lease_time, ntp_servers and domain_name are only " +
			"managed when set; leave one out to leave it alone. Destroying the resource turns network boot off, " +
			"clears the boot server, stops handing out the TFTP server, and puts the managed DHCP options back to " +
			"the router's defaults. Manage each network from one resource only.",
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
			"dns_servers": schema.ListAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "DNS servers handed out, up to four IPv4 addresses. An empty list hands out the router " +
					"itself (the default). Leave it out to leave the setting alone.",
				Validators: []validator.List{listvalidator.SizeAtMost(4)},
			},
			"lease_time": schema.Int64Attribute{
				Optional: true,
				Description: "DHCP lease time in seconds, from 120 (2 minutes) to 31536000 (a year); the router's " +
					"default is 86400 (a day). Leave it out to leave the setting alone.",
				Validators: []validator.Int64{int64validator.Between(check.MinLeaseTime, check.MaxLeaseTime)},
			},
			"ntp_servers": schema.ListAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "NTP servers handed out (DHCP option 42), up to two IPv4 addresses. An empty list " +
					"hands out none (the default). Leave it out to leave the setting alone.",
				Validators: []validator.List{listvalidator.SizeAtMost(2)},
			},
			"domain_name": schema.StringAttribute{
				Optional: true,
				Description: `Domain name handed out as the search domain, e.g. "home.internal"; the router's ` +
					`default is "localdomain". Leave it out to leave the setting alone.`,
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
	if l, ok := stringList(cfg.DNSServers); ok {
		if err := check.DHCPDNS(l); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("dns_servers"), "Invalid DNS servers", err.Error())
		}
	}
	if l, ok := stringList(cfg.NTPServers); ok {
		if err := check.NTPServers(l); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("ntp_servers"), "Invalid NTP servers", err.Error())
		}
	}
	if d := knownString(cfg.DomainName); d != nil {
		if err := check.DomainName(*d); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("domain_name"), "Invalid domain name", err.Error())
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
	if dns, ok := stringList(plan.DNSServers); ok && strings.Join(dns, ",") != strings.Join(n.DNSServers(), ",") {
		for k, v := range unifi.DNSFields(dns) {
			f[k] = v
		}
	}
	if !plan.LeaseTime.IsNull() && !plan.LeaseTime.IsUnknown() && int64(n.Lease().Seconds()) != plan.LeaseTime.ValueInt64() {
		f["dhcpd_leasetime"] = plan.LeaseTime.ValueInt64()
	}
	if ntp, ok := stringList(plan.NTPServers); ok && strings.Join(ntp, ",") != strings.Join(n.NTPServers(), ",") {
		for k, v := range unifi.NTPFields(ntp) {
			f[k] = v
		}
	}
	if d := knownString(plan.DomainName); d != nil && *d != n.DomainName {
		f["domain_name"] = *d
	}
	return f
}

// stateFrom describes network n in the resource's terms, keeping the
// configured network name. DHCP options that prev doesn't manage stay null.
func stateFrom(n unifi.Network, network types.String, prev networkDHCPModel) networkDHCPModel {
	m := networkDHCPModel{ID: types.StringValue(n.ID), Network: network, TFTPServer: types.StringNull(),
		DNSServers: types.ListNull(types.StringType), LeaseTime: types.Int64Null(),
		NTPServers: types.ListNull(types.StringType), DomainName: types.StringNull()}
	if !prev.DNSServers.IsNull() {
		m.DNSServers = listOf(n.DNSServers())
	}
	if !prev.LeaseTime.IsNull() {
		m.LeaseTime = types.Int64Value(int64(n.Lease().Seconds()))
	}
	if !prev.NTPServers.IsNull() {
		m.NTPServers = listOf(n.NTPServers())
	}
	if !prev.DomainName.IsNull() {
		m.DomainName = types.StringValue(n.DomainName)
	}
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
	return stateFrom(n, plan.Network, plan), nil
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
			resp.Diagnostics.Append(resp.State.Set(ctx, stateFrom(n, state.Network, state))...)
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
	// Back to the defaults: no boot, no TFTP server, and the router's defaults
	// for the DHCP options this resource managed.
	reset := networkDHCPModel{TFTPServer: types.StringValue(""), DNSServers: types.ListNull(types.StringType),
		LeaseTime: types.Int64Null(), NTPServers: types.ListNull(types.StringType), DomainName: types.StringNull()}
	if !state.DNSServers.IsNull() {
		reset.DNSServers = listOf(nil)
	}
	if !state.LeaseTime.IsNull() {
		reset.LeaseTime = types.Int64Value(unifi.DefaultLeaseTime)
	}
	if !state.NTPServers.IsNull() {
		reset.NTPServers = listOf(nil)
	}
	if !state.DomainName.IsNull() {
		reset.DomainName = types.StringValue("localdomain")
	}
	fields := fieldsFor(n, reset)
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
