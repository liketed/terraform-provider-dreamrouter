data "dreamrouter_ports" "all" {}

output "ports" {
  value = { for p in data.dreamrouter_ports.all.ports : p.name => "${p.interface} ${p.role}" }
}
