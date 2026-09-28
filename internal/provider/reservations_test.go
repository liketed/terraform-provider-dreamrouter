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

func checkClient(r *fakerouter.Router, mac string, check func(fakerouter.Client, bool) error) resource.TestCheckFunc {
	return func(*terraform.State) error {
		c, ok := r.Client(mac)
		return check(c, ok)
	}
}

func TestReservationLifecycle(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	// A device the router already knows, with a name from the web UI.
	r.PutClient(fakerouter.Client{MAC: "aa:bb:cc:00:00:02", Name: "printer", LastIP: "192.168.1.77"})
	cfg := func(ip, extra string) string {
		return fakeProviderConfig(r) + fmt.Sprintf(`
resource "dreamrouter_dhcp_reservation" "nas" {
  mac  = "aa:bb:cc:00:00:01"
  ip   = %q
  name = "nas"
}
resource "dreamrouter_dhcp_reservation" "printer" {
  mac = "aa:bb:cc:00:00:02"
  ip  = "192.168.1.60"
  %s
}
`, ip, extra)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories,
		CheckDestroy: func(*terraform.State) error {
			if c, _ := r.Client("aa:bb:cc:00:00:02"); c.UseFixedIP || c.Name != "printer" {
				return fmt.Errorf("printer after destroy: %+v (want the device kept, without a reservation)", c)
			}
			if _, ok := r.Client("aa:bb:cc:00:00:01"); ok {
				return fmt.Errorf("nas should have been forgotten (forget_on_destroy)")
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: cfg("192.168.1.50", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dreamrouter_dhcp_reservation.nas", "network_id", fakerouter.NetworkID),
					resource.TestCheckResourceAttrSet("dreamrouter_dhcp_reservation.nas", "id"),
					resource.TestCheckResourceAttr("dreamrouter_dhcp_reservation.printer", "name", "printer"),
					checkClient(r, "aa:bb:cc:00:00:01", func(c fakerouter.Client, ok bool) error {
						if !ok || !c.UseFixedIP || c.FixedIP != "192.168.1.50" || c.Name != "nas" || c.NetworkID != fakerouter.NetworkID {
							return fmt.Errorf("nas stored as %+v", c)
						}
						return nil
					}),
					func(*terraform.State) error {
						if n := len(r.Clients()); n != 2 {
							return fmt.Errorf("%d clients, want 2 (the known printer updated, not duplicated)", n)
						}
						return nil
					},
				),
			},
			{
				// IP changes are made in place.
				Config: cfg("192.168.1.51", `forget_on_destroy = false`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("dreamrouter_dhcp_reservation.nas", plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction("dreamrouter_dhcp_reservation.printer", plancheck.ResourceActionNoop),
				}},
				Check: checkClient(r, "aa:bb:cc:00:00:01", func(c fakerouter.Client, _ bool) error {
					if c.FixedIP != "192.168.1.51" {
						return fmt.Errorf("IP = %s", c.FixedIP)
					}
					return nil
				}),
			},
			{
				// Someone changes the IP in the web UI: put back. Someone removes the
				// reservation: recreated.
				PreConfig: func() {
					r.ModifyClient("aa:bb:cc:00:00:01", func(c *fakerouter.Client) { c.FixedIP = "192.168.1.99" })
					r.ModifyClient("aa:bb:cc:00:00:02", func(c *fakerouter.Client) { c.UseFixedIP = false })
				},
				Config: cfg("192.168.1.51", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("dreamrouter_dhcp_reservation.nas", plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction("dreamrouter_dhcp_reservation.printer", plancheck.ResourceActionCreate),
				}},
			},
			{
				ResourceName:            "dreamrouter_dhcp_reservation.nas",
				ImportState:             true,
				ImportStateId:           "AA-BB-CC-00-00-01",
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"forget_on_destroy"},
			},
			{
				// Set forget_on_destroy on nas for the destroy check.
				Config: fakeProviderConfig(r) + `
resource "dreamrouter_dhcp_reservation" "nas" {
  mac               = "aa:bb:cc:00:00:01"
  ip                = "192.168.1.51"
  name              = "nas"
  forget_on_destroy = true
}
resource "dreamrouter_dhcp_reservation" "printer" {
  mac = "aa:bb:cc:00:00:02"
  ip  = "192.168.1.60"
}
`,
			},
		},
	})
}

func TestReservationErrors(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	r.PutClient(fakerouter.Client{MAC: "aa:bb:cc:00:00:09", Name: "other", UseFixedIP: true, FixedIP: "192.168.1.90", NetworkID: fakerouter.NetworkID})
	res := func(mac, ip string) string {
		return fakeProviderConfig(r) + fmt.Sprintf("resource \"dreamrouter_dhcp_reservation\" \"x\" {\n  mac = %q\n  ip = %q\n}\n", mac, ip)
	}
	for name, tc := range map[string]struct{ cfg, want string }{
		"upper-case MAC":         {res("AA:BB:CC:00:00:01", "192.168.1.50"), `write the MAC address as "aa:bb:cc:00:00:01"`},
		"bad MAC":                {res("nope", "192.168.1.50"), `"nope" is not a MAC address`},
		"bad IP":                 {res("aa:bb:cc:00:00:01", "192.168.1.500"), `is not an IPv4 address`},
		"outside subnet":         {res("aa:bb:cc:00:00:01", "10.0.0.5"), `not in any network's subnet`},
		"router address":         {res("aa:bb:cc:00:00:01", "192.168.1.1"), `the router's own address`},
		"reserved for other":     {res("aa:bb:cc:00:00:01", "192.168.1.90"), `192.168.1.90 is already reserved for aa:bb:cc:00:00:09 \(other\)`},
		"device already has one": {res("aa:bb:cc:00:00:09", "192.168.1.91"), `(?s)already has a DHCP reservation.*terraform import <address> aa:bb:cc:00:00:09`},
	} {
		t.Run(name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: providerFactories,
				Steps:                    []resource.TestStep{{Config: tc.cfg, ExpectError: regexp.MustCompile(tc.want)}},
			})
		})
	}
	if c, _ := r.Client("aa:bb:cc:00:00:09"); c.FixedIP != "192.168.1.90" {
		t.Fatalf("the unmanaged reservation was changed: %+v", c)
	}
}

func TestHostLifecycle(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	r.PutDNS(fakerouter.DNSRecord{RecordType: "A", Key: "static.home.internal", Value: "192.168.1.5", Enabled: true})
	cfg := func(name, ip string) string {
		return fakeProviderConfig(r) + fmt.Sprintf(`
resource "dreamrouter_host" "nas" {
  name = %q
  ip   = %q
  mac  = "aa:bb:cc:00:00:01"
}
data "dreamrouter_dns_records" "all" {
  depends_on = [dreamrouter_host.nas]
}
data "dreamrouter_dns_records" "static" {
  static_only = true
  depends_on  = [dreamrouter_host.nas]
}
`, name, ip)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories,
		CheckDestroy: checkClient(r, "aa:bb:cc:00:00:01", func(c fakerouter.Client, ok bool) error {
			if ok && (c.UseFixedIP || c.LocalDNSRecordEnabled) {
				return fmt.Errorf("after destroy: %+v", c)
			}
			return nil
		}),
		Steps: []resource.TestStep{
			{
				Config: cfg("nas.home.internal", "192.168.1.50"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dreamrouter_host.nas", "device_name", "nas.home.internal"),
					checkClient(r, "aa:bb:cc:00:00:01", func(c fakerouter.Client, ok bool) error {
						if !ok || !c.UseFixedIP || c.FixedIP != "192.168.1.50" || c.LocalDNSRecord != "nas.home.internal" || !c.LocalDNSRecordEnabled {
							return fmt.Errorf("stored %+v", c)
						}
						return nil
					}),
					// The data source shows device names, marked "host", unless static_only.
					resource.TestCheckResourceAttr("data.dreamrouter_dns_records.all", "records.#", "2"),
					resource.TestCheckResourceAttr("data.dreamrouter_dns_records.all", "records.0.name", "nas.home.internal"),
					resource.TestCheckResourceAttr("data.dreamrouter_dns_records.all", "records.0.source", "host"),
					resource.TestCheckResourceAttr("data.dreamrouter_dns_records.all", "records.0.mac", "aa:bb:cc:00:00:01"),
					resource.TestCheckResourceAttr("data.dreamrouter_dns_records.all", "records.1.source", "static"),
					resource.TestCheckResourceAttr("data.dreamrouter_dns_records.static", "records.#", "1"),
				),
			},
			{
				// Renaming and moving a host are in-place updates.
				Config: cfg("storage.home.internal", "192.168.1.52"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("dreamrouter_host.nas", plancheck.ResourceActionUpdate),
				}},
				Check: checkClient(r, "aa:bb:cc:00:00:01", func(c fakerouter.Client, _ bool) error {
					if c.LocalDNSRecord != "storage.home.internal" || c.FixedIP != "192.168.1.52" {
						return fmt.Errorf("stored %+v", c)
					}
					return nil
				}),
			},
			{
				// Someone turns the name off in the web UI: turned back on in place.
				PreConfig: func() {
					r.ModifyClient("aa:bb:cc:00:00:01", func(c *fakerouter.Client) { c.LocalDNSRecordEnabled = false })
				},
				Config: cfg("storage.home.internal", "192.168.1.52"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("dreamrouter_host.nas", plancheck.ResourceActionUpdate),
				}},
				Check: checkClient(r, "aa:bb:cc:00:00:01", func(c fakerouter.Client, _ bool) error {
					if !c.LocalDNSRecordEnabled {
						return fmt.Errorf("name not turned back on: %+v", c)
					}
					return nil
				}),
			},
			{
				// Someone removes the whole reservation: recreated.
				PreConfig: func() {
					r.ModifyClient("aa:bb:cc:00:00:01", func(c *fakerouter.Client) { c.UseFixedIP, c.LocalDNSRecordEnabled = false, false })
				},
				Config: cfg("storage.home.internal", "192.168.1.52"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("dreamrouter_host.nas", plancheck.ResourceActionCreate),
				}},
			},
			{
				ResourceName:            "dreamrouter_host.nas",
				ImportState:             true,
				ImportStateId:           "storage.home.internal",
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"forget_on_destroy"},
			},
			{
				// A name that is already a static record is refused with a clear message.
				Config:      cfg("static.home.internal", "192.168.1.52"),
				ExpectError: regexp.MustCompile(`static\.home\.internal is already a static DNS record \(A\s+static\.home\.internal\s+->\s+192\.168\.1\.5\)`),
			},
		},
	})
}

func TestDNSRecordClashesWithHost(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	r.PutClient(fakerouter.Client{MAC: "aa:bb:cc:00:00:01", UseFixedIP: true, FixedIP: "192.168.1.50",
		NetworkID: fakerouter.NetworkID, LocalDNSRecord: "nas.home.internal", LocalDNSRecordEnabled: true})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories,
		Steps: []resource.TestStep{{
			Config: fakeProviderConfig(r) + `
resource "dreamrouter_dns_record" "nas" {
  type  = "A"
  name  = "nas.home.internal"
  value = "192.168.1.51"
}
`,
			ExpectError: regexp.MustCompile(`(?s)"nas\.home\.internal" is already a device's DNS name`),
		}},
	})
}

func TestNetworksDataSource(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories,
		Steps: []resource.TestStep{
			{
				Config: fakeProviderConfig(r) + `
data "dreamrouter_networks" "all" {}
data "dreamrouter_networks" "lan" { name = "default" }
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.dreamrouter_networks.all", "networks.#", "2"),
					resource.TestCheckResourceAttr("data.dreamrouter_networks.lan", "networks.#", "1"),
					resource.TestCheckResourceAttr("data.dreamrouter_networks.lan", "networks.0.id", fakerouter.NetworkID),
					resource.TestCheckResourceAttr("data.dreamrouter_networks.lan", "networks.0.subnet", "192.168.1.1/24"),
					resource.TestCheckResourceAttr("data.dreamrouter_networks.lan", "networks.0.dhcp_start", "192.168.1.6"),
				),
			},
			{
				Config:      fakeProviderConfig(r) + `data "dreamrouter_networks" "x" { name = "IoT" }`,
				ExpectError: regexp.MustCompile(`The router has no network named "IoT"`),
			},
		},
	})
}
