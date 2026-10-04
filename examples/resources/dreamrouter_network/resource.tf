# A network for the children's devices, using AdGuard DNS Family
# (blocks ads, trackers and adult content, and enforces safe search).
resource "dreamrouter_network" "kids" {
  name        = "Kids"
  vlan        = 30
  subnet      = "192.168.30.1/24"
  dns_servers = ["94.140.14.15", "94.140.15.16"]
}
