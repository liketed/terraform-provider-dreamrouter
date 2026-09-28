terraform {
  required_providers {
    dreamrouter = {
      source = "liketed/dreamrouter"
    }
  }
}

# Credentials come from the environment:
#   export DREAMROUTER_PASSWORD=...        (or UNIFI_PASS)
#   export DREAMROUTER_USERNAME=admin      (optional, default "admin")
#   export DREAMROUTER_HOST=192.168.1.1    (optional, default 192.168.1.1)
provider "dreamrouter" {}

# Simple host records from a map.
locals {
  hosts = {
    "nas.home.internal"     = "192.168.1.50"
    "printer.home.internal" = "192.168.1.60"
    "media.home.internal"   = "192.168.1.70"
  }
}

resource "dreamrouter_dns_record" "host" {
  for_each = local.hosts

  type  = "A"
  name  = each.key
  value = each.value
}

resource "dreamrouter_dns_record" "nas_v6" {
  type  = "AAAA"
  name  = "nas.home.internal"
  value = "fd00::50"
}

resource "dreamrouter_dns_record" "files" {
  type  = "CNAME"
  name  = "files.home.internal"
  value = dreamrouter_dns_record.host["nas.home.internal"].name
  ttl   = 300
}

resource "dreamrouter_dns_record" "mail" {
  type     = "MX"
  name     = "home.internal"
  value    = "mail.home.internal"
  priority = 10
}

resource "dreamrouter_dns_record" "sip" {
  type     = "SRV"
  name     = "_sip._tcp.home.internal"
  value    = "pbx.home.internal"
  priority = 10
  weight   = 5
  port     = 5060
}

resource "dreamrouter_dns_record" "spf" {
  type  = "TXT"
  name  = "home.internal"
  value = "v=spf1 -all"
}

# On the router, NS records forward a domain to another DNS server.
resource "dreamrouter_dns_record" "lab" {
  type  = "NS"
  name  = "lab.home.internal"
  value = "192.168.1.2"
}

# A device with a fixed IP and a DNS name.
resource "dreamrouter_host" "tv" {
  name = "tv.home.internal"
  ip   = "192.168.1.80"
  mac  = "aa:bb:cc:dd:ee:80"
}

# A fixed IP without a DNS name.
resource "dreamrouter_dhcp_reservation" "printer" {
  mac = "aa:bb:cc:dd:ee:60"
  ip  = "192.168.1.61"
}

# Everything on the router, including records not managed here.
data "dreamrouter_dns_records" "all" {}

output "all_records" {
  value = [for r in data.dreamrouter_dns_records.all.records : "${r.type} ${r.name} -> ${r.value}"]
}
