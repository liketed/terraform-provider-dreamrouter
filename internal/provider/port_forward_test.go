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

func checkForward(r *fakerouter.Router, name string, want map[string]any) resource.TestCheckFunc {
	return func(*terraform.State) error {
		for _, f := range r.PortForwards() {
			if f["name"] == name {
				for k, v := range want {
					if f[k] != v {
						return fmt.Errorf("forward %s: %s = %v, want %v (%v)", name, k, f[k], v, f)
					}
				}
				return nil
			}
		}
		return fmt.Errorf("no forward named %s", name)
	}
}

func checkNoForwards(r *fakerouter.Router) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if n := len(r.PortForwards()); n != 0 {
			return fmt.Errorf("%d port forwards left: %v", n, r.PortForwards())
		}
		return nil
	}
}

func TestPortForwardLifecycle(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	cfg := func(body string) string {
		return fakeProviderConfig(r) + "resource \"dreamrouter_port_forward\" \"web\" {\n  name       = \"web\"\n  forward_ip = \"192.168.1.20\"\n" + body + "}\n"
	}
	pf := "dreamrouter_port_forward.web"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories,
		CheckDestroy:             checkNoForwards(r),
		Steps: []resource.TestStep{
			{
				// Defaults: enabled, both protocols, from anywhere, first WAN, forward port = port.
				Config: cfg("  port = \"8443\"\n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(pf, "id"),
					resource.TestCheckResourceAttr(pf, "protocol", "tcp_udp"),
					resource.TestCheckResourceAttr(pf, "source", "any"),
					resource.TestCheckResourceAttr(pf, "wan", "wan"),
					resource.TestCheckResourceAttr(pf, "enabled", "true"),
					resource.TestCheckResourceAttr(pf, "log", "false"),
					resource.TestCheckNoResourceAttr(pf, "forward_port"),
					checkForward(r, "web", map[string]any{"dst_port": "8443", "fwd_port": "8443", "fwd": "192.168.1.20",
						"enabled": true, "proto": "tcp_udp", "src": "any", "pfwd_interface": "wan", "log": false}),
				),
			},
			{
				// Without forward_port, the forward port follows the port.
				Config: cfg("  port = \"9443\"\n"),
				Check:  checkForward(r, "web", map[string]any{"dst_port": "9443", "fwd_port": "9443"}),
			},
			{
				Config: cfg("  port = \"9443\"\n  forward_port = \"443\"\n  protocol = \"tcp\"\n  source = \"203.0.113.0/24\"\n  enabled = false\n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(pf, "forward_port", "443"),
					checkForward(r, "web", map[string]any{"fwd_port": "443", "proto": "tcp", "src": "203.0.113.0/24", "enabled": false}),
				),
			},
			{
				// The plan is stable: nothing to change after an apply.
				Config: cfg("  port = \"9443\"\n  forward_port = \"443\"\n  protocol = \"tcp\"\n  source = \"203.0.113.0/24\"\n  enabled = false\n"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(pf, plancheck.ResourceActionNoop),
				}},
			},
			{
				ResourceName:      pf,
				ImportState:       true,
				ImportStateId:     "WEB",
				ImportStateVerify: true,
			},
		},
	})
}

func TestPortForwardDrift(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	cfg := fakeProviderConfig(r) + `
resource "dreamrouter_port_forward" "ssh" {
  name         = "ssh"
  port         = "2222"
  forward_ip   = "192.168.1.20"
  forward_port = "22"
  protocol     = "tcp"
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories,
		CheckDestroy:             checkNoForwards(r),
		Steps: []resource.TestStep{
			{Config: cfg, Check: checkForward(r, "ssh", map[string]any{"fwd_port": "22"})},
			{
				// Someone points it elsewhere in the web UI: the plan sets it back.
				PreConfig: func() {
					for _, f := range r.PortForwards() {
						id := f["_id"].(string)
						r.ModifyPortForward(id, func(m map[string]any) { m["fwd"] = "192.168.1.99"; m["enabled"] = false })
					}
				},
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("dreamrouter_port_forward.ssh", plancheck.ResourceActionUpdate),
				}},
				Check: checkForward(r, "ssh", map[string]any{"fwd": "192.168.1.20", "enabled": true}),
			},
			{
				// Deleted in the web UI: the plan creates it again.
				PreConfig: func() {
					for _, f := range r.PortForwards() {
						r.RemovePortForward(f["_id"].(string))
					}
				},
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("dreamrouter_port_forward.ssh", plancheck.ResourceActionCreate),
				}},
				Check: checkForward(r, "ssh", map[string]any{"fwd_port": "22", "enabled": true}),
			},
		},
	})
}

func TestPortForwardValidation(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	r.PutPortForward(map[string]any{"name": "Minecraft", "enabled": true, "pfwd_interface": "wan", "src": "any",
		"dst_port": "25565", "fwd": "192.168.1.50", "fwd_port": "25565", "proto": "tcp", "log": false})
	for name, tc := range map[string]struct {
		body     string
		want     string
		planOnly bool
	}{
		"spaces in port":       {`port = "80, 443"`, `write "80, 443" without spaces: "80,443"`, true},
		"reversed range":       {`port = "40090-40080"`, `reversed; write it as 40080-40090`, true},
		"port out of range":    {`port = "70000"`, `outside 1–65535`, true},
		"range to other range": {"port = \"40010-40020\"\n forward_port = \"50010-50020\"", `can only be forwarded to the same ports`, true},
		"source not canonical": {"port = \"1\"\n source = \"203.0.113.9/24\"", `write the source as "203.0.113.0/24"`, true},
		"source invalid":       {"port = \"1\"\n source = \"nope\"", `source "nope"`, true},
		"bad protocol":         {"port = \"1\"\n protocol = \"icmp\"", `value must be one of`, true},
		"bad forward ip":       {"port = \"1\"\n forward_ip = \"nas.local\"", `not an IPv4 address`, true},
		"outside the LANs":     {"port = \"1\"\n forward_ip = \"8.8.8.8\"", `forward address: 8.8.8.8 is not in any network`, false},
		"the router itself":    {"port = \"1\"\n forward_ip = \"192.168.1.1\"", `router's own address`, false},
		"second WAN missing":   {"port = \"1\"\n wan = \"wan2\"", `needs a second internet connection`, false},
		"port already used":    {`port = "25560-25570"`, `already forwarded by "Minecraft"`, false},
		"name already used":    {"port = \"1\"\n name = \"minecraft\"", `named "Minecraft" already exists`, false},
	} {
		t.Run(name, func(t *testing.T) {
			body := tc.body
			if !regexp.MustCompile(`forward_ip`).MatchString(body) {
				body += "\n forward_ip = \"192.168.1.20\""
			}
			if !regexp.MustCompile(`\bname\b`).MatchString(body) {
				body += "\n name = \"x\""
			}
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: providerFactories,
				Steps: []resource.TestStep{{
					Config:      fakeProviderConfig(r) + "resource \"dreamrouter_port_forward\" \"x\" {\n " + body + "\n}\n",
					PlanOnly:    tc.planOnly,
					ExpectError: regexp.MustCompile(tc.want),
				}},
			})
		})
	}
	if n := len(r.PortForwards()); n != 1 {
		t.Fatalf("invalid rules reached the router: %v", r.PortForwards())
	}
}

func TestPortForwardsDataSource(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	r.PutPortForward(map[string]any{"name": "web", "enabled": true, "pfwd_interface": "wan", "src": "any",
		"dst_port": "8443", "fwd": "192.168.1.20", "fwd_port": "443", "proto": "tcp", "log": false})
	// Made in the web UI without a forward port.
	r.PutPortForward(map[string]any{"name": "Games", "enabled": false, "pfwd_interface": "wan", "src": "any",
		"dst_port": "27000-27010", "fwd": "192.168.1.51", "proto": "udp", "log": true})
	ds := "data.dreamrouter_port_forwards.all"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories,
		Steps: []resource.TestStep{{
			Config: fakeProviderConfig(r) + `data "dreamrouter_port_forwards" "all" {}`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(ds, "port_forwards.#", "2"),
				resource.TestCheckResourceAttr(ds, "port_forwards.0.name", "Games"),
				resource.TestCheckResourceAttr(ds, "port_forwards.0.forward_port", "27000-27010"),
				resource.TestCheckResourceAttr(ds, "port_forwards.0.enabled", "false"),
				resource.TestCheckResourceAttr(ds, "port_forwards.0.log", "true"),
				resource.TestCheckResourceAttr(ds, "port_forwards.1.name", "web"),
				resource.TestCheckResourceAttr(ds, "port_forwards.1.forward_port", "443"),
				resource.TestCheckResourceAttr(ds, "port_forwards.1.protocol", "tcp"),
			),
		}},
	})
}
