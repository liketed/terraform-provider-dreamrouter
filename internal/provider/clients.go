package provider

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/liketed/dreamrouter-go/unifi"
)

// Shared by dreamrouter_clients and dreamrouter_client_block.

// localAddrs returns the MAC and IP addresses of the machine running
// Terraform, so it is never blocked. Replaced in tests.
var localAddrs = func() (macs, ips []string) {
	ifaces, _ := net.Interfaces()
	for _, i := range ifaces {
		if len(i.HardwareAddr) == 6 {
			macs = append(macs, i.HardwareAddr.String())
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			if p, err := netip.ParsePrefix(a.String()); err == nil {
				ips = append(ips, p.Addr().String())
			}
		}
	}
	return macs, ips
}

// clientInfo combines a device's status (connected now or seen recently)
// with its stored record (name, note, reservation, blocked). Either may be nil.
type clientInfo struct {
	status *unifi.ClientStatus
	device *unifi.ClientDevice
}

func (ci clientInfo) mac() string {
	if ci.device != nil {
		return ci.device.MAC
	}
	return ci.status.MAC
}

func (ci clientInfo) name() string {
	if ci.device != nil && ci.device.Name != "" {
		return ci.device.Name
	}
	if ci.status != nil && ci.status.Label() != "" {
		return ci.status.Label()
	}
	if ci.device != nil {
		return ci.device.Hostname
	}
	return ""
}

func (ci clientInfo) ip() string {
	if ci.status != nil && ci.status.Address() != "" {
		return ci.status.Address()
	}
	if ci.device != nil {
		if ci.device.UseFixedIP {
			return ci.device.FixedIP
		}
		return ci.device.LastIP
	}
	return ""
}

func (ci clientInfo) blocked() bool {
	return (ci.device != nil && ci.device.Blocked) || (ci.status != nil && ci.status.Blocked)
}

func (ci clientInfo) lastSeen() time.Time {
	if ci.status != nil && ci.status.LastSeen > 0 {
		return time.Unix(int64(ci.status.LastSeen), 0)
	}
	if ci.device != nil && ci.device.LastSeen > 0 {
		return time.Unix(ci.device.LastSeen, 0)
	}
	return time.Time{}
}

// isLocal reports whether the device is the machine running Terraform.
func (ci clientInfo) isLocal() bool {
	macs, ips := localAddrs()
	for _, m := range macs {
		if strings.EqualFold(m, ci.mac()) {
			return true
		}
	}
	for _, ip := range ips {
		if a := ci.ip(); a != "" && a == ip {
			return true
		}
	}
	return false
}

// loadClients returns connected devices, devices seen within offlineHours,
// and stored devices that are blocked (however long ago they were seen),
// sorted by IP address. With allStored, every stored device is included.
func loadClients(ctx context.Context, c *unifi.Client, offlineHours int, allStored bool) ([]clientInfo, error) {
	active, err := c.ListActiveClients(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing connected devices: %w", err)
	}
	offline, err := c.ListOfflineClients(ctx, offlineHours)
	if err != nil {
		return nil, fmt.Errorf("listing recently seen devices: %w", err)
	}
	devices, err := c.ListClients(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing devices: %w", err)
	}
	byMAC := map[string]*unifi.ClientDevice{}
	for i := range devices {
		byMAC[devices[i].MAC] = &devices[i]
	}
	var out []clientInfo
	seen := map[string]bool{}
	for _, list := range [][]unifi.ClientStatus{active, offline} {
		for i := range list {
			s := &list[i]
			if !seen[s.MAC] {
				seen[s.MAC] = true
				out = append(out, clientInfo{status: s, device: byMAC[s.MAC]})
			}
		}
	}
	for i := range devices {
		if !seen[devices[i].MAC] && (allStored || devices[i].Blocked) {
			out = append(out, clientInfo{device: &devices[i]})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, errA := netip.ParseAddr(out[i].ip())
		b, errB := netip.ParseAddr(out[j].ip())
		switch {
		case errA == nil && errB == nil:
			return a.Less(b)
		case errA == nil || errB == nil:
			return errA == nil // devices without an address last
		}
		return out[i].mac() < out[j].mac()
	})
	return out, nil
}

// band names the Wi-Fi band from the router's radio code.
func band(radio string) string {
	switch radio {
	case "ng":
		return "2.4GHz"
	case "na":
		return "5GHz"
	case "6e":
		return "6GHz"
	}
	return radio
}
