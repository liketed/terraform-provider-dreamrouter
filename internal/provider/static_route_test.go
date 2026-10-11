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

func routeConfig(r *fakerouter.Router, hop, extra string) string {
	return fakeProviderConfig(r) + `
resource "dreamrouter_static_route" "cudy" {
  name     = "cudy"
  network  = "192.168.2.0/24"
  next_hop = "` + hop + `"
` + extra + `}
data "dreamrouter_static_routes" "all" {
  depends_on = [dreamrouter_static_route.cudy]
}
`
}

func TestStaticRouteResource(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	res := "dreamrouter_static_route.cudy"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories,
		CheckDestroy: func(*terraform.State) error {
			if n := len(r.Routes()); n != 0 {
				return fmt.Errorf("%d routes left", n)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: routeConfig(r, "192.168.1.4", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(res, "id"),
					resource.TestCheckResourceAttr(res, "distance", "1"),
					resource.TestCheckResourceAttr(res, "enabled", "true"),
					resource.TestCheckResourceAttr("data.dreamrouter_static_routes.all", "routes.#", "1"),
					resource.TestCheckResourceAttr("data.dreamrouter_static_routes.all", "routes.0.next_hop", "192.168.1.4"),
					func(*terraform.State) error {
						rt := r.Routes()[0]
						if rt["static-route_network"] != "192.168.2.0/24" || rt["static-route_nexthop"] != "192.168.1.4" || rt["static-route_type"] != "nexthop-route" {
							return fmt.Errorf("stored: %v", rt)
						}
						return nil
					},
				),
			},
			{
				// In-place change: another next hop, a distance, disabled.
				Config: routeConfig(r, "192.168.1.5", "  distance = 10\n  enabled = false\n"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(res, plancheck.ResourceActionUpdate),
				}},
				Check: func(*terraform.State) error {
					if rt := r.Routes()[0]; rt["static-route_nexthop"] != "192.168.1.5" || rt["static-route_distance"] != 10.0 || rt["enabled"] != false {
						return fmt.Errorf("stored: %v", rt)
					}
					return nil
				},
			},
			{
				// Deleted in the web UI: created again.
				PreConfig: func() {
					c := newRouteClient(t, r)
					for _, rt := range r.Routes() {
						if err := c.DeleteRoute(t.Context(), rt["_id"].(string)); err != nil {
							t.Fatal(err)
						}
					}
				},
				Config: routeConfig(r, "192.168.1.5", "  distance = 10\n  enabled = false\n"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(res, plancheck.ResourceActionCreate),
				}},
			},
			{ResourceName: res, ImportState: true, ImportStateId: "192.168.2.0/24", ImportStateVerify: true},
			{ResourceName: res, ImportState: true, ImportStateId: "CUDY", ImportStateVerify: true},
		},
	})
}

func TestStaticRouteValidation(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	for name, tc := range map[string]struct{ net, hop, want string }{
		"not network form":   {"192.168.2.5/24", "192.168.1.4", `write the network as "192.168.2.0/24"`},
		"default route":      {"0.0.0.0/0", "192.168.1.4", `default route`},
		"not a network":      {"nope", "192.168.1.4", `must be a network`},
		"unreachable hop":    {"192.168.2.0/24", "8.8.8.8", `must be a router on one of your networks`},
		"router as next hop": {"192.168.2.0/24", "192.168.1.1", `router's own address`},
		"own network":        {"192.168.1.0/24", "192.168.1.4", `overlaps the router's own network "Default"`},
		"next hop not IPv4":  {"192.168.2.0/24", "cudy.local", `not an IPv4 address`},
	} {
		t.Run(name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: providerFactories,
				Steps: []resource.TestStep{{
					Config:      fakeProviderConfig(r) + fmt.Sprintf("resource \"dreamrouter_static_route\" \"x\" {\n  name = \"x\"\n  network = %q\n  next_hop = %q\n}\n", tc.net, tc.hop),
					ExpectError: regexp.MustCompile(tc.want),
				}},
			})
		})
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories,
		Steps: []resource.TestStep{{
			Config: fakeProviderConfig(r) + `
resource "dreamrouter_static_route" "a" {
  name     = "cudy"
  network  = "192.168.2.0/24"
  next_hop = "192.168.1.4"
}
resource "dreamrouter_static_route" "b" {
  name       = "CUDY"
  network    = "192.168.3.0/24"
  next_hop   = "192.168.1.4"
  depends_on = [dreamrouter_static_route.a]
}
`,
			ExpectError: regexp.MustCompile(`named "cudy" already exists`),
		}},
	})
	if n := len(r.Routes()); n != 0 {
		t.Fatalf("invalid routes left on the router: %v", r.Routes())
	}
}

func newRouteClient(t *testing.T, r *fakerouter.Router) *unifi.Client {
	t.Helper()
	c, err := unifi.New(unifi.Config{Host: r.Host(), Username: fakerouter.Username, Password: fakerouter.Password, InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
