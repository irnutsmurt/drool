# Installing DROOL

DROOL runs on **one** Linux machine, the *controller*. Everything else (the machines you want to
manage) only needs SSH, or later a Shepherd. This page gets the controller running. Every method
ends the same way: DROOL prints an address and a one-time **setup code**, and you continue with
[First Login](First-Login.md).

## Before you start

You need:

- **A 64-bit Linux machine** (amd64 or arm64) to run DROOL on. It can be a small VM or a mini PC;
  DROOL is a single unprivileged program with its own database (SQLite). In the examples this
  machine is `drool.example.net` (10.10.0.10).
- **One of:** Docker (with Docker Compose), rootless Podman 4.4 or newer, or a Debian, Ubuntu,
  Rocky Linux, AlmaLinux, RHEL or Fedora system where you can install a package.
- **Two network ports on that machine:**
  - **8080** — the web interface. Your browser must be able to reach it.
  - **9443** — where Shepherds (DROOL agents) connect. Only needed if you'll use Shepherds; your
    managed hosts must be able to reach it.

  DROOL never changes the firewall of the machine it runs on. If that machine has a firewall, open
  these ports yourself.
- **Room for history.** Plan on roughly 1.5 MB per host for monitoring history and about 1 MB per
  host for alert history, plus the output of your runs (kept for 30 days by default).

DROOL doesn't need root to run, and the container image needs no special privileges.

## Where to get DROOL

There are three sources, from most to least recommended:

1. **Release files**, from the [GitHub Releases page](https://github.com/irnutsmurt/drool/releases):
   packages (`.deb`, `.rpm`), a container image file, archives and a `SHA256SUMS` file. *At the time
   this wiki was written, no official release had been published yet.* When there is one, prefer it.
2. **The development image** on GitHub Container Registry, `ghcr.io/irnutsmurt/drool:main`: the
   newest code that passed DROOL's automated tests. It is **not a release** — it changes with every
   commit, isn't signed, and `drool version` reports it as a *development build*. Fine for trying
   DROOL out and for following development.
3. **Building it yourself** from the source code with `make release`, which produces the same files
   as a release (see the README's *Build and run from source*).

## Choose a method

| Method | Good for | Data lives in |
|---|---|---|
| [Docker Compose](#docker-compose) | Most people; any Linux with Docker | The Docker volume `<folder>_drool-data` |
| [Rootless Podman](#rootless-podman) | Podman users who want a systemd-managed container without root | The Podman volume `drool-data` |
| [Native package](#native-package-deb-or-rpm) | Debian, Ubuntu, Rocky/Alma/RHEL, Fedora | `/var/lib/drool` |
| [Release archive](#release-archive) | Other systemd-based distributions | `/var/lib/drool` |

Not supported for the controller: Windows, macOS, BSD and Kubernetes.

## Docker Compose

DROOL ships a `compose.yaml` that runs the container read-only, without any Linux capabilities and
without access to the host network, the Docker socket or any host device. Keep it that way: DROOL
needs none of them.

**With the development image** (works today):

```sh
mkdir drool && cd drool
curl -fsSLO https://raw.githubusercontent.com/irnutsmurt/drool/main/compose.yaml
echo 'DROOL_IMAGE=ghcr.io/irnutsmurt/drool:main' > .env
docker compose pull
docker compose up -d
docker compose logs drool
```

What each line does:

- `mkdir drool && cd drool` — a folder of its own. Docker names DROOL's data volume after this
  folder (`drool_drool-data`), so always run later `docker compose` commands from here.
- `curl … compose.yaml` — downloads the Compose file that describes the container.
- `echo 'DROOL_IMAGE=…' > .env` — tells that Compose file which image to run. The single quotes
  are part of the command; type them as shown.
- `docker compose pull` and `up -d` — download the image and start DROOL in the background.
- `docker compose logs drool` — shows DROOL's startup message with the address and setup code.

**With a release** (once one is published), put the release's `compose.yaml` and its image file in
the folder and load the image instead of pulling it:

```sh
sha256sum --check --ignore-missing SHA256SUMS
docker load -i drool_1.0.0_image.oci.tar
docker compose up -d
docker compose logs drool
```

The first line checks that the files you downloaded are the ones the release lists (each should
print `OK`). The version in the file name (`1.0.0` here) is whatever release you downloaded.

**If you'll use Shepherds**, tell DROOL the name or address your hosts should use to reach it.
Inside a container DROOL can't work that out. Edit `compose.yaml` and replace the commented-out
`environment:` lines under the `drool` service with:

```yaml
    environment:
      DROOL_AGENT_ADVERTISE: drool.example.net:9443
```

Use your controller's real name or address in place of `drool.example.net`, keep the four leading
spaces, then run `docker compose up -d` again.

## Rootless Podman

As your normal user (not root), with Podman 4.4 or newer. Debian 12 ships Podman 4.3, which can't do
this: use Docker or the native package there.

1. Create Podman's folder for systemd units and copy the release's `drool.container` file into it:

   ```sh
   mkdir -p ~/.config/containers/systemd
   cp drool.container ~/.config/containers/systemd/
   ```

   With a release image file, load the image too: `podman load -i drool_1.0.0_image.oci.tar`.
   With the development image instead, open `~/.config/containers/systemd/drool.container` and
   change its `Image=` line to `Image=ghcr.io/irnutsmurt/drool:main`.
2. If you'll use Shepherds, remove the `#` in front of the `Environment=DROOL_AGENT_ADVERTISE=…`
   line in that file and put your controller's name or address there.
3. Start it and read the startup message:

   ```sh
   systemctl --user daemon-reload
   systemctl --user start drool
   journalctl --user -u drool -n 30
   loginctl enable-linger "$USER"
   ```

   The last line keeps DROOL running after you log out and starts it at boot.

DROOL's commands then go through `podman exec drool drool …`.

## Native package (.deb or .rpm)

Download the package for your system and CPU from the release, then:

```sh
sha256sum --check --ignore-missing SHA256SUMS
sudo apt install ./drool_1.0.0-1_amd64.deb          # Debian, Ubuntu
sudo dnf install ./drool-1.0.0-1.x86_64.rpm         # Rocky Linux, AlmaLinux, RHEL, Fedora
sudo systemctl enable --now drool
sudo journalctl -u drool -n 30
```

Run only the install line that matches your distribution. The `./` in front of the file name
matters: it tells `apt` and `dnf` to install that file rather than look for a package with that name.

The package:

- creates a `drool` system user and runs DROOL as the `drool.service` systemd service, with no
  special privileges;
- puts the program in `/usr/bin/drool`, the Shepherd programs it hands to new hosts in
  `/usr/lib/drool/`, and all data in `/var/lib/drool` (owned by `drool`, mode 0750);
- reads an optional configuration file, `/etc/drool/config.yaml` (an example is in
  `/usr/share/drool/config.example.yaml`).

Run DROOL's own commands as its user, for example `sudo -u drool drool backup`. As root they refuse,
because files root writes in DROOL's data folder would break the service.

**Removing** the package (`apt remove`, `apt purge`, `dnf remove`) stops DROOL and removes the
program, but never your data: `/var/lib/drool`, `/etc/drool` and the `drool` user stay until you
delete them yourself.

## Release archive

For systemd-based distributions without a package. The archive contains `INSTALL.md` with these
steps:

```sh
sudo install -m 0755 drool /usr/local/bin/drool
sudo install -d /usr/local/lib/drool
sudo install -m 0755 drool-agent-linux-amd64 drool-agent-linux-arm64 /usr/local/lib/drool/
sudo useradd --system --user-group --home-dir /var/lib/drool --no-create-home --shell /usr/sbin/nologin drool
sudo install -m 0644 drool.service /etc/systemd/system/drool.service
sudo systemctl daemon-reload && sudo systemctl enable --now drool
sudo journalctl -u drool -n 30
```

## Finding the setup code

Whichever method you used, the last command showed DROOL's startup message. It looks like this
(the code and times differ on your machine):

```text
================================================================
 DROOL is ready.

 Open:
   https://<this-host's-address>:8080/setup

 Setup code:
   482913

 Single use. Expires 15:25 UTC (in 60 min).
 New code: restart, or run `drool setup code`.
================================================================
```

In a container DROOL can't know how your browser reaches it, so it prints `<this-host's-address>`:
replace that with the name or address of the machine DROOL runs on, in the examples
`https://drool.example.net:8080/setup`. A package or archive installation lists the machine's own
addresses instead, for example `https://10.10.0.10:8080/setup`.

The code works once and expires after 60 minutes. If it expired, get a new one:

| Installation | Command |
|---|---|
| Docker Compose | `docker compose exec drool drool setup code` |
| Podman | `podman exec drool drool setup code` |
| Package or archive | `sudo -u drool drool setup code` |

Restarting DROOL also prints a new code, as long as nobody has finished setup yet.

## Next

Open the address in your browser and continue with [First Login](First-Login.md).

**Related:** [Updating DROOL](Updating-DROOL.md) · [Backup and Recovery](Backup-and-Recovery.md) ·
[Ports and File Locations](Ports-and-Files.md) · [Configuration](Configuration.md)
