#!/bin/sh
# DROOL package: the service account (install and upgrade; never changed
# when it exists, e.g. from a manual installation).
set -e
if ! getent group drool >/dev/null 2>&1; then
  groupadd --system drool
fi
if ! getent passwd drool >/dev/null 2>&1; then
  useradd --system --gid drool --home-dir /var/lib/drool --no-create-home \
    --shell /usr/sbin/nologin --comment "DROOL controller" drool
fi
exit 0
