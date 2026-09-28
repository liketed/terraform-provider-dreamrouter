# A device with a fixed IP and a DNS name, managed together.
resource "dreamrouter_host" "nas" {
  name = "nas.home.internal"
  ip   = "192.168.1.50"
  mac  = "aa:bb:cc:dd:ee:01"
}

# Other records can point at it.
resource "dreamrouter_dns_record" "files" {
  type  = "CNAME"
  name  = "files.home.internal"
  value = dreamrouter_host.nas.name
}
