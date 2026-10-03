# Connected devices, and devices seen in the last 7 days.
data "dreamrouter_clients" "all" {}

# Only Wi-Fi devices that are online now.
data "dreamrouter_clients" "wifi" {
  status          = "online"
  connection_type = "wifi"
}

# Blocked devices, however long ago they were seen.
data "dreamrouter_clients" "blocked" {
  blocked = true
}

locals {
  # Devices with a weak Wi-Fi signal, by name.
  weak_signal = { for c in data.dreamrouter_clients.wifi.clients : c.mac => c.name if c.signal < -75 }
}
