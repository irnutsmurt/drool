# Backing up a native installation

For DROOL installed from a package (`.deb`, `.rpm`) or the release archive, running as the `drool` user
with its data in `/var/lib/drool`.

## Before you start

- You need `sudo` on the controller.
- Backups are safe to make while DROOL runs. Nothing needs to stop.

## Make a backup

```sh
sudo -u drool drool backup
```

The backup is written to `/var/lib/drool/backups/drool-backup-<date>-<time>.tar.gz` (the time is UTC),
for example `/var/lib/drool/backups/drool-backup-20261012-101500.tar.gz`. Read what DROOL prints: it says
what the backup contains and reminds you about the [master key](Master-Key.md).

To write it somewhere else, give `-o` a path the `drool` user can write:

```sh
sudo -u drool drool backup -o /var/lib/drool/backups/before-upgrade.tar.gz
```

## Copy it off the controller

A backup on the controller's own disk doesn't survive that disk. Copy it to another machine, for example
with `rsync` to your backup server (`backup-host.example.net` stands for yours):

```sh
sudo rsync -a /var/lib/drool/backups/ backup-host.example.net:drool-backups/
```

## Every night, automatically

DROOL doesn't schedule backups. Use cron (`sudo crontab -e`):

```text
15 3 * * * sudo -u drool drool backup && rsync -a /var/lib/drool/backups/ backup-host.example.net:drool-backups/
```

or a systemd timer, or your backup tool. DROOL doesn't delete old backups: remove old files from
`/var/lib/drool/backups/` now and then, for example those older than 30 days:

```sh
sudo find /var/lib/drool/backups -name 'drool-backup-*.tar.gz' -mtime +30 -delete
```

## Related

[Verifying a Backup](Verifying-a-Backup.md) · [Restoring a Controller](Restoring-a-Controller.md) ·
[The Master Key](Master-Key.md) · [Backup and Recovery](Backup-and-Recovery.md)
