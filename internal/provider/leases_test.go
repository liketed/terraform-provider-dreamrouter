package provider

import (
	"regexp"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/liketed/dreamrouter-go/fakerouter"
)

func TestLeasesDataSource(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	exp := time.Date(2026, 10, 3, 20, 41, 0, 0, time.UTC)
	r.PutLease(fakerouter.Lease{IP: "192.168.1.37", MAC: "aa:bb:cc:00:00:37", Hostname: "tv", DisplayName: "Samsung TV 00:37",
		OUI: "Samsung", Status: "online", ClientType: "WIRED", ExpiresUnix: float64(exp.Unix())})
	r.PutLease(fakerouter.Lease{IP: "192.168.1.6", MAC: "aa:bb:cc:00:00:06", Hostname: "macbook", OUI: "Apple", Status: "online", ClientType: "WIRELESS"})
	r.PutLease(fakerouter.Lease{IP: "192.168.1.88", MAC: "aa:bb:cc:00:00:88", Hostname: "old-laptop", Status: "offline"})
	r.PutClient(fakerouter.Client{MAC: "aa:bb:cc:00:00:37", UseFixedIP: true, FixedIP: "192.168.1.37", NetworkID: fakerouter.NetworkID,
		LocalDNSRecord: "tv.home.internal", LocalDNSRecordEnabled: true})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories,
		Steps: []resource.TestStep{
			{
				Config: fakeProviderConfig(r) + `
data "dreamrouter_leases" "all" {}
data "dreamrouter_leases" "online" {
  network = "default"
  status  = "online"
}
locals {
  tv = one([for l in data.dreamrouter_leases.all.leases : l if l.hostname == "tv"])
}
output "tv_mac" { value = local.tv.mac }
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.dreamrouter_leases.all", "leases.#", "3"),
					// Sorted by IP: .6 before .37 before .88.
					resource.TestCheckResourceAttr("data.dreamrouter_leases.all", "leases.0.ip", "192.168.1.6"),
					resource.TestCheckResourceAttr("data.dreamrouter_leases.all", "leases.1.ip", "192.168.1.37"),
					resource.TestCheckResourceAttr("data.dreamrouter_leases.all", "leases.1.name", "Samsung TV"),
					resource.TestCheckResourceAttr("data.dreamrouter_leases.all", "leases.1.vendor", "Samsung"),
					resource.TestCheckResourceAttr("data.dreamrouter_leases.all", "leases.1.connection", "wired"),
					resource.TestCheckResourceAttr("data.dreamrouter_leases.all", "leases.1.expires", "2026-10-03T20:41:00Z"),
					resource.TestCheckResourceAttr("data.dreamrouter_leases.all", "leases.1.reserved", "true"),
					resource.TestCheckResourceAttr("data.dreamrouter_leases.all", "leases.1.dns_name", "tv.home.internal"),
					resource.TestCheckResourceAttr("data.dreamrouter_leases.all", "leases.0.expires", ""),
					resource.TestCheckResourceAttr("data.dreamrouter_leases.all", "leases.2.status", "offline"),
					resource.TestCheckResourceAttr("data.dreamrouter_leases.online", "leases.#", "2"),
					resource.TestCheckOutput("tv_mac", "aa:bb:cc:00:00:37"),
				),
			},
		},
	})
}

func TestLeasesDataSourceErrors(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	for name, tc := range map[string]struct{ cfg, want string }{
		"unknown network": {`data "dreamrouter_leases" "x" { network = "IoT" }`, `router has no network named "IoT"`},
		"bad status":      {`data "dreamrouter_leases" "x" { status = "away" }`, `value must be one of`},
	} {
		t.Run(name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: providerFactories,
				Steps:                    []resource.TestStep{{Config: fakeProviderConfig(r) + tc.cfg, ExpectError: regexp.MustCompile(tc.want)}},
			})
		})
	}
}
