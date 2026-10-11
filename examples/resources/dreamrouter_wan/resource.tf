variable "eir_password" {
  type      = string
  sensitive = true
}

# A second line as failover: eir's PPPoE login on VLAN 10, on Port 3.
resource "dreamrouter_wan" "failover" {
  name     = "Internet 2"
  type     = "pppoe"
  username = "eir@eir.ie"
  password = var.eir_password
  vlan     = 10
  port     = "Port 3"
  # primary = true   # switch traffic to this line
}
