terraform {
  required_providers {
    dreamrouter = {
      source = "liketed/dreamrouter"
    }
  }
}

# Set the password in the environment rather than here:
#   export DREAMROUTER_PASSWORD=...
provider "dreamrouter" {
  host     = "192.168.1.1" # default
  username = "admin"       # default; a local UniFi OS account
}
