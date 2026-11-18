resource "random_password" "db" {
  length = 32
}

# Compose content as native HCL. Outputs of other resources can be used
# directly, including values only known during apply.
resource "dockercompose_project" "inline" {
  name = "app-inline"

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

# The same project kept in a regular compose.yaml next to this file. Tools
# that understand compose files, such as Renovate, can then update the image
# tags. Use file(), not templatefile(): ${DB_PASSWORD} is interpolated by
# compose from env.
resource "dockercompose_project" "from_file" {
  name    = "app-from-file"
  compose = file("${path.module}/compose.yaml")

  env = {
    DB_PASSWORD = random_password.db.result
  }
}
