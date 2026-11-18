# SSH server for tests: key-only root login, SFTP, and stream-local forwarding
# to a Docker socket bind-mounted from the test host.
FROM alpine:3.22
RUN apk add --no-cache openssh-server openssh-sftp-server \
 && ssh-keygen -A \
 && mkdir -p /root/.ssh && chmod 700 /root/.ssh \
 && printf '%s\n' \
      'PermitRootLogin prohibit-password' \
      'PasswordAuthentication no' \
      'AllowStreamLocalForwarding yes' \
      'AllowTcpForwarding local' \
      'Subsystem sftp /usr/lib/ssh/sftp-server' \
      > /etc/ssh/sshd_config.d/test.conf
COPY entrypoint.sh /entrypoint.sh
EXPOSE 22
ENTRYPOINT ["/bin/sh", "/entrypoint.sh"]
