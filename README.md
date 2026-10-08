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

`main` additionally contains file management and file deployment ([Files](#files)), the
[DROOL agent](#the-drool-agent) and the [firewall](#firewall), which are not part of a tagged
release yet.

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
- **Files** (on `main`, after v0.1.0-alpha): browse a host's files, upload, download, edit text
  files, rename, move, delete, change permissions and owners, as the login user or explicitly
  with administrator privileges. **File deployment** sends one file to many hosts with a per-host
  preview and diff, an optional validation command, an optional service reload or restart, and
  one-click rollback.
- **DROOL agent** (on `main`, after v0.1.0-alpha): an optional agent that connects out to DROOL
  over mutually authenticated TLS. It runs the same operations as SSH, within limits set on the
  host itself, and falls back to SSH safely when it's offline. It updates itself from signed
  releases and rolls back automatically.
- **Firewall** (on `main`): policies from hosts, groups and services ("web may reach database on
  PostgreSQL"), compiled to nftables, explained per host, simulated ("can A reach B?"), and applied
  as dead-man transactions that roll back on their own if DROOL loses the host. Plain-nftables
  hosts only; Docker, firewalld, UFW and Tailscale are detected and refused. The **Firewall
  Designer** edits the policy as a topology, as rules or as YAML, and shows DROOL's own management
  paths and anything that would cut them.
- **Network monitoring** (on `main`): what each host actually talks to — inbound, outbound,
  internal, external, which process — how the firewall policy classifies every connection, and,
  for public addresses, the country and network owner from local GeoIP/ASN databases you install.
  Agent hosts are observed continuously (conntrack when available), SSH hosts by snapshot. See
  [Network monitoring](#network-monitoring).
- **System monitoring** (on `main`): CPU, memory, disks, network, temperatures, GPUs, services and
  storage device health for every host, with history (hour to 30 days), health issues that need a
  condition to last before they appear, and a fleet view. Agent hosts are continuous, within a
  small enforced resource budget; SSH hosts are polled every 5 minutes. See
  [System monitoring](#system-monitoring).
- **Appearance:** Light, Dark or System (follows your OS), from the header or *Settings → Account*.
- **Accounts and audit:** local users with roles (admin, operator, auditor, viewer), API tokens,
  credentials encrypted at rest, an append-only audit log of every change.
- **Backup and restore** from the command line.

### Not in this release

These are planned (see `DESIGN.md`) but **do not exist yet**:

- firewall support for Docker and container networks, WireGuard identity networking
- scheduling (jobs run when you start them)
- desired state / drift detection
- templated files, directory sync, desired-state file baselines (file management and single-file
  deployment exist on `main`; see [Files](#files))
- plugins
- batch/canary rollouts for jobs (agent updates do have them), workflows, MFA, single sign-on, high availability, Windows
- network security features beyond visibility: blocking by address or country, threat scores or
  reputation, packet capture, intrusion detection, alerting
- notifications and alert rules for monitoring (health issues are shown in DROOL only), custom
  dashboards, log collection

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

## Files

### File manager

*Hosts → (host) → Files* browses one host's filesystem. The current remote path is always shown;
type a path to jump there.

- **Who you are on the host.** By default every action runs as the host's **login user** over
  SFTP, with that user's permissions. To read or change files the login user can't (for example
  in `/etc`), turn on **Use administrator privileges**. The page then shows a warning banner, and
  every action runs through the host's sudo setting. DROOL never switches to root on its own.
- **Upload and download** stream; nothing is held in the controller's memory, so large files work.
  An upload goes to a temporary file next to the destination and only replaces the destination
  once the whole file arrived and its checksum matched. A cancelled upload or a lost connection
  leaves the destination as it was.
- **Edit** UTF-8 text files up to 1 MiB in the browser. Larger or binary files are downloaded
  instead. If the file changes on the host after you opened it, saving is refused: *"This file
  changed on the host after you opened it. Reload it or explicitly overwrite the newer version."*
- **Permissions** are shown as `rw-r--r--` and `0644` with a plain-language description on hover
  ("Owner: read, write · Group: read · Everyone else: read"). The permission editor uses
  checkboxes.
- **Symlinks** are shown with their target and opened only when you click them. Deleting a
  symlink removes the link, never its target, and DROOL never writes, changes permissions on or
  replaces something through a symlink.
- With administrator privileges, DROOL refuses to write into a directory that a non-root user
  could modify (for example `/tmp` or a world-writable directory), because another user could
  swap files there while root is writing.
- Deleting asks once; recursive delete of top-level directories (`/etc`, `/home`, …) is refused.

### File deployment

*Jobs → New job → Deploy file* puts one file on many hosts (by host, group or tag):

1. Upload the file. DROOL stores it once, identified by its SHA-256 checksum.
2. Choose the destination path, whether to replace an existing file or only create a missing
   one, and optionally owner, group and permissions (empty keeps the existing file's, or
   `root:root 0644` / the login user's for new files).
3. **Preview** shows each host's state (*missing: would create*, *identical*, *different: would
   replace*, *only owner/permissions change*, *inaccessible*) and a diff for text files. Nothing is changed.
4. Optional **validation command**, run on the new file before it replaces the old one, with
   `{path}` standing for the new file, e.g. `nginx -t -c {path}` or `visudo -cf {path}`. If it
   fails, the live file is not touched.
5. Optional **service reload or restart** after the file was installed. If that fails, the job
   says the file was installed but the service action failed, so you know what state the host is
   in.

Hosts that already have identical content are a no-op (*no change*). For every changed file DROOL
keeps the previous version (up to 64 MiB) with its owner, group and permissions. The host page
lists *Files deployed by DROOL*; each deployment shows before/after checksums, the diff,
validation output and a **Roll back** button. A rollback is itself an audited job, restores
content and permissions, and is refused if the file was changed again since that deployment.

### Permissions

| Permission | Allows | admin | operator | auditor | viewer |
|---|---|---|---|---|---|
| `files.read` | browse, download, view text, deployment history | ✓ | ✓ | | |
| `files.write` | upload, edit, mkdir, rename, move, delete, chmod, chown | ✓ | ✓ | | |
| `files.deploy` | upload deployment files, preview and run deployments | ✓ | ✓ | | |
| `files.elevate` | anything above with administrator privileges | ✓ | ✓ | | |

Auditors and viewers get no file access, because file contents (keys, configs) are often
secrets. Every change is audited with host, path, action, result, checksums before and after,
owner, group and permissions; file contents never go into the audit log. Reads with
administrator privileges are audited too.

## The DROOL agent

SSH stays fully supported. The agent is optional, and useful when:

- DROOL can't reach the host (behind NAT, on another network): the agent connects **out** to
  DROOL, and the host needs no open port.
- You want **limits on the host itself** that DROOL can't override: the agent only does what
  `/etc/drool/agent.yaml` on that host allows.
- You want cheap, frequent monitoring without SSH polling.

The agent talks to DROOL on **port 9443** (`agent.port`). That port must be reachable from your
hosts. With Docker, `compose.yaml` publishes it.

### Installing

**From an SSH host (easiest):** open the host, then *Install DROOL agent* on the *Management* card. Choose a
preset and confirm. DROOL copies the agent over the SSH connection it already trusts, enrolls it,
and switches the host to *Preferred: Agent, Fallback: SSH*.

**Any host, with a token:** *Agents → Enrollment → New token* gives you a one-line install command:

```sh
curl -fsSL -k --pinnedpubkey 'sha256//…' https://drool.example:8080/agent/install.sh | sudo sh -s -- --token 'drool-enroll-1.…' --preset managed
```

The command pins DROOL's web certificate, so `-k` doesn't weaken it. Tokens expire after an hour
by default and work once. They can be revoked, and they can be bound to one host or put new
hosts into a group.

**Manually:** copy `drool-agent-linux-<arch>` (from *Agents → Enrollment*, or `make agent`) to the
host, then run:

```sh
sudo ./drool-agent install --preset managed --token-file -     # paste the token, then Ctrl-D
```

Enrollment never trusts a host because its name or address matches an existing one. A new agent
becomes a new host unless its token was created for a specific host.

The agent creates its private key on the host, and the key never leaves it. The agent trusts DROOL
only through the **Agent Root CA** fingerprint inside the token, never through browser
certificates.

### What the agent may do: presets and the local ceiling

`/etc/drool/agent.yaml` (root-owned, 0600) is the **ceiling**. DROOL can narrow it per host
(*Narrow…* on the host's *Management* card) but never widen it. Edit the file and restart the agent
(`systemctl restart drool-agent`) to change it. `drool-agent presets` shows exactly what each
preset enables:

| Preset | Allows |
|---|---|
| Monitor Only | Telemetry only. No commands, packages, services, reboots or files |
| Managed | Packages from configured repositories, all services, reboots. No commands, no file access |
| Full | Everything, including commands and scripts as root |

The UI marks **root-equivalent** capabilities: commands, unrestricted files, package installs and
unrestricted services. Anything the ceiling forbids fails with **CapabilityError**, and DROOL
never retries it over SSH. Note that SSH, when configured, is bounded only by the SSH account's
rights. If the ceiling must be absolute, set the host's fallback to *None*.

### Agent first, SSH as fallback

Each host shows *Preferred*, *Fallback* and the path in use (*Management* card, *Managed via*
column), and each job result shows the path used (*Via*).

- If the agent isn't connected (or is incompatible) **before** a step is sent, DROOL uses SSH and
  says so on the job.
- Once a step was sent to the agent, DROOL never switches transports. If the agent vanishes
  mid-step, DROOL asks it what happened when it's back. If that isn't possible, the result is
  *outcome unknown*, as with SSH.
- A revoked or quarantined agent **blocks** management of that host. DROOL doesn't fall back to
  SSH: someone has to look at it first.

### Certificates, revocation and cloned machines

Agent certificates last 30 days and renew automatically. An agent that was off long enough for its
certificate to expire recovers by proving it still holds its key, within 180 days
(`agent.recovery_grace`). After that it must be enrolled again. *Revoke* on the Agents page takes
effect immediately.

If two machines connect with the same identity (typically a cloned VM), DROOL quarantines the
identity and shows both connections. You decide which one is real; DROOL never guesses.

### Agent Root CA: keep it offline

On first start DROOL creates an **Agent Root CA** and an intermediate that signs agent
certificates. The root key stays in DROOL until you move it, and the UI warns until you do:

```sh
drool agent-ca export-root -o drool-agent-root      # writes .key/.crt: store them offline, encrypted
drool agent-ca remove-root --confirm <fingerprint>
```

(*Agents → Trust* does the same in the browser.) With the root key offline you still need it for
two jobs: **rotating the intermediate** (`drool agent-ca rotate --root-key FILE [--revoke-old]`)
and signing a new trust manifest. Agents only accept a newer manifest signed by the root, and they
never accept an older one.

**Backups** contain the intermediate (encrypted with the master key), manifests, identities and
update metadata, but never a root key that you removed. For disaster recovery, keep the following:

- the backup and `master.key`, which together restore everything, including agent trust;
- the offline root key, needed to rotate if the intermediate is ever compromised or lost.

If you restore with `--discard-credentials`, the agent CA keys are discarded too. Agents then
can't be managed until you rotate the intermediate with the offline root
(`drool agent-ca rotate --root-key …`). Without the root key, every agent has to be re-enrolled.

### Agent updates

Agents update themselves from releases signed with a **release-signing key that is not on the
controller**. A compromised DROOL can offer an update, but it can't make agents run unsigned code.

```sh
drool release keygen -o drool-release              # once, on a machine you trust; keep .key offline
make agent                                         # bin/drool-agent-linux-{amd64,arm64}
drool release sign --key drool-release.key --version v1.2.3 bin/drool-agent-linux-amd64
```

1. Register `drool-release.pub` under *Agents → Updates*. Hosts must trust it too: pass it to
   *Install DROOL agent*, use `--trust-release-key` at install, or run
   `drool-agent trust-release-key drool-release.pub` on the host. Trusting a key is
   security-sensitive, and DROOL can't do it for you remotely.
2. Upload the binary together with its `.release.json` under *Agents → Updates*.
3. Start a rollout, or set the policy to *Automatic* (newest version in a channel, or one exact
   version).

A rollout updates a canary first, then batches. It **pauses** after the configured number of
failures and waits for you. Offline hosts update when they come back. Each host stages the new
binary next to the current one, switches atomically and restarts. It keeps the new version only
when it starts and completes a session with DROOL within the health window. Otherwise the host
rolls back to the previous version on its own, without needing the new binary to work. A power
cut leaves either the old or the new version complete. Downgrades are refused, except for this
automatic rollback.

### Agent compatibility and versions

The agent protocol has its own version, separate from DROOL's (currently protocol 1, accepted range
1–1). The Agents page shows each agent's version, protocol and compatibility. An incompatible agent
isn't used, and hosts with an SSH fallback keep working over SSH.

### On the host

- `drool-agent status`: identity, certificate, trust, endpoints, capabilities.
- `/var/log/drool-agent/operations.log`: every operation the agent received (id, type, controller,
  time, outcome). It records no command text, file contents or secrets. DROOL can't change it.
- `updates: {policy: deny}` in `agent.yaml` keeps the agent on its installed version, whatever
  DROOL asks.
- Without systemd (containers), `drool-agent install` runs the agent under a small supervisor
  script instead of a unit.

### Changing DROOL's address

*Agents → Controller address* proposes a new address. Every connected agent tests it first. Commit
once enough agents have confirmed. Agents keep the old address as a fallback, so none are stranded.

## Firewall

DROOL manages host firewalls from **network intent**: you say who may reach whom over which
services ("the `web` group may reach the `database` group on PostgreSQL"), and DROOL compiles that
into nftables rules for every affected host. It explains the result, and applies it so that a
mistake can't lock you out.

### Where enforcement works

Enforcement needs **systemd** and **nftables** (`nft`). DROOL refuses to enforce, and says why, when
another firewall manager is in charge:

| Environment | Enforcement |
|---|---|
| Plain nftables, no other firewall manager | **Supported** |
| Docker (either backend), firewalld, UFW (active), Tailscale | Refused |
| Legacy iptables rules, or other nftables rules (including iptables-nft, libvirt, Podman, Kubernetes) | Refused |
| No systemd (e.g. containers), or no `nft` installed | Refused |

On a refused host you can still check the environment, observe connections, simulate and use
Explain. If another firewall appears after DROOL enforced (Docker installed, UFW enabled), you can
still **Remove DROOL policy**: removal only needs systemd and `nft`. After `ufw disable`, UFW's rules
stay loaded until a reboot, so DROOL keeps refusing until then (it says so). DROOL only ever touches its own table (`inet drool`), its files in
`/var/lib/drool/firewall` and `/usr/libexec/drool-firewall`, and its boot unit
`drool-firewall.service`. It never runs `nft flush ruleset` and never edits `/etc/nftables.conf`.

Minimal cloud images (Debian 12 genericcloud, for example) don't include nftables: install the
`nftables` package first (a DROOL package job works).

### Getting started

1. On a host page, open the **Firewall** card and click **Check** to inspect the host. Check also
   records DROOL's management path, so **Effective policy** shows it from the start.
2. Choose a mode:
   - **Monitor** observes connections and explains what a policy would do.
   - **Guided** also suggests policies from what it saw.
   - **Enforce** applies the policy.
3. In **Firewall → Designer**, draw or write the policy (see below): hosts, groups, named networks,
   **internal networks**, **the internet** (everything outside the internal ranges), **anywhere** or
   a CIDR, and the services between them. **Save policy** when the preview looks right.
4. Check the result: the host's **Effective policy** tab lists every allowance with its reason, and
   **Firewall → Can A reach B?** answers questions like "can web01 reach database01 on TCP 5432?".
5. Click **Apply policy** on the host, or select hosts under **Firewall → Hosts**.

Inbound traffic is denied unless a policy, or the management path, allows it. Outbound traffic is
allowed unless you set the host's outbound mode to **restrict**. Deny policies always apply and win
over allows.

Built-in services: ssh, dns (UDP **and** TCP 53), http, https, ntp, postgres, mysql, redis, smtp,
submission, imap, smb, nfs, ping, mdns, mqtt, wireguard. Add your own in the designer (a custom
protocol/port, or **Rules → Services**). Internal ranges (RFC 1918, CGNAT/Tailscale, IPv6 ULA, link-local, loopback) are editable
under **Firewall → Settings**. Add your routed LANs, VLANs and VPNs there.

### Firewall Designer

**Firewall → Designer** shows the same policy three ways. Edit in whichever suits you; the other two
follow immediately, because they are views of one document.

- **Topology** — boxes for hosts, groups, networks, the Internet and internal networks; arrows for
  allowed (blue) or denied (red, dashed) communication, labelled with each service and what it
  means on the wire (`ssh (TCP 22)`, `app-8812 (TCP 8812)`). Drag from the right edge of one box to
  another to add a rule; click a line or its label to see the rules behind it, the affected hosts
  and **Can it reach…?**; click a host to see what it allows inbound and outbound, its denies and
  DROOL's own access. Lines mean *allowed communication*, not cabling: moving boxes only changes the
  picture (it is remembered). A group is one box and one line however many hosts it has; **Show
  members** expands it. Use the filter, **All hosts**, **Auto layout** and **Fit** on large fleets.
- **Rules** — the same with ordinary forms and tables (keyboard-friendly): rules in order, with
  move up/down, plus networks, groups and custom services.
- **YAML** — the same policy as text, e.g.

  ```yaml
  networks:
    admin-lan:
      cidr: 192.168.1.0/24
  groups:
    ssh-managed:
      members: [server-a, server-b, server-c]
  services:
    app-8812:
      protocol: tcp
      port: 8812
  policy:
    - name: admin-to-jump
      from: admin-lan
      to: jump-a
      allow: [ssh]
    - name: server-a-to-server-b
      from: server-a
      to: server-b
      allow: [app-8812]
  ```

  Names can be written bare when they're unambiguous, or as `host:web1`, `group:web`,
  `network:admin-lan`. YAML goes through exactly the same checks as the other views; mistakes are
  shown with their line. Comments and formatting are not kept (DROOL rewrites it in a standard
  form).

The **Preview** panel lists every host that changes (new allowances, new denies, removed ones),
hosts where enforcement is refused, validation problems and anything that would block applying.
Nothing reaches a host until you apply.

**DROOL's own access is drawn separately**, as dashed green *Protected DROOL management path* lines
from the **DROOL Controller** (through jump hosts) or from agents to the controller. You can't edit
them. If a draft would cut one — a deny, a group or network change, a redefined service, anything
whose effect blocks it — a red **Management path blocked** banner names the host, the required path
and the blocked segment, the canvas highlights it, saving asks you to confirm, and applying is
refused. (Deleting an allow can't cut it: DROOL always allows its own path ahead of every rule.)

Groups in the designer are DROOL's host groups: changing members here changes the group
everywhere. Removing a group from the YAML doesn't delete it (do that on the Groups page).

### How applying works (you can't lock yourself out)

Each host gets a **dead-man transaction**:

1. DROOL measures a brand-new management connection to the host.
2. The host writes a durable journal and arms a rollback timer (default **120 s**, configurable per
   host or group). Only then does it apply the new rules, atomically.
3. DROOL must then reach the host over a completely **new** connection to commit: a new agent
   connection, or a new SSH connection through every jump host. An old connection proves nothing,
   because firewalls keep established connections open.
4. Without that commit, the host restores its previous policy on its own: by the timer, or at boot if
   it restarted. This covers a policy that cuts the path, a controller that died, a crashed agent and
   a reboot. DROOL then shows the transaction as **rolled back** with the reason.

While a transaction is pending, DROOL refuses reboots, system updates and restarts of network
services on that host.

Policies that would remove DROOL's own management path (SSH from the controller or jump host, the
agent's connection to the controller) are **refused** with the reason. An administrator can use the
audited **emergency override** with a written reason. The dead-man rollback still applies.

### Address changes

Groups and hosts become nftables sets. When a host gets a new address, DROOL updates only the set
elements on the hosts that refer to it. That's one atomic nftables update per host, without a new
transaction. The Firewall page shows "Policy propagation incomplete" until every host is current.

### Drift, removal and recovery

- **Drift:** if someone edits DROOL's table by hand, the host shows **drift**. The next apply refuses
  until an administrator chooses to replace the drifted rules.
- **Remove DROOL policy** (host card) takes DROOL's rules off the host, using a transaction as well.
- **Locked out anyway?** On the host's console, run:
  ```sh
  sudo /usr/libexec/drool-firewall/drool-fw rollback   # restores the last committed policy, if a transaction is pending
  sudo nft delete table inet drool                     # removes DROOL's rules entirely
  sudo systemctl disable drool-firewall.service        # stops DROOL's rules loading at boot
  ```

### Observations

Observed connections are shown under [Network monitoring](#network-monitoring), each with what
the firewall policy says about it. Flows that no policy covers are **unclassified**: in Guided
mode they become suggested policies (host page → Firewall → Observed connections), and from any
connection's details **Create policy from observed connection** starts a draft rule in the
Designer. Nothing is created until you review, save and apply it.

### Permissions

| Permission | Roles | Allows |
|---|---|---|
| `firewall.read` | every role | Viewing |
| `firewall.write` | administrators | Policies, modes, checks and applying |
| `firewall.override` | administrators | Emergency override, short timeouts, replacing drift |

## System monitoring

**Monitoring** in the sidebar shows the whole fleet: how many hosts need attention and why (high
CPU or memory, low disk space, failed services, storage health, temperature, stale data), with
search, sorting and paging. Every host page has a **Monitoring** card with tiles for CPU, Memory,
Storage, Temperature, Network, Services, Storage devices and Active health issues; each opens the
host's monitoring page:

- **Metrics:** charts for the last hour, 6 hours, 24 hours, 7 days or 30 days — CPU (and per core
  now), load, memory and swap, filesystems, network throughput and errors, disk IO, temperatures,
  GPUs. Lines differ by pattern as well as colour, maxima are dotted, missing data is a gap (never
  zero), and **Show as table** gives the same numbers as text.
- **Services:** every systemd service (the noise hidden unless you ask for **Show all units**),
  filtered by Running, Stopped, Failed, Enabled, Disabled or All, with each one's state history.
  **Watch** a service to get a health issue when it stays stopped. Start/stop/restart are ordinary
  service jobs.
- **Storage health:** each device with its partitions, filesystems and mounts, what the device can
  tell about its own health, and kernel evidence (IO errors, resets, read-only remounts).
- **Sensors & GPU:** temperatures with the limits the hardware itself reports; GPU use, memory,
  temperature and power where the driver provides them.
- **Collection & helpers:** how the host is monitored, which optional tools would add
  information, and (agent hosts) the agent's resource budget and use.

Nothing is installed, changed or sent anywhere to monitor a host. Health issues are listed in
DROOL; there are no notifications yet.

### Agent hosts and SSH hosts

| | Agent hosts | SSH-only hosts |
|---|---|---|
| How | **Continuous**: samples every 10 s (services every 30 s), kept as one-minute averages and maxima; storage health every 30 min; kernel storage errors as they happen | **Polled** every 5 min (Settings → Monitoring); storage health and kernel errors hourly, and only where sudo is allowed without a password |
| While DROOL is unreachable | Kept in the agent's memory (2 MB by default, about a day for a typical host) and delivered later; anything older is dropped and shown as a gap | Missed polls are gaps |
| On the host's disk | Nothing but the agent's configuration (and the optional probe file, below) | Nothing |

### The agent stays out of the way

The agent runs under a resource budget enforced by systemd, shown on each host's **Collection &
helpers** tab:

| | Default |
|---|---|
| CPU | at most 25% of one core, low priority (weight 20) when the host is busy |
| Memory | reclaimed above 96 MB, never more than 160 MB |
| Disk IO, tasks | low priority; at most 256 tasks |

In practice it uses far less: about 0.1% of a CPU and 25 MB on a normal PC, about 4% of an
emulated CPU and 14 MB on a slow virtual machine. Jobs you run through the agent (package updates,
scripts) are **not** limited by this budget. When the host is under pressure — or the agent nears
its own limits — the agent samples less often and postpones storage scans; the host page says
**Reduced to save resources** and why, and full detail returns once things are calm. If the agent's own memory
ever kept growing (a bug), it restarts itself rather than lingering at its limit. Admins can
change the budget for all hosts (Settings → Monitoring) or one host.

### Storage health

Each device shows **how much it can tell** about its health — *Full*, *Partial*,
*Vendor-specific*, *Limited* or *Unavailable* — and only then a verdict. SATA and SAS disks need
`smartmontools`, NVMe drives `smartmontools` or `nvme-cli`; eMMC reports its own wear. **SD cards
and USB sticks report nothing**: DROOL says "Limited — no failure indicators detected", never
"healthy", and watches kernel errors and read-only remounts instead. For those you can turn on the
**write/read probe** for a filesystem (agent hosts): once an hour it writes 512 bytes to a file in
a DROOL-owned directory, flushes it to the card and reads it back. More frequent probes are
possible but add flash wear, and DROOL warns you. DROOL never starts SMART self-tests and never
wakes a sleeping disk.

### Optional helpers

`smartmontools`, `nvme-cli` and `lm-sensors` add information. A host's **Collection & helpers**
tab says which would help and why; **Install** shows exactly what would be installed and runs an
ordinary package job (audited, in the job history). On the fleet page, select hosts and choose
**Install recommended helpers** for a preview with counts first. Admins can also give a standing
approval (all hosts or a group) in Settings → Monitoring, after which DROOL installs a needed
helper once per host. DROOL never uninstalls helpers and never installs GPU drivers.

### Moving SSH hosts to the agent

On the fleet page, select hosts and choose **Convert to agent**. The preview counts compatible,
already-converted, unsupported and unreachable hosts; the conversion then runs as a staged rollout
(a canary host first, then batches) and pauses on a failure. Each host gets the agent build that
matches your DROOL version, verified, with a single-use token; a host where anything fails stays
exactly as it was, managed over SSH.

### Health issues

Issues appear only when a condition lasts (for example CPU above 95% for 5 minutes, a disk over
85% full on two checks in a row) and clear only after it has properly recovered, so short spikes
don't flap. Temperatures are judged only against limits the hardware reports — DROOL has no
universal "too hot". **Settings → Monitoring** offers the recommended thresholds or your own, and
any check can be turned off per host.

### History, size and permissions

History is kept as one-minute values for 26 hours, five-minute values for 8 days and hourly
values for 30 days (adjustable). Measured: about **1.5 MB per host** — 8.6 MB for 5 hosts, 78 MB
for 50, 308 MB for 200.

| Permission | Roles | Allows |
|---|---|---|
| `monitoring.read` | everyone | All monitoring views |
| `monitoring.manage` | administrators, operators | Watching services, probes, per-host checks, refreshing, helper installs (with `jobs.execute`) |
| `monitoring.settings` | administrators | Global settings, agent budgets, standing helper approvals (audited) |

## Network monitoring

**Network** in the sidebar shows what your hosts actually talk to; every host page has a
**Network connections** card. Monitoring only: nothing here blocks traffic, scores addresses or
raises alarms.

For every observed connection DROOL shows the direction, the process (or **Unknown** — it never
guesses), local and remote addresses and ports, the protocol, first and last seen, how many
connections, and its **policy status**:

| Status | Means |
|---|---|
| ✓ Covered by policy | A firewall policy allows it (named in the details) |
| ⛨ DROOL management | DROOL's own SSH or agent connection — always protected |
| ? Unclassified | No policy says anything about it |
| ✕ Denied / would be denied | A deny policy, or the default drop once the firewall is enforced |
| ↻ not applied yet | The host still runs an older policy that treats it differently |

Internal addresses (the firewall's **internal ranges**, Firewall → Settings) show the DROOL host,
its groups or the named network they belong to. Public addresses show the **country and network
owner (ASN)** when a database is installed (below). Filters cover host, group, direction,
internal/external, protocol, ports, process, service, country, network, policy status, first and
last seen, and **new external destinations** (first seen by a host, or by any host, in the last
24 hours or 7 days). The Firewall Designer's topology can show **Configured**, **Observed** or
**Configured + Observed**: observed traffic is dotted, external addresses are grouped by the
network that owns them, and the same lines are listed below the canvas.

### What agent and SSH hosts can see

| | Agent hosts | SSH-only hosts |
|---|---|---|
| How | **Continuous.** *conntrack* when the kernel tracks connections (a firewall uses it — for example once DROOL's firewall is enforced): every new connection, however short. Otherwise *socket sampling* every 30 s, which misses connections that open and close in between | **Snapshot only**: **Take snapshot** shows what is open at that moment |
| Processes | From who owns the socket: the listening service for inbound connections; for outbound ones, if the connection still exists when DROOL looks | The same, at the moment of the snapshot |
| Never | Packet contents, DNS queries, byte counts | The same |

Each host shows which applies and why ("Continuous: conntrack", "Continuous: socket sampling (every
30 s)" with the reason conntrack isn't used, "Snapshot only: SSH"). DROOL never installs packages,
loads kernel modules or changes sysctls to collect more. Connections routed *through* a host
(containers on a bridge, routers) are counted but not shown. Settings: **Network → Collection and
enrichment** (collect on every agent host, only on hosts whose firewall mode isn't Off, or not at
all).

### GeoIP and ASN (optional)

DROOL looks public addresses up **locally** in MaxMind-format (`.mmdb`) files in
`<data_dir>/geoip` (`network.geoip_dir`; in Docker `/data/geoip`). It never sends addresses to an
online service, and never looks up private, loopback, link-local, multicast or documentation
addresses. Without databases everything works; country and network show as *Unknown*.

Use any of these (DROOL recognises them by content; keep a country and an ASN database, or one
that has both):

- **MaxMind GeoLite2** Country and ASN (free account; `geoipupdate` keeps them current — point its
  `DatabaseDirectory` at the directory above).
- **DB-IP Lite** country and ASN (monthly downloads, CC BY 4.0):
  `curl -L https://download.db-ip.com/free/dbip-country-lite-$(date +%Y-%m).mmdb.gz | gunzip > dbip-country-lite.mmdb`
  (and the same for `asn`).
- **IPinfo Lite** (country and ASN in one file).

Files are reloaded automatically when they change (or with **Reload databases**); a half-written
or corrupt replacement keeps the previous copy. Country and network owner are context and can be
wrong or out of date — DROOL never labels anything suspicious because of them.

### Retention and size

Connections are stored as **flows** (host, direction, protocol, local address, service port and
remote address), not one row per connection. Flows not seen for 30 days are removed, and each host
keeps at most 5 000 (the least recently seen go first); both are settings. A flow takes about
250 bytes: a typical server with a few hundred peers needs well under 1 MB; 200 hosts at the
5 000-flow cap would need about 250 MB.

### Permissions

| Permission | Roles | Allows |
|---|---|---|
| `network.read` | administrators, operators, auditors | Observed connections, topology, enrichment status (not viewers: connection metadata is sensitive) |
| `network.snapshot` | administrators, operators | Taking a snapshot of an SSH host |
| `network.settings` | administrators | Collection, retention, enrichment (audited) |

## Backup and restore

```sh
# Docker: write a backup to your machine (safe while DROOL runs)
docker compose exec -T drool drool backup -o - > drool-backup.tar.gz
docker compose cp drool:/data/master.key ./master.key    # once; keep it safe and separate

# Native
sudo -u drool drool backup             # → /var/lib/drool/backups/drool-backup-<time>.tar.gz
```

A backup contains the database (hosts, pinned host keys, groups, users, credentials in encrypted
form, job history and output, audit log, firewall policies, services, settings and transaction
history), the self-signed TLS certificate, and the config file if you use one. Hosts keep their
committed firewall policy whatever happens to the controller. After restoring an older backup, a
host whose firewall revision is newer than the controller's is flagged; adopt its state on the host
page before applying again.

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

**Upgrading to a build with the agent:** the agent endpoint uses port 9443 by default. If your
`server.web_port` is 9443, set `agent.port` to something else (DROOL refuses to start with both on
one port). Set `agent.enabled: false` if you don't want the endpoint at all.

## Configuration

Precedence: built-in defaults → config file (`--config`, default `/etc/drool/config.yaml`
if present) → `DROOL_*` environment variables. The variable name is the key path in upper
snake case, e.g. `server.web_port` → `DROOL_SERVER_WEB_PORT`. **Settings → System** in the UI
shows every effective value and where it came from.

Network monitoring reads optional GeoIP/ASN databases from `network.geoip_dir` (default
`<data_dir>/geoip`); see [GeoIP and ASN](#geoip-and-asn-optional).

TLS defaults to a self-signed certificate. Use `server.tls.mode: provided` with
`server.tls.cert_file`/`key_file` for your own certificate. `server.tls.mode: off` (for a reverse
proxy on the same host) is only accepted with a loopback `server.bind`, so DROOL never serves
plain HTTP to the network by accident.

## Known limitations

- **One controller, SQLite, no high availability.** Sized for homelabs and small fleets; tested
  with tens of hosts, not thousands.
- **At most one jump host.** Agents connect directly to DROOL (no relays).
- **Agent:** Linux amd64/arm64 only. Service management and reboots need systemd, as with SSH.
  Restoring a backup older than the hosts' agent state is detected and shown (*host ahead*), but
  full reconciliation is manual (*Adopt host state*). Agent updates need a release-signing key that
  you manage yourself.
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
- **Files:** with administrator privileges, listings and reads run as typed commands through sudo
  (`find`, `cat`) rather than SFTP, which is slower. Text editing is limited to 1 MiB; the
  previous version of a deployed file is kept only up to 64 MiB (larger files can't be rolled
  back); deployment files are limited to 1 GiB. No recursive upload or download of directories,
  no templating. Rollback restores content, owner, group and permissions, not ACLs, SELinux labels
  or extended attributes (new files get the label their directory's policy assigns).
- **Firewall:**
  - Enforcement only on plain-nftables hosts with systemd. Docker, firewalld, UFW, Tailscale,
    legacy iptables and any other nftables rules are refused.
  - IP addresses stand for identities: on a network where another machine can take an authorized
    address, segmentation is only as strong as address assignment (DESIGN §4.5). Use static
    addresses or DHCP reservations for important hosts.
  - The SSH management path is allowed from the address the host saw at the last transaction. If
    the controller or jump host changes address, apply again before relying on the new one.
  - Reloading `nftables.service` with a config that flushes the ruleset removes DROOL's rules
    until the next apply or boot; DROOL reports it as drift.
  - Agents without conntrack (no firewall using connection tracking, or no permission) sample
    connections every 30 seconds, so very short-lived connections can be missed; the host's
    Network card says so.
  - Designer: one person editing at a time works best — a second save from an older draft is
    refused (reload and redo). YAML comments and formatting are not kept. Drawing is mouse/touch;
    everything can also be done from the keyboard in the Rules view. Very large expanded groups
    (hundreds of hosts) are slow to draw; keep them collapsed or filter.
  - Appearance is saved per browser, not per account.
- **Network monitoring:** no byte counts; processes only when the socket can still be seen (often
  "Unknown" for very short outbound connections); connections routed through a host (containers)
  are not shown; "new" is relative to the retained history (30 days by default); GeoIP/ASN only
  from databases you install and update; observations from M8 agents have no processes until the
  agent is updated.
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
make test               # Go tests + frontend typecheck + frontend unit tests (vitest)
make vet fmt-check
make generate           # regenerate sqlc code and TypeScript API types
make check-generated    # CI: fail if generated code is stale
make test-integration   # real distributions in unprivileged containers (Docker, ~10 min)
```

The VM tests for real systemd and reboots are opt-in and slow (software emulation, no special
privileges): `go test -tags integration ./test/integration -run TestVM -vm debian-12 -v -timeout 4h`.

Browser tests (`web/e2e`: the Firewall Designer, network monitoring, Light/Dark/System, contrast
in both themes) run against containers with
`go test -tags integration ./test/integration -run TestBrowser -v`. They need
Chromium (`CHROME=/path/to/chromium`, default `/usr/bin/chromium`) and `npm ci` in `web/`.

The REST contract lives in [`api/openapi.yaml`](api/openapi.yaml). A Go test fails if routes
and the spec drift apart. The UI's TypeScript client types are generated from the spec.

### Frontend styling

All colors, radii and fonts are semantic design tokens in `web/src/styles/index.css`, and
components use only those tokens through Tailwind utilities (`bg-surface`, `text-muted`,
`border-line`, ...). Each token has a light and a dark value (not an inversion); the browser
contrast test checks both themes against WCAG AA. UI primitives live in `web/src/components/ui/`. Restyling means changing
tokens and primitives, not pages.
