# terraform-provider-dockercompose

A Terraform provider that deploys Docker Compose projects to local or remote Docker hosts.

- **No local tooling:** compose runs in-process over the Docker Engine API. The machine running Terraform (for example a Terraform Cloud runner) needs no `docker`, `docker compose` or `ssh` binary.
- **In-memory SSH:** the private key and host key are provider attributes, not files.
- **Inline config:** compose content and environment variables are plain attributes, so they can reference outputs of other resources, including secrets generated in the same run.
- **Debuggable on the server:** the compose file and `.env` are mirrored into a project directory on the host, so `docker compose` works there as usual.

> Status: under development, not yet published.

## Development

The Nix flake provides the toolchain:

```sh
nix develop
make build     # compile
make test      # unit tests
make testacc   # acceptance tests, needs a reachable Docker daemon
make lint
make docs      # regenerate docs/ with tfplugindocs
```

Commits and PR titles follow [Conventional Commits](https://www.conventionalcommits.org/). Releases are cut by release-please and built by GoReleaser.

## Publishing to the Terraform Registry (one-time setup)

1. Create a GPG key for signing and add the private key and passphrase as the `GPG_PRIVATE_KEY` and `GPG_PASSPHRASE` repository secrets.
2. Add the ASCII-armored public key in the Terraform Registry under *User Settings → Signing Keys*.
3. Make the repository public, then publish it on registry.terraform.io. New releases are picked up through the GitHub webhook.
