# Verifying a backup

A backup you've never checked is a hope, not a backup. `drool backup verify` checks a backup **without
restoring it** and changes nothing: the archive and its checksums, the database's integrity, that it
upgrades to this DROOL's version (on a temporary copy), which master key opens its credentials, and its
audit history.

## Steps

Native installation (the file must be readable by the `drool` user):

```sh
sudo -u drool drool backup verify /var/lib/drool/backups/drool-backup-20261012-213603.tar.gz
```

Docker Compose, in your DROOL folder (`cd ~/drool` first; the backup file is on the Docker host, and `-`
reads it from standard input):

```sh
docker compose exec -T drool drool backup verify - < drool-backup-20261012-213603.tar.gz
```

Podman:

```sh
podman exec -i drool drool backup verify - < drool-backup-20261012-213603.tar.gz
```

For a backup made **without** its master key on another machine (or before a key rotation), send the
key file you kept along with the backup, the key first (`--master-key -` reads it from standard input,
so it never has to be readable by anyone but you):

```sh
cat master.key drool-backup-20261012-213603.tar.gz | sudo -u drool drool backup verify --master-key - -     # native
cat master.key drool-backup-20261012-213603.tar.gz | docker compose exec -T drool drool backup verify --master-key - -   # Docker, in ~/drool
cat master.key drool-backup-20261012-213603.tar.gz | podman exec -i drool drool backup verify --master-key - -          # Podman
```

Run only the line for your installation.

## Reading the result

This is the result for the example network's backup:

```text
Backup made 2026-10-12T21:36:03Z on drool by DROOL v0.1.0-alpha-73-ge3c0c58 (schema 20).
  Archive: intact (10 files, checksums match); 3 users, 6 hosts, 1 credentials, 17 runs, 186 audit events
  Database: integrity check passed; same schema as this DROOL
  Audit history: 186 records verified (not sealed by a checkpoint yet)
  Note: its 7 stored credential(s) and keys open only with master key id 12a1738bb919c12b, which the backup doesn't include: restore it with that key file (--master-key), or without credentials (--discard-credentials).
Restorable, but not complete on its own (see the note above).
```

The last line, and the command's **exit status**, sum it up:

| Exit status | Last line | Meaning |
|---|---|---|
| `0` | *Restorable and complete.* | Everything, including credentials, can be restored with what's available |
| `2` | *Restorable, but not complete on its own (see the note above).* | Restorable, but its credentials need a master key it doesn't include. Fine **if you have kept the key** ([The Master Key](Master-Key.md)) |
| `1` | *NOT RESTORABLE:* and the problems | Damaged, incomplete, or from an incompatible version. Make a new backup and find out why |

In a script, `$?` right after the command is the exit status. For example, to be warned about anything
but a complete backup:

```sh
sudo -u drool drool backup verify "$f" || echo "backup $f needs attention (exit $?)"
```

(Exit status 2 counts as needing attention here, which is right if you expect self-contained backups.)

## Related

[Restoring a Controller](Restoring-a-Controller.md) · [The Master Key](Master-Key.md) · [Backup and Recovery](Backup-and-Recovery.md)
