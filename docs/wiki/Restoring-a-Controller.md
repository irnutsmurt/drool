# Restoring a controller

Restore a backup when the controller's disk died, when you move DROOL to another machine, or to go back
to an earlier state. A restore checks **everything first** (the archive's checksums, the database, the
version, and that a master key opens every credential); if anything is wrong, it stops and your
installation is untouched.

## Before you start

- **DROOL must be stopped** during the restore.
- You need the backup file, and, unless the backup was made with `--include-master-key`, the
  **master key** file you kept ([The Master Key](Master-Key.md)). On the same machine, without a key
  rotation since the backup, the installed key works.
- Check the backup first if you can: [Verifying a Backup](Verifying-a-Backup.md).
- On a new machine: [install DROOL](Installing-DROOL.md) first, the same version or newer, but **don't
  sign in or set it up**: restore before using it.
- Everyone will have to sign in again afterwards.

## Native installation

On the same machine (the installed key opens the backup):

```sh
sudo systemctl stop drool
sudo -u drool drool restore - < drool-backup-20261012-214018.tar.gz
sudo systemctl start drool
```

With the key file you kept (another machine, or after a key rotation), send the key and the backup
in together, the key first:

```sh
sudo systemctl stop drool
cat master.key drool-backup-20261012-214018.tar.gz | sudo -u drool drool restore --master-key - -
sudo systemctl start drool
```

- `-` reads the backup from standard input (`<` or `|`), so the `drool` user doesn't need to be able
  to read the file where it is.
- `--master-key -` reads the key file from standard input too, ahead of the backup: `cat` sends both,
  `master.key` first. Your copy of the key stays as it is (readable only by you); it's never copied
  anywhere or made readable to anyone else.

## Docker Compose

In your DROOL folder (`cd ~/drool` first):

```sh
docker compose stop drool
cat master.key drool-backup-20261012-214018.tar.gz | docker compose run --rm -T drool restore --master-key - -
docker compose start drool
```

`docker compose run --rm -T drool` runs the restore in a one-off container on DROOL's data volume, and
passes it what `cat` sends: the key, then the backup. Nothing is mounted into the container, and your
`master.key` keeps its permissions.

For a backup made with `--include-master-key`, or on the same installation without a key rotation
since the backup, leave out the key:

```sh
docker compose run --rm -T drool restore - < drool-backup-20261012-214018.tar.gz
```

## Podman

```sh
systemctl --user stop drool
image=$(sed -n 's/^Image=//p' ~/.config/containers/systemd/drool.container)
cat master.key drool-backup-20261012-214018.tar.gz | podman run --rm -i --read-only --tmpfs /tmp --cap-drop ALL \
  --security-opt no-new-privileges -v drool-data:/data "$image" restore --master-key - -
systemctl --user start drool
```

The second line reads the image your DROOL runs from its Quadlet unit (`echo "$image"` shows it, for
example `ghcr.io/irnutsmurt/drool:main`), so the restore runs the same version. Without a separate key
file, leave out `cat master.key` and `--master-key -`, and send the backup with
`< drool-backup-20261012-214018.tar.gz`.

## What the restore prints

This is the restore of the example network's backup:

```text
Restored backup from 2026-10-12T21:40:18Z (DROOL v0.1.0-alpha-73-ge3c0c58, made on drool).
  3 users, 6 hosts, 1 credentials, 17 jobs, 208 audit events
  7 credential(s) verified with the master key already installed here.
  TLS certificate restored.
  All users must sign in again (5 session(s) cleared).
  The previous files were moved to /var/lib/drool/pre-restore-20261012-214125; delete it once you're satisfied.

The restored controller has a new controller epoch (fde11f43fcfe, was e18c7e796bce). Its hosts
still answer to the backup's: once DROOL is started, it compares each host
with the restored data and moves it to the new epoch when nothing would be
lost or rolled back; hosts changed after the backup wait for you
(Settings → System → Restore). Set aside, never repeated automatically:
  0 unfinished job(s) marked interrupted (outcome unknown)
  0 open firewall transaction(s) closed as unknown
  0 unsent notification(s) dropped, 0 queued scheduled run(s) skipped
  0 job schedule(s) held, 0 automatic Desired State restore(s) paused, 0 Shepherd rollout(s) paused

Start DROOL again (systemctl start drool, or docker compose start drool).
```

- **The previous files** are kept in `pre-restore-<time>/` in the data directory. Delete that folder
  once everything works.
- If the backup contained a configuration file, it is saved next to yours, **not applied**: compare it
  with your current configuration.

## Then

1. Start DROOL and sign in.
2. Open **Settings → System → Restore** and follow [What Happens After Restore](What-Happens-After-Restore.md).
3. If the controller's address changed (a new machine), point the name Shepherds use
   (`drool.example.net` in the examples) at the new machine, or change DROOL's address for Shepherds
   (**Shepherds → Controller address**).

## If something goes wrong

- **No key opens the backup:** the restore says so and explains your options: give the right key with
  `--master-key`, or restore without the credentials ([Recovering Without Credentials](Recovering-Without-Credentials.md)).
- **The restore was interrupted** (power loss, a killed process): DROOL refuses to start on a
  half-restored data directory. Put the previous installation back, then restore again:

  | Installation | Command |
  |---|---|
  | Package or archive | `sudo -u drool drool restore --recover` |
  | Docker Compose (in `~/drool`) | `docker compose run --rm drool restore --recover` |
  | Podman | `podman run --rm --read-only --tmpfs /tmp --cap-drop ALL --security-opt no-new-privileges -v drool-data:/data "$(sed -n 's/^Image=//p' ~/.config/containers/systemd/drool.container)" restore --recover` |

## Related

[What Happens After Restore](What-Happens-After-Restore.md) · [Verifying a Backup](Verifying-a-Backup.md) ·
[The Master Key](Master-Key.md) · [Backup and Recovery](Backup-and-Recovery.md)
