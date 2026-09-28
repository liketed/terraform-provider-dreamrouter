data "dreamrouter_networks" "iot" {
  name = "IoT"
}

# Reserve an address on a specific network.
resource "dreamrouter_dhcp_reservation" "sensor" {
  mac        = "aa:bb:cc:dd:ee:20"
  ip         = "10.0.20.5"
  network_id = data.dreamrouter_networks.iot.networks[0].id
}
