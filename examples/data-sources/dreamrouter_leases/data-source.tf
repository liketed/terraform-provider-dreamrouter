# All current DHCP leases.
data "dreamrouter_leases" "all" {}

# Only devices that are online on one network.
data "dreamrouter_leases" "online" {
  network = "Default"
  status  = "online"
}

# Look a device up by host name and reserve its current address.
locals {
  tv = one([for l in data.dreamrouter_leases.all.leases : l if l.hostname == "tv"])
}

resource "dreamrouter_dhcp_reservation" "tv" {
  mac = local.tv.mac
  ip  = local.tv.ip
}
