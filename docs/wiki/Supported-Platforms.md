# Supported platforms

Where the DROOL controller runs, which machines it manages, on which CPUs, and which Shepherd versions
work together.

**Supported** means DROOL's release tests run on it
([RELEASING.md](https://github.com/irnutsmurt/drool/blob/main/RELEASING.md)) and a problem there is a
bug. **Expected to work** means it's built from the same packages or binaries as something supported,
but not tested: tell us if it doesn't. Anything else is **not supported**. DROOL has no stable release
yet; these are the platforms its release tests cover today, and the ones the first stable release is
being qualified on.

## Where DROOL runs (the controller)

| How | Supported | Expected to work |
|---|---|---|
| Container image (`linux/amd64`, `linux/arm64`) | Docker Engine, also rootless Docker; rootless Podman 4.4 or newer, with the Quadlet unit | Rootful Podman, other OCI runtimes with the same settings; Synology Container Manager (DSM 7.2 or newer: Docker with Compose underneath, [guide](Install-On-a-Synology-NAS.md)) |
| `.deb` package | Debian 12, 13; Ubuntu 22.04, 24.04, 26.04 | Other Debian-based distributions with systemd |
| `.rpm` package | Rocky Linux 9, 10; AlmaLinux 9, 10; Fedora 44 | RHEL 9, 10 and other RHEL rebuilds |
| Release archive (static binary) | The distributions above, on amd64 and arm64 | Any Linux with systemd on amd64 or arm64 |

- The controller needs no root, no capabilities and no access to its machine's firewall or devices: it
  is one unprivileged program and a data folder.
- Podman older than 4.4 has no Quadlet support (Debian 12 ships 4.3): use Docker or the package there.
- Not supported for the controller: Kubernetes (there are no manifests for it), Windows, macOS and BSD.
- DROOL is sized for homelabs and small fleets: one controller with one database (SQLite), tested with
  tens of hosts, not thousands, and no high availability.

How to install with each: [Installing DROOL](Installing-DROOL.md).

## Hosts DROOL manages

| Distribution | Status |
|---|---|
| Debian 12, 13 | Supported |
| Ubuntu 22.04, 24.04, 26.04 | Supported |
| Rocky Linux 9, 10 | Supported |
| AlmaLinux 9, 10 | Supported |
| Fedora 44 | Supported |
| RHEL 9, 10 | Expected to work: the same packages as Rocky Linux and AlmaLinux, not tested (testing needs a subscription) |
| Other Debian- or RHEL-based distributions (Raspberry Pi OS, Linux Mint, CentOS Stream, Oracle Linux), newer Fedora | Expected to work |
| Other Linux distributions (Arch, openSUSE, Alpine, …) | Not supported: packages and updates need APT or DNF; commands and scripts may work |
| Windows, macOS, BSD | Not supported |

What a host needs:

- `sshd` reachable from the controller, directly or through one [jump host](Jump-Hosts.md), or a
  [Shepherd](SSH-vs-Shepherds.md);
- a user DROOL can log in as, with an SSH key, a password or one of DROOL's
  [SSH certificates](Short-Lived-SSH-Certificates.md);
- for anything that changes the system: root login, or `sudo` (without a password, or with one you store
  in DROOL);
- **systemd** for service management, reboots, the firewall and a Shepherd's resource budget. Hosts
  without it (most containers) still work for commands, scripts, packages and updates; DROOL shows them as
  *Unsupported*, with the reason.

Nothing else: no agent and no Python. Setting up your first one: [Your First Host](Your-First-Host.md).

## CPU architectures

| | amd64 (x86-64) | arm64 (aarch64) | Others (32-bit ARM, i386, RISC-V, …) |
|---|---|---|---|
| Controller: packages, archive, container image | Supported | Supported | Not built |
| Shepherd | Supported | Supported | Not built: manage those hosts over SSH |
| Hosts managed over SSH | Supported | Supported | Expected to work (DROOL only runs standard tools on them) |

arm64 is tested on an emulated arm64 machine (Debian 12): the controller package and the archive under
systemd, the container image under rootless Docker, and a Shepherd with the same tests as on amd64
(services, reboots, updates, SSH certificates, the firewall). It isn't tested on physical boards such as a
Raspberry Pi: the binaries are the same, but their speed there hasn't been measured.

## Shepherd versions

The Shepherd protocol has its own version (currently 1), separate from DROOL's version and from its
database's.

- **A controller works with every Shepherd that speaks the same protocol**, older or newer than itself.
- **What a Shepherd can do is negotiated, not guessed from its version.** It announces its capabilities;
  DROOL never sends it an operation it can't do. It uses SSH where the host allows it, or refuses and says
  to update the Shepherd. The Shepherds page shows what each one lacks
  ([Shepherd Incompatible](Shepherd-Incompatible.md)).
- **Update the controller first, then the Shepherds** ([Updating DROOL](Updating-DROOL.md),
  [Updating Shepherds](Updating-Shepherds.md)). After going back to an older controller, Shepherds that
  were already updated keep working with it.

## Related

[Installing DROOL](Installing-DROOL.md) · [SSH vs Shepherds](SSH-vs-Shepherds.md) ·
[Shepherd Incompatible](Shepherd-Incompatible.md) · [What DROOL Does](What-DROOL-Does.md)
