# Installing DROOL

DROOL runs on **one** Linux machine, the *controller*. The machines you want to manage need nothing
from this page: DROOL reaches them over SSH, or later through a Shepherd (its optional agent).

This page helps you choose how to install DROOL. Each way has its own step-by-step page, which starts
with nothing installed and ends with DROOL's setup screen open in your browser.

## How it goes, whichever way you choose

1. You install DROOL on one machine and start it.
2. When it starts, DROOL prints a short **startup message** with two things: the **address** of its
   setup page (for example `https://10.10.0.10:8080/setup`) and a one-time **setup code** (six
   digits, for example `482913`). The install page for your method shows exactly where that message
   appears.
3. You open the address in your browser, enter the code, and create the first administrator account
   ([First Login](First-Login.md)). From then on, setup is closed and everyone signs in with a username
   and password.

## Before you start

You need:

- **A 64-bit Linux machine** (amd64 or arm64) to run DROOL on, which stays on. A small VM or a mini PC
  is plenty: DROOL is a single unprivileged program with its own database (SQLite). In the examples
  this machine is `drool.example.net` (10.10.0.10).
- **One of:** Docker with Docker Compose; rootless Podman 4.4 or newer; or, once DROOL has a release,
  a Debian, Ubuntu, Rocky Linux, AlmaLinux, RHEL or Fedora system to install a package on.
- **Two network ports on that machine:**
  - **8080**: the web interface. Your browser must be able to reach it.
  - **9443**: where Shepherds (DROOL agents) connect. Only needed if you'll use Shepherds; your managed
    hosts must be able to reach it.

  DROOL never changes the firewall of the machine it runs on. If that machine has a firewall, you open
  these ports yourself (the install pages show how).
- **A computer with a web browser** that can reach that machine over the network (it can be the same
  machine).
- **A terminal on that machine**, where you type or paste commands. Every command on the install pages
  is explained.
- **Room for history.** Plan on roughly 1.5 MB per host for monitoring history and about 1 MB per host
  for alert history, plus the output of your runs (kept for 30 days by default).

DROOL doesn't need root to run, and its container needs no special privileges.

## Development image or release?

- **The development image**, `ghcr.io/irnutsmurt/drool:main` on GitHub Container Registry, is the
  newest DROOL code that passed DROOL's automated tests. It's built and published automatically, so
  it changes with every change to DROOL: when you update, you get whatever is newest. It isn't signed
  with DROOL's release key, and `drool version` reports it as a *development* build. **DROOL has no
  official release yet, so today this is the only ready-made way to run DROOL**, with
  [Docker Compose](Install-With-Docker-Compose.md) or [Podman](Install-With-Podman.md). Good for
  trying DROOL and following its development; keep backups.
- **A release**, published on the [Releases page](https://github.com/irnutsmurt/drool/releases), is a
  fixed version: a set of files (packages, archives, a container image file, `compose.yaml`) that
  never change once published, with a `SHA256SUMS` file to check them against and, for official
  releases, a signature. Packages and the release archive exist only as part of a release. When there
  is one, prefer it for anything you depend on.
- **Building it yourself** from the source code with `make release` produces the same files as a
  release (see the README's
  [Build and run from source](https://github.com/irnutsmurt/drool#build-and-run-from-source)).

## Choose a method

| Method | Available | Good for | DROOL's data is in |
|---|---|---|---|
| [Docker Compose](Install-With-Docker-Compose.md) | **Today**, with the development image | Most people: any 64-bit Linux with Docker. The quickest way to try DROOL | The Docker volume `drool_drool-data` |
| [Rootless Podman](Install-With-Podman.md) | **Today**, with the development image | Podman users who want DROOL as a systemd service without root | The Podman volume `drool-data` |
| [Package (.deb or .rpm)](Install-From-a-Package.md) | With a release | Debian, Ubuntu, Rocky Linux, AlmaLinux, RHEL, Fedora | `/var/lib/drool` |
| [Release archive](Install-From-the-Release-Archive.md) | With a release | Other systemd-based distributions | `/var/lib/drool` |

Not sure? Use [Docker Compose](Install-With-Docker-Compose.md).

Not supported for the controller: Windows, macOS, BSD and Kubernetes.

## After installing

- [First Login](First-Login.md): enter the setup code and create your administrator account.
  [Where do I get the setup code?](First-Login.md#where-do-i-get-the-setup-code) sums up, for every
  method, where the code appears and how to get a new one.
- Something went wrong? [Setup Problems](Setup-Problems.md).

**Related:** [Updating DROOL](Updating-DROOL.md) · [Backup and Recovery](Backup-and-Recovery.md) ·
[Ports and File Locations](Ports-and-Files.md) · [Configuration](Configuration.md)
