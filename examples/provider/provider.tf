# Remote Docker host over SSH. The key comes from another resource or remote
# state and stays in memory: no key file, no ssh binary.
provider "dockercompose" {
  ssh {
    host        = hcloud_server.app.ipv4_address
    user        = "deploy"
    private_key = tls_private_key.deploy.private_key_openssh
    host_key    = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI..."
  }
}

# Docker daemon on the machine running Terraform (DOCKER_HOST or the default socket).
provider "dockercompose" {
  alias = "local"
  local {}
}
