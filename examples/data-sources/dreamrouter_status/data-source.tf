data "dreamrouter_status" "router" {}

output "wan_ip" {
  value = data.dreamrouter_status.router.wan_ip
}

output "firmware" {
  value = {
    for d in data.dreamrouter_status.router.devices : d.name => d.upgradable ? "${d.version} (update: ${d.upgrade_to})" : d.version
  }
}
