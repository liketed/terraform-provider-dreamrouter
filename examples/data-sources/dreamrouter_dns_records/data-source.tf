# Every name the router answers for, including ones not managed by Terraform:
# static records and devices' DNS names (source = "host").
data "dreamrouter_dns_records" "all" {}

# Static records only.
data "dreamrouter_dns_records" "static" {
  static_only = true
}

# Only MX records for one name.
data "dreamrouter_dns_records" "mail" {
  type = "MX"
  name = "home.internal"
}

output "all_records" {
  value = [for r in data.dreamrouter_dns_records.all.records : "${r.type} ${r.name} -> ${r.value}"]
}
