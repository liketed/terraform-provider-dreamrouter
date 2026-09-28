package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	"github.com/liketed/dreamrouter-go/check"
	"github.com/liketed/dreamrouter-go/unifi"
)

// Shared by dreamrouter_dhcp_reservation and dreamrouter_host, which both
// manage a device's fixed IP (and, for hosts, its DNS name).

var pathIP = path.Root("ip")

// macPattern accepts MAC addresses in the form the router stores them, so the
// configuration and the state always match.
var macPattern = regexp.MustCompile(`^([0-9a-f]{2}:){5}[0-9a-f]{2}$`)

type macValidator struct{}

func (macValidator) Description(context.Context) string {
	return "a MAC address in lower-case colon form, e.g. aa:bb:cc:dd:ee:ff"
}
func (v macValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }
func (macValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	v := req.ConfigValue.ValueString()
	if macPattern.MatchString(v) {
		return
	}
	msg := fmt.Sprintf("%q is not a MAC address in lower-case colon form, e.g. aa:bb:cc:dd:ee:ff", v)
	if norm, err := check.MAC(v); err == nil {
		msg = fmt.Sprintf("write the MAC address as %q (lower case, with colons), the form the router uses", norm)
	}
	resp.Diagnostics.AddAttributeError(req.Path, "Invalid MAC address", msg)
}

type ipv4Validator struct{}

func (ipv4Validator) Description(context.Context) string               { return "an IPv4 address" }
func (v ipv4Validator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }
func (ipv4Validator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if a, err := check.IPv4(req.ConfigValue.ValueString()); err != nil || a.String() != req.ConfigValue.ValueString() {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid IPv4 address",
			fmt.Sprintf("%q is not an IPv4 address in its usual form, e.g. 192.168.1.50", req.ConfigValue.ValueString()))
	}
}

// fixedIPRequest is a validated request for a device's fixed IP.
type fixedIPRequest struct {
	mac, ip, name, networkID string // name and networkID may be empty
	dnsName                  string // dreamrouter_host only
}

// routerState is what reservation operations need from the router.
type routerState struct {
	networks []unifi.Network
	clients  []unifi.ClientDevice
}

func loadRouterState(ctx context.Context, c *unifi.Client) (*routerState, error) {
	networks, err := c.ListNetworks(ctx)
	if err != nil {
		return nil, err
	}
	clients, err := c.ListClients(ctx)
	if err != nil {
		return nil, err
	}
	return &routerState{networks: networks, clients: clients}, nil
}

func (s *routerState) byMAC(mac string) *unifi.ClientDevice {
	for i := range s.clients {
		if s.clients[i].MAC == mac {
			return &s.clients[i]
		}
	}
	return nil
}

func describeDevice(d *unifi.ClientDevice) string {
	if n := d.DisplayName(); n != "" {
		return fmt.Sprintf("%s (%s)", d.MAC, n)
	}
	return d.MAC
}

// resolveNetwork checks the IP against the router's networks and returns the
// network ID to use: the configured one, or the one whose subnet contains ip.
func (s *routerState) resolveNetwork(req fixedIPRequest) (string, error) {
	ip, err := check.IPv4(req.ip)
	if err != nil {
		return "", err
	}
	n, err := check.NetworkFor(s.networks, ip, req.networkID)
	if err != nil {
		return "", err
	}
	return n.ID, nil
}

// checkIPFree fails if another device has ip reserved.
func (s *routerState) checkIPFree(ip, exceptMAC string) error {
	for i := range s.clients {
		d := &s.clients[i]
		if d.MAC != exceptMAC && d.UseFixedIP && d.FixedIP == ip {
			return fmt.Errorf("%s is already reserved for %s", ip, describeDevice(d))
		}
	}
	return nil
}

// warnIfInUse adds a warning if another device currently has ip.
func (s *routerState) warnIfInUse(ip, exceptMAC string, diags *diag.Diagnostics) {
	for i := range s.clients {
		d := &s.clients[i]
		if d.MAC != exceptMAC && d.LastIP == ip {
			diags.AddWarning("IP address in use",
				fmt.Sprintf("%s is currently in use by %s; that device will get another address when its lease renews.", ip, describeDevice(d)))
			return
		}
	}
}

// fixedIPFields builds the API fields for a request.
func fixedIPFields(req fixedIPRequest, networkID string) map[string]any {
	f := map[string]any{"use_fixedip": true, "fixed_ip": req.ip, "network_id": networkID}
	if req.name != "" {
		f["name"] = req.name
	}
	if req.dnsName != "" {
		f["local_dns_record"] = req.dnsName
		f["local_dns_record_enabled"] = true
	}
	return f
}

// createFixedIP gives a device a fixed IP (and DNS name), creating the client
// if the router hasn't seen the device. It refuses to take over a device that
// already has a reservation, which another project or the web UI manages.
func createFixedIP(ctx context.Context, c *unifi.Client, req fixedIPRequest, kind string, diags *diag.Diagnostics) (unifi.ClientDevice, bool) {
	s, err := loadRouterState(ctx, c)
	if err != nil {
		diags.AddError("Error reading router state", err.Error())
		return unifi.ClientDevice{}, false
	}
	networkID, err := s.resolveNetwork(req)
	if err != nil {
		diags.AddAttributeError(pathIP, "Invalid IP address for the network", err.Error())
		return unifi.ClientDevice{}, false
	}
	if err := s.checkIPFree(req.ip, req.mac); err != nil {
		diags.AddAttributeError(pathIP, "IP address already reserved", err.Error())
		return unifi.ClientDevice{}, false
	}
	existing := s.byMAC(req.mac)
	if existing != nil && existing.UseFixedIP {
		importID := req.mac
		if kind == "host" && existing.HasDNSName() {
			importID = existing.LocalDNSRecord
		}
		diags.AddError("Device already has a reservation",
			fmt.Sprintf("%s already has a DHCP reservation (%s). It may be managed by another Terraform project, "+
				"a dreamrouter_%s resource, drctl or the web UI. If nothing else manages it, import it:\n"+
				"  terraform import <address> %s", describeDevice(existing), existing.FixedIP, kindResource(existing), importID))
		return unifi.ClientDevice{}, false
	}
	s.warnIfInUse(req.ip, req.mac, diags)
	fields := fixedIPFields(req, networkID)
	var d unifi.ClientDevice
	if existing == nil {
		fields["mac"] = req.mac
		if req.name == "" && req.dnsName != "" {
			fields["name"] = req.dnsName // label new devices in the web UI
		}
		d, err = c.CreateClient(ctx, fields)
	} else {
		d, err = c.UpdateClient(ctx, existing.ID, fields)
	}
	if err != nil {
		diags.AddError("Error creating "+kind, describeClientError(err))
		return unifi.ClientDevice{}, false
	}
	return d, true
}

func kindResource(d *unifi.ClientDevice) string {
	if d.HasDNSName() {
		return "host"
	}
	return "dhcp_reservation"
}

// updateFixedIP changes a managed device's fixed IP settings.
func updateFixedIP(ctx context.Context, c *unifi.Client, id string, req fixedIPRequest, kind string, diags *diag.Diagnostics) (unifi.ClientDevice, bool) {
	s, err := loadRouterState(ctx, c)
	if err != nil {
		diags.AddError("Error reading router state", err.Error())
		return unifi.ClientDevice{}, false
	}
	networkID, err := s.resolveNetwork(req)
	if err != nil {
		diags.AddAttributeError(pathIP, "Invalid IP address for the network", err.Error())
		return unifi.ClientDevice{}, false
	}
	if err := s.checkIPFree(req.ip, req.mac); err != nil {
		diags.AddAttributeError(pathIP, "IP address already reserved", err.Error())
		return unifi.ClientDevice{}, false
	}
	s.warnIfInUse(req.ip, req.mac, diags)
	d, err := c.UpdateClient(ctx, id, fixedIPFields(req, networkID))
	if err != nil {
		diags.AddError("Error updating "+kind, describeClientError(err))
		return unifi.ClientDevice{}, false
	}
	return d, true
}

// removeFixedIP clears a device's fixed IP and DNS name (the router requires a
// fixed IP for the name), or forgets the device entirely.
func removeFixedIP(ctx context.Context, c *unifi.Client, id, mac string, forget bool, kind string, diags *diag.Diagnostics) {
	var err error
	if forget {
		err = c.ForgetClient(ctx, mac)
	} else {
		_, err = c.UpdateClient(ctx, id, map[string]any{"use_fixedip": false, "local_dns_record_enabled": false})
	}
	if err != nil && !unifi.IsNotFound(err) {
		diags.AddError("Error removing "+kind, err.Error())
	}
}

// describeClientError adds context to the router's terse client API errors.
func describeClientError(err error) string {
	msg := err.Error()
	if unifi.HasCode(err, "api.err.Invalid") {
		msg += "\n\nThe router gives this error when a device's DNS name is already a static DNS record."
	}
	return msg
}

// sameName compares DNS names as the router does, ignoring case.
func sameName(a, b string) bool { return strings.EqualFold(a, b) }
