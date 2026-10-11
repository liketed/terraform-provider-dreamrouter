# The network behind a second router on the LAN.
resource "dreamrouter_static_route" "cudy" {
  name     = "cudy"
  network  = "192.168.2.0/24"
  next_hop = "192.168.1.4"
}
