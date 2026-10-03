# HTTPS on port 8443 from the internet to a server's port 443.
resource "dreamrouter_port_forward" "web" {
  name         = "web"
  port         = "8443"
  forward_ip   = "192.168.1.20"
  forward_port = "443"
  protocol     = "tcp"
}

# A game server's port range, only from one network, switched off for now.
resource "dreamrouter_port_forward" "games" {
  name       = "games"
  port       = "27000-27010" # ranges and lists are forwarded to the same ports
  forward_ip = "192.168.1.51"
  protocol   = "udp"
  source     = "203.0.113.0/24"
  enabled    = false
}
