#!/bin/sh
# DROOL package, after its files are in place.
#   deb: postinst configure [previous version]    rpm: %post 1 (install) | 2 (upgrade)
# The package owns no files in /var/lib/drool or /etc/drool: removing or
# upgrading it never touches the database, the master key, backups or the
# configuration.
set -e
case "$1" in
  configure) if [ -n "${2:-}" ]; then action=upgrade; else action=install; fi ;;
  1) action=install ;;
  2) action=upgrade ;;
  *) exit 0 ;;
esac

[ -d /var/lib/drool ] || install -d -m 0750 -o drool -g drool /var/lib/drool
[ -d /etc/drool ] || install -d -m 0755 /etc/drool

# Leftovers of a manual installation shadow the package.
if [ -f /etc/systemd/system/drool.service ]; then
  echo "Note: /etc/systemd/system/drool.service (a manual installation) overrides this package's unit," >&2
  echo "  so the service would keep running the old binary. Remove it; your data stays:" >&2
  echo "    sudo rm /etc/systemd/system/drool.service && sudo systemctl daemon-reload" >&2
fi
if [ -e /usr/local/bin/drool ]; then
  echo "Note: /usr/local/bin/drool (a manual installation) comes first on PATH. Remove it so" >&2
  echo "  'drool' runs this package's version: sudo rm /usr/local/bin/drool" >&2
fi

if [ -d /run/systemd/system ]; then
  systemctl daemon-reload >/dev/null 2>&1 || true
  if [ "$action" = upgrade ]; then
    # A running controller restarts with the new version, which backs the
    # database up before upgrading it (backup.before_upgrade).
    systemctl try-restart drool.service >/dev/null 2>&1 || true
  fi
fi

if [ "$action" = install ]; then
  cat <<'MSG'

DROOL is installed. Start it:

    sudo systemctl enable --now drool

then open the address and use the one-time setup code it prints:

    sudo journalctl -u drool -n 30

Data lives in /var/lib/drool. Keep a copy of /var/lib/drool/master.key somewhere safe
and separate from your backups (/usr/share/doc/drool/PACKAGE.md says why and how).

MSG
fi
exit 0
