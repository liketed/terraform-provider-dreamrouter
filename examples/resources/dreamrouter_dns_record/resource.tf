# A host record.
resource "dreamrouter_dns_record" "nas" {
  type  = "A"
  name  = "nas.home.internal"
  value = "192.168.1.50"
}

# Several host records from a map.
resource "dreamrouter_dns_record" "hosts" {
  for_each = {
    "printer.home.internal" = "192.168.1.60"
    "media.home.internal"   = "192.168.1.70"
  }

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
  value = dreamrouter_dns_record.nas.name
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
