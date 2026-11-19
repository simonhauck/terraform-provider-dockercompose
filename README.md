# Terraform Provider: Docker Compose

[![CI/CD](https://github.com/simonhauck/terraform-provider-dockercompose/actions/workflows/cicd.yml/badge.svg)](https://github.com/simonhauck/terraform-provider-dockercompose/actions/workflows/cicd.yml)
[![Release](https://img.shields.io/github/v/release/simonhauck/terraform-provider-dockercompose)](https://github.com/simonhauck/terraform-provider-dockercompose/releases)
[![Terraform Registry](https://img.shields.io/badge/terraform-registry-7B42BC?logo=terraform)](https://registry.terraform.io/providers/simonhauck/dockercompose/latest)

Deploy Docker Compose projects to a server over SSH, straight from Terraform, without installing anything on the machine that runs Terraform.

It is built for setups like Terraform Cloud (HCP Terraform) runners. Those runners have no `docker` or `ssh` binary and no good place for key files, but they can still deploy to a plain Linux server running Docker.

> [!NOTE]
> The provider is under active development and not yet published to the Terraform Registry. The registry link above will work once the first release is out.

## Features

- **Nothing to install on the runner.** Compose runs inside the provider (using the official Docker Compose Go SDK) and talks to the Docker Engine API. You don't need `docker`, `docker compose` or `ssh` on the machine running Terraform.
- **SSH keys stay in memory.** Pass the private key and host key as attributes, for example from another resource or remote state. There's no key file to write before `plan`, and the host key is verified.
- **Inline configuration.** The compose content and its variables are ordinary attributes. Reference other resources' outputs directly, including secrets that are generated in the same run.
- **Your compose files, unchanged.** Write the compose file as HCL with `yamlencode()`, or read an existing `compose.yaml` with `file()`. The second option lets tools like Renovate keep image tags up to date.
- **Easy to debug on the server.** The compose file and `.env` are copied into a project directory on the host, so `docker compose ps`, `logs` and similar commands work there as usual. Moving away from the provider is just `docker compose up` in that directory.
- **Drift detection.** Stopped or missing containers and hand-edited files show up in the next `plan`, with the reason.
- **In-place updates.** Changing one value only recreates the services it affects, just like `docker compose up`.

## Example

```hcl
terraform {
  required_providers {
    dockercompose = {
      source = "simonhauck/dockercompose"
    }
  }
}

provider "dockercompose" {
  ssh {
    host        = "203.0.113.10"
    user        = "deploy"
    private_key = tls_private_key.deploy.private_key_openssh
    host_key    = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI..."
  }
}

resource "random_password" "db" {
  length = 32
}

resource "dockercompose_project" "app" {
  name = "app"

  # Or: compose = file("${path.module}/compose.yaml")
  compose = yamlencode({
    services = {
      db = {
        image       = "postgres:17-alpine"
        restart     = "unless-stopped"
        environment = { POSTGRES_PASSWORD = "$${DB_PASSWORD}" }
        volumes     = ["./data:/var/lib/postgresql/data"]
      }
    }
  })

  # Interpolation variables for ${...} in the compose file, like a .env file.
  env = {
    DB_PASSWORD = random_password.db.result
  }
}
```

On the server this creates `/opt/dockercompose/app/compose.yaml` and `.env`, and starts the project. All options are described in the [provider docs](docs/index.md) and the [`dockercompose_project` docs](docs/resources/project.md).

To develop against Docker on your own machine, use `local {}` instead of `ssh {}`.

## Server requirements

- A Docker daemon. The Docker CLI is not needed on the server either.
- An OpenSSH server with SFTP enabled. It must allow local forwarding: `AllowTcpForwarding` has to be `yes` (the default) or `local`, because OpenSSH also applies this setting to forwarding the Docker socket.
- An SSH user that can access the Docker socket and write to the project directories (by default `/opt/dockercompose/<name>`, configurable with `project_dir`).

## How it compares

This provider builds on ideas from two existing projects, and both are worth a look:

- [**kreuzwerker/terraform-provider-docker**](https://github.com/kreuzwerker/terraform-provider-docker): the established Docker provider. It manages individual containers, images and networks, and has a `docker_compose` resource. It showed that compose can run inside a Terraform provider.
- [**xRizur/terraform-provider-dockercompose**](https://github.com/xRizur/terraform-provider-dockercompose): a provider focused on compose. Its simple, compose-first interface was the model for this one.

What this provider does differently:

| | this provider | kreuzwerker/docker | xRizur/dockercompose |
|---|---|---|---|
| Runs without `docker`/`ssh` binaries on the runner | yes | `ssh` binary needed | no, calls the `docker compose` CLI |
| SSH key passed in memory | yes | no, key file or ssh-agent | no, relies on the local ssh setup |
| Host key verification without `known_hosts` file | yes, `host_key` attribute | via ssh options | via the local ssh setup |
| Compose content and env inline (no local files) | yes | no, file paths | yes (`compose_yaml`, HCL schema) |
| Values known only at apply time in env | yes | needs workarounds | yes |
| Compose files mirrored on the server for `docker compose` debugging | yes | no | no |
| Scope | compose projects only | full Docker API | compose projects |

If you need to manage individual Docker resources such as images, networks or volumes, use kreuzwerker/docker; it can be used alongside this provider. For example, create networks that your compose files then reference as `external`.

## Development

The Nix flake provides the whole toolchain:

```sh
nix develop
make build     # compile
make test      # unit tests
make testacc   # acceptance and end-to-end tests (need a Docker daemon; SSH tests use testcontainers)
make teste2e   # only the end-to-end test: builds the provider and drives it with the terraform CLI
make lint
make docs      # regenerate docs/ from the schema and examples/
```

Commit messages and PR titles follow [Conventional Commits](https://www.conventionalcommits.org/). [release-please](https://github.com/googleapis/release-please) turns them into versions and a changelog, and GoReleaser builds and signs the release binaries. Design decisions are recorded in [`docs/adr/`](docs/adr/).

### Publishing to the Terraform Registry (one-time setup)

1. Create a GPG key for signing. Add the private key and its passphrase as the `GPG_PRIVATE_KEY` and `GPG_PASSPHRASE` repository secrets.
2. In the Terraform Registry, add the ASCII-armored public key under *User Settings → Signing Keys*.
3. Make the repository public and publish it on [registry.terraform.io](https://registry.terraform.io). New releases are picked up automatically through the GitHub webhook.
