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

func checkNetwork(r *fakerouter.Router, want map[string]any) resource.TestCheckFunc {
	return func(*terraform.State) error {
		n := r.Network("Default")
		for k, v := range want {
			if n[k] != v {
				return fmt.Errorf("network %s = %v, want %v (network: %v)", k, n[k], v, n)
			}
		}
		return nil
	}
}

func TestNetworkDHCPLifecycle(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	cfg := func(body string) string {
		return fakeProviderConfig(r) + "resource \"dreamrouter_network_dhcp\" \"lan\" {\n  network = \"default\"\n" + body + "}\n"
	}
	boot := func(file string) string {
		return fmt.Sprintf("  boot = {\n    server = \"192.168.1.20\"\n    file   = %q\n  }\n", file)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories,
		CheckDestroy: checkNetwork(r, map[string]any{"dhcpd_boot_enabled": false, "dhcpd_tftp_server": "", "dhcpd_boot_server": "",
			"dhcpd_start": "192.168.1.6", "name": "Default"}),
		Steps: []resource.TestStep{
			{
				Config: cfg(boot("netboot.xyz.efi") + "  tftp_server = \"tftp.home.internal\"\n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dreamrouter_network_dhcp.lan", "id", fakerouter.NetworkID),
					resource.TestCheckResourceAttr("dreamrouter_network_dhcp.lan", "boot.file", "netboot.xyz.efi"),
					checkNetwork(r, map[string]any{"dhcpd_boot_enabled": true, "dhcpd_boot_server": "192.168.1.20",
						"dhcpd_boot_filename": "netboot.xyz.efi", "dhcpd_tftp_server": "tftp.home.internal", "dhcpd_start": "192.168.1.6"}),
				),
			},
			{
				// Changing the file and dropping tftp_server are in-place updates.
				Config: cfg(boot("ipxe.efi")),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("dreamrouter_network_dhcp.lan", plancheck.ResourceActionUpdate),
				}},
				Check: checkNetwork(r, map[string]any{"dhcpd_boot_filename": "ipxe.efi", "dhcpd_tftp_server": ""}),
			},
			{
				// Someone changes the boot server in the web UI: set back.
				PreConfig: func() {
					r.ModifyNetwork("Default", func(n map[string]any) { n["dhcpd_boot_server"] = "192.168.1.99" })
				},
				Config: cfg(boot("ipxe.efi")),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("dreamrouter_network_dhcp.lan", plancheck.ResourceActionUpdate),
				}},
				Check: checkNetwork(r, map[string]any{"dhcpd_boot_server": "192.168.1.20"}),
			},
			{
				ResourceName:      "dreamrouter_network_dhcp.lan",
				ImportState:       true,
				ImportStateId:     "Default",
				ImportStateVerify: true,
				// The import uses the router's spelling of the name.
				ImportStateVerifyIgnore: []string{"network"},
			},
			{
				// Removing boot turns network boot off; the router keeps the file.
				Config: cfg(""),
				Check:  checkNetwork(r, map[string]any{"dhcpd_boot_enabled": false, "dhcpd_boot_filename": "ipxe.efi"}),
			},
		},
	})
}

func TestNetworkDHCPValidation(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	for name, tc := range map[string]struct{ body, want string }{
		"server host name": {`boot = { server = "boot.home.internal", file = "a.efi" }`, `boot server "boot.home.internal" must be an IPv4 address`},
		"file with comma":  {`boot = { server = "192.168.1.20", file = "a,b.efi" }`, `spaces or commas`},
		"file with space":  {`boot = { server = "192.168.1.20", file = "a b.efi" }`, `spaces or commas`},
		"tftp with comma":  {`tftp_server = "a,b"`, `without spaces or commas`},
		"file missing":     {`boot = { server = "192.168.1.20" }`, `(?s)attribute "file" is required`},
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
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories,
		Steps: []resource.TestStep{{
			Config:      fakeProviderConfig(r) + `resource "dreamrouter_network_dhcp" "x" { network = "IoT" }`,
			ExpectError: regexp.MustCompile(`router has no network named "IoT" \(networks: Default 192\.168\.1\.1/24\)`),
		}},
	})
	if n := r.Network("Default"); n["dhcpd_boot_enabled"] != nil {
		t.Fatalf("validation failures reached the router: %v", n)
	}
}
