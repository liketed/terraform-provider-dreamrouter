package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/liketed/dreamrouter-go/fakerouter"
	"github.com/liketed/dreamrouter-go/unifi"
)

func TestSSHResource(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	cfg := func(body string) string {
		return fakeProviderConfig(r) + "resource \"dreamrouter_ssh\" \"this\" {\n" + body + "}\n"
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories,
		// Destroying leaves SSH as it was last set.
		CheckDestroy: func(*terraform.State) error {
			if r.RouterSSH() {
				return fmt.Errorf("destroy changed router SSH")
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				// Only router SSH is managed; device SSH is left alone.
				Config: cfg("  router = false\n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dreamrouter_ssh.this", "id", "ssh"),
					resource.TestCheckResourceAttr("dreamrouter_ssh.this", "router", "false"),
					resource.TestCheckNoResourceAttr("dreamrouter_ssh.this", "devices"),
					func(*terraform.State) error {
						if r.RouterSSH() {
							return fmt.Errorf("router SSH still on")
						}
						if _, writes := r.Mgmt(); writes != 0 {
							return fmt.Errorf("device SSH written %d times though not managed", writes)
						}
						return nil
					},
				),
			},
			{
				// Turned on in the web UI: the plan turns it off again.
				PreConfig: func() { setRouterSSH(t, r, true) },
				Config:    cfg("  router = false\n"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("dreamrouter_ssh.this", plancheck.ResourceActionUpdate),
				}},
				Check: func(*terraform.State) error {
					if r.RouterSSH() {
						return fmt.Errorf("router SSH not turned off again")
					}
					return nil
				},
			},
			{
				// Managing device SSH too: already on, so nothing is written.
				Config: cfg("  router  = false\n  devices = true\n"),
				Check: func(*terraform.State) error {
					if _, writes := r.Mgmt(); writes != 0 {
						return fmt.Errorf("device SSH written %d times for no change", writes)
					}
					return nil
				},
			},
			{
				Config: cfg("  router  = false\n  devices = false\n"),
				Check: func(*terraform.State) error {
					if m, writes := r.Mgmt(); m["x_ssh_enabled"] != false || writes != 1 {
						return fmt.Errorf("device SSH: %v, %d writes", m["x_ssh_enabled"], writes)
					}
					return nil
				},
			},
			{
				ResourceName:      "dreamrouter_ssh.this",
				ImportState:       true,
				ImportStateId:     "ssh",
				ImportStateVerify: true,
			},
		},
	})
}

// setRouterSSH changes router SSH as the web UI would.
func setRouterSSH(t *testing.T, r *fakerouter.Router, on bool) {
	t.Helper()
	c, err := unifi.New(unifi.Config{Host: r.Host(), Username: fakerouter.Username, Password: fakerouter.Password, InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetRouterSSH(t.Context(), on); err != nil {
		t.Fatal(err)
	}
}

func TestSSHResourceNeedsSomething(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories,
		Steps: []resource.TestStep{{
			Config:      fakeProviderConfig(r) + `resource "dreamrouter_ssh" "x" {}`,
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`Set "router", "devices" or both`),
		}},
	})
}
