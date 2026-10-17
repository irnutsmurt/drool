# Updating DROOL

DROOL doesn't update itself: there is no "update now" button and no package repository that
`apt upgrade` would pick it up from. You replace the program, package or image yourself, the same
way you installed it. This page shows how, what DROOL does on the first start of a new version, and
how to go back if you need to.

You update the **controller** (DROOL itself). Its **Shepherds** (the agents on your hosts) then follow
by themselves: DROOL rolls out the Shepherd version it came with, one host first and then in batches
([Updating Shepherds](Updating-Shepherds.md)).

## Before you start

- **Read the release notes** of the new version: they list breaking changes and what Shepherds need.
- **Make a backup** and make sure you have a copy of the **master key** ([Backup and Recovery](Backup-and-Recovery.md),
  [The Master Key](Master-Key.md)). DROOL makes its own backup before changing the database (see
  below), but that copy stays on the same disk.
- **Docker Compose and Synology:** replacing a `compose.yaml` (or a project's YAML) from before
  2026-10-14 with the current one? Check your data volume's name first
  ([Compose Data Volume](Compose-Data-Volume.md#updating-from-an-older-composeyaml)).
- **Check nothing important is running**: an update restarts DROOL, and runs in progress at that
  moment end as *interrupted* with an unknown outcome on the hosts they were running on
  ([why](Failed-Interrupted-and-Unknown-Outcomes.md)).

## Update

| Installation | How |
|---|---|
| Docker Compose, release image | `docker load -i drool_1.0.1_image.oci.tar`, put the new release's `compose.yaml` in place of the old one, in `~/drool`, then, in that folder, `docker compose up -d`. Replacing a `compose.yaml` from before 2026-10-14? Check your data volume's name first ([Compose Data Volume](Compose-Data-Volume.md#updating-from-an-older-composeyaml)) |
| Docker Compose, development image | In your DROOL folder: `cd ~/drool`, then `docker compose pull && docker compose up -d`. This gets the newest `main` |
| Synology Container Manager | In Container Manager: stop the project, delete its container and DROOL's image, then **Action → Build** ([step by step](Install-On-a-Synology-NAS.md#update-drool)). Never **Action → Clean**: it can remove DROOL's data volume |
| Rootless Podman, release image | `podman load -i drool_1.0.1_image.oci.tar`, copy the new `drool.container` to `~/.config/containers/systemd/`, then `systemctl --user daemon-reload && systemctl --user restart drool` |
| Rootless Podman, development image | `podman pull ghcr.io/irnutsmurt/drool:main && systemctl --user restart drool` |
| Native package | `sudo apt install ./drool_1.0.1-1_amd64.deb` or `sudo dnf install ./drool-1.0.1-1.x86_64.rpm` — a running DROOL restarts with the new version |
| Release archive | `sudo systemctl stop drool`, install the new `drool` and Shepherd programs as in the archive's `INSTALL.md` (step 3 of [Install From the Release Archive](Install-From-the-Release-Archive.md#step-3-install-it), without the `useradd` line), then `sudo systemctl start drool` |

(`1.0.1` stands for whichever version you're installing.)

Afterwards, **Settings → System** shows the version that's running.

## What happens on the first start of a new version

- **Database upgrades run by themselves**, each one complete or not at all: an interrupted upgrade
  changes nothing.
- **Before changing the database, DROOL backs it up**, without the master key, to
  `backups/drool-pre-upgrade-<time>-schema<N>.tar.gz` in its data folder, where `<N>` is the
  database version it upgraded *from* (for example `drool-pre-upgrade-20261012-031500-schema20.tar.gz`). If it can't (a full disk,
  for example), it doesn't start, and nothing has been changed. Not every version changes the
  database; when nothing changes, no such backup is made.
- **DROOL keeps the newest three** pre-upgrade backups. Each is a full copy of the database (about
  the size of a normal backup: tens of megabytes for a small fleet, more for a busy one), so on a small
  disk delete old ones once an upgrade has proved good. Or turn them off with
  `backup.before_upgrade: false` (`DROOL_BACKUP_BEFORE_UPGRADE=false`) and back up yourself before
  every update ([Configuration](Configuration.md)).
- **Databases from builds older than `v0.1.0-alpha`** aren't guaranteed to upgrade: their database
  changes were rewritten during early development. From `v0.1.0-alpha` on, database changes are only
  ever added.
- **Shepherds follow.** About a minute after it starts, DROOL checks the release page for the signed
  description of its own Shepherd and, with automatic updates on (the default), updates the Shepherds:
  **Shepherds → Updates** shows it. Until they're updated, older Shepherds keep working: a newer
  controller works with Shepherds that speak the same protocol, and tells you what an older one can't
  do ([Shepherd Incompatible](Shepherd-Incompatible.md)). A development image (`:main`) isn't an official
  release, so its Shepherds aren't updated this way ([Updating Shepherds](Updating-Shepherds.md)).

## If something went wrong: going back

DROOL never downgrades its database in place. An older version refuses a database that a newer one
upgraded, says which version upgraded it, and leaves it alone. So going back depends on whether the
update changed the database:

- **The update made a `drool-pre-upgrade-…` backup** (it changed the database): stop DROOL, install
  the older version, restore that backup with the older version, start it. Anything done after the
  update (new hosts, runs, settings) is not in that backup and is lost.

  For a native installation:

  ```sh
  sudo systemctl stop drool
  sudo apt install --allow-downgrades ./drool_1.0.0-1_amd64.deb
  sudo -u drool drool restore /var/lib/drool/backups/drool-pre-upgrade-20261012-031500-schema20.tar.gz
  sudo systemctl start drool
  ```

  On Rocky, AlmaLinux, RHEL or Fedora, use `sudo dnf downgrade ./drool-1.0.0-1.x86_64.rpm` for the
  second line. The backup's file name is an example: use the newest `drool-pre-upgrade-…` file in
  `/var/lib/drool/backups/`. In a container, run the older image's `restore` on the same volume, as
  shown in [Restoring a Controller](Restoring-a-Controller.md); the backup is in the volume's
  `backups/` folder.
- **No pre-upgrade backup was made** (the database didn't change): install or start the older
  version again. Nothing else is needed.

Shepherds that were already updated stay on their newer version (Shepherds never downgrade) and
keep working with the older controller.

## Moving from a manual installation to the package

If you installed DROOL by hand, from the release archive or an older build, that installation's unit
file (`/etc/systemd/system/drool.service`) would keep running its own program after you install the
package, and the package warns about it. Remove exactly the files the manual installation added; your
data in `/var/lib/drool`, your settings in `/etc/drool` and the `drool` user stay as they are:

```sh
sudo systemctl stop drool
sudo rm -f /etc/systemd/system/drool.service /usr/local/bin/drool
sudo rm -f /usr/local/lib/drool/drool-agent-linux-amd64 /usr/local/lib/drool/drool-agent-linux-arm64
sudo rmdir /usr/local/lib/drool
sudo apt install ./drool_1.0.0-1_amd64.deb
sudo systemctl daemon-reload && sudo systemctl enable --now drool
```

- The second line removes the unit file and the program, the third the two Shepherd programs the
  release archive puts in `/usr/local/lib/drool/`.
- `rmdir` then removes that folder, but only if it's empty. If it says the folder doesn't exist (an
  installation older than the archive didn't have it) or isn't empty (something else is in it),
  nothing was removed; that's fine, go on.
- Use `dnf install` with the `.rpm` on RHEL-family systems.

## Related

[Updating Shepherds](Updating-Shepherds.md) · [Backup and Recovery](Backup-and-Recovery.md) ·
[Restoring a Controller](Restoring-a-Controller.md)
