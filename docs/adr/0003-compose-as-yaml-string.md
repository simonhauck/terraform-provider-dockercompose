# Compose input is a YAML string, mirrored to the server

A project's compose content is a single `compose` string attribute. It accepts `file()`, `templatefile()` or `yamlencode({...})`; the last one gives an HCL-native structure that can reference other resources' outputs. We do not model compose services as a typed HCL schema, because such a schema always lags behind the compose spec.

Interpolation variables come from a sensitive `env` map, not from `.env` files on the runner. Values that are unknown at plan time simply make the planned hash unknown, so secrets generated in the same run work on the first apply.

On apply, the provider writes `compose.yaml` and `.env` into a project directory on the server and loads the project from there. Compose labels then point at real files, so `docker compose` works on the server for debugging, and migrating away from the provider only needs `docker compose up` in that directory. Terraform state stays the source of truth: hand edits to those files show up as drift and are overwritten on the next apply.
