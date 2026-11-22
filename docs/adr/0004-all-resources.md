# Unused resources are opt-in, like Compose

Docker Compose normally discards top-level networks, volumes and other resources that no service references. `docker compose --all-resources` turns that pruning off, which is what makes networks-only compose files deployable. The provider used to call `WithoutUnnecessaryResources` unconditionally, so such files loaded as an empty project.

We expose the same choice as an `all_resources` attribute on `dockercompose_project`. When it is false, loading and hashing keep the previous behavior. When it is true, the project keeps unused resources and the provider's status checks their existence, so a project can consist only of networks or volumes. A compose file with no services and no `all_resources` is rejected at plan time instead of silently doing nothing.

Keeping this opt-in matches Compose and avoids creating unused resources for existing projects. The cost is that the attribute has to be set explicitly for the networks-only use case. Import still relies on container labels, so a project with no containers cannot be discovered from the host yet.
