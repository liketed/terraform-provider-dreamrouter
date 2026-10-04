package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/liketed/dreamrouter-go/fakerouter"
)

func kidsConfig(r *fakerouter.Router, extraNet, extraWiFi string) string {
	return fakeProviderConfig(r) + `
resource "dreamrouter_network" "kids" {
  name        = "Kids"
  vlan        = 30
  subnet      = "192.168.30.1/24"
  dns_servers = ["94.140.14.15", "94.140.15.16"]
` + extraNet + `}

resource "dreamrouter_wifi" "kids" {
  name       = "kids-wifi"
  password   = "correct horse battery"
  network_id = dreamrouter_network.kids.id
` + extraWiFi + `}
`
}

func TestNetworkAndWiFiResources(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	fakeLocal(t, "02:00:00:00:00:01", "192.168.1.99")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories,
		CheckDestroy: func(*terraform.State) error {
			if r.Network("Kids") != nil || len(r.WiFis()) != 1 {
				return fmt.Errorf("left behind: network %v, Wi-Fi %v", r.Network("Kids"), r.WiFis())
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: kidsConfig(r, "", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dreamrouter_network.kids", "dhcp_start", "192.168.30.6"),
					resource.TestCheckResourceAttr("dreamrouter_network.kids", "dhcp_stop", "192.168.30.254"),
					resource.TestCheckResourceAttr("dreamrouter_wifi.kids", "bands.#", "2"),
					resource.TestCheckResourceAttr("dreamrouter_wifi.kids", "enabled", "true"),
					func(*terraform.State) error {
						n := r.Network("Kids")
						if n["vlan"] != 30.0 || n["dhcpd_dns_1"] != "94.140.14.15" || n["dhcpd_dns_2"] != "94.140.15.16" {
							return fmt.Errorf("network: %v", n)
						}
						w := r.WiFis()[1]
						if w["name"] != "kids-wifi" || w["x_passphrase"] != "correct horse battery" || w["networkconf_id"] != n["_id"] {
							return fmt.Errorf("Wi-Fi: %v", w)
						}
						return nil
					},
				),
			},
			{
				// Password and DNS changed in the web UI: set back.
				PreConfig: func() {
					r.ModifyWiFi("kids-wifi", func(w map[string]any) { w["x_passphrase"] = "changed-in-ui" })
					r.ModifyNetwork("Kids", func(n map[string]any) { n["dhcpd_dns_2"] = "8.8.8.8" })
				},
				Config: kidsConfig(r, "", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("dreamrouter_wifi.kids", plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction("dreamrouter_network.kids", plancheck.ResourceActionUpdate),
				}},
				Check: func(*terraform.State) error {
					if r.WiFis()[1]["x_passphrase"] != "correct horse battery" || r.Network("Kids")["dhcpd_dns_2"] != "94.140.15.16" {
						return fmt.Errorf("not set back")
					}
					return nil
				},
			},
			{
				// In-place changes: Wi-Fi hidden and on 6 GHz too, network renamed.
				Config: kidsConfig(r, "", "  hidden = true\n  bands = [\"2g\", \"5g\", \"6g\"]\n"),
				Check: func(*terraform.State) error {
					if w := r.WiFis()[1]; w["hide_ssid"] != true || len(w["wlan_bands"].([]any)) != 3 {
						return fmt.Errorf("Wi-Fi: %v", w)
					}
					return nil
				},
			},
			{
				// A new VLAN replaces the network.
				Config: kidsConfig(r, "", "  hidden = true\n  bands = [\"2g\", \"5g\", \"6g\"]\n"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("dreamrouter_network.kids", plancheck.ResourceActionNoop),
				}},
			},
			{ResourceName: "dreamrouter_network.kids", ImportState: true, ImportStateId: "kids", ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"dns_servers"}},
			{ResourceName: "dreamrouter_wifi.kids", ImportState: true, ImportStateId: "kids-wifi", ImportStateVerify: true},
		},
	})
}

func TestNetworkAndWiFiValidation(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	nw := func(body string) string { return "resource \"dreamrouter_network\" \"x\" {\n" + body + "\n}\n" }
	wf := func(body string) string { return "resource \"dreamrouter_wifi\" \"x\" {\n" + body + "\n}\n" }
	for name, tc := range map[string]struct{ cfg, want string }{
		"public subnet":      {nw("name = \"x\"\nvlan = 30\nsubnet = \"8.8.8.1/24\""), `not a private range`},
		"overlap with LAN":   {nw("name = \"x\"\nvlan = 30\nsubnet = \"192.168.1.1/24\""), `overlaps network "Default"`},
		"duplicate name":     {nw("name = \"default\"\nvlan = 30\nsubnet = \"192.168.30.1/24\""), `named "Default" already exists`},
		"VLAN 1":             {nw("name = \"x\"\nvlan = 1\nsubnet = \"192.168.30.1/24\""), `between 2 and 4094`},
		"DNS host name":      {nw("name = \"x\"\nvlan = 30\nsubnet = \"192.168.30.1/24\"\ndns_servers = [\"dns.adguard.com\"]"), `must be an IPv4 address`},
		"short password":     {wf("name = \"x\"\npassword = \"short12\"\nnetwork_id = \"net-default\""), `8 to 63 characters`},
		"duplicate Wi-Fi":    {wf("name = \"home\"\npassword = \"correct horse\"\nnetwork_id = \"net-default\""), `named "home" already exists`},
		"bad band":           {wf("name = \"x\"\npassword = \"correct horse\"\nnetwork_id = \"net-default\"\nbands = [\"60g\"]"), `band "60g"`},
		"unknown network id": {wf("name = \"x\"\npassword = \"correct horse\"\nnetwork_id = \"nope\""), `no network with ID "nope"`},
	} {
		t.Run(name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: providerFactories,
				Steps:                    []resource.TestStep{{Config: fakeProviderConfig(r) + tc.cfg, ExpectError: regexp.MustCompile(tc.want)}},
			})
		})
	}
	if len(r.WiFis()) != 1 || r.Network("x") != nil {
		t.Fatal("invalid items reached the router")
	}
}

func TestWiFiRefusesOwnNetwork(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	fakeLocal(t, "02:00:00:00:00:01", "192.168.1.99")
	r.PutActive(fakerouter.Status{MAC: "02:00:00:00:00:01", IP: "192.168.1.99", ESSID: "home", Type: "WIRELESS"})
	cfg := func(enabled string) string {
		return fakeProviderConfig(r) + `
resource "dreamrouter_wifi" "home" {
  name       = "home"
  password   = "home-password"
  network_id = "net-default"
  bands      = ["2g", "5g", "6g"]
  enabled    = ` + enabled + `
}
`
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories,
		Steps: []resource.TestStep{
			{ResourceName: "dreamrouter_wifi.home", Config: cfg("true"), ImportState: true, ImportStateId: "home", ImportStatePersist: true},
			{Config: cfg("false"), ExpectError: regexp.MustCompile(`Refusing to disable this Wi-Fi network`)},
			{Config: fakeProviderConfig(r), ExpectError: regexp.MustCompile(`Refusing to delete this Wi-Fi network`)},
			{
				// Neither refusal changed anything; then move this machine to
				// another network so the test can clean up.
				PreConfig: func() {
					if w := r.WiFis()[0]; w["enabled"] != true {
						t.Fatal("own Wi-Fi network was disabled")
					}
					if len(r.WiFis()) != 1 {
						t.Fatal("own Wi-Fi network was deleted")
					}
					fakeLocal(t, "02:00:00:00:00:09", "192.168.1.98")
				},
				Config: cfg("true"),
			},
		},
	})
}

// TestNetworkInUseNotDeleted checks a network isn't deleted while a Wi-Fi
// network made outside Terraform still uses it.
func TestNetworkInUseNotDeleted(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	cfg := fakeProviderConfig(r) + `
resource "dreamrouter_network" "kids" {
  name   = "Kids"
  vlan   = 30
  subnet = "192.168.30.1/24"
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories,
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				PreConfig: func() {
					kidsID := r.Network("Kids")["_id"] // outside ModifyWiFi, which holds the fake router's lock
					r.ModifyWiFi("home", func(w map[string]any) { w["networkconf_id"] = kidsID })
				},
				Config:      fakeProviderConfig(r),
				ExpectError: regexp.MustCompile(`Wi-Fi network home uses network Kids`),
			},
			{
				// Move the Wi-Fi network back so the test can clean up.
				PreConfig: func() {
					if r.Network("Kids") == nil {
						t.Fatal("network in use was deleted")
					}
					r.ModifyWiFi("home", func(w map[string]any) { w["networkconf_id"] = "net-default" })
				},
				Config: fakeProviderConfig(r),
			},
		},
	})
}
