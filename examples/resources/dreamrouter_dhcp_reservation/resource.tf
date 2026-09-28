# A fixed IP for a device, by MAC address.
resource "dreamrouter_dhcp_reservation" "printer" {
  mac  = "aa:bb:cc:dd:ee:02"
  ip   = "192.168.1.60"
  name = "printer" # optional: the name shown in the web UI
}

# Several reservations from a map.
resource "dreamrouter_dhcp_reservation" "cameras" {
  for_each = {
    "aa:bb:cc:dd:ee:10" = "192.168.1.70"
    "aa:bb:cc:dd:ee:11" = "192.168.1.71"
  }

  mac = each.key
  ip  = each.value
}
