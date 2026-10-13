# Ports and files

What DROOL listens on, what it connects to, and what it puts where.

## Network connections

| From | To | Port | What for |
|---|---|---|---|
| Your browser | The controller | **TCP 8080** (HTTPS) | The web interface and the API (`server.web_port`) |
| Shepherds | The controller | **TCP 9443** (mutual TLS) | Shepherd connections; only if you use Shepherds (`agent.port`) |
| The controller | Managed hosts (or the jump host) | **TCP 22** (SSH), or the port set on the host | Managing hosts over SSH |
| The jump host | Hosts behind it | Their SSH port | Connections forwarded through the jump host |
| The controller | Notification destinations | Their ports (SMTP 587/465/25, HTTPS 443, …) | Alerts ([Notification Routing](Notification-Routing.md)) |
| The controller | Your audit receiver | HTTPS | The [remote audit copy](Remote-Audit-Copy.md) |

The controller makes no other connections: no telemetry, no update checks, no online GeoIP lookups.

## On the controller

| Path (native) | In containers | What |
|---|---|---|
| `/var/lib/drool/` | `/data/` (Docker: the volume `drool_drool-data`; Podman: `drool-data`) | The data directory |
| `…/drool.db` | | The database |
| `…/master.key` | | The [master key](Master-Key.md) |
| `…/tls/` | | The self-signed certificate |
| `…/artifacts/` | | Stored files: deployments, previous versions for rollback, Desired State contents |
| `…/backups/` | | Backups made with `drool backup` (and before upgrades) |
| `…/logs/` | (logs go to the container's output) | DROOL's logs, rotated: `app.log`, `audit.log`, `security.log`, `job.log` |
| `…/geoip/` | | [GeoIP and ASN](GeoIP-and-ASN.md) databases, if you add them |
| `…/pre-restore-<time>/` | | Your previous files after a restore |
| `/etc/drool/config.yaml` | (environment variables) | The [configuration file](Configuration.md), if any |
| `/usr/bin/drool` (package), `/usr/local/bin/drool` (archive) | | The program |
| `/usr/lib/drool/` (package), `/usr/local/lib/drool/` (archive) | | The Shepherd binaries DROOL installs on hosts |
| `/usr/share/drool/` | | Licence notices and the example configuration |
| `/usr/lib/systemd/system/drool.service` (package), `/etc/systemd/system/drool.service` (archive) | | The `drool` service |

DROOL's **startup message**, with the setup page's address and, until the first administrator
exists, the [setup code](First-Login.md#where-do-i-get-the-setup-code), goes to the system journal on
a native installation (`sudo journalctl -u drool -n 50`), to the container's log with Docker
(`docker compose logs drool`, in `~/drool`), and to your user's journal with Podman
(`journalctl --user -u drool -n 50`).

### Container installations

| File | What |
|---|---|
| `~/drool/compose.yaml` | Docker Compose: how to run DROOL ([Install with Docker Compose](Install-With-Docker-Compose.md)). Run `docker compose` commands in this folder |
| `~/drool/.env` | Docker Compose, optional: another image to run than the one `compose.yaml` names (`DROOL_IMAGE=…`), for example one you built yourself |
| `~/.config/containers/systemd/drool.container` | Podman: DROOL's Quadlet unit ([Install with Podman](Install-With-Podman.md)) |

## On managed hosts

DROOL installs no software on an SSH host to manage it. What appears there depends on what you use:

| What | Where |
|---|---|
| [DROOL's own account](Dedicated-SSH-Account.md) (when DROOL created it), in the examples `drooladmin` | The account itself, `/etc/sudoers.d/drool-account-drooladmin` (its passwordless sudo), `/var/lib/drool/ssh-accounts/drooladmin` (DROOL's record, root only) |
| DROOL's SSH key (DROOL puts it there for its own account; you, for an account of yours) | `~drooladmin/.ssh/authorized_keys` |
| Trust in DROOL's [SSH certificate authority](Short-Lived-SSH-Certificates.md) | `/etc/ssh/drool_user_ca.pub`, `/etc/ssh/sshd_config.d/40-drool-user-ca.conf` |
| DROOL's [firewall](What-DROOLs-Firewall-Does.md) (enforced hosts) | The nftables table `inet drool`, `/var/lib/drool/firewall/`, `/usr/libexec/drool-firewall/`, `drool-firewall.service` |
| Previous versions of [deployed files](Rolling-Back-a-File.md) | Kept by DROOL on the controller, not on the host |
| The optional storage write/read probe | A small test file on the filesystems you choose ([Storage Health](Storage-Health.md)) |

### A Shepherd

| What | Where |
|---|---|
| The service | `drool-agent.service` (`/etc/systemd/system/drool-agent.service`) |
| The program, and staged updates | `/usr/libexec/drool-agent/` |
| What DROOL may do on this host (the capability ceiling) | `/etc/drool/agent.yaml` ([Shepherd Capability Presets](Shepherd-Capability-Presets.md)) |
| Release-signing keys the host trusts | `/etc/drool/release-keys.d/` |
| Identity and state | `/var/lib/drool-agent/` |
| Every operation it received (no command text, file contents or secrets) | `/var/log/drool-agent/operations.log` |

`drool-agent status` on the host shows its identity, certificate, trust, endpoints and capabilities.

## Related

[Configuration](Configuration.md) · [Backup and Recovery](Backup-and-Recovery.md) · [SSH vs Shepherds](SSH-vs-Shepherds.md)
