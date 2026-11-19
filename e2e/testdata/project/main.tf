# Fixture module for the end-to-end test. The test copies it into a temporary
# working directory and runs the real terraform CLI against it.

terraform {
  required_providers {
    dockercompose = {
      # Resolved from the filesystem mirror the test builds; no version
      # constraint because the mirror holds exactly one build.
      source = "simonhauck/dockercompose"
    }
  }
}

variable "ssh_host" {
  type = string
}

variable "ssh_port" {
  type = number
}

variable "ssh_user" {
  type = string
}

variable "ssh_private_key" {
  type      = string
  sensitive = true
}

variable "ssh_host_key" {
  type = string
}

variable "name" {
  type = string
}

variable "project_dir" {
  type = string
}

variable "greeting" {
  type = string
}

provider "dockercompose" {
  ssh {
    host        = var.ssh_host
    port        = var.ssh_port
    user        = var.ssh_user
    private_key = var.ssh_private_key
    host_key    = var.ssh_host_key
  }
}

resource "dockercompose_project" "this" {
  name        = var.name
  project_dir = var.project_dir

  compose = yamlencode({
    # init: the commands would run as PID 1 and ignore SIGTERM, so every
    # recreate and destroy would wait out the 10s stop timeout.
    services = {
      # Depends on GREETING, so changing it must recreate this service only.
      app = {
        image       = "busybox:1.37"
        init        = true
        command     = ["sh", "-c", "echo $$GREETING; sleep 3600"]
        environment = { GREETING = "$${GREETING}" }
      }
      # Independent of GREETING. Holds data in a named volume that must
      # survive updates and destroy (remove_volumes_on_destroy is not set).
      store = {
        image   = "busybox:1.37"
        init    = true
        command = ["sleep", "3600"]
        volumes = ["data:/data"]
      }
    }
    volumes = {
      data = {}
    }
  })

  env = {
    GREETING = var.greeting
  }
}

output "services" {
  value = dockercompose_project.this.services
}
