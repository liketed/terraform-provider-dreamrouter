package provider

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
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
	_ resource.ResourceWithConfigure      = &wanResource{}
	_ resource.ResourceWithImportState    = &wanResource{}
	_ resource.ResourceWithValidateConfig = &wanResource{}
)

// wanWait is how long the provider lets the router apply a change before
// watching (right after a change the router still reports the old state),
// how often it then looks, and how long it watches a connection before
// putting the previous settings back. Replaced in tests.
var wanWait = struct{ settle, interval, timeout time.Duration }{20 * time.Second, 5 * time.Second, 2 * time.Minute}

// isRouterName tells whether a client's uplink is the router itself. Replaced in tests.
var isRouterName = func(name string) bool { return strings.Contains(name, "Dream Router") || strings.Contains(name, "UDR") }

type wanResource struct {
	data *providerData
}

type wanModel struct {
	ID         types.String `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	Type       types.String `tfsdk:"type"`
	Username   types.String `tfsdk:"username"`
	Password   types.String `tfsdk:"password"`
	VLAN       types.Int64  `tfsdk:"vlan"`
	DNSServers types.List   `tfsdk:"dns_servers"`
	IP         types.String `tfsdk:"ip"`
	Gateway    types.String `tfsdk:"gateway"`
	Port       types.String `tfsdk:"port"`
	ForcePort  types.Bool   `tfsdk:"force_port"`
}

func newWANResource() resource.Resource { return &wanResource{} }

func (r *wanResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_wan"
}

func (r *wanResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The settings of an existing internet connection (Settings → Internet), e.g. \"Internet 1\" or " +
			"\"Internet 2\": PPPoE login, VLAN, DHCP or static, DNS, and optionally the port it uses. Changing a working " +
			"connection makes it reconnect; the provider then watches it for up to two minutes and puts the previous " +
			"settings (or ports) back if it doesn't come back. Review plans for the primary connection carefully: it " +
			"carries your internet. Connections can't be deleted, so destroying this resource leaves the connection as it is.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "The connection's ID on the router.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:      true,
				Description:   `Name of the connection, e.g. "Internet 2" (case-insensitive). Changing it manages another connection.`,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"type": schema.StringAttribute{
				Required:    true,
				Description: `"pppoe", "dhcp" or "static".`,
				Validators:  []validator.String{stringvalidator.OneOf("pppoe", "dhcp", "static")},
			},
			"username": schema.StringAttribute{
				Optional:    true,
				Description: "PPPoE username; required for pppoe.",
			},
			"password": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "PPPoE password; required for pppoe. Changed in the web UI, it is set back.",
			},
			"vlan": schema.Int64Attribute{
				Optional:    true,
				Description: "VLAN ID the provider uses, 1 to 4094 (e.g. 10 on Irish fibre lines). Left out: no VLAN.",
				Validators:  []validator.Int64{int64validator.Between(1, 4094)},
			},
			"dns_servers": schema.ListAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "DNS servers the router itself uses, up to two IPv4 addresses. Left out: the provider's.",
				Validators:  []validator.List{listvalidator.SizeBetween(1, 2)},
			},
			"ip": schema.StringAttribute{
				Optional:    true,
				Description: `Static address with prefix, e.g. "203.0.113.10/24"; required for static.`,
			},
			"gateway": schema.StringAttribute{
				Optional:    true,
				Description: "Static gateway; required for static.",
			},
			"port": schema.StringAttribute{
				Optional: true,
				Description: `The port the connection uses: its name ("Port 3", "SFP+ 1"), number ("3") or interface ("eth2"). ` +
					`The port it leaves becomes a LAN port. Left out: not managed.`,
			},
			"force_port": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "Move to port even if LAN devices are connected to it (they lose their connection).",
			},
		},
	}
}

func (r *wanResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// apply puts the plan's settings onto WAN w (keeping its ID and fields the
// plan doesn't cover) and returns the result.
func (m wanModel) apply(w unifi.WAN) (unifi.WAN, error) {
	w.Type = m.Type.ValueString()
	w.Username, w.Password = m.Username.ValueString(), m.Password.ValueString()
	w.VLANEnabled, w.VLAN = !m.VLAN.IsNull(), unifi.VLANID(m.VLAN.ValueInt64())
	if dns, ok := stringList(m.DNSServers); ok && len(dns) > 0 {
		w.DNSPreference, w.DNS1, w.DNS2 = "manual", dns[0], ""
		if len(dns) > 1 {
			w.DNS2 = dns[1]
		}
	} else {
		w.DNSPreference, w.DNS1, w.DNS2 = "auto", "", ""
	}
	if w.Type == "static" {
		p, err := netip.ParsePrefix(m.IP.ValueString())
		if err != nil || !p.Addr().Is4() {
			return w, fmt.Errorf("ip %q must be an address with a prefix, e.g. 203.0.113.10/24", m.IP.ValueString())
		}
		w.IP, w.Netmask, w.Gateway = p.Addr().String(), netmaskOf(p.Bits()), m.Gateway.ValueString()
	}
	return w, check.WAN(&w)
}

func netmaskOf(bits int) string {
	var b [4]byte
	for i := 0; i < bits; i++ {
		b[i/8] |= 1 << (7 - uint(i%8))
	}
	return netip.AddrFrom4(b).String()
}

func (r *wanResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg wanModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || cfg.Type.IsUnknown() {
		return
	}
	switch cfg.Type.ValueString() {
	case "pppoe":
		if cfg.Username.IsNull() || cfg.Password.IsNull() {
			resp.Diagnostics.AddError("Missing PPPoE login", `type = "pppoe" needs username and password.`)
		}
	case "static":
		if cfg.IP.IsNull() || cfg.Gateway.IsNull() {
			resp.Diagnostics.AddError("Missing static address", `type = "static" needs ip and gateway.`)
		}
	}
	if cfg.Username.IsUnknown() || cfg.Password.IsUnknown() || cfg.IP.IsUnknown() || cfg.Gateway.IsUnknown() || cfg.DNSServers.IsUnknown() || resp.Diagnostics.HasError() {
		return
	}
	if _, err := cfg.apply(unifi.WAN{}); err != nil {
		resp.Diagnostics.AddError("Invalid internet connection settings", err.Error())
	}
}

// wanState describes a connection in the resource's terms.
func wanState(w unifi.WAN, ports []unifi.Port, prev wanModel) wanModel {
	m := wanModel{ID: types.StringValue(w.ID), Name: prev.Name, Type: types.StringValue(w.Type),
		Username: types.StringNull(), Password: types.StringNull(), VLAN: types.Int64Null(), DNSServers: types.ListNull(types.StringType),
		IP: types.StringNull(), Gateway: types.StringNull(), Port: types.StringNull(), ForcePort: prev.ForcePort}
	if m.ForcePort.IsNull() || m.ForcePort.IsUnknown() {
		m.ForcePort = types.BoolValue(false)
	}
	if w.Type == "pppoe" {
		m.Username, m.Password = types.StringValue(w.Username), types.StringValue(w.Password)
	}
	if w.VLANEnabled {
		m.VLAN = types.Int64Value(int64(w.VLAN))
	}
	if w.DNSPreference == "manual" {
		var dns []string
		for _, d := range []string{w.DNS1, w.DNS2} {
			if d != "" {
				dns = append(dns, d)
			}
		}
		m.DNSServers = listOf(dns)
	}
	if w.Type == "static" {
		if mask, err := netip.ParseAddr(w.Netmask); err == nil {
			bits := 0
			for _, b := range mask.As4() {
				for i := 7; i >= 0; i-- {
					if b&(1<<i) != 0 {
						bits++
					}
				}
			}
			m.IP = types.StringValue(fmt.Sprintf("%s/%d", w.IP, bits))
		}
		m.Gateway = types.StringValue(w.Gateway)
	}
	if !prev.Port.IsNull() {
		m.Port = prev.Port
		for _, p := range ports {
			if p.Role == w.NetworkGroup && !portMatches(p, prev.Port.ValueString()) {
				m.Port = types.StringValue(p.Name) // moved outside Terraform
			}
		}
	}
	return m
}

func portMatches(p unifi.Port, key string) bool {
	return strings.EqualFold(p.Name, key) || p.Interface == key || fmt.Sprint(p.Index) == key
}

func (r *wanResource) find(ctx context.Context, name string) (unifi.WAN, []unifi.Port, []unifi.WANLink, error) {
	wans, err := r.data.client.ListWANs(ctx)
	if err != nil {
		return unifi.WAN{}, nil, nil, err
	}
	ports, links, err := r.data.client.Ports(ctx)
	if err != nil {
		return unifi.WAN{}, nil, nil, err
	}
	var names []string
	for _, w := range wans {
		if strings.EqualFold(w.Name, name) || w.ID == name {
			return w, ports, links, nil
		}
		names = append(names, w.Name)
	}
	return unifi.WAN{}, nil, nil, fmt.Errorf("the router has no internet connection named %q (connections: %s)", name, strings.Join(names, ", "))
}

func linkUp(links []unifi.WANLink, group string) bool {
	for _, l := range links {
		if l.NetworkGroup == group && l.Up {
			return true
		}
	}
	return false
}

// waitUp lets the router apply a change, then waits until the connection
// for group is up.
func (r *wanResource) waitUp(ctx context.Context, group string) bool {
	deadline := time.Now().Add(wanWait.timeout)
	select {
	case <-ctx.Done():
		return false
	case <-time.After(wanWait.settle):
	}
	for {
		if _, links, err := r.data.client.Ports(ctx); err == nil {
			for _, l := range links {
				if l.NetworkGroup == group && l.Up && l.IP != "" {
					return true
				}
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(wanWait.interval):
		}
	}
}

// fieldsFor returns the update that turns from into to.
func wanFields(from, to unifi.WAN) map[string]any {
	f := map[string]any{}
	if from.Type != to.Type {
		f["wan_type"] = to.Type
	}
	if to.Type == "pppoe" && (from.Username != to.Username || from.Password != to.Password) {
		f["wan_username"], f["x_wan_password"] = to.Username, to.Password
	}
	if to.Type == "static" && (from.IP != to.IP || from.Netmask != to.Netmask || from.Gateway != to.Gateway) {
		f["wan_ip"], f["wan_netmask"], f["wan_gateway"] = to.IP, to.Netmask, to.Gateway
	}
	if from.VLANEnabled != to.VLANEnabled || (to.VLANEnabled && from.VLAN != to.VLAN) {
		f["wan_vlan_enabled"] = to.VLANEnabled
		if to.VLANEnabled {
			f["wan_vlan"] = int(to.VLAN)
		}
	}
	if from.DNSPreference != to.DNSPreference || from.DNS1 != to.DNS1 || from.DNS2 != to.DNS2 {
		f["wan_dns_preference"], f["wan_dns1"], f["wan_dns2"] = to.DNSPreference, to.DNS1, to.DNS2
	}
	return f
}

// set applies the plan: settings first, then the port, each watched and
// rolled back if a connection that was up doesn't come back.
func (r *wanResource) set(ctx context.Context, plan wanModel) (wanModel, error) {
	old, ports, links, err := r.find(ctx, plan.Name.ValueString())
	if err != nil {
		return wanModel{}, err
	}
	want, err := plan.apply(old)
	if err != nil {
		return wanModel{}, err
	}
	if f := wanFields(old, want); len(f) > 0 {
		wasUp := linkUp(links, old.NetworkGroup)
		if _, err := r.data.client.UpdateWAN(ctx, old.ID, f); err != nil {
			return wanModel{}, err
		}
		if wasUp && !r.waitUp(ctx, old.NetworkGroup) {
			if _, err := r.data.client.UpdateWAN(ctx, old.ID, wanFields(want, old)); err != nil {
				return wanModel{}, fmt.Errorf("%s didn't come back within %s, and putting the previous settings back failed: %w", old.Name, wanWait.timeout, err)
			}
			return wanModel{}, fmt.Errorf("%s didn't come back within %s with the new settings; put the previous settings back", old.Name, wanWait.timeout)
		}
	}
	if !plan.Port.IsNull() {
		if err := r.movePort(ctx, old, ports, links, plan.Port.ValueString(), plan.ForcePort.ValueBool()); err != nil {
			return wanModel{}, err
		}
	}
	w, ports, _, err := r.find(ctx, old.ID)
	if err != nil {
		return wanModel{}, err
	}
	return wanState(w, ports, plan), nil
}

func (r *wanResource) movePort(ctx context.Context, w unifi.WAN, ports []unifi.Port, links []unifi.WANLink, key string, force bool) error {
	var target *unifi.Port
	var names []string
	roles := map[string]string{}
	for i, p := range ports {
		roles[p.Interface] = p.Role
		names = append(names, p.Name)
		if portMatches(p, key) {
			target = &ports[i]
		}
	}
	if target == nil {
		return fmt.Errorf("the router has no port %q (ports: %s)", key, strings.Join(names, ", "))
	}
	if target.Role == w.NetworkGroup {
		return nil
	}
	if target.Role != "LAN" {
		return fmt.Errorf("%s is used by another internet connection (%s); move that first", target.Name, target.Role)
	}
	if active, err := r.data.client.ListActiveClients(ctx); err == nil {
		macs, ips := localAddrs()
		var on []string
		for _, c := range active {
			if !c.IsWired || int(c.SwitchPort) != target.Index || !isRouterName(c.UplinkName) {
				continue
			}
			for _, m := range macs {
				if strings.EqualFold(m, c.MAC) {
					return fmt.Errorf("the machine running Terraform is connected to %s; refusing to make it an internet port", target.Name)
				}
			}
			for _, ip := range ips {
				if ip == c.IP {
					return fmt.Errorf("the machine running Terraform is connected to %s; refusing to make it an internet port", target.Name)
				}
			}
			on = append(on, c.MAC)
		}
		if len(on) > 0 && !force {
			return fmt.Errorf("LAN devices are connected to %s (%s); they'd lose their connection. Set force_port = true to move anyway", target.Name, strings.Join(on, ", "))
		}
	}
	oldRoles := map[string]string{}
	for k, v := range roles {
		oldRoles[k] = v
		if v == w.NetworkGroup {
			roles[k] = "LAN"
		}
	}
	roles[target.Interface] = w.NetworkGroup
	if err := r.data.client.SetPortRoles(ctx, roles); err != nil {
		return err
	}
	if linkUp(links, "WAN") && !r.waitUp(ctx, "WAN") {
		if err := r.data.client.SetPortRoles(ctx, oldRoles); err != nil {
			return fmt.Errorf("the primary connection didn't come back within %s, and moving the ports back failed: %w", wanWait.timeout, err)
		}
		return fmt.Errorf("the primary connection didn't come back within %s; moved the ports back", wanWait.timeout)
	}
	return nil
}

func (r *wanResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan wanModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state, err := r.set(ctx, plan)
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error setting internet connection", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *wanResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state wanModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	w, ports, _, err := r.find(ctx, state.ID.ValueString())
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error reading internet connection", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, wanState(w, ports, state))...)
}

func (r *wanResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan wanModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state, err := r.set(ctx, plan)
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error updating internet connection", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Delete leaves the connection as it is: the router's connections can't be
// deleted, and resetting one could take the internet down.
func (r *wanResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning("Internet connection left as it is", "The connection's settings stay on the router; they are no longer managed by Terraform.")
}

// ImportState takes the connection's name, e.g. "Internet 2".
func (r *wanResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	w, ports, _, err := r.find(ctx, req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Internet connection not found", err.Error())
		return
	}
	m := wanState(w, ports, wanModel{Name: types.StringValue(w.Name), Port: types.StringNull(), ForcePort: types.BoolValue(false)})
	for _, p := range ports {
		if p.Role == w.NetworkGroup {
			m.Port = types.StringValue(p.Name)
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}
