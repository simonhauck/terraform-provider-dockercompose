# Pure-Go SSH dialer with in-memory keys

`kreuzwerker/docker` reaches remote hosts through docker's `connhelper`, which runs the local `ssh` binary and `docker system dial-stdio` on the remote host. The key has to be written to disk before plan, and host-key checking is usually disabled to make it work in CI.

We dial with `golang.org/x/crypto/ssh` instead. The private key and host key are provider attributes held in memory, and the Docker socket is reached through an SSH `direct-streamlocal` channel. This needs no local `ssh` binary and no docker CLI on the remote host. The same SSH client carries SFTP for writing project files (see [ADR-0003](0003-compose-as-yaml-string.md)).

The SSH server must allow local forwarding: OpenSSH applies `AllowTcpForwarding` to Unix-socket channels too, so it must be `yes` (the upstream and Debian/Ubuntu default) or `local`. Alpine sets it to `no`. The `sftp` subsystem must be enabled.
