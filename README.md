# DROOL

**Distributed Remote Operations & Observability Layer**

DROOL is a web-first platform for managing, automating, monitoring, and eventually segmenting
Linux systems through SSH or an agent.

It is built for self-hosters, advanced homelabs, enthusiasts and small organizations, and follows
a few principles:

- **Web first:** normal administration happens in the UI, without writing config files.
- **Simple by default, progressively hardened:** usable within minutes; hardening is recommended
  after first login, not required before it.
- **Identity, not IP:** a host's address is an attribute; its identity is cryptographic.
- **SSH and agent are both first class.**
- **A small core:** plugins extend application support without bloating it.
- **Network intent:** describing who should talk to whom, with safe firewall automation, is the
  long-term differentiator.

Linux is the product scope.

- **Design:** [`DESIGN.md`](DESIGN.md) (product and trust architecture, including future plans)
- **Implementation:** [`ARCHITECTURE-ALPHA.md`](ARCHITECTURE-ALPHA.md) (what the Technical Alpha
  actually does, and why)

## Status: v0.1.0-alpha (Technical Alpha)

"Technical Alpha" means: the core works end to end and has been tested against real
distributions, but DROOL is young. Expect rough edges, plain visuals, and breaking changes before
a beta. Run it where a mistake is recoverable, keep backups, and read the
[limitations](#known-limitations).

### What works

- **Setup:** one container or one static binary; first administrator claimed in the browser with
  a one-time setup code.
- **Hosts over SSH:** add hosts, approve their SSH host key by fingerprint (DROOL never connects
  to an unapproved key), one jump host, step-by-step connection test that explains failures.
- **Inventory and monitoring:** automatic discovery (OS, kernel, CPU, memory, disks, network,
  services, listening ports), background status checks (reachable, uptime, load, disk, failed
  units, pending and security updates, reboot required), a dashboard of what needs attention.
- **Jobs:** run a command or script on many hosts with live output, per-host results, timeouts,
  cancellation, retries of connection failures, and OS conditions (e.g. a different script for
  Debian and RHEL hosts).
- **Packages and updates:** install, remove and upgrade packages; system updates (APT and DNF,
  including dnf5) with a dry-run preview; removals of other packages always need your explicit
  approval.
- **Services and reboots:** start, stop, restart, reload, enable and disable systemd services, with
  the result verified; reboots that wait for the host to come back with its trusted key and a new
  boot id.
- **Accounts and audit:** local users with roles (admin, operator, auditor, viewer), API tokens,
  credentials encrypted at rest, an append-only audit log of every change.
- **Backup and restore** from the command line.

### Not in this release

These are planned (see `DESIGN.md`) but **do not exist yet**:

- the DROOL agent (SSH is the only way DROOL talks to hosts)
- firewall management and network intent
- scheduling (jobs run when you start them)
- desired state / drift detection
- managed files, file deployment, and the file manager
- plugins
- batch/canary rollouts, workflows, MFA, single sign-on, high availability, Windows

## Supported hosts

| | Tested in this release |
|---|---|
| Debian | 12, 13 |
| Ubuntu | 24.04, 26.04 (22.04 is expected to work) |
| RHEL family | Rocky Linux 9, 10 (Alma/RHEL 9+ are expected to work) |
| Fedora | 44 (dnf5) |

What a host needs:

- `sshd` reachable from the controller (directly, or through one jump host)
- a user DROOL can log in as with an SSH key or password
- for anything that changes the system: root login, or `sudo` (passwordless, or with a password
  you store in DROOL)
- **systemd** for service management and reboots. Hosts without it (most containers) still
  work for commands, scripts, packages and updates; DROOL shows them as *Unsupported* with an
  explanation.
- no agent, no Python

## Quick start (Docker)

```sh
docker compose up -d
docker compose logs drool
```

The logs show the URL and a one-time **setup code**. Open `https://<host>:8080/setup`, enter the
code and create the administrator account; you're signed in straight away. The browser warns
about the self-signed certificate on first visit. That's expected; you can configure your own
certificate later (see [Configuration](#configuration)).

The code is single use and expires after 60 minutes. To get a new one, restart the container or run
`docker compose exec drool drool setup code`. Once an administrator exists, setup is closed for
good.

For automated deployments, provide your own setup code with `DROOL_SETUP_SECRET_FILE`
(e.g. a Docker secret; see `compose.yaml`) or `DROOL_SETUP_SECRET` (at least 12 characters).

`docker compose up -d` builds the image on first use; `make image` builds it with the version
stamped in (compose then uses that `drool:local` image).

All data lives in the `drool-data` volume (`/data` in the container). The container runs as an
unprivileged user and needs no special permissions.

## Native installation (systemd)

```sh
make build                                   # or use a release binary
sudo install -m 0755 bin/drool /usr/local/bin/drool
sudo useradd --system --home-dir /var/lib/drool --shell /usr/sbin/nologin drool
sudo install -m 0644 packaging/systemd/drool.service /etc/systemd/system/drool.service
sudo systemctl daemon-reload
sudo systemctl enable --now drool
sudo journalctl -u drool                     # URL and setup code
```

Data, logs, TLS material and the credential master key are kept in `/var/lib/drool` (owner-only).
An optional config file goes in `/etc/drool/config.yaml`.

## First steps

1. **Claim the controller** at `/setup` with the setup code.
2. **Give DROOL access to a host.** *Credentials* shows DROOL's own SSH public key and a one-line
   command that adds it to a user's `authorized_keys`. Run it on the host as the user DROOL should
   log in as. (You can also store your own SSH key or a password.)
3. **Add the host** (*Hosts → Add host*): address, port, user, and the sudo setting.
4. **Approve its host key.** DROOL shows the fingerprint the host offered. Compare it with the one on
   the host itself (`ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`) and approve it. DROOL
   refuses to connect if the key ever changes, until you review it.
5. **Test the connection.** Each step (reachable, host key, login, sudo, OS, systemd) is shown
   with an explanation if it fails. Inventory is collected automatically afterwards.
6. From the host page or *Jobs → New job*: run commands and scripts, install updates, manage
   services, reboot.

### SSH and sudo

- DROOL connects only to hosts whose key you approved, and verifies the key on every connection,
  including after reboots and through the jump host.
- Commands run as the login user unless you tick *Run as root (sudo)*. Package, update, service and
  reboot operations always run as root: directly for root logins, otherwise through sudo.
- For `sudo` with a password, store it as a *sudo password* credential and select it on the host.
  If sudo requires a terminal (`requiretty`), enable *Requires a terminal for sudo* on the host.
- Passwords are sent to sudo on standard input, never on the command line, and are removed from
  job output.

### Packages and updates

- **Install / remove / upgrade** packages by name. Installing something already installed (or
  removing something absent) changes nothing and is reported as *no change*.
- **System update** modes: *Upgrade* never removes packages (on Debian/Ubuntu, updates that need
  new or removed packages are held back); *Full upgrade* may install or remove packages; *Security
  updates only* is available on RHEL-family and Fedora hosts (on Debian/Ubuntu security updates are
  counted and shown, but security-only updating is not supported yet).
- Every change is **simulated first**. If it would remove any package you didn't name, the job
  stops before changing anything unless you allowed removals. *Preview changes* shows exactly what
  each host would upgrade, install and remove; the *Allow these removals* box only appears when
  there are removals to allow.
- Metadata is refreshed before a system update by default. Reboots can follow *never*, *only if
  required*, or *always*.
- If another package operation holds the lock (often unattended-upgrades), DROOL waits and retries.
  If dpkg was interrupted earlier, DROOL tells you how to fix it (`sudo dpkg --configure -a`) rather
  than repairing it on its own.
- DROOL does **not** restart services during updates (on Ubuntu, needrestart is suspended for
  DROOL's runs). Services keep using old library versions until they are restarted or the host
  reboots; use a reboot afterwards (or restart the affected services) when an update says so.

## Backup and restore

```sh
# Docker: write a backup to your machine (safe while DROOL runs)
docker compose exec -T drool drool backup -o - > drool-backup.tar.gz
docker compose cp drool:/data/master.key ./master.key    # once; keep it safe and separate

# Native
sudo -u drool drool backup             # → /var/lib/drool/backups/drool-backup-<time>.tar.gz
```

A backup contains the database (hosts, pinned host keys, groups, users, credentials in encrypted
form, job history and output, audit log), the self-signed TLS certificate, and the config file if
you use one.

**The master key.** Stored credentials (SSH keys, passwords) are encrypted with
`master.key`, which is **not** included unless you ask for it:

- `drool backup` (default): safe to keep almost anywhere, but **not a complete disaster-recovery
  backup on its own**. To get your credentials back you also need the `master.key` file (keep a
  copy somewhere safe and separate). The command tells you this, and the key's id, every time.
- `drool backup --include-master-key`: self-contained, but anyone holding the file can decrypt your
  credentials. Store it encrypted.

Restore (DROOL must be stopped):

```sh
# Docker (the container runs as an unprivileged user, so the key file must be readable for it)
docker compose stop drool
chmod 644 master.key
docker compose run --rm -T -v "$PWD/master.key:/tmp/master.key:ro" drool \
  restore --master-key /tmp/master.key - < drool-backup.tar.gz
chmod 600 master.key
docker compose start drool
# For a backup made with --include-master-key, leave out both chmod lines and -v/--master-key.

# Native
sudo systemctl stop drool
sudo -u drool drool restore - < /path/to/drool-backup.tar.gz
sudo systemctl start drool
```

Restore checks the whole archive (checksums, database integrity, version) and that a master key
opens every credential **before** changing anything; if anything is wrong it stops and your
installation is untouched. The previous files are kept in `pre-restore-<time>/` in the data
directory. Everyone signs in again afterwards.

If the backup has no master key and this machine doesn't have the right one, restore explains your
options: `--master-key /path/to/master.key`, or `--discard-credentials` to restore everything
except the stored credentials (hosts, keys, history and users are kept; you assign credentials
again). The same command is the way out if you ever lose `master.key`: DROOL refuses to start
with a missing or wrong key rather than silently making your credentials unreadable.

## Upgrading

Take a backup, then replace the binary (native: stop, install, start) or rebuild the image
(`git pull && docker compose up -d --build`). Database migrations
run automatically on start and only move forward. A newer database is never opened by an older
DROOL.

## Configuration

Precedence: built-in defaults → config file (`--config`, default `/etc/drool/config.yaml`
if present) → `DROOL_*` environment variables. The variable name is the key path in upper
snake case, e.g. `server.web_port` → `DROOL_SERVER_WEB_PORT`. **Settings → System** in the UI
shows every effective value and where it came from.

TLS defaults to a self-signed certificate. Use `server.tls.mode: provided` with
`server.tls.cert_file`/`key_file` for your own certificate. `server.tls.mode: off` (for a reverse
proxy on the same host) is only accepted with a loopback `server.bind`, so DROOL never serves
plain HTTP to the network by accident.

## Known limitations

- **One controller, SQLite, no high availability.** Sized for homelabs and small fleets; tested
  with tens of hosts, not thousands.
- **SSH only, at most one jump host.**
- **No scheduling, no rollout batches:** a job runs on its targets now, a configurable number of
  hosts at a time, optionally stopping after N failures.
- **If the controller crashes or restarts during a job,** commands already running on hosts keep
  running; those hosts are marked *interrupted (outcome unknown)*. Check them before re-running.
- **If a connection drops while a command runs,** the outcome is reported as unknown; only
  idempotent operations (packages, updates, service states) are retried automatically.
- **Security-only updates** are not supported on Debian/Ubuntu.
- **No `--no-install-recommends` option:** package installs follow APT's default for recommended
  packages.
- **Hosts without systemd** can't use service management or reboots.
- **Local accounts only;** no MFA or single sign-on yet.
- **Backup and restore are command-line only** (Settings → System shows the commands).
- The **web UI is functional, not polished**; a visual design pass comes later.
- Migrations made before `v0.1.0-alpha` were edited during development; databases from
  pre-release builds are not guaranteed to upgrade. From `v0.1.0-alpha` on, migrations are
  append-only.

## Build and run from source

Requirements: Go 1.27+, Node 22+ and npm. [sqlc](https://sqlc.dev) only to regenerate query code
(`go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1`).

```sh
make build      # web UI + static binary at bin/drool
make run        # serves https://127.0.0.1:8080 and prints the setup code
```

`drool user create --username NAME` remains available as a recovery path.

### Frontend development

```sh
make run     # terminal 1: controller on https://127.0.0.1:8080
make dev     # terminal 2: Vite dev server with hot reload, proxying /api
```

## Development

```sh
make test               # Go tests + frontend typecheck
make vet fmt-check
make generate           # regenerate sqlc code and TypeScript API types
make check-generated    # CI: fail if generated code is stale
make test-integration   # real distributions in unprivileged containers (Docker, ~10 min)
```

The VM tests for real systemd and reboots are opt-in and slow (software emulation, no special
privileges): `go test -tags integration ./test/integration -run TestVM -vm debian-12 -v -timeout 4h`.

The REST contract lives in [`api/openapi.yaml`](api/openapi.yaml). A Go test fails if routes
and the spec drift apart. The UI's TypeScript client types are generated from the spec.

### Frontend styling

All colors, radii and fonts are semantic design tokens in `web/src/styles/index.css`, and
components use only those tokens through Tailwind utilities (`bg-surface`, `text-muted`,
`border-line`, ...). UI primitives live in `web/src/components/ui/`. Restyling means changing
tokens and primitives, not pages.
