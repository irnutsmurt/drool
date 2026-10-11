#!/bin/sh
# DROOL package, after its files are removed.
#   deb: postrm remove | purge | upgrade | ...    rpm: %postun 0 (erase) | 1 (upgrade)
# Neither removing nor purging deletes DROOL's data: the database, the
# master key and the backups are the operator's to delete.
set -e
case "$1" in
  remove|purge|0)
    if [ -d /run/systemd/system ]; then
      systemctl daemon-reload >/dev/null 2>&1 || true
    fi
    if [ -d /var/lib/drool ]; then
      cat <<'MSG'

DROOL was removed. Its data was kept: /var/lib/drool (database, master key, backups)
and /etc/drool (configuration), and the drool user. Installing DROOL again picks them up.
To delete them for good, once you are sure you don't need them:

    sudo rm -rf /var/lib/drool /etc/drool && sudo userdel drool

MSG
    fi
    ;;
esac
exit 0
