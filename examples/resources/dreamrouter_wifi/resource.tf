variable "kids_wifi_password" {
  type      = string
  sensitive = true
}

# A Wi-Fi network whose devices join the Kids network.
resource "dreamrouter_wifi" "kids" {
  name       = "home-kids"
  password   = var.kids_wifi_password
  network_id = dreamrouter_network.kids.id
}
