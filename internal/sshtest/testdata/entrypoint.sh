#!/bin/sh
set -eu
printf '%s\n' "$AUTHORIZED_KEY" > /root/.ssh/authorized_keys
chmod 600 /root/.ssh/authorized_keys
# Alpine locks root without a password; sshd refuses even key logins for locked accounts.
sed -i 's/^root:!:/root:*:/' /etc/shadow
exec /usr/sbin/sshd -D -e
