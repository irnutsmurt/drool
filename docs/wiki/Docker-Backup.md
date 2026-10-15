# Backing up DROOL in Docker

For DROOL running with Docker Compose ([Install with Docker Compose](Install-With-Docker-Compose.md)). All
data lives in DROOL's Docker volume, `drool_drool-data` (or `<DROOL_INSTANCE>_drool-data`:
[Compose Data Volume](Compose-Data-Volume.md)).

## Before you start

- Run the commands in your DROOL folder, the one with `compose.yaml`: `cd ~/drool` first.
- Backups are safe to make while DROOL runs.

## Make a backup

```sh
docker compose exec -T drool drool backup -o - > drool-backup-$(date +%Y%m%d-%H%M%S).tar.gz
```

- `-o -` writes the backup to standard output, and `>` saves it on the Docker host, outside the
  container. DROOL's messages appear on the screen, not in the file.
- `-T` matters: without it, Docker adds terminal control characters and the file is damaged.
- `$(date +%Y%m%d-%H%M%S)` puts the date and time in the name, for example
  `drool-backup-20261012-101500.tar.gz`.

Then copy the file to another machine, like any other file.

## Every night, automatically

A cron line on the Docker host (`crontab -e`, as the user who runs `docker compose`), with `$HOME/drool`
as the DROOL folder and `$HOME/backups/drool` for where backups go (create it first:
`mkdir -p ~/backups/drool`):

```text
15 3 * * * cd $HOME/drool && docker compose exec -T drool drool backup -o - > $HOME/backups/drool/drool-backup-$(date +\%Y\%m\%d).tar.gz
```

In a crontab, `%` must be written `\%`.

## Don't forget the master key

The backup doesn't contain the [master key](Master-Key.md). Copy it out once, and after every rotation:

```sh
docker compose cp drool:/data/master.key ./master.key
```

and store it apart from the backups.

## Next

**[Verifying a Backup](Verifying-a-Backup.md)**: check that the backup you just made can be restored.

## Related

[Verifying a Backup](Verifying-a-Backup.md) · [Restoring a Controller](Restoring-a-Controller.md) ·
[Podman Backup](Podman-Backup.md) · [Backup and Recovery](Backup-and-Recovery.md)
