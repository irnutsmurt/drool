#!/bin/sh
# Fresh host keys per container, so "the host key changed" can be simulated
# by recreating a container behind the same address.
set -e
ssh-keygen -A >/dev/null
# The harness writes the controller's public key here before tests connect.
for u in deploy pwuser; do touch /home/$u/.ssh/authorized_keys; chown $u:$u /home/$u/.ssh/authorized_keys; chmod 600 /home/$u/.ssh/authorized_keys; done
exec /usr/sbin/sshd -D -e
