# In-process compose over the Docker Engine API

The provider must run on Terraform Cloud runners, which have no `docker` or `docker compose` binary. We therefore embed the Docker Compose Go SDK (`github.com/docker/compose/v5`) and drive it over the Docker Engine API, instead of shelling out to the CLI as `xRizur/terraform-provider-dockercompose` does.

The remote host only needs a Docker daemon. Neither side needs the compose CLI. The cost is a large dependency tree that we upgrade together with compose releases.
