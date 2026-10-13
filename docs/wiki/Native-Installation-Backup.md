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
with `rsync` over SSH to your backup server. In the examples, `backup@backup-host.example.net` stands
for an account on your backup machine; `rsync` must be installed on both machines.

### First: let root reach the backup machine without a password

The nightly copy below runs from root's crontab, so **root on the controller** must be able to log in to
the backup machine with an SSH key, without anyone typing a password or answering a question:

```sh
sudo ssh-keygen -t ed25519
sudo ssh-copy-id backup@backup-host.example.net
```

- The first line creates an SSH key for root. Press Enter at each question: the key gets no passphrase,
  because cron can't type one. (If it says the key already exists, answer `n` and keep the existing one.)
- The second line puts the key's public half on the backup machine. It asks you, once, to confirm the
  backup machine's host key (compare its fingerprint, then type `yes`) and for that account's password.
- Use an account on the backup machine that only holds backups: anyone who is root on the controller
  can use this key.

### Then: test the copy by hand

```sh
sudo rsync -a /var/lib/drool/backups/ backup@backup-host.example.net:drool-backups/
```

It must finish **without asking anything**. Check on the backup machine that the files arrived (for
example `ls drool-backups/` there). If it asks for a password, root's key isn't accepted yet; if it
asks about the host key, answer it now, so cron never has to. Only automate once this works.

## Every night, automatically

DROOL doesn't schedule backups. Once the copy works by hand, add both steps to **root's** crontab
(`sudo crontab -e`):

```text
15 3 * * * sudo -u drool drool backup && rsync -a /var/lib/drool/backups/ backup@backup-host.example.net:drool-backups/
```

At 03:15 every night this makes a backup and, only if that worked, copies the backups folder. A cron
job fails silently, so check the next morning that the new backup arrived on the backup machine. A
systemd timer or your backup tool works as well.

DROOL doesn't delete old backups: remove old files from `/var/lib/drool/backups/` now and then, for
example those older than 30 days:

```sh
sudo find /var/lib/drool/backups -name 'drool-backup-*.tar.gz' -mtime +30 -delete
```

## Next

**[Verifying a Backup](Verifying-a-Backup.md)**: check that the backup you just made can be restored.

## Related

[Verifying a Backup](Verifying-a-Backup.md) · [Restoring a Controller](Restoring-a-Controller.md) ·
[The Master Key](Master-Key.md) · [Backup and Recovery](Backup-and-Recovery.md)
