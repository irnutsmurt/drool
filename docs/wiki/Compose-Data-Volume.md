# Compose data volume

Where DROOL keeps its data when it runs with Docker Compose or Synology Container Manager, how that
place is named, and what to check when you update from an older `compose.yaml`.

## In short

- Everything DROOL stores (hosts, credentials, history, its keys) is in one Docker **volume** called
  **`drool_drool-data`**.
- `compose.yaml` writes that name out itself, so it doesn't depend on the name of your DROOL folder
  or Synology project: moving or renaming the folder keeps your data.
- A second DROOL on the same machine gets a name of its own with `DROOL_INSTANCE`
  ([below](#more-than-one-drool-on-one-machine)); its volume is `<DROOL_INSTANCE>_drool-data`.
- **Installed with a `compose.yaml` from before 2026-10-14?** Check your volume's name once before
  you start DROOL with the new file ([below](#updating-from-an-older-composeyaml)). If it's
  `drool_drool-data`, as in this guide's folder `~/drool` or project `drool`, there's nothing to do.

## Why the name is written out

Older `compose.yaml` files left the name to Docker, which names a volume after the folder (or
Synology project): `<folder>_drool-data`. Renaming the folder, moving the files to a folder with
another name, or giving a project another name then gave DROOL a new, empty volume: DROOL looked
brand-new and asked for setup again, while your data sat unused in the old volume.

The name in the current file is the one those files gave in this guide's folder `~/drool` and
project `drool`, so most installations keep their volume without changing anything.

## Updating from an older compose.yaml

Do this once, when you replace your `compose.yaml` with the current one (Docker Compose), or paste the
current YAML into your project (Synology). Updating the image alone (`docker compose pull`) doesn't
change the name.

### Docker Compose

1. Go to your DROOL folder: `cd ~/drool`.
2. **Make a backup first**, and make sure you have a copy of the master key
   ([Docker Backup](Docker-Backup.md)):

   ```sh
   docker compose exec -T drool drool backup -o - > drool-backup-$(date +%Y%m%d-%H%M%S).tar.gz
   ```

3. See which volume DROOL uses now:

   ```sh
   docker inspect -f '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Name}}{{end}}{{end}}' "$(docker compose ps -q drool)"
   ```

   It prints the volume's name, for example `drool_drool-data`. (If DROOL isn't running,
   `docker volume ls --filter name=drool-data` lists the candidates.)
4. - **`drool_drool-data`**: nothing to do. Update as usual ([Updating DROOL](Updating-DROOL.md)).
   - **Another name**, for example `homelab_drool-data`: tell the new `compose.yaml` that name, with
     the part before `_drool-data`, in a file named `.env` next to it:

     ```sh
     echo 'DROOL_INSTANCE=homelab' >> .env
     ```

     `>>` adds the line to `.env`, or creates the file if there isn't one. Then update as usual.
5. Open DROOL. You should see **Sign in to DROOL**, not **Set up DROOL**. If DROOL asks for setup,
   stop: see [DROOL asks for setup after an update](#drool-asks-for-setup-after-an-update-or-a-move).

### Synology Container Manager

1. In Container Manager, open **Project** and look at your DROOL project's name.
   - **`drool`**: nothing to do. Paste the current YAML as usual.
   - **Another name**, for example `homelab`: its data is in `homelab_drool-data`. Do the steps below.
2. **Make a backup first.** It needs a terminal once: turn on SSH in DSM (**Control Panel → Terminal
   & SNMP → Enable SSH service**), sign in to the NAS with SSH as an administrator, and run, with your
   project's name in place of `homelab`:

   ```sh
   cd /volume1/docker
   sudo docker exec homelab-drool-1 drool backup -o - > drool-backup-$(date +%Y%m%d-%H%M%S).tar.gz
   sudo docker cp homelab-drool-1:/data/master.key ./master.key
   ```

   The backup and a copy of the [master key](Master-Key.md) are then in the **docker** shared folder:
   copy both somewhere else, and keep the key apart from the backups.
3. Select the project, **Action → Stop**, and open its **YAML Configurations** tab.
4. Paste the current YAML ([step 3 of the installation](Install-On-a-Synology-NAS.md#step-3-paste-drools-yaml)),
   copying over any changes you made to the old one. Then find this line near the top:

   ```yaml
     volume: &data-volume ${DROOL_INSTANCE:-drool}_drool-data
   ```

   and replace `drool` after `:-` with your project's name:

   ```yaml
     volume: &data-volume ${DROOL_INSTANCE:-homelab}_drool-data
   ```

5. Save, then **Action → Build**. If the project doesn't show as running afterwards, choose
   **Action → Start**.
6. Open DROOL: you should see **Sign in to DROOL**.

## DROOL asks for setup after an update or a move

When DROOL starts with no data, its setup page and startup message say so, with the volume it
started in: *This DROOL has no data yet: it starts new in the volume drool_drool-data.* If you
expected your existing DROOL:

1. **Don't set it up.** Stop it without deleting anything: `docker compose down` (never with `-v`).
2. List DROOL's volumes and when each was made:

   ```sh
   docker volume ls --filter name=drool-data
   docker volume inspect -f '{{.Name}} {{.CreatedAt}}' $(docker volume ls -q --filter name=drool-data)
   ```

   The one created a few minutes ago is the new, empty one; the older one is your DROOL.
3. Put the older one's name in `.env` as in [step 4 above](#docker-compose), for example
   `DROOL_INSTANCE=homelab` for `homelab_drool-data`, and start DROOL again: `docker compose up -d`.
4. Once you've signed in and your hosts are there, remove the empty volume, by its exact name, for
   example `docker volume rm drool_drool-data`. Check the name twice: removing a volume deletes
   everything in it.

On a Synology NAS: stop the project, edit the YAML line as in
[the Synology steps](#synology-container-manager), and build it again.

## More than one DROOL on one machine

Give each its own folder, and in each folder a `.env` file with a name of its own and its own ports:

```text
DROOL_INSTANCE=lab
DROOL_AGENT_PUBLIC_PORT=9444
```

This DROOL keeps its data in `lab_drool-data`. Change its web port too, on the `web:` line at the top
of its `compose.yaml` ([Use other ports](Install-With-Docker-Compose.md#use-other-ports)).

If two installations end up with the same name, the second one doesn't start while the first runs:
its log says *the data directory is in use by a running DROOL controller*. Give it its own name. Two
DROOLs never share one database at the same time.

## Starting over

To throw away a test installation's data completely, remove its volume by name. **This deletes
everything DROOL stored** (hosts, credentials, history):

```sh
cd ~/drool
docker compose down
docker volume rm drool_drool-data
docker compose up -d
```

Use `<DROOL_INSTANCE>_drool-data` if you set `DROOL_INSTANCE`. (`docker compose down -v` removes the
volume too, but only from the project that created it: after a move or a rename it leaves the volume
in place.)

## Related

[Install With Docker Compose](Install-With-Docker-Compose.md) ·
[Install on a Synology NAS](Install-On-a-Synology-NAS.md) · [Docker Backup](Docker-Backup.md) ·
[Updating DROOL](Updating-DROOL.md) · [Setup Problems](Setup-Problems.md)
