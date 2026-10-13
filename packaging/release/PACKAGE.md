# The DROOL package

This package runs the DROOL controller as a systemd service.

| What | Where |
|---|---|
| Program | `/usr/bin/drool` |
| Shepherd (DROOL agent) binaries handed to new hosts | `/usr/lib/drool/` |
| Service | `drool.service` (user `drool`), `/usr/lib/systemd/system/drool.service` |
| Data: database, `master.key`, TLS certificate, backups, logs | `/var/lib/drool` |
| Configuration (optional) | `/etc/drool/config.yaml` (example: `/usr/share/drool/config.example.yaml`) |
| Third-party licenses | `/usr/share/drool/THIRD_PARTY_NOTICES.txt` |
| Logs | `/var/lib/drool/logs/`; startup messages and the setup code: `journalctl -u drool` |

**First start:** `sudo systemctl enable --now drool`, then `sudo journalctl -u drool -n 30`
for the address and the one-time setup code. Run DROOL's commands as its user, e.g.
`sudo -u drool drool backup`.

**Upgrading** the package restarts a running controller with the new version, which backs
the database up (`/var/lib/drool/backups/drool-pre-upgrade-…`) before upgrading it. If it
can't, it doesn't start and says why. Going back to an older version means restoring that
backup with the older version (see *Updating DROOL* in the user guide:
https://github.com/irnutsmurt/drool/wiki/Updating-DROOL).

**Removing** the package (`apt remove`, `apt purge`, `dnf remove`) stops the service and
removes the program, but never your data: `/var/lib/drool`, `/etc/drool` and the `drool` user
stay until you delete them yourself (`sudo rm -rf /var/lib/drool /etc/drool && sudo userdel
drool`). A purge doesn't delete them either: they hold your database, master key and backups.

Keep a copy of `/var/lib/drool/master.key` somewhere safe and separate from your backups.
`README.md` in this directory introduces DROOL; the user guide has everything else, step by step:
https://github.com/irnutsmurt/drool/wiki
