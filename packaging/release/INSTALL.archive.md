# Installing DROOL from this archive

This archive is the DROOL controller for one CPU architecture, with the Shepherd (DROOL
agent) binaries it hands to new hosts. On Debian, Ubuntu, RHEL-family and Fedora machines,
the `.deb`/`.rpm` packages of the same release are simpler; use this archive elsewhere or by
choice. You need systemd and root (sudo).

```sh
sudo install -m 0755 drool /usr/local/bin/drool
sudo install -d /usr/local/lib/drool
sudo install -m 0755 drool-agent-linux-amd64 drool-agent-linux-arm64 /usr/local/lib/drool/
sudo useradd --system --user-group --home-dir /var/lib/drool --no-create-home --shell /usr/sbin/nologin drool
sudo install -m 0644 drool.service /etc/systemd/system/drool.service
sudo systemctl daemon-reload
sudo systemctl enable --now drool
sudo journalctl -u drool -n 30        # the address and the one-time setup code
```

Open the address, enter the setup code and create the administrator account.

- **Data:** `/var/lib/drool` (database, `master.key`, TLS certificate, logs in `logs/`,
  backups in `backups/`). Keep a copy of `master.key` somewhere safe and separate from your
  backups: without it, a backup can't give you your stored credentials back.
- **Configuration (optional):** `/etc/drool/config.yaml`; `config.example.yaml` lists the
  settings. Run DROOL's commands as its user: `sudo -u drool drool backup`.
- **Upgrading:** stop the service, install the new `drool` binary and Shepherd binaries the
  same way, start it. DROOL backs the database up by itself before upgrading it.
- **Removing:** `sudo systemctl disable --now drool`, then delete the files above. Your data
  in `/var/lib/drool` stays until you delete it.

The full documentation is `README.md`. Third-party licenses: `THIRD_PARTY_NOTICES.txt`.
