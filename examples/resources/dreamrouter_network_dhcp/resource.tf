# Network boot (PXE) on the default network: machines that boot over the
# network load netboot.xyz from the boot server.
resource "dreamrouter_network_dhcp" "lan" {
  network = "Default"

  boot = {
    server = "192.168.1.20"
    file   = "netboot.xyz.efi"
  }

  # Optional: a TFTP server handed out as DHCP option 66, e.g. for IP phones.
  tftp_server = "tftp.home.internal"
}
