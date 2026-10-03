# Keep SSH to the router off, and SSH to access points on.
resource "dreamrouter_ssh" "this" {
  router  = false
  devices = true
}
