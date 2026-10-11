#!/bin/sh
# DROOL package, before its files are removed.
#   deb: prerm remove | upgrade | ...    rpm: %preun 0 (erase) | 1 (upgrade)
set -e
case "$1" in
  remove|0)
    if [ -d /run/systemd/system ]; then
      systemctl disable --now drool.service >/dev/null 2>&1 || true
    fi
    ;;
esac
exit 0
