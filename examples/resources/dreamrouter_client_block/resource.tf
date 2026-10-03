# Block a device by MAC address. Destroying the resource unblocks it.
resource "dreamrouter_client_block" "old_tablet" {
  mac = "aa:bb:cc:dd:ee:40"
}

# Block several devices, keyed by MAC address.
locals {
  blocked = {
    "aa:bb:cc:dd:ee:41" = "neighbour's laptop"
    "aa:bb:cc:dd:ee:42" = "lost phone"
  }
}

resource "dreamrouter_client_block" "listed" {
  for_each = local.blocked
  mac      = each.key
}
