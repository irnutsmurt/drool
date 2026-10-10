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

Linux is the platform DROOL implements today. A Windows agent is a deferred future product goal;
the controller's typed operations are intentionally kept platform-neutral so Windows-native
implementations can be added later.

- **Design:** [`DESIGN.md`](DESIGN.md) (product and trust architecture, including future plans)
- **Implementation:** [`ARCHITECTURE-ALPHA.md`](ARCHITECTURE-ALPHA.md) (what the Technical Alpha
  actually does, and why)

## Status: v0.1.0-alpha (Technical Alpha)

`main` additionally contains file management and file deployment ([Files](#files)), the
[DROOL agent](#the-drool-agent-shepherds) and the [firewall](#firewall), which are not part of a tagged
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
  units, pending and security updates, reboot required), and an Overview of what needs attention.
- **Runs (jobs):** run a command or script on many hosts with live output, per-host results, timeouts,
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
- **Shepherds — the DROOL agent** (on `main`, after v0.1.0-alpha): an optional agent that connects out to DROOL
  over mutually authenticated TLS. It runs the same operations as SSH, within limits set on the
  host itself, and falls back to SSH safely when it's offline. It updates itself from signed
  releases and rolls back automatically.
- **Firewall** (on `main`): policies from hosts, packs (host groups) and services ("web may reach
  database on PostgreSQL"), compiled to nftables, explained per host, simulated ("can A reach
  B?"), and applied with automatic rollback protection (dead-man transactions that roll back on
  their own if DROOL loses the host). Plain-nftables
  hosts only; Docker, firewalld, UFW and Tailscale are detected and refused. The **Firewall
  Designer** edits the policy as a topology, as rules or as YAML, and shows DROOL's own management
  paths and anything that would cut them.
- **Network monitoring** (on `main`): what each host actually talks to — inbound, outbound,
  internal, external, which process — how the firewall policy classifies every connection, and,
  for public addresses, the country and network owner from local GeoIP/ASN databases you install.
  Agent hosts are observed continuously (conntrack when available), SSH hosts by snapshot. See
  [Network monitoring](#network-monitoring-network-activity).
- **System monitoring** (on `main`): CPU, memory, disks, network, temperatures, GPUs, services and
  storage device health for every host, with history (hour to 30 days), health issues that need a
  condition to last before they appear, and a fleet view. Agent hosts are continuous, within a
  small enforced resource budget; SSH hosts are polled every 5 minutes. The Recommended health
  profile can be adjusted for the whole fleet, a pack, a host or a single filesystem, disk, GPU,
  sensor or service, each setting showing where it comes from. See
  [System monitoring](#system-monitoring).
- **Alerts and notifications** (on `main`): problems monitoring confirms become alerts you can
  acknowledge, with one message when they start and one when they're fixed — by email, Discord,
  Slack or webhook — and maintenance mode to keep quiet while you work. See
  [Alerts and notifications](#alerts-and-notifications).
- **Scheduling and workflows** (on `main`): "update these machines every Sunday at 3:00, try one
  first, then five at a time, stop if two fail, reboot only if needed, and check that Jellyfin and
  SSH are running afterwards" — set up in the browser, without cron or playbooks. Rollouts with a
  canary and batches, reusable workflows with revisions, recurring maintenance windows. See
  [Scheduling and workflows](#scheduling-and-workflows).
- **Desired State** (on `main`): say what a machine should look like — these services running
  and enabled, these packages installed or absent, these files with exactly this content — for
  the whole fleet, a pack or one host. DROOL tells you whether each machine still matches
  (*Compliant*, *Drift detected*), shows exactly what differs, and lets you **review** it,
  **accept** what's there for that host, or **restore** it with an ordinary run. Optional
  automatic restore, off by default. See [Desired state](#desired-state).
- **Appearance:** Light, Dark or System (follows your OS), from the header or *Settings → Account*.
- **Accounts and audit:** local users with roles (admin, operator, auditor, viewer), API tokens,
  credentials encrypted at rest, an audit log of every change whose records are chained by hashes (so
  changing stored history is detectable) with an optional remote copy, two-factor authentication
  (authenticator apps) and re-authentication before high-impact actions.
- **Backup and restore** from the command line: backups that can be checked without restoring
  them, a restore that can't leave a half-restored controller, an automatic backup before every
  database upgrade, and after a restore a controller that compares every host before trusting it
  again (nothing in progress at backup time is repeated; a host changed since waits for you).
  Master key rotation. Short-lived SSH certificates from DROOL's own SSH CA instead of stored keys,
  if you want them.

### Not in this release

These are planned (see `DESIGN.md`) but **do not exist yet**:

- firewall support for Docker and container networks, WireGuard identity networking
- desired state beyond services, packages and files (users, cron, sysctl, containers), package
  version policy, compliance benchmarks (CIS, STIG) or policy expressions, remediation scripts
- templated files, directory sync (file management, single-file deployment and managed files in
  desired state exist on `main`; see [Files](#files) and [Desired state](#desired-state))
- plugins
- single sign-on, high availability, Windows
- workflow branching, conditions or approval steps; notifications about job results
- network security features beyond visibility: blocking by address or country, threat scores or
  reputation, packet capture, intrusion detection, alerting on network activity
- on-call rotations, escalation and paging, incident grouping, alert rule expressions, custom
  dashboards, log collection

## Finding your way around

The sidebar lists destinations directly, under headings that are only labels:

| Heading | Destinations |
|---|---|
| | **Overview** — fleet triage: what needs attention right now |
| Fleet | **Hosts** · **Packs** (host groups) · **Shepherds** (DROOL agents) |
| Observe | **Monitoring** · **Alerts** · **Network Activity** |
| Automate | **Runs** (jobs) · **Schedules** · **Workflows** · **Desired State** |
| Protect | **Firewall** · **Credentials** · **Audit Log** |
| Settings | **Settings** — your account, API tokens, users, notifications, the controller |

Three names in the UI are DROOL's own:

- A **Pack** is a *host group*: a named set of hosts you target together, which also gives its hosts
  shared settings (monitoring overrides, desired state, firewall rollback timeouts). The API and
  the database still call it a group (`/api/v1/groups`), and so do the firewall YAML (`group:NAME`)
  and the CLI.
- A **Shepherd** is the *DROOL agent* (`drool-agent`, `drool-agent.service`) on a host. Everything
  technical — the binary, the service, the API (`/api/v1/agents`), the protocol — keeps the name
  agent.
- A **Run** is a *job*: a command, update, file deployment, scheduled run or desired-state
  correction. The API calls them jobs (`/api/v1/jobs`).

**Search:** press **Ctrl+K** (**⌘K** on a Mac) or **/**, or click the search box at the top, and
type what you're looking for in your own words — "api token", "threshold", "can A reach B", "who
changed", "agent", "group" or "cron" all go straight to the right page or setting. It finds pages
and settings (not hosts or runs), and only those your role may open.

A **host's page** is a triage summary — is it okay, how DROOL manages it, what needs attention —
with a short summary of its monitoring, alerts, desired state, firewall, network activity and
recent runs. Each links to that area's page for the host (for example *Hosts → web-01 → View
firewall* opens `/hosts/<id>/firewall`).

**Health checks and thresholds** are under *Monitoring → Health checks and settings*
(`/monitoring/settings`; the old *Settings → Monitoring* address still works). Where notifications
go is under *Settings → Notifications*, linked from *Alerts*.

On a **phone**, the sidebar is behind the ☰ button, tables become one card per row, and the
firewall opens in its Rules view (Topology is a desktop map). Unresolved security posture (the
Agent Root CA key still on the controller, plaintext HTTP) stays visible: in full on the Overview,
and as a compact indicator at the top of every page.

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
unprivileged user and needs no special permissions. Precisely:

- **Ports:** 8080 (web UI and API) and 9443 (the Shepherd endpoint, only if you use Shepherds;
  `DROOL_AGENT_ENABLED=false` turns it off). Both are above 1024: no capability is needed to bind them.
- **Storage:** one writable volume at `/data`, owned by the image's `nonroot` user. Nothing else is
  written except `/tmp` (only by `drool backup verify`), so the root file system can be read-only.
- **Privileges:** none. `compose.yaml` runs it with `read_only: true`, a `tmpfs` for `/tmp`,
  `cap_drop: [ALL]` and `no-new-privileges`, and that's what the integration tests run it with.
  It doesn't use the host network, the Docker socket, host devices or host mounts.
- **Firewalls:** DROOL manages the firewalls of the hosts it manages, through SSH or their
  Shepherd. It never touches the firewall of the machine it runs on, and the container gets no
  access to it.
- **Health:** the image's health check runs `drool healthcheck` (the container has no shell or curl).

Rootless Docker or Podman work the same way as far as DROOL is concerned (nothing it does needs
root); they aren't part of the automated tests.

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
6. From the host page or *Runs → New run*: run commands and scripts, install updates, manage
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

### Short-lived SSH certificates (instead of a stored key)

DROOL can sign in with a fresh SSH certificate for every connection instead of a key that works
for ever. It has its own OpenSSH user certificate authority (CA): for each connection to a host in
*certificate mode* it creates a new Ed25519 key in memory, signs it for the host's login user only
(or the principal you set), valid from 5 minutes before until 10 minutes after it is issued
(`ssh_ca.clock_skew`, `ssh_ca.cert_ttl`), uses it for the handshake and forgets it. OpenSSH
enforces the expiry. The CA's private key is stored encrypted with the master key; certificates
and their keys are never stored or logged. Every certificate issued is in the audit log (host,
principal, serial, validity, CA fingerprint; the host's sshd log shows the same `ID drool:…`).
Because sshd checks a certificate only when a connection signs in, DROOL hands out a pooled
connection opened with a certificate only until 30 seconds before that certificate expires (with
the default lifetime, for about 9½ minutes), and the same goes for connections through a jump host
in certificate mode; then it connects again with a new key and certificate. An operation already
running on the connection finishes on it, as an SSH session would.

Moving a host over, without ever stranding it:

1. **Credentials → SSH certificate authority → Create.**
2. **Set up trust** on hosts (from that card for many hosts, or on a host page). This run writes
   DROOL's CA public keys to `/etc/ssh/drool_user_ca.pub`, points `TrustedUserCAKeys` at it in
   `/etc/ssh/sshd_config.d/40-drool-user-ca.conf`, checks with `sshd -t` and `sshd -T`, reloads
   sshd (open sessions stay up) and then proves it with a brand-new connection that offers only a
   certificate. It puts the previous files back if sshd rejects the result, and it doesn't touch a
   host that already trusts another CA file (add DROOL's key there yourself; the card shows it).
3. **Use certificates** on the host page: DROOL checks once more on a fresh connection and only then
   switches. From then on the stored credential is not used; keep it as a way back, or remove it
   (DROOL then warns that its CA is the only way in).

**Rotation** (Credentials → Rotate): a new CA is introduced while the old one keeps issuing; set up
trust again (hosts then trust both), and only when every host in certificate mode has accepted the
new one can it start issuing; then retire the old one (its private key is destroyed) and set up
trust once more so hosts drop it. The issuing CA can never be retired. Needs `credentials.manage`
and a fresh password confirmation, like every CA change.

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

*Runs → New run → Deploy file* puts one file on many hosts (by host, pack or tag):

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

## The DROOL agent (Shepherds)

In the UI the DROOL agent is called a **Shepherd** (*Shepherds* in the sidebar); on the host and in
the API it is `drool-agent`. SSH stays fully supported. The agent is optional, and useful when:

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

**Any host, with a token:** *Shepherds → Enrollment → New token* gives you a one-line install command:

```sh
curl -fsSL -k --pinnedpubkey 'sha256//…' https://drool.example:8080/agent/install.sh | sudo sh -s -- --token 'drool-enroll-1.…' --preset managed
```

The command pins DROOL's web certificate, so `-k` doesn't weaken it. Tokens expire after an hour
by default and work once. They can be revoked, and they can be bound to one host or put new
hosts into a group.

**Manually:** copy `drool-agent-linux-<arch>` (from *Shepherds → Enrollment*, or `make agent`) to the
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
(`agent.recovery_grace`). After that it must be enrolled again. *Revoke* on the host's page takes
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

(*Shepherds → Trust (PKI)* does the same in the browser.) With the root key offline you still need it for
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

1. Register `drool-release.pub` under *Shepherds → Updates*. Hosts must trust it too: pass it to
   *Install DROOL agent*, use `--trust-release-key` at install, or run
   `drool-agent trust-release-key drool-release.pub` on the host. Trusting a key is
   security-sensitive, and DROOL can't do it for you remotely.
2. Upload the binary together with its `.release.json` under *Shepherds → Updates*.
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
1–1). Within it, a Shepherd announces optional capabilities (firewall management, network activity,
system monitoring, Desired State checks, moving to a restored controller's epoch), and DROOL never
sends one an operation it can't do: it uses SSH where that is allowed, or refuses and says to update
the Shepherd. The Shepherds page and a host's Shepherd card show its compatibility: **compatible**,
**update available**, **reduced capabilities** (works, without some optional capability), **update
required** (it lacks something this host's configuration needs, such as firewall management),
**incompatible** (protocol), or an **identity / trust problem** (revoked, in conflict, or a certificate
that expired too long ago to be renewed). An incompatible agent isn't used, and hosts with an SSH
fallback keep working over SSH. A Shepherd whose certificate expired while the controller was down
renews it by itself when it reconnects, within `agent.recovery_grace` (180 days); after that it has to
be enrolled again.

### On the host

- `drool-agent status`: identity, certificate, trust, endpoints, capabilities.
- `/var/log/drool-agent/operations.log`: every operation the agent received (id, type, controller,
  time, outcome). It records no command text, file contents or secrets. DROOL can't change it.
- `updates: {policy: deny}` in `agent.yaml` keeps the agent on its installed version, whatever
  DROOL asks.
- Without systemd (containers), `drool-agent install` runs the agent under a small supervisor
  script instead of a unit.

### Changing DROOL's address

*Shepherds → Controller address* proposes a new address. Every connected agent tests it first. Commit
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

1. On a host's page, open **View firewall** (the host's firewall page, `/hosts/<id>/firewall`) and
   click **Check** to inspect the host. Check also records DROOL's management path, so **Effective
   policy** shows it from the start.
2. Choose a mode:
   - **Monitor** observes connections and explains what a policy would do.
   - **Guided** also suggests policies from what it saw.
   - **Enforce** applies the policy, with automatic rollback protection (below).
3. In **Firewall → Policy**, draw or write the policy (see below): hosts, packs (host groups), named networks,
   **internal networks**, **the internet** (everything outside the internal ranges), **anywhere** or
   a CIDR, and the services between them. **Save policy** when the preview looks right.
4. Check the result: the host's **Effective policy** tab lists every allowance with its reason, and
   **Firewall → Can A reach B?** answers questions like "can web01 reach database01 on TCP 5432?".
5. Click **Apply policy** on the host's firewall page, or select hosts under **Firewall → Hosts**.

Inbound traffic is denied unless a policy, or the management path, allows it. Outbound traffic is
allowed unless you set the host's outbound mode to **restrict**. Deny policies always apply and win
over allows.

Built-in services: ssh, dns (UDP **and** TCP 53), http, https, ntp, postgres, mysql, redis, smtp,
submission, imap, smb, nfs, ping, mdns, mqtt, wireguard. Add your own in the designer (a custom
protocol/port, or **Rules → Services**). Internal ranges (RFC 1918, CGNAT/Tailscale, IPv6 ULA, link-local, loopback) are editable
under **Firewall → Settings**. Add your routed LANs, VLANs and VPNs there.

### Firewall Designer

**Firewall → Policy** (the designer) shows the same policy three ways — a view switcher with
**Topology**, **Rules** and **YAML**. Edit in whichever suits you; the other two follow immediately,
because they are views of one document. On a phone it opens in Rules; Topology is a desktop map.

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

Groups in the designer (*Packs* in the UI, `group:` in YAML) are DROOL's host groups: changing
members here changes the pack everywhere. Removing a group from the YAML doesn't delete it (do that
on the Packs page).

### How applying works (you can't lock yourself out)

Each host gets a **dead-man transaction** — *automatic rollback protection* in the UI:

1. DROOL measures a brand-new management connection to the host.
2. The host writes a durable journal and arms a rollback timer (default **120 s**, configurable per
   host or pack). Only then does it apply the new rules, atomically.
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

Observed connections are shown under [Network monitoring](#network-monitoring-network-activity), each with what
the firewall policy says about it. Flows that no policy covers are **unclassified**: in Guided
mode they become suggested policies (the host's firewall page → Observed connections), and from any
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
- **Health settings:** where this host's health settings come from (the Recommended profile, your
  global changes, its groups, the host itself), and customizing one filesystem, disk, GPU, sensor
  or service (see [Health checks and overrides](#health-checks-and-overrides)).
- **Collection & helpers:** how the host is monitored, which optional tools would add
  information, and (agent hosts) the agent's resource budget and use.

Nothing is installed, changed or sent anywhere to monitor a host. Monitoring itself only evaluates
health. Warning and Critical health issues feed DROOL's [Alerts](#alerts-and-notifications)
system, which handles acknowledgement, maintenance suppression and configured notifications.

### Agent hosts and SSH hosts

| | Agent hosts | SSH-only hosts |
|---|---|---|
| How | **Continuous**: samples every 10 s (services every 30 s), kept as one-minute averages and maxima; storage health every 30 min; kernel storage errors as they happen | **Polled** every 5 min (Monitoring → Health checks and settings); storage health and kernel errors hourly, and only where sudo is allowed without a password |
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
change the budget for all hosts (Monitoring → Health checks and settings) or one host.

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
approval (all hosts or a pack) in Monitoring → Health checks and settings, after which DROOL installs a needed
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
universal "too hot". Warning and Critical issues become [alerts](#alerts-and-notifications);
informational ones (such as reduced monitoring fidelity) are shown here only.

### Health checks and overrides

Out of the box every host uses the **Recommended** profile, and most fleets never need anything
else. But a gaming PC, an LLM server, a NAS and a Raspberry Pi can have good reasons to differ, so
any single setting can be changed where it matters:

| Where | Example | How |
|---|---|---|
| Every host | "Disk warning at 80%" | Monitoring → Health checks and settings → Health checks → **Customize** |
| A group | *AI-Hosts*: high GPU utilization off | Groups → **Customize monitoring** |
| A host | *gaming-pc*: high CPU utilization off | The host's monitoring page → **Health settings** |
| One device or service | `/srv/media`: warning 92%, critical 98% (while `/` keeps 85%/95%) | **Customize health check** next to the filesystem, disk, sensor, GPU or service |

The most specific setting wins: a device's own setting, then the host's, then its groups', then
your fleet-wide change, then Recommended. You change only what you need — everything else keeps
coming from the level above — and **Reset to inherited** undoes a change without copying any
values back. Every setting shows where it comes from: *Recommended default*, *Hardware-reported
limit*, *Global custom setting*, *Inherited from pack: NAS*, *Custom for this host*, *Custom for
/srv/media*.

- **What you can change** depends on the check: on/off, warning and critical levels, how long a
  condition must last, and the level and time it needs to clear. A yes/no check such as a SMART
  failure has only on/off.
- **High GPU utilization** is a check you can turn on, and it is **off by default**: a busy GPU is
  usually working, not failing. Turning it on (say for a render farm) or off never affects GPU
  temperature, disk health, memory pressure or anything else.
- **High CPU utilization** stays on by default (a saturated CPU slows the whole machine); turn it
  off for compute, render or gaming machines. Memory pressure, temperature and services are
  separate checks.
- **Temperatures** use each sensor's own hardware limits. You can turn one sensor off, make it wait
  longer, or set your own limit for it; a custom limit is marked as such, never shown as the
  hardware's.
- **Filesystem space** and **disk health** are separate: raising `/srv/media`'s space threshold
  never weakens SMART, NVMe, media-error or kernel-error detection for the disk under it.
- **Devices are recognized by their own identity**: a filesystem by its UUID, a disk by its WWN or
  serial number, an NVIDIA GPU by its UUID, a service by its unit name — so a setting stays with
  the device when it is renamed, remounted or reordered after a reboot. Where only a weaker
  identity exists (a PCI slot, a kernel name), DROOL says so. If a customized device disappears
  its setting is kept, shown as *currently unavailable*, and applies again if it returns; it is
  never moved to a different device.
- **Hosts in several groups:** if two groups set the same thing differently, the group with the
  higher **priority** (Monitoring → Health checks and settings → Health overrides) wins. With equal priorities DROOL
  doesn't pick one: it shows a *pack conflict* and uses your fleet-wide value until you set a
  priority or a host setting.
- **Changes apply at once.** A problem that still qualifies stays the same alert. One that no
  longer qualifies — its check turned off, or its threshold now above the current value — closes
  as **Cleared by configuration**: the alert history says why, and the closing message says
  *CLEARED BY CONFIGURATION*, never *RECOVERED*, because the machine itself didn't change. A newly
  stricter rule starts counting from the change: lowering a threshold never claims a problem
  existed for the past five minutes.
- **Fleet-wide and group changes show their impact first**: how many hosts are affected, how many
  are above the old and the new threshold, and which open problems would close.

Health settings decide *whether something is a problem*. Who hears about it is separate:
"don't warn me about GPU load on gaming-pc" is a health setting; "send gaming-pc's Critical alerts
only to Discord" is a [notification route](#advanced-routing).

### History, size and permissions

History is kept as one-minute values for 26 hours, five-minute values for 8 days and hourly
values for 30 days (adjustable). Measured: about **1.5 MB per host** — 8.6 MB for 5 hosts, 78 MB
for 50, 308 MB for 200.

| Permission | Roles | Allows |
|---|---|---|
| `monitoring.read` | everyone | All monitoring views |
| `monitoring.manage` | administrators, operators | Watching services, probes, refreshing, helper installs (with `jobs.execute`), and host or device health settings that make detection *stricter* |
| `monitoring.settings` | administrators | Global settings, agent budgets, standing helper approvals, fleet-wide and group health settings, group priorities, and any host or device health change that makes detection less sensitive (a check off, a higher threshold, a longer wait) |

Every health-setting change, reset, turned-off check and priority change is in the audit log with
where, which check, which device and which fields.

## Alerts and notifications

Monitoring decides *what is wrong*: a **health issue** appears when a condition has lasted long
enough (see [Health issues](#health-issues)). An **alert** is DROOL telling you about it. Every time
a Warning or Critical issue starts, DROOL opens an alert; when the issue goes away, the alert is
resolved and you're told how long it lasted. If `jellyfin.service` fails today and again tomorrow,
those are two alerts, linked to each other.

### Setting up notifications

**Settings → Notifications → Add destination**, pick one, fill in one or two fields, press
**Send test**, then **Save**:

| Destination | What you need |
|---|---|
| **Discord** | A webhook URL: channel settings → Integrations → Webhooks → New Webhook → Copy Webhook URL |
| **Slack** | An incoming-webhook URL (`https://hooks.slack.com/services/…`) from a Slack app |
| **Email** | Your SMTP server, port, a username and password (or app password), a From address and one or more recipients. **STARTTLS** on port 587 is the usual choice; **TLS** for port 465. Certificates are checked normally. Sending without encryption is possible only for a relay on a trusted network, after you confirm it, and never with a password |
| **Webhook** | Any `http://` or `https://` URL, including services on your own network (Home Assistant, ntfy, n8n, …), and optionally a secret header such as `Authorization: Bearer …`. DROOL POSTs JSON (format below) |

That's all most people need: a new destination gets **Recommended** — Warning and Critical alerts,
plus a message when they're fixed. The test message says it's a test and creates no alert. The
links in messages point to the address you're using DROOL from (change it under *Advanced*).

What arrives looks like this:

```
CRITICAL: storage01
nvme0n1 (Samsung 990 PRO): new media errors
media errors 2 → 3
First detected: 2026-10-08 02:14 CEST
Open in DROOL: https://drool.lan:8443/alerts/alr_…

RECOVERED: media01
Recovered: jellyfin.service failed
Duration: 7m 42s
Acknowledged by rob: Restarting after the config fix

CLEARED BY CONFIGURATION: render01
Cleared by configuration: CPU at 95% or more for 5 minutes
Why: High CPU utilization was turned off (Custom for this host).
This is not a recovery: the check was turned off; the condition may still be present.
```

A *cleared* message goes where a recovery message would; it is never worded as one.

### Working with alerts

**Alerts** (in the menu, with the number of unacknowledged alerts) has three lists: **Active**,
**Acknowledged** and **Resolved / history**, with filters for severity, host, group, category,
maintenance and dates. Severity is always shown as a word and an icon, not just a colour. An
alert's page shows its timeline (when the condition was first seen, when the alert opened, who was
notified, who acknowledged it, when it recovered), the health issue behind it, every notification
attempt, and earlier occurrences of the same problem.

- **Acknowledge** (with an optional note, e.g. "Replacing the disk this evening") tells others
  someone is on it and stops reminders. It does **not** close the alert: only the condition
  recovering does. Nobody can mark a failing system as healthy.
- **One message when it starts, one when it's fixed.** A problem that stays for hours doesn't send
  a message every minute. If a Warning becomes Critical you're told once.
- **Reminders** for alerts nobody has acknowledged are off unless you add them to a route
  (hourly at most often, at most 12).

### Maintenance

Working on a machine? On its host page (or **Alerts → Maintenance**) choose **Start maintenance**,
for 1, 2, 4 or 24 hours, until a time, or until you end it, with a reason ("Kernel upgrade").
Groups can be put in maintenance too.

During maintenance DROOL **doesn't send notifications** for those hosts — and nothing else changes:
monitoring, health issues and alerts carry on (marked *Suppressed by maintenance*), and firewall
rollback, agent recovery and host-key checks keep protecting you. A problem that starts and ends
during maintenance is kept in the history and never sent. One still there when maintenance ends
is sent once, after a two-minute grace period (in case it was just finishing). For maintenance
that happens on a timetable ("every Sunday 02:00–04:00"), create a
[recurring window](#maintenance-windows) under **Alerts → Maintenance → Recurring windows**.

### Advanced: routing

Under **Settings → Notifications → Advanced**. The **Recommended** route sends Warning and
Critical alerts to the destinations that chose it. You can add routes such as:

- *All Critical* → Email + Discord
- *Storage warnings on the NAS group* → Email
- *Service warnings on the Media group* → Discord, without recovery messages

A route filters by severity, hosts and groups, and categories (CPU, memory, disk space, storage
health, services, interfaces, temperature, GPU, unreachable). There's no order to get right: every
route that matches contributes its destinations, and each destination gets an alert once even
if several routes chose it. Recovery messages go to the destinations that got the alert.

### Delivery, retries and duplicates

Notifications are saved in DROOL's database in the same step that opens the alert, then sent in
the background, so a restart or crash never loses one. If a destination fails, DROOL retries for
about an hour with growing gaps (30 seconds up to 30 minutes), unless the failure can't fix itself
— wrong credentials, a rejected request, a certificate problem — which stops at once. It never
retries forever, and a retry is never a new alert. Each destination shows **Last success** and
**Last failure**; when one keeps failing, the Overview's event list says so once (and again when
it works). Alerts stay correct whatever happens to the destinations.

Delivery is *at least once*: if a server received a notification but its answer got lost (a
timeout, a restart at the wrong moment), DROOL can't know and sends it again — marked "may have
arrived twice" in the delivery history. Webhooks get a `delivery_id` (also as the
`Idempotency-Key` header) that stays the same across retries, so a receiver can drop duplicates.

To protect your chat channels during a big outage, DROOL sends at most 30 messages a minute to one
Discord or Slack destination (60 for email, 120 for a webhook); the rest wait their turn.

### Webhook format

```json
{
  "schema": "drool.notification.v1",
  "event_id": "alr_01J…:opening",
  "delivery_id": "ndel_01J…",
  "event": "opened",
  "sent_at": "2026-10-08T02:14:05Z",
  "test": false,
  "summary": "CRITICAL: storage01",
  "text": "nvme0n1 (Samsung 990 PRO): new media errors\nFirst detected: …",
  "alert": {
    "id": "alr_01J…", "fingerprint": "host_01J…/media_errors/nvme0n1",
    "state": "active", "severity": "critical",
    "host": { "id": "host_01J…", "name": "storage01" },
    "check": "media_errors", "category": "storage", "subject": "nvme0n1",
    "title": "nvme0n1 (Samsung 990 PRO): new media errors", "detail": "media errors 2 → 3",
    "value": 3, "observed_at": "…", "opened_at": "…", "url": "https://drool.lan:8443/alerts/alr_01J…"
  }
}
```

`event` is `opened`, `escalated`, `reminder`, `recovered`, `cleared` or `test`; `recovered` and
`cleared` add `resolved_at`, `duration_s` and `resolution` (`recovered`; for `cleared`: `disabled`
— the check was turned off, `reconfigured` — a threshold changed and the current value no longer
qualifies, or `gone` — the device is no longer reported) with a `resolution_note`. Only
`recovered` means the system got better. Acknowledged alerts carry an `acknowledgement` (`by`,
`at`, `note`). `event_id` is the
same for every destination; `fingerprint` is the same for every occurrence of a problem. Headers:
`X-DROOL-Event`, `X-DROOL-Delivery`, `Idempotency-Key`, and your secret header if set. A 2xx answer
is success; redirects are followed (up to 3, only to `http(s)`, never from `https` to `http`, and
your secret header is not sent to another host).

### Secrets, retention and permissions

SMTP passwords, webhook URLs and secret headers are encrypted with the master key like SSH
credentials, never shown again after saving (the page shows `https://discord.com/…•••••••`), and
never written to logs or the audit log (which records *that* a secret changed). A backup restored
without its master key keeps destinations but disables them until you enter their secrets again.

Alert history is kept **180 days**, delivery details **30 days** (both adjustable). Measured with
a pessimistic three alerts per host per day: about **1 MB per host** at 200 hosts (208 MB); lists
and details stay around a millisecond.

| Permission | Roles | Allows |
|---|---|---|
| `alerts.read` | everyone | Alerts, history, maintenance list, event timeline |
| `alerts.acknowledge` | administrators, operators | Acknowledging alerts (audited) |
| `maintenance.manage` | administrators, operators | Starting and ending maintenance (audited) |
| `notifications.read` | administrators, operators, auditors | Destinations (without secrets), routes, delivery history |
| `notifications.test` | administrators, operators | Sending a test to an existing destination (audited) |
| `notifications.manage` | administrators | Adding, changing and deleting destinations and routes, testing unsaved ones, settings (audited) |

## Scheduling and workflows

A **schedule** runs something at the times you choose: a job, a saved workflow, or a maintenance
window. Every scheduled run is an ordinary job — it appears under **Runs** with live output
and per-host results, and it is checked, locked, logged and cancellable exactly like a job you
start by hand. You never write cron expressions or playbooks.

### Schedule a run

The quickest way: set up a job as usual (**Runs → New run**), then choose **Schedule this run**
instead of running it. Pick when, check the preview, save. **Save as workflow** keeps the job as a
reusable workflow instead.

Or go to **Schedules → New schedule** and fill in three things:

1. **When.** *Once*, *every few hours*, *every day*, *selected weekdays*, *every week* or *every
   month*, at a time, in a **time zone** you choose (your browser's is suggested). DROOL never
   uses the server's own time zone, so moving the server doesn't move your schedules. The next
   runs are shown as you edit.
2. **Targets.** Hosts, groups or tags. Groups and tags are looked up again at every run: a host
   you add to the group next month takes part from then on. Each run keeps the list of hosts it
   started with.
3. **What to run.** Steps (the same ones as a job), or a saved workflow.

The **preview** shows exactly what would happen if the schedule ran now: which hosts, in which
order and batches, the steps, whether hosts may reboot, which checks run afterwards, how missed
runs and overlaps are handled, and the next run times. For package updates it reminds you that
today's preview can't promise what next week's run installs (repositories change).

**Daylight saving time:** if a time doesn't exist on a given day (the clocks jump from 02:00 to
03:00 and you chose 02:30), that day's run happens at the first valid time after the jump (03:00).
If a time happens twice (the clocks go back), it runs once, the first time. Either way there is
exactly one run per day. "Every few hours" counts real hours, so its clock times shift by an hour
across a change.

### Rollouts: try one first, then a few at a time

For a change to several hosts, DROOL recommends a careful rollout — **one canary host first, then
batches of five, pausing after two failures** — and applies it with one click. Under
**Advanced** you can choose:

- **Canary:** the first hosts by name, or hosts you pick. They run on their own first. If a canary
  fails, the job **pauses** before any other host starts.
- **Batch size:** after the canaries, hosts run in batches; a batch starts only when the previous
  one has finished.
- **Hosts at a time:** how many run in parallel inside a batch.
- **Pause after failures:** after that many failures DROOL starts no more hosts, lets the running
  ones finish, and pauses: "Paused after 2 failures. 13 hosts have not started."

A paused job shows why, and waits for you: look at the failed hosts, then **Resume** (the rest
continue; failed hosts stay failed) or **Cancel**. The order of hosts and batches is fixed when
the run starts and shown on the job page (*Rollout progress*).

### Workflows

A **workflow** is a named list of steps that runs in order on each host, for example:

1. System update, rebooting only if the update requires it (DROOL waits for the host to come back)
2. Verify the host is reachable
3. Verify services are running: `jellyfin`, `ssh`
4. Verify health: no active Critical issues

Steps are the usual job operations (commands, scripts, packages, system updates, services,
reboots, file deployments) plus checks DROOL runs itself, with nothing executed on the host for
them: **Wait**, **Verify host is reachable**, **Verify services are running** and **Verify health**
(no active Critical — or no Warning or Critical — health issues). Checks use fresh monitoring data
and keep trying for a while (a service still starting gets two minutes by default); a check that
doesn't pass fails that host, with the reason. There are no branches or conditions: if a step
fails, that host stops.

**Templates** (*Scheduled system update*, *Safe fleet update*, *Update and verify services*) are
starting points; once saved they're ordinary workflows you can change.

**Revisions:** every change to a workflow is saved as a new revision. A schedule runs the exact
revision you reviewed when you saved it. When the workflow changes, the schedule says *A newer
workflow revision is available*, and keeps running its revision until you choose **Use revision
N** and save. Past runs always show exactly what they ran.

### If DROOL wasn't running

- **Recurring schedules** skip runs missed while DROOL was down and continue with the next one.
  The history records "Missed 3 runs while DROOL was not running" — one entry, not three. Under
  *Advanced* you can choose *Run one missed occurrence when DROOL returns* instead (only the most
  recent one; never a backlog of five daily updates in a row).
- **One-time schedules** run when DROOL returns, marked *started late*. Optionally: *don't start
  if more than N minutes late*.
- A run that was in progress when DROOL stopped is marked *interrupted* like any job, and is not
  started again automatically.

### If the previous run is still going

By default the new run is skipped ("Skipped: previous run still active"). Under *Advanced*,
*Queue one run* starts it when the previous one finishes — at most one waits.

**Disabling a schedule doesn't stop a run in progress, and cancelling a run doesn't disable its
schedule.** The pages say so where it matters.

### Maintenance windows

**Alerts → Maintenance → Recurring windows** creates maintenance on a timetable: hosts and groups,
when, how long, and a reason. Each window is ordinary maintenance (notifications paused, everything
else carries on; group membership is checked live while it lasts).

- **Scheduled jobs skip hosts that are in maintenance** ("skipped: maintenance active"), so a
  nightly job doesn't run on a machine you're working on. Jobs you start by hand, and **Run now**,
  still run.
- A schedule can be **tied to a recurring window** (*Advanced → Maintenance window*): it then runs
  only while that window is active and may work on the hosts the window put in maintenance. Other
  maintenance still applies: a window doesn't let a job past someone else's maintenance.
- If DROOL was down when a window should have started, it starts the window when it returns — with
  the original end time — or records it as missed if the window has already ended. It never sends
  notifications late.

### Who a schedule runs as

Every schedule has an **owner**: whoever created it, or whoever last edited it (editing someone
else's schedule makes you its owner, so it never runs your changes with their rights). Scheduled runs
use the owner's permissions, and DROOL checks them **before every run**: if the owner's account was
disabled or deleted, or no longer has the permissions the steps need, the run is **blocked**, the
schedule is **disabled** and marked *needs attention*, with the reason. An administrator can
**Change owner**. The audit log records each scheduled run as "system execution via schedule X,
owned by Y". **Run now** runs with *your* permissions, as you. Schedules never get around a host's agent
limits, the SSH privilege settings or permission checks: a host that can't do a step fails or is
skipped with the reason, as in any job.

### History and permissions

Each schedule keeps its own **History**: when a run was due, when it started, what happened
(succeeded, failed on 2 hosts, skipped and why, missed, blocked) and a link to the job. The list
under **Schedules** shows the next run, the last result, the owner, and which schedules need
attention.

| Permission | Allows |
|---|---|
| `schedules.read`, `workflows.read` | see schedules, their history and workflows (all roles) |
| `schedules.manage` | create schedules, change or archive your own, enable, disable, run now (operators, admins) — you also need the permissions the steps need |
| `workflows.manage` | create and change workflows (operators, admins) |
| `schedules.admin` | change anyone's schedule and change owners (admins) |

Creating, changing, enabling, disabling, archiving, running now and changing owners are all in the
audit log.

## Desired State

*What should this machine look like, is it still that way, and how do I safely put it back?*

A **baseline** is a named list of things a machine should have, for example *Server Defaults*:

- `ssh` running and enabled at boot
- `unattended-upgrades` installed, `telnet` absent
- `/etc/motd` with exactly this content, owned by root, mode 0644

That's all a baseline can say: a **service** is running or stopped, and/or enabled or disabled
at boot; a **package** is installed or absent (no versions); a **file** has exact content, and
optionally an owner, group and mode, a validation command (e.g. `sshd -t`) and a service to
reload or restart after it changes. There are no expressions, scripts or conditions — DROOL
doesn't evaluate statements like "SSH root login must be disabled", but you can manage the
`sshd_config` drop-in that does it. The firewall isn't part of desired state: its policy has its
own page, and a host's desired-state view links to it.

### Getting started

**Desired State → Baselines → Create baseline:** name it, choose where it applies (*everywhere*,
packs or hosts), add resources, check the **impact preview** — which hosts are affected, which
would start to differ and which would comply — and save. DROOL checks the hosts and shows each
one as **Compliant**, **Drift detected**, **Conflict**, **Unknown**, **Restoring…** or **Restore
failed**, with exactly what differs:

```
jellyfin.service  wants running, enabled at boot  is failed, enabled at boot   Drift detected
/etc/motd         content: sha256 4f1e2a9c03b7… → sha256 9a07b3d1e8c2…; mode: 0644 → 0664
```

The **Overview** shows hosts that need attention; a host's **Desired State** page shows every
managed resource, where each desired value comes from ("Required by Global baseline Server
Defaults", "Inherited from pack: media-servers", "Custom for this host"), and its history.

### Where desired values come from

The most specific declaration of a resource wins: **this host** → **baselines assigned to the
host** → **baselines of its packs (groups)** → **everywhere**. When two of a host's packs declare the
same resource differently, the pack with the higher **priority** wins (set on the Packs page,
default 0). Same priority, different values: that's a **Conflict**. DROOL never picks one:
the resource is shown as a conflict with both values and where they come from, is never checked
against either, and can't be restored (by hand or automatically) until you resolve it — give one
pack a higher priority, change a baseline, or accept the current state for that host.

### Review, accept or restore

Open a resource that differs:

- **Review difference** — read-only: desired and current state; for packages the installed
  version; for files both hashes, owner, group and mode, and a line-by-line diff for text files
  up to 1 MiB (showing file contents needs file permissions and is logged).
- **Accept current state** — what the host has now becomes its desired state **for this host
  only** ("Custom for this host"). Baselines and other hosts don't change. A file's current
  content is stored in DROOL's file store (up to 64 MiB); a file that doesn't exist becomes *not
  managed on this host*. *Reset to inherited* undoes it.
- **Change baseline** — opens the baseline's editor with its impact preview, for when the
  baseline itself should change.
- **Restore managed state** — DROOL checks the hosts again, works out exactly what to change, and
  shows it before anything happens: each host and resource (*now → will be*), the job's steps,
  the rollout, and what can't be restored and why. Confirm and DROOL creates an **ordinary job**
  — under **Jobs** with live output, locks, retries, cancellation and audit like any other.
  Several hosts or resources become **one job** (one canary host first, then batches of five,
  pausing after two failures). Afterwards DROOL checks again and shows **Restored** only when it
  sees the result; otherwise *Restore failed* with the reason. If nothing differs, no job is
  created.

Restoring keeps every safeguard of the underlying operation: removing a package that would also
remove others fails safely unless you explicitly allow it; files are deployed with backup,
validation and rollback; DROOL never follows or replaces a symlink where a managed file should
be; a host's agent limits still apply. Hosts DROOL must not change right now — an agent that
belongs to another (restored) controller, a quarantined agent, file state that is newer on the
host than in DROOL — are left out with the reason.

### History

Each resource keeps a history of what changed: drift detected, a conflict, restore requested,
**Restored** (DROOL corrected it and saw the result), **Resolved by configuration** (the desired
state changed, or you accepted the current state), **Compliant again (changed on the host)**
(someone fixed it by hand). Checks that find nothing new aren't recorded.

### How DROOL checks

- **Services** come from system monitoring (it must be on). Agents report every change within
  about a minute; SSH hosts are polled every 5 minutes. Before restoring or accepting, and after a correction,
  DROOL asks for fresh data and waits for it. (Agents from before this version only report
  changes; update them so DROOL can confirm a service's state on request.)
- **Packages** are checked with a read-only query of just those packages — no updates, no
  package transaction, no root needed.
- **Files** are checked by size, owner, group, mode and a SHA-256 computed on the host; their
  content is only transferred when you review or accept.

Packages and files are checked every 15 minutes per host (spread out), and sooner after a
change, a reconnect, a correction or **Check now**. Data that is too old, an unreachable host or
monitoring being off makes a resource **Unknown** — never compliant.

### Automatic restore (optional)

Off by default. An administrator can turn on **Automatically restore managed state** for a
baseline (*Edit → Advanced*); they become its owner, and DROOL checks before every automatic job that
the owner still exists, is enabled and has the permissions. Automatic restore uses the same
ordinary jobs and rollouts, only for services, packages and files, and is careful not to fight
anyone:

- it never touches hosts in maintenance, and waits 5 minutes after maintenance ends;
- it never restores a host's own accepted values or conflicts, and never removes packages that
  would take others with them;
- a failed restore is retried after 15 minutes, then after an hour, then paused for that resource;
- something that keeps drifting back (three times in a day) is paused for that resource;
- if an automatic job's rollout pauses (a canary failed, too many failures), or the owner loses
  the permissions, automatic restore for that baseline pauses — *Automatic restore paused — needs
  attention* — until an administrator resumes it.

At most one automatic job per baseline and three in total run at once.

### Maintenance and warnings

During **maintenance** DROOL keeps checking and showing drift, but restores nothing
automatically; restoring by hand is possible, with a warning. **New job** warns you when a job
would contradict desired state ("On media-01: jellyfin.service is managed to keep running …
Stopping it will create desired-state drift"), adding that automatic restore may undo it when
that's on — use maintenance, or change desired state, if the change is intentional.

### Permissions

| Permission | Allows |
|---|---|
| `desired.read` | see baselines, desired and current state, drift and history (all roles) |
| `desired.restore` | check now and restore managed state (operators, admins) — restoring also needs `jobs.execute` and what the operations need (`files.deploy`, `files.elevate` for files) |
| `desired.manage` | create and change baselines and group priorities, accept current state, set or reset a host's own values (admins) |
| `desired.auto` | turn automatic restore on or off (admins) |

Creating, changing and archiving baselines, priorities, accepting, restoring (by hand and
automatically, with the owner), reviewing file contents and automatic-restore changes and pauses
are all in the audit log.

## Network monitoring (Network Activity)

**Network Activity** in the sidebar shows what your hosts actually talk to; every host has its own
**Network Activity** page (*Hosts → web-01 → View network activity*), with what it listens on and a
**Take snapshot** button for SSH hosts. Monitoring only: nothing here blocks traffic, scores addresses or
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

## Accounts, sign-in and the audit history

**Two-factor authentication** (*Settings → Account*): scan the QR code (or type the key) into an
authenticator app, confirm with a code, and keep the ten recovery codes it shows once (each works
once; making new ones replaces them). From then on signing in asks for a code after the password.
Lost the authenticator? Use a recovery code, ask another administrator (*Settings → Users*), or
on the controller: `drool user mfa-reset --username NAME`. The header warns an administrator who
hasn't turned it on.

**Confirming it's you.** Actions that change trust or could lock DROOL out — managing users and
tokens, two-factor, the SSH and Shepherd certificate authorities, revoking a Shepherd, release
keys, the controller address, audit settings, restore decisions, replacing a credential hosts
use, firewall overrides, accepting a changed host key — ask for your password again (and a code,
with two-factor) if you haven't confirmed within the last 5 minutes (`auth.reauth_window`). The
server enforces it; an API token can never do these.

**The audit history** (*Audit Log*) records every change. Its records are chained: each one
includes a hash of the one before it, and DROOL seals the chain every hour with a key derived from
the master key. DROOL checks the whole history when it starts, every day and when you ask, and
says plainly when a record was changed, removed, added or reordered outside DROOL (the details,
and a file with what was found, come second). It is **tamper-evident, not tamper-proof**: someone
who controls this machine and its master key can rewrite all of it consistently, and even without
the key, someone who can write DROOL's database can rewrite the records written since the last
hourly seal, or (while DROOL is stopped) cut off the end of the history, without DROOL noticing.
The remote copy below is what catches that. After investigating a break you can acknowledge it
with a note; a later break is still reported.

**A remote copy** (*Audit Log → Remote copy*) sends every record, as it is written, to an HTTPS
endpoint you run (a log collector, a small web app on another machine): what you need when the
controller itself may be compromised. Records are sent in order, at least once, in JSON batches of
up to 500 (`POST`, `Content-Type: application/json`, optionally one header you choose, e.g. a
token; a CA certificate for a private TLS endpoint):

```json
{ "format": "drool-audit-export-v1", "chain_format": "drool-audit-v1", "chain_id": "…",
  "controller": "…", "controller_epoch": "…", "sent_at": "…",
  "records": [ { "seq": 1, "id": "aud_…", "ts": 1760000000000, "time": "…", "actor_type": "user",
    "actor_id": "…", "actor_name": "…", "action": "…", "target_type": "…", "target_id": "…",
    "result": "success", "source_ip": "…", "request_id": "…", "details": { },
    "details_json": "{…}", "prev_hash": "…", "hash": "…" } ] }
```

Answer any 2xx once the batch is stored; anything else is retried with backoff (up to
10 minutes) and the header shows when the copy fails or falls behind. Deduplicate by `seq` and
`hash`. To verify a record without DROOL: `hash` is the hex SHA-256 of these fields, each written
as a netstring (`<length in bytes>:<bytes>,`), in this order — `drool-audit-v1`, `prev_hash` (hex;
64 zeros for the first record), `seq`, `id`, `ts` (decimal milliseconds), `actor_type`,
`actor_id`, `actor_name`, `action`, `target_type`, `target_id`, `result`, `source_ip`,
`request_id`, `details_json` (exactly as sent, not re-serialized) — and each record's `prev_hash`
is the previous record's `hash`. After the controller is restored from a backup, its new records
continue from the backup's last one, so the copy receives sequence numbers it already has with
other contents, under a new `controller_epoch`: keep both, that fork is the evidence of what
happened after the backup.

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
history), the self-signed TLS certificate, and the config file if you use one. A backup is
written to a temporary file and renamed into place only when complete, so a full disk or a crash
never leaves a partial file under its name.

**Check a backup without restoring it:**

```sh
drool backup verify drool-backup.tar.gz                       # add --master-key FILE for one made without its key
```

It checks the archive and its checksums, the database's integrity, that it upgrades to this DROOL's
schema (on a temporary copy), which master key opens its credentials, and its audit history, and
changes nothing. Exit status: `0` restorable and complete, `2` restorable but its credentials need
a master key it doesn't include, `1` not restorable.

**The master key.** Stored credentials (SSH keys, passwords) are encrypted with
`master.key`, which is **not** included unless you ask for it:

- `drool backup` (default): safe to keep almost anywhere, but **not a complete disaster-recovery
  backup on its own**. To get your credentials back you also need the `master.key` file (keep a
  copy somewhere safe and separate). The command tells you this, and the key's id, every time.
- `drool backup --include-master-key`: self-contained, but anyone holding the file can decrypt your
  credentials. Store it encrypted.

**Rotating the master key** (for example after a copy of it may have leaked), with DROOL stopped:

```sh
sudo systemctl stop drool && sudo -u drool drool secrets rotate-key && sudo systemctl start drool
# Docker: docker compose stop drool && docker compose run --rm drool secrets rotate-key && docker compose start drool
```

It adds a new key version, re-seals every stored secret (credentials, CA keys, notification and
audit-sink secrets, two-factor secrets) with it in one transaction, checks that the new version
alone opens all of them, and only then removes the old version from `master.key`. A crash at any
point leaves a key file that still opens everything. The key file as it was is saved next to it
(`master.key.previous-<time>`): backups made before the rotation need it (`drool restore
--master-key`), so keep it with them, offline, and delete it from the controller. Make a new
backup afterwards.

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
directory. Everyone signs in again afterwards. If the restore itself is interrupted (power loss, a
killed process), DROOL refuses to start on the half-restored data directory; run
`drool restore --recover` to put the previous installation back.

**What happens after a restore.** The world moved on after the backup was taken, so the restored
controller doesn't assume it didn't. It gets a new *controller epoch*: Shepherds and DROOL's
firewall tool on each host still answer to the backup's and refuse this controller's commands and
firewall changes until DROOL has compared the host with the restored data (a host with SSH as its
fallback still runs ordinary jobs over SSH meanwhile). A host that is unchanged (or that DROOL has newer configuration for) is moved to
the new epoch by itself, a few hosts at a time; a host changed after the backup (a firewall
applied, a Shepherd's configuration changed), a Shepherd revoked or re-enrolled since, a machine
whose identity changed, or a Shepherd too old to move waits for you under **Settings → System →
Restore**, where you accept its state (nothing on the host changes; DROOL never rolls it back to
the backup) or deal with it otherwise. Nothing that was in progress when the backup was taken is
repeated: unfinished runs are marked interrupted with an unknown outcome, open firewall
transactions are closed as unknown, unsent notifications are dropped, and job schedules, automatic
Desired State restore and Shepherd update rollouts are held until you resume them. Shepherds
enrolled after the backup are refused and listed on the Shepherds page; enroll them again.

If the backup has no master key and this machine doesn't have the right one, restore explains your
options: `--master-key /path/to/master.key`, or `--discard-credentials` to restore everything
except the stored credentials (hosts, keys, history and users are kept; you assign credentials
again). The same command is the way out if you ever lose `master.key`: DROOL refuses to start
with a missing or wrong key rather than silently making your credentials unreadable.

### Recovery procedures

| Situation | What to do |
|---|---|
| The controller machine is gone | Install DROOL on another machine, `drool restore` the latest backup (with its master key: `--include-master-key` backups have it, otherwise `--master-key FILE`), start it, point Shepherds' DNS name or address at it if it changed. Then work through *Settings → System → Restore*. |
| The master key is lost | DROOL refuses to start with a missing or wrong key. Restore the latest backup with `drool restore --discard-credentials`: everything but stored credentials comes back; assign hosts a credential again. The agent CA keys go with it: issue a new intermediate with the offline root key (`drool agent-ca rotate --root-key FILE`); Shepherds move to it by themselves. |
| A restore was interrupted | DROOL refuses to start and says so; `drool restore --recover` puts the previous installation back, then restore again. |
| After a restore, hosts are *Ahead of DROOL*, *Needs attention* or *Blocked* | *Settings → System → Restore* explains each: accept the host's state, deal with the cause (revoke, re-enroll, accept a changed host key on the host's page), or check again. Until then DROOL doesn't change their firewalls, and their Shepherds refuse this controller's commands. |
| Shepherds can't connect after a restore and the agent CA was replaced after the backup | Issue a new intermediate with the root key (*Shepherds → Trust*, or `drool agent-ca rotate --root-key FILE`); its trust manifest supersedes the newer one they hold. |
| A newer DROOL was installed and you need the older one back | Downgrades aren't done in place: run the newer version again, or stop DROOL and `drool restore` the `drool-pre-upgrade-…` backup with the older version. |
| The audit history failed verification | Investigate (the Audit Log shows what and where, the evidence file has the records); compare with the remote copy if you have one; acknowledge the break with a note when understood. |
| A Shepherd was compromised | Revoke it (*host page* or *Shepherds*); re-enroll after reinstalling the host. If you restore an older backup later, revoke it again (older Shepherds don't report the revocation). |
| The master key may have leaked | `drool secrets rotate-key` with DROOL stopped; keep the saved previous key file with older backups, offline. |

These situations intentionally need a person: DROOL won't guess which side of a disagreement is
right, roll a host back to an old backup, or repeat an operation whose outcome it can't know.

## Upgrading

Take a backup, then replace the binary (native: stop, install, start) or rebuild the image
(`git pull && docker compose up -d --build`). Database migrations
run automatically on start and only move forward, each in its own transaction (an interrupted one
changes nothing). Before migrating, DROOL backs the database up by itself to
`<data_dir>/backups/drool-pre-upgrade-<time>-schema<N>.tar.gz` (the newest three are kept; without
the master key, which stays in the data directory) and doesn't start if it can't; set
`backup.before_upgrade: false` to skip that.

**DROOL doesn't update itself.** There is no download-and-install of the controller from the web UI
or anywhere else: you replace the binary or image as above. Shepherds do update themselves, but only
to releases signed with a release key you hold and register (Shepherds → Updates), staged, health
checked and rolled back automatically if the new version doesn't come up.

**Going back to an older version** isn't done in place: an older DROOL refuses a database a newer
one upgraded, and says which version upgraded it. Run that version again, or stop DROOL, restore
the pre-upgrade backup with the older version (`drool restore …/drool-pre-upgrade-….tar.gz`) and
start it; anything done since the upgrade is not in that backup.

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
  Agent updates need a release-signing key that you manage yourself.
- **After restoring a backup** (see [What happens after a restore](#backup-and-restore)): DROOL holds
  no copy of state a host got after the backup, so it can't show the difference — accepting a
  host's state means it stands, and DROOL's own configuration becomes the next change. Shepherds
  from before this version can't move to the restored controller's epoch: update them, or enroll
  them again. Only Shepherds from this version report a revocation made after the backup: repeat
  revocations you made since (the remote audit copy lists them). Until a host is reconciled, its
  Shepherd takes nothing from this controller and its firewall isn't changed; a host with SSH as
  its fallback still runs ordinary jobs (including Desired State restores you start) over SSH.
- **Scheduling:** times have minute precision; the scheduler looks for due work every 15 seconds,
  so a run starts within about that of its time. A recurring run that would start more than
  5 minutes late counts as missed (one-time runs start late instead). One controller only:
  schedules don't fail over. Job results aren't sent as
  notifications (watch the schedule's history or the Overview's events). A burst of thousands of
  runs due in the same minute is handled (tested with 3 000), but they queue in the job engine,
  which runs 20 hosts at a time.
- **Workflows** are a list of steps — no branches, conditions, loops or approval steps. *Verify
  services* and *Verify health* need system monitoring to be on.
- **Desired state:** services, packages and managed files only (no users, cron, sysctl or
  containers; no package versions). Services are read from system monitoring, so they need it to
  be on, and only `.service` units can be managed (not masked or static units' boot state).
  Packages and files are checked every 15 minutes, so drift made in between shows up then (or
  with *Check now*). A baseline holds at most 200 resources; one correction at most 50 different
  changes per job (larger ones are split by host, or refused for a single host — restore it in
  parts). Agents from before this version can't report packages, or confirm services on request,
  until updated.
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
- **Health overrides:** per-device settings are per host (a group can't target "/srv/media on
  every member"); GPU hardware/driver error health (NVIDIA Xid and similar) isn't collected, so
  only GPU utilization and temperature are judged; NVIDIA GPUs report no temperature limit, so
  their temperature is judged only against a limit you set; interfaces are identified by name;
  older agents report GPUs and sensors without their stable identity until updated.
- **Alerts and notifications:** delivery is at least once (a rare duplicate is possible; webhooks
  get an id to drop it); an SMTP server or webhook with a self-signed certificate needs its CA in
  the controller's system trust store (there is no per-destination certificate setting); no SMS,
  push apps or plugins beyond the four built-in destinations (a webhook can feed ntfy, Gotify or
  Home Assistant); deleting a host deletes its alert history.
- **Local accounts only;** no single sign-on. Two-factor authentication is TOTP (authenticator apps);
  no WebAuthn/passkeys yet.
- **Audit history** is tamper-evident, not tamper-proof: whoever controls the controller and its
  master key can rewrite all of it consistently; without the key, the newest records (since the
  last hourly seal) can still be rewritten, or the end cut off while DROOL is stopped. The remote
  copy is what survives that. After a
  restore, the remote copy receives the restored controller's records under the same sequence
  numbers as the ones it got after the backup, with a new controller epoch, and keeps both.
- **Master key rotation** needs DROOL stopped (it re-seals everything while nothing else writes).
- **Containers:** tested with Docker, read-only and without capabilities; rootless Docker and
  Podman aren't part of the automated tests.
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

Browser tests (`web/e2e`: the Firewall Designer, network and system monitoring, alerts, health
settings, scheduling and workflows, Light/Dark/System, contrast in both themes) run against
containers with `go test -tags integration ./test/integration -run TestBrowser -v`. They need
Chromium (`CHROME=/path/to/chromium`, default `/usr/bin/chromium`) and `npm ci` in `web/`.
`DROOL_E2E_FILES=automation,contrast` runs only those files (handy while iterating).

The REST contract lives in [`api/openapi.yaml`](api/openapi.yaml). A Go test fails if routes
and the spec drift apart. The UI's TypeScript client types are generated from the spec.

### Frontend styling

All colors, radii and fonts are semantic design tokens in `web/src/styles/index.css`, and
components use only those tokens through Tailwind utilities (`bg-surface`, `text-muted`,
`border-line`, ...). Each token has a light and a dark value (not an inversion); the browser
contrast test checks both themes against WCAG AA. UI primitives live in `web/src/components/ui/`. Restyling means changing
tokens and primitives, not pages.
