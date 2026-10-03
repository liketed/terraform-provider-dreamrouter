package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/liketed/dreamrouter-go/fakerouter"
)

func TestStatusDataSource(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	s := "data.dreamrouter_status.router"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories,
		Steps: []resource.TestStep{
			{
				Config: fakeProviderConfig(r) + `
data "dreamrouter_status" "router" {}
output "wan_ip" { value = data.dreamrouter_status.router.wan_ip }
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(s, "name", "Dream Router 7"),
					resource.TestCheckResourceAttr(s, "model", "UDMA67A"),
					resource.TestCheckResourceAttr(s, "os_version", "5.1.33"),
					resource.TestCheckResourceAttr(s, "network_version", "10.6.106"),
					resource.TestCheckResourceAttr(s, "uptime", "581855"),
					resource.TestCheckResourceAttr(s, "internet_status", "ok"),
					resource.TestCheckResourceAttr(s, "wan_up", "true"),
					resource.TestCheckResourceAttr(s, "isp", "Example ISP"),
					resource.TestCheckResourceAttr(s, "asn", "64500"),
					resource.TestCheckResourceAttr(s, "wan_interface", "eth3"),
					resource.TestCheckResourceAttr(s, "wan_link_mbps", "2500"),
					resource.TestCheckResourceAttr(s, "pppoe", "true"),
					resource.TestCheckResourceAttr(s, "latency_ms", "15"),
					resource.TestCheckResourceAttr(s, "cpu_percent", "13.9"),
					resource.TestCheckResourceAttr(s, "cpu_temperature", "60.5"),
					resource.TestCheckResourceAttr(s, "clients", "42"),
					resource.TestCheckResourceAttr(s, "wifi_clients", "22"),
					resource.TestCheckResourceAttr(s, "access_points", "1"),
					resource.TestCheckResourceAttr(s, "update_available", "false"),
					resource.TestCheckResourceAttr(s, "devices.#", "2"),
					resource.TestCheckResourceAttr(s, "devices.1.name", "U7 Pro"),
					resource.TestCheckResourceAttr(s, "devices.1.type", "uap"),
					resource.TestCheckResourceAttr(s, "devices.1.online", "true"),
					resource.TestCheckResourceAttr(s, "speedtest_run", ""),
					resource.TestCheckOutput("wan_ip", "203.0.113.7"),
				),
			},
			{
				PreConfig: func() {
					r.ModifyStat(func(st *fakerouter.Stat) {
						st.Devices[1]["upgradable"], st.Devices[1]["upgrade_to_firmware"] = true, "8.8.0.1"
						st.Devices[0]["speedtest-status"] = map[string]any{"rundate": 1791013530, "xput_download": 2210.4, "xput_upload": 105.2}
					})
				},
				Config: fakeProviderConfig(r) + `data "dreamrouter_status" "router" {}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(s, "update_available", "true"),
					resource.TestCheckResourceAttr(s, "devices.1.upgradable", "true"),
					resource.TestCheckResourceAttr(s, "devices.1.upgrade_to", "8.8.0.1"),
					resource.TestCheckResourceAttr(s, "speedtest_run", "2026-10-03T07:45:30Z"),
					resource.TestCheckResourceAttr(s, "speedtest_download_mbps", "2210.4"),
				),
			},
		},
	})
}
