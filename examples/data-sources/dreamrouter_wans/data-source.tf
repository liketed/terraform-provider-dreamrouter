data "dreamrouter_wans" "all" {}

output "connections" {
  value = { for w in data.dreamrouter_wans.all.wans : w.name => "${w.port}: ${w.up ? w.ip : "down"} (${w.role})" }
}
