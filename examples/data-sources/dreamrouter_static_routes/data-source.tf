data "dreamrouter_static_routes" "all" {}

output "routes" {
  value = { for r in data.dreamrouter_static_routes.all.routes : r.network => r.next_hop if r.enabled }
}
