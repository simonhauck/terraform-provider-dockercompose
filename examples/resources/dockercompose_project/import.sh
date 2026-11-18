# Import by compose project name. The project must have been deployed by this
# provider with write_files enabled, because compose and env are read back from
# the files mirrored into its project directory.
terraform import dockercompose_project.app app
