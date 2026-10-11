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

func fastWANWait(t *testing.T) {
	old := wanWait
	wanWait.settle, wanWait.interval, wanWait.timeout = 0, 10*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { wanWait = old })
}

func wanTestRouter(t *testing.T) *fakerouter.Router {
	r := fakerouter.New()
	t.Cleanup(r.Close)
	r.AddWAN2()
	r.AcceptPPPoE("eir@eir.ie")
	fastWANWait(t)
	fakeLocal(t, "02:00:00:00:00:01", "192.168.1.99")
	return r
}

func eirConfig(r *fakerouter.Router, extra string) string {
	return fakeProviderConfig(r) + `
resource "dreamrouter_wan" "eir" {
  name     = "Internet 2"
  type     = "pppoe"
  username = "eir@eir.ie"
  password = "broadband1"
  vlan     = 10
  port     = "Port 3"
` + extra + `}
data "dreamrouter_wans" "all" {
  depends_on = [dreamrouter_wan.eir]
}
data "dreamrouter_ports" "all" {
  depends_on = [dreamrouter_wan.eir]
}
`
}

func TestWANResourceLikeEir(t *testing.T) {
	r := wanTestRouter(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories,
		// Destroying leaves the connection as it is.
		CheckDestroy: func(*terraform.State) error {
			if n := r.Network("Internet 2"); n["wan_username"] != "eir@eir.ie" {
				return fmt.Errorf("destroy changed the connection: %v", n)
			}
			if roles, _ := r.PortRoles(); roles["eth2"] != "WAN2" {
				return fmt.Errorf("destroy moved the port: %v", roles)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: eirConfig(r, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					func(*terraform.State) error {
						n := r.Network("Internet 2")
						if n["wan_type"] != "pppoe" || n["wan_username"] != "eir@eir.ie" || n["x_wan_password"] != "broadband1" || n["wan_vlan"] != 10.0 {
							return fmt.Errorf("stored: %v", n)
						}
						if roles, _ := r.PortRoles(); roles["eth2"] != "WAN2" || roles["eth4"] != "LAN" || roles["eth3"] != "WAN" {
							return fmt.Errorf("roles: %v", roles)
						}
						return nil
					},
					resource.TestCheckResourceAttr("data.dreamrouter_wans.all", "wans.#", "2"),
					resource.TestCheckResourceAttr("data.dreamrouter_wans.all", "wans.0.role", "primary"),
					resource.TestCheckResourceAttr("data.dreamrouter_wans.all", "wans.1.port", "Port 3"),
					resource.TestCheckResourceAttr("data.dreamrouter_wans.all", "wans.1.up", "true"),
					resource.TestCheckResourceAttr("data.dreamrouter_wans.all", "wans.1.ip", "198.51.100.9"),
					resource.TestCheckResourceAttr("data.dreamrouter_wans.all", "wans.1.role", "failover"),
					resource.TestCheckResourceAttr("data.dreamrouter_ports.all", "ports.2.role", "WAN2"),
					resource.TestCheckResourceAttr("data.dreamrouter_ports.all", "ports.4.role", "LAN"),
				),
			},
			{
				// Password and port changed outside Terraform: set back.
				PreConfig: func() {
					r.ModifyNetwork("Internet 2", func(n map[string]any) { n["x_wan_password"] = "changed-in-ui" })
				},
				Config: eirConfig(r, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("dreamrouter_wan.eir", plancheck.ResourceActionUpdate),
				}},
				Check: func(*terraform.State) error {
					if r.Network("Internet 2")["x_wan_password"] != "broadband1" {
						return fmt.Errorf("password not set back")
					}
					return nil
				},
			},
			{
				Config: eirConfig(r, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("dreamrouter_wan.eir", plancheck.ResourceActionNoop),
				}},
			},
			{ResourceName: "dreamrouter_wan.eir", ImportState: true, ImportStateId: "Internet 2", ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"force_port"}},
		},
	})
}

func TestWANResourceRollsBack(t *testing.T) {
	r := wanTestRouter(t)
	before := r.Network("Internet 1")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories,
		Steps: []resource.TestStep{
			{
				// A login the provider rejects: Internet 1 doesn't come back,
				// so the previous settings are put back.
				Config: fakeProviderConfig(r) + `
resource "dreamrouter_wan" "primary" {
  name     = "Internet 1"
  type     = "pppoe"
  username = "typo@isp.example"
  password = "wrong"
  vlan     = 10
}
`,
				ExpectError: regexp.MustCompile(`(?s)Internet 1 didn't come back within 300ms with the new settings; put the\s+previous settings back`),
			},
		},
	})
	if n := r.Network("Internet 1"); n["wan_username"] != before["wan_username"] || n["x_wan_password"] != before["x_wan_password"] {
		t.Fatalf("not rolled back: %v", n)
	}
}

func TestWANResourcePortSafeguards(t *testing.T) {
	r := wanTestRouter(t)
	r.PutActive(fakerouter.Status{MAC: "aa:bb:cc:00:00:33", IP: "192.168.1.33", Type: "WIRED", IsWired: true, UplinkName: "Dream Router 7", SwitchPort: 3})
	cfg := func(port, extra string) string {
		return fakeProviderConfig(r) + `
resource "dreamrouter_wan" "eir" {
  name = "Internet 2"
  type = "dhcp"
  port = "` + port + `"
` + extra + `}
`
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories,
		Steps: []resource.TestStep{
			{Config: cfg("Port 3", ""), ExpectError: regexp.MustCompile(`LAN devices are connected to Port 3`)},
			{Config: cfg("Port 4", ""), ExpectError: regexp.MustCompile(`Port 4 is used by another internet connection`)},
			{Config: cfg("Port 9", ""), ExpectError: regexp.MustCompile(`no port "Port 9"`)},
			{Config: cfg("Port 3", "  force_port = true\n"), Check: func(*terraform.State) error {
				if roles, _ := r.PortRoles(); roles["eth2"] != "WAN2" {
					return fmt.Errorf("not moved with force_port: %v", roles)
				}
				return nil
			}},
		},
	})
}

func TestWANResourceValidation(t *testing.T) {
	r := wanTestRouter(t)
	for name, tc := range map[string]struct{ body, want string }{
		"pppoe without password": {"type = \"pppoe\"\nusername = \"x\"", `needs username and password`},
		"static without gateway": {"type = \"static\"\nip = \"203.0.113.10/24\"", `needs ip and gateway`},
		"static bad gateway":     {"type = \"static\"\nip = \"203.0.113.10/24\"\ngateway = \"198.51.100.1\"", `must be another address in 203.0.113.0/24`},
		"VLAN 4095":              {"type = \"dhcp\"\nvlan = 4095", `between 1 and 4094`},
		"bad type":               {"type = \"cable\"", `value must be one of`},
		"DNS host name":          {"type = \"dhcp\"\ndns_servers = [\"dns.google\"]", `must be an IPv4 address`},
		"unknown connection":     {"type = \"dhcp\"\nname = \"Internet 3\"", `no internet connection named "Internet 3"`},
	} {
		t.Run(name, func(t *testing.T) {
			body := tc.body
			if !regexp.MustCompile(`\bname\b`).MatchString(body) {
				body = "name = \"Internet 2\"\n" + body
			}
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: providerFactories,
				Steps:                    []resource.TestStep{{Config: fakeProviderConfig(r) + "resource \"dreamrouter_wan\" \"x\" {\n" + body + "\n}\n", ExpectError: regexp.MustCompile(tc.want)}},
			})
		})
	}
	if n := r.Network("Internet 2"); n["wan_type"] != "dhcp" {
		t.Fatalf("invalid settings reached the router: %v", n)
	}
}
