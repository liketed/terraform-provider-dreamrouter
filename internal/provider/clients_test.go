package provider

import (
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/liketed/dreamrouter-go/fakerouter"
)

// clientsRouter has a connected Wi-Fi laptop (reserved, with a DNS name and
// a note), a connected wired NAS, an offline phone and a device blocked long ago.
func clientsRouter(t *testing.T) *fakerouter.Router {
	t.Helper()
	r := fakerouter.New()
	t.Cleanup(r.Close)
	seen := time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC)
	r.PutClient(fakerouter.Client{MAC: "aa:bb:cc:00:00:10", Name: "Laptop", Hostname: "laptop", UseFixedIP: true, FixedIP: "192.168.1.10",
		NetworkID: fakerouter.NetworkID, LocalDNSRecord: "laptop.home.internal", LocalDNSRecordEnabled: true, Note: "work"})
	r.PutActive(fakerouter.Status{MAC: "aa:bb:cc:00:00:10", IP: "192.168.1.10", DisplayName: "Laptop", Hostname: "laptop", Type: "WIRELESS",
		ESSID: "home", Radio: "6e", Signal: -61, UplinkName: "U7 Pro", TxBytes: 2.5e9, RxBytes: 1.2e8, Uptime: 3600,
		OUI: "Apple, Inc.", NetworkID: fakerouter.NetworkID})
	r.PutClient(fakerouter.Client{MAC: "aa:bb:cc:00:00:20", Hostname: "nas"})
	r.PutActive(fakerouter.Status{MAC: "aa:bb:cc:00:00:20", IP: "192.168.1.20", DisplayName: "aa:bb:cc:00:00:20", Hostname: "nas",
		Type: "WIRED", IsWired: true, UplinkName: "Dream Router 7", SwitchPort: 3, WiredRateMbps: 1000, Uptime: 86400, NetworkID: fakerouter.NetworkID})
	r.PutClient(fakerouter.Client{MAC: "aa:bb:cc:00:00:30", Hostname: "phone"})
	r.PutOffline(fakerouter.Status{MAC: "aa:bb:cc:00:00:30", LastIP: "192.168.1.30", DisplayName: "Phone 00:30", Hostname: "phone",
		Type: "WIRELESS", ESSID: "home", Radio: "ng", Signal: -70, Uptime: 99, LastSeen: float64(seen.Unix()), NetworkID: fakerouter.NetworkID})
	r.PutClient(fakerouter.Client{MAC: "aa:bb:cc:00:00:40", Name: "Old tablet", Blocked: true})
	return r
}

// fakeLocal pretends Terraform runs on a machine with these addresses.
func fakeLocal(t *testing.T, mac, ip string) {
	old := localAddrs
	localAddrs = func() ([]string, []string) { return []string{mac}, []string{"127.0.0.1", ip} }
	t.Cleanup(func() { localAddrs = old })
}

func TestClientsDataSource(t *testing.T) {
	r := clientsRouter(t)
	all := "data.dreamrouter_clients.all"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories,
		Steps: []resource.TestStep{{
			Config: fakeProviderConfig(r) + `
data "dreamrouter_clients" "all" {}
data "dreamrouter_clients" "online" {
  status = "online"
}
data "dreamrouter_clients" "wired" {
  connection_type = "wired"
}
data "dreamrouter_clients" "wifi_default" {
  connection_type = "wifi"
  network         = "default"
}
data "dreamrouter_clients" "blocked" {
  blocked = true
}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				// Connected, recently seen and blocked devices, sorted by IP (no IP last).
				resource.TestCheckResourceAttr(all, "clients.#", "4"),
				resource.TestCheckResourceAttr(all, "clients.0.mac", "aa:bb:cc:00:00:10"),
				resource.TestCheckResourceAttr(all, "clients.0.name", "Laptop"),
				resource.TestCheckResourceAttr(all, "clients.0.status", "online"),
				resource.TestCheckResourceAttr(all, "clients.0.connection", "wifi"),
				resource.TestCheckResourceAttr(all, "clients.0.ssid", "home"),
				resource.TestCheckResourceAttr(all, "clients.0.band", "6GHz"),
				resource.TestCheckResourceAttr(all, "clients.0.signal", "-61"),
				resource.TestCheckResourceAttr(all, "clients.0.uplink", "U7 Pro"),
				resource.TestCheckResourceAttr(all, "clients.0.uptime", "3600"),
				resource.TestCheckResourceAttr(all, "clients.0.download_bytes", "2500000000"),
				resource.TestCheckResourceAttr(all, "clients.0.upload_bytes", "120000000"),
				resource.TestCheckResourceAttr(all, "clients.0.vendor", "Apple, Inc."),
				resource.TestCheckResourceAttr(all, "clients.0.reserved", "true"),
				resource.TestCheckResourceAttr(all, "clients.0.dns_name", "laptop.home.internal"),
				resource.TestCheckResourceAttr(all, "clients.0.note", "work"),
				resource.TestCheckResourceAttr(all, "clients.0.blocked", "false"),
				// A MAC-only display name falls back to the host name.
				resource.TestCheckResourceAttr(all, "clients.1.name", "nas"),
				resource.TestCheckResourceAttr(all, "clients.1.connection", "wired"),
				resource.TestCheckResourceAttr(all, "clients.1.port", "3"),
				resource.TestCheckResourceAttr(all, "clients.1.link_mbps", "1000"),
				resource.TestCheckResourceAttr(all, "clients.1.ssid", ""),
				// Offline: last IP and last seen; no live signal or uptime.
				resource.TestCheckResourceAttr(all, "clients.2.name", "Phone"),
				resource.TestCheckResourceAttr(all, "clients.2.ip", "192.168.1.30"),
				resource.TestCheckResourceAttr(all, "clients.2.status", "offline"),
				resource.TestCheckResourceAttr(all, "clients.2.last_seen", "2026-10-03T08:00:00Z"),
				resource.TestCheckResourceAttr(all, "clients.2.signal", "0"),
				resource.TestCheckResourceAttr(all, "clients.2.uptime", "0"),
				// Blocked long ago: only a stored record.
				resource.TestCheckResourceAttr(all, "clients.3.name", "Old tablet"),
				resource.TestCheckResourceAttr(all, "clients.3.blocked", "true"),
				resource.TestCheckResourceAttr(all, "clients.3.connection", ""),
				resource.TestCheckResourceAttr(all, "clients.3.last_seen", ""),

				resource.TestCheckResourceAttr("data.dreamrouter_clients.online", "clients.#", "2"),
				resource.TestCheckResourceAttr("data.dreamrouter_clients.wired", "clients.#", "1"),
				resource.TestCheckResourceAttr("data.dreamrouter_clients.wired", "clients.0.mac", "aa:bb:cc:00:00:20"),
				resource.TestCheckResourceAttr("data.dreamrouter_clients.wifi_default", "clients.#", "2"),
				resource.TestCheckResourceAttr("data.dreamrouter_clients.blocked", "clients.#", "1"),
				resource.TestCheckResourceAttr("data.dreamrouter_clients.blocked", "clients.0.mac", "aa:bb:cc:00:00:40"),
			),
		}},
	})
}

func TestClientsDataSourceErrors(t *testing.T) {
	r := clientsRouter(t)
	for name, tc := range map[string]struct{ body, want string }{
		"bad status":     {`status = "away"`, `value must be one of`},
		"bad connection": {`connection_type = "fibre"`, `value must be one of`},
		"days too small": {`days = 0`, `must be between 1 and 365`},
		"no network":     {`network = "IoT"`, `router has no network named "IoT"`},
	} {
		t.Run(name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: providerFactories,
				Steps: []resource.TestStep{{
					Config:      fakeProviderConfig(r) + "data \"dreamrouter_clients\" \"x\" {\n  " + tc.body + "\n}\n",
					ExpectError: regexp.MustCompile(tc.want),
				}},
			})
		})
	}
}

func checkBlocked(r *fakerouter.Router, mac string, want bool) resource.TestCheckFunc {
	return func(*terraform.State) error {
		d, ok := r.Client(mac)
		if !ok {
			return fmt.Errorf("%s has no record", mac)
		}
		if d.Blocked != want {
			return fmt.Errorf("%s blocked = %v, want %v", mac, d.Blocked, want)
		}
		return nil
	}
}

func blockConfig(r *fakerouter.Router, mac string) string {
	return fakeProviderConfig(r) + fmt.Sprintf("resource \"dreamrouter_client_block\" \"x\" {\n  mac = %q\n}\n", mac)
}

func TestClientBlockLifecycle(t *testing.T) {
	r := clientsRouter(t)
	fakeLocal(t, "02:00:00:00:00:01", "192.168.1.99")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories,
		// Destroy unblocks, and keeps the device's record.
		CheckDestroy: checkBlocked(r, "aa:bb:cc:00:00:20", false),
		Steps: []resource.TestStep{
			{
				Config: blockConfig(r, "aa:bb:cc:00:00:20"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dreamrouter_client_block.x", "id", "aa:bb:cc:00:00:20"),
					checkBlocked(r, "aa:bb:cc:00:00:20", true),
				),
			},
			{
				// Unblocked in the web UI: the plan blocks it again.
				PreConfig: func() { r.ModifyClient("aa:bb:cc:00:00:20", func(c *fakerouter.Client) { c.Blocked = false }) },
				Config:    blockConfig(r, "aa:bb:cc:00:00:20"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("dreamrouter_client_block.x", plancheck.ResourceActionCreate),
				}},
				Check: checkBlocked(r, "aa:bb:cc:00:00:20", true),
			},
			{
				ResourceName:      "dreamrouter_client_block.x",
				ImportState:       true,
				ImportStateId:     "AA-BB-CC-00-00-20",
				ImportStateVerify: true,
			},
		},
	})
	if _, ok := r.Client("aa:bb:cc:00:00:20"); !ok {
		t.Fatal("destroy forgot a real device's record")
	}
}

func TestClientBlockUnknownDevice(t *testing.T) {
	r := clientsRouter(t)
	fakeLocal(t, "02:00:00:00:00:01", "192.168.1.99")
	before := len(r.Clients())
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories,
		// Destroy unblocks and removes the record the block created.
		CheckDestroy: func(*terraform.State) error {
			if _, ok := r.Client("aa:bb:cc:00:00:99"); ok {
				return fmt.Errorf("the record created for the unknown device was left behind")
			}
			if n := len(r.Clients()); n != before {
				return fmt.Errorf("client records changed from %d to %d", before, n)
			}
			return nil
		},
		Steps: []resource.TestStep{{
			Config: blockConfig(r, "aa:bb:cc:00:00:99"),
			Check:  checkBlocked(r, "aa:bb:cc:00:00:99", true),
		}},
	})
}

func TestClientBlockRefusals(t *testing.T) {
	r := clientsRouter(t)
	// The machine running Terraform, by MAC (even unknown to the router) or by IP.
	for name, tc := range map[string]struct{ localMAC, localIP, block string }{
		"by MAC":         {"aa:bb:cc:00:00:10", "10.0.0.5", "aa:bb:cc:00:00:10"},
		"by IP":          {"02:00:00:00:00:01", "192.168.1.20", "aa:bb:cc:00:00:20"},
		"unknown by MAC": {"aa:bb:cc:00:00:97", "10.0.0.5", "aa:bb:cc:00:00:97"},
	} {
		t.Run(name, func(t *testing.T) {
			fakeLocal(t, tc.localMAC, tc.localIP)
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: providerFactories,
				Steps: []resource.TestStep{{
					Config:      blockConfig(r, tc.block),
					ExpectError: regexp.MustCompile(`is the machine Terraform is running on`),
				}},
			})
			if d, ok := r.Client(tc.block); ok && d.Blocked {
				t.Fatalf("%s was blocked", tc.block)
			}
		})
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories,
		Steps: []resource.TestStep{{
			Config:      blockConfig(r, "AA:BB:CC:00:00:20"),
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`write the MAC address as "aa:bb:cc:00:00:20"`),
		}},
	})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories,
		Steps: []resource.TestStep{{
			Config:        blockConfig(r, "aa:bb:cc:00:00:20"),
			ResourceName:  "dreamrouter_client_block.x",
			ImportState:   true,
			ImportStateId: "aa:bb:cc:00:00:20",
			ExpectError:   regexp.MustCompile(`No blocked device has MAC aa:bb:cc:00:00:20`),
		}},
	})
}

func TestClientBlockKeepsStoredDevice(t *testing.T) {
	// A device the router only has a stored record for (not seen recently)
	// keeps it on destroy: only empty, never-connected records are removed.
	r := clientsRouter(t)
	fakeLocal(t, "02:00:00:00:00:01", "192.168.1.99")
	r.PutClient(fakerouter.Client{MAC: "aa:bb:cc:00:00:50", Hostname: "printer"})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories,
		CheckDestroy:             checkBlocked(r, "aa:bb:cc:00:00:50", false),
		Steps: []resource.TestStep{{
			Config: blockConfig(r, "aa:bb:cc:00:00:50"),
			Check:  checkBlocked(r, "aa:bb:cc:00:00:50", true),
		}},
	})
}
