# Backing up DROOL in Podman

For DROOL running rootless under Podman with the release's Quadlet unit (`drool.container`). All data
lives in the `drool-data` Podman volume.

## Make a backup

As the user that runs DROOL:

```sh
podman exec drool drool backup -o - > drool-backup-$(date +%Y%m%d-%H%M%S).tar.gz
```

`-o -` writes the backup to standard output, which `>` saves outside the container. Backups are safe to
make while DROOL runs. Copy the file to another machine.

## Every night, automatically

A cron line for the same user (`crontab -e`), with `~/backups/drool` standing for where backups go:

```text
15 3 * * * podman exec drool drool backup -o - > $HOME/backups/drool/drool-backup-$(date +\%Y\%m\%d).tar.gz
```

In a crontab, `%` must be written `\%`. A systemd user timer works as well.

## Don't forget the master key

```sh
podman cp drool:/data/master.key ./master.key
```

Store it apart from the backups ([The Master Key](Master-Key.md)).

## Related

[Verifying a Backup](Verifying-a-Backup.md) · [Restoring a Controller](Restoring-a-Controller.md) ·
[Docker Backup](Docker-Backup.md) · [Backup and Recovery](Backup-and-Recovery.md)
