package provider

import (
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"github.com/liketed/dreamrouter-go/fakerouter"
)

func TestNetworkDHCPOptions(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	cfg := func(body string) string {
		return fakeProviderConfig(r) + "resource \"dreamrouter_network_dhcp\" \"lan\" {\n  network = \"Default\"\n" + body + "}\n"
	}
	res := "dreamrouter_network_dhcp.lan"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories,
		// Destroy puts managed options back to the defaults; domain_name was
		// never managed here, so it keeps the value set in the web UI.
		CheckDestroy: checkNetwork(r, map[string]any{"dhcpd_dns_enabled": false, "dhcpd_dns_1": "", "dhcpd_leasetime": 86400.0,
			"dhcpd_ntp_enabled": false, "domain_name": "set-in-ui.internal"}),
		Steps: []resource.TestStep{
			{
				Config: cfg("  dns_servers = [\"192.168.1.1\", \"1.1.1.1\"]\n  lease_time = 43200\n  ntp_servers = [\"192.168.1.1\"]\n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(res, "dns_servers.#", "2"),
					resource.TestCheckResourceAttr(res, "dns_servers.1", "1.1.1.1"),
					resource.TestCheckResourceAttr(res, "lease_time", "43200"),
					resource.TestCheckResourceAttr(res, "ntp_servers.0", "192.168.1.1"),
					resource.TestCheckNoResourceAttr(res, "domain_name"),
					checkNetwork(r, map[string]any{"dhcpd_dns_enabled": true, "dhcpd_dns_1": "192.168.1.1", "dhcpd_dns_2": "1.1.1.1",
						"dhcpd_dns_3": "", "dhcpd_leasetime": 43200.0, "dhcpd_ntp_enabled": true, "dhcpd_ntp_1": "192.168.1.1",
						"domain_name": "localdomain"}),
				),
			},
			{
				// Changed in the web UI: managed options are set back; the
				// unmanaged domain name is left as the web UI set it.
				PreConfig: func() {
					r.ModifyNetwork("Default", func(n map[string]any) {
						n["dhcpd_dns_2"] = "8.8.8.8"
						n["dhcpd_leasetime"] = 3600.0
						n["domain_name"] = "set-in-ui.internal"
					})
				},
				Config: cfg("  dns_servers = [\"192.168.1.1\", \"1.1.1.1\"]\n  lease_time = 43200\n  ntp_servers = [\"192.168.1.1\"]\n"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(res, plancheck.ResourceActionUpdate),
				}},
				Check: checkNetwork(r, map[string]any{"dhcpd_dns_2": "1.1.1.1", "dhcpd_leasetime": 43200.0, "domain_name": "set-in-ui.internal"}),
			},
			{
				// Empty lists mean the defaults: the router as DNS, no NTP.
				Config: cfg("  dns_servers = []\n  lease_time = 43200\n  ntp_servers = []\n"),
				Check: checkNetwork(r, map[string]any{"dhcpd_dns_enabled": false, "dhcpd_dns_1": "", "dhcpd_dns_2": "",
					"dhcpd_ntp_enabled": false, "dhcpd_ntp_1": ""}),
			},
			{
				Config: cfg("  dns_servers = []\n  lease_time = 43200\n  ntp_servers = []\n"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(res, plancheck.ResourceActionNoop),
				}},
			},
		},
	})
}

// TestNetworkDHCPOptionsUntouched checks that a configuration without the
// new options (e.g. one written for an older provider) leaves DHCP options
// set in the web UI alone.
func TestNetworkDHCPOptionsUntouched(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	r.ModifyNetwork("Default", func(n map[string]any) {
		n["dhcpd_dns_enabled"], n["dhcpd_dns_1"], n["dhcpd_leasetime"], n["domain_name"] = true, "9.9.9.9", 7200.0, "ui.internal"
	})
	cfg := fakeProviderConfig(r) + "resource \"dreamrouter_network_dhcp\" \"lan\" {\n  network = \"Default\"\n  tftp_server = \"tftp.home.internal\"\n}\n"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories,
		CheckDestroy:             checkNetwork(r, map[string]any{"dhcpd_dns_1": "9.9.9.9", "dhcpd_leasetime": 7200.0, "domain_name": "ui.internal"}),
		Steps: []resource.TestStep{
			{Config: cfg, Check: checkNetwork(r, map[string]any{"dhcpd_dns_1": "9.9.9.9", "dhcpd_leasetime": 7200.0, "domain_name": "ui.internal",
				"dhcpd_tftp_server": "tftp.home.internal"})},
			{Config: cfg, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction("dreamrouter_network_dhcp.lan", plancheck.ResourceActionNoop),
			}}},
		},
	})
}

func TestNetworkDHCPOptionsValidation(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	for name, tc := range map[string]struct{ body, want string }{
		"DNS not an IP":     {`dns_servers = ["not-an-ip"]`, `DNS server "not-an-ip" must be an IPv4 address`},
		"DNS host name":     {`dns_servers = ["dns.google"]`, `must be an IPv4 address`},
		"DNS IPv6":          {`dns_servers = ["2606:4700:4700::1111"]`, `must be an IPv4 address`},
		"DNS twice":         {`dns_servers = ["1.1.1.1", "1.1.1.1"]`, `listed twice`},
		"five DNS servers":  {`dns_servers = ["1.1.1.1", "1.1.1.2", "1.1.1.3", "1.1.1.4", "1.1.1.5"]`, `list must contain at most 4`},
		"lease too short":   {`lease_time = 60`, `between 120 and 31536000`},
		"lease too long":    {`lease_time = 31536001`, `between 120 and 31536000`},
		"NTP host name":     {`ntp_servers = ["pool.ntp.org"]`, `NTP server "pool.ntp.org" must be an IPv4 address`},
		"three NTP servers": {`ntp_servers = ["1.1.1.1", "1.1.1.2", "1.1.1.3"]`, `list must contain at most 2`},
		"bad domain":        {`domain_name = "home internal"`, `lower-case letters, digits and hyphens`},
	} {
		t.Run(name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: providerFactories,
				Steps: []resource.TestStep{{
					Config:      fakeProviderConfig(r) + "resource \"dreamrouter_network_dhcp\" \"x\" {\n  network = \"Default\"\n  " + tc.body + "\n}\n",
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tc.want),
				}},
			})
		})
	}
	if n := r.Network("Default"); n["dhcpd_dns_enabled"] != nil || n["dhcpd_leasetime"] != nil {
		t.Fatalf("invalid values reached the router: %v", n)
	}
}
