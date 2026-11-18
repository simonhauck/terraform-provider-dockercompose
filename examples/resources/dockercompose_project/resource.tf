resource "random_password" "db" {
  length = 32
}

# Compose content as native HCL. Outputs of other resources can be used
# directly, including values only known during apply.
resource "dockercompose_project" "app" {
  name = "app"

  compose = yamlencode({
    services = {
      db = {
        image   = "postgres:17-alpine"
        restart = "unless-stopped"
        environment = {
          POSTGRES_PASSWORD = "$${DB_PASSWORD}"
        }
        volumes = ["./data:/var/lib/postgresql/data"]
      }
    }
  })

  env = {
    DB_PASSWORD = random_password.db.result
  }
}

# An existing compose file works too.
resource "dockercompose_project" "proxy" {
  name    = "proxy"
  compose = file("${path.module}/proxy/compose.yaml")
}
