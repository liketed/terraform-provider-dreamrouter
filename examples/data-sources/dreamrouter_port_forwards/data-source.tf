data "dreamrouter_port_forwards" "all" {}

# Which ports are open to the internet right now.
output "open_ports" {
  value = [for f in data.dreamrouter_port_forwards.all.port_forwards : "${f.protocol} ${f.port} -> ${f.forward_ip}:${f.forward_port}" if f.enabled]
}
