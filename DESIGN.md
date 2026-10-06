# Linux Fleet Management Platform

**Design Document v0.3**
Status: Draft · 2026-10-06 · High-level design frozen; further work moves to `ARCHITECTURE-ALPHA.md`
Working name: *Fleet Manager* (placeholder). Binary/path placeholder: `fleetmgr`.

---

## 0. Changes

### v0.2 → v0.3

| # | Change | Section |
|---|--------|---------|
| 1 | Root-signed **trust manifest** so agents learn about revoked intermediates | §4.3 |
| 2 | Long-lived identity key under short-lived certificates, plus an **expired-certificate recovery** flow | §6.4 |
| 3 | **Durable firewall transaction journal** and a boot-time policy loader so pending transactions survive reboots; no reboot while a transaction is pending | §17 |
| 4 | Local audit is **not trustworthy after full controller compromise**. Hash chaining and an optional remote sink are planned for 1.0 | §4.2, §22.6 |
| 5 | **Address-set propagation generations** with visible convergence status | §5.3, §15.4 |
| 6 | Resolved the former open questions: expiry, `internet`, Beta coexistence matrix, root CA, timer, presets | §5.3, §15.3, §16.4, §4.3, §17, §6.7 |

### v0.1 → v0.2

v0.1 was mostly a feature list. v0.2 adds the safety and trust architecture that the implementation will depend on. Main changes:

| # | Change | Section |
|---|--------|---------|
| 1 | Firewall changes become a transaction that the managed host owns, with a local dead-man rollback timer and confirmation over a **new** connection | §17 |
| 2 | Rules for coexisting with other firewall managers. We never modify rules we don't own, and we refuse to enforce when rule precedence can't be determined | §16 |
| 3 | Identity-to-address mapping becomes a continuous observation stream. Firewall rules reference nftables sets, so address changes are small set updates | §5, §15 |
| 4 | Documented limitations for SSH-only hosts with dynamic addresses | §5.4 |
| 5 | New threat model that treats the controller as the fleet's root of trust, plus the L2 address-spoofing limitation | §4 |
| 6 | Agent capabilities have a local ceiling. The controller can only narrow them | §6.7 |
| 7 | Network learning mode requires an agent. SSH hosts get snapshots only | §19 |
| 8 | Release plan rebuilt as Technical Alpha → Alpha 2 → Beta → 1.0 | §33 |
| 9 | Target audience: self-hosters, advanced homelabs and small organizations | §1.1 |
| 10 | Canonical typed schema. The UI and YAML both edit it. No arbitrary YAML round-tripping | §12.7 |
| 11 | One operation engine underneath both imperative actions and desired-state reconciliation | §12.1 |
| 12 | State versioning with `{controller_epoch, revision, content_hash}` and stale-command rejection | §9 |
| 13 | Defined semantics for controller restore and reconciliation | §10 |
| 14 | PKI with an offline root CA and an online intermediate CA on the controller | §4.3 |
| 15 | Protocols: gRPC/protobuf over mTLS for agents and plugins, REST/JSON for the public API | §25 |
| 16 | Tech stack chosen: Go, React/TypeScript, SQLite first | §30 |

---

## 1. Overview

A web-first platform for managing, automating, monitoring and segmenting Linux fleets.

It's an alternative to tools such as Ansible for people who want to manage Linux systems mainly through a web interface, without building and maintaining inventories, roles, playbooks and command-line automation.

Two management models, both first class:

1. Agent-based
2. Agentless (SSH)

The platform includes a declarative network policy engine for host firewall segmentation and container network security. A plugin architecture lets applications (Arr stack, Jellyfin, Proxmox, Dockhand, Caddy, Home Assistant, ...) integrate without the core growing indefinitely.

### 1.1 Target audience

**Primary:** self-hosters, advanced homelabs, enthusiasts and small organizations (roughly 5–200 hosts).

The core is still built correctly enough for businesses to use. RBAC, audit, canaries, jump hosts and maintenance windows are **safety features**, not enterprise features. They matter just as much to someone managing 20 machines at home.

**Deferred enterprise features:** SAML, SCIM, four-eyes approval, cryptographic operator signatures, multi-controller HA, hardware-backed admin identities, compliance reporting.

---

## 2. Design Principles

### 2.1 Web first

The web UI is the primary interface. Normal administration (adding hosts, updates, scripts, services, files, users, firewall policy, workflows, schedules, logs, monitoring, plugins) needs no configuration files.

### 2.2 One canonical model

```
Web UI ─┐
YAML  ──┼──→  Canonical Typed Model  →  Operation Engine
CLI   ──┤
API   ──┘
```

Every interface reads and writes the same typed schema. YAML is a serialization of that schema, not a separate language (§12.7).

### 2.3 Intent over mechanism

> The user declares intent. The platform determines how to safely achieve and continuously verify that intent.

### 2.4 Fail safe

When the platform cannot prove an action is safe, such as firewall precedence, an identity conflict or a stale command, it refuses and explains why rather than guessing.

---

## 3. Core Product Areas

```
Fleet Manager
├── Inventory & Identity
├── Monitoring
├── Automation (Operation Engine)
├── Package Management
├── Configuration Management / Desired State
├── Network Segmentation
├── Container Security
├── Security & Compliance
├── Logging & Auditing
├── Scheduling
└── Plugins
```

---

## 4. Threat Model and Trust Architecture

### 4.1 Assumptions

The system assumes that any of the following may happen:

- Agents may be compromised
- Managed hosts may be compromised
- Plugins may be buggy or malicious
- Network traffic may be intercepted
- Devices on the same L2 segment may spoof addresses
- Administrators may make mistakes
- **The controller itself may be compromised**

### 4.2 The controller is the fleet's root of trust

The controller has the ability to run privileged operations on every host, holds the online intermediate CA and every secret. It is the most valuable target in the system. Mitigations:

| Mitigation | Purpose |
|---|---|
| Agent local capability ceiling (§6.7) | A compromised controller cannot do what a host never allowed |
| Offline root CA (§4.3) | Controller compromise is recoverable without re-trusting the whole fleet |
| Key separation (§4.4) | One leaked key (e.g. from a backup) doesn't give away everything |
| MFA support | Protects admin accounts |
| Re-authentication for destructive actions | Limits damage from a hijacked session |
| Session expiry, rate limits | Standard hardening |
| Secrets encrypted at rest | Limits damage from a database or backup leak |
| Append-oriented audit log | Detection and forensics (see limitation below) |
| Agent-side operation log | Independent record on each host of what it was asked to do |

**Audit limitation:** after full controller compromise, the attacker controls the database and can rewrite local audit history. Local audit is reliable for honest mistakes and partial compromise, not for full controller compromise. The 1.0 mitigations are hash-chained audit records (tampering becomes detectable) and an optional remote append-only sink (syslog, webhook, external store). Agents also keep their own local log of received operations, which the controller cannot rewrite.

**Deferred high-security mode:** jobs must carry an operator signature in addition to controller authorization before an agent accepts them.

### 4.3 PKI

```
Offline Root CA            (kept offline / backed up securely by the admin)
       │
       ▼
Controller Intermediate CA (online, held by controller)
       │
       ▼
Agent certificates         (short lived, auto-rotated)
```

- Agents pin the **root**, not the intermediate.
- The controller can restart unattended, e.g. after a power outage. No passphrase is needed at startup.
- First-run setup generates the root, signs the intermediate, and **strongly encourages** exporting the root key and removing it from the controller. It's not required. Keeping the root online shows a **persistent security warning** in the UI.

#### 4.3.1 Trust manifest

Chaining to the root isn't enough. An attacker holding a revoked intermediate could keep issuing certificates that chain to the root. Agents therefore also require that the intermediate appears in a **root-signed trust manifest**:

```yaml
trust_manifest:
  version: 4                       # monotonic
  issued_at: 2026-10-06T00:00:00Z
  trusted_intermediates:   [sha256:NEW...]
  revoked_intermediates:   [sha256:OLD...]
  signature: <signed by offline root>
```

- An agent accepts a controller or peer certificate only if it chains to the root **and** its intermediate is in `trusted_intermediates` of the newest manifest the agent has accepted.
- Agents store the highest manifest version they've accepted and **reject older manifests** (no rollback).
- Any controller can *distribute* a manifest, but only the root can *create* one. A compromised controller can't forge one to un-revoke itself.
- The first manifest is delivered at enrollment.

**Intermediate compromise recovery:** bring the root online briefly → sign the new intermediate → sign manifest N+1 (new trusted, old revoked) → the new controller distributes it → agents switch over. No re-enrollment.

Limitation: an agent that's offline during recovery still trusts the old intermediate until it receives manifest N+1, so reconnecting agents should get the newest manifest before anything else.

### 4.4 Key separation

```
Controller TLS server key  ≠  Job/policy signing key  ≠  Intermediate CA key
```

If all three live on one host, a full compromise still exposes them all. Separation protects against **partial** exposure (a backup, a log, one misconfigured file). It is not counted as protection against full controller compromise.

### 4.5 L2 limitation (explicit)

> Host identity is cryptographic, but traditional firewall enforcement ultimately operates on network addresses. On networks where another system can impersonate an authorized address, IP-based segmentation does not provide cryptographic identity enforcement.

Future direction: a WireGuard overlay backend where the peer public key is the identity (§15.6).

### 4.6 Security rules

- Never trust an IP as identity
- Authenticate every agent (mTLS)
- Pin SSH host keys
- Least privilege everywhere (capabilities, RBAC, plugin permissions)
- Plugins run out of process
- Secrets are used through a broker, not read
- Audit every privileged action
- Validate destructive changes and support rollback
- Reject stale or replayed commands (§9)

---

## 5. Host Identity and Addresses

### 5.1 Identity / Location / Management Path

```
Identity          WHO is this system?      Cryptographic (agent key or pinned SSH host key)
Location          WHERE is it?             Set of observed addresses (changes)
Management Path   HOW do we reach it?      Agent connection / SSH via jump hosts
```

```yaml
host:
  id: host_12ndf9fdfa31
  name: web01
  addresses:              # observed, many, changing
    - 10.20.10.51
    - fd00:20:10::51
  management:
    method: agent
```

### 5.2 Address observation stream

Address change is **continuous**, not an occasional DHCP event. Sources of churn:

- DHCP lease changes
- IPv6 SLAAC and temporary/privacy addresses (rotate on their own)
- VPN interfaces coming and going
- Docker containers recreated with new IPs
- Multiple interfaces

```
Identity
   ↓
Address Observation Stream   (agent reports, SSH discovery, plugins)
   ↓
Validated Address Set        (authenticated source only)
   ↓
Policy Sets                  (nftables set deltas on peers, §15)
```

The subsystem must handle a high rate of changes efficiently. Policy prefers stable addresses or prefixes where possible. For IPv6, policy can match the host's stable address and/or its delegated prefix rather than chasing temporary addresses.

### 5.3 Address changes are security-sensitive

```
Agent reports address change (over authenticated mTLS session)
        ↓
Controller verifies agent identity
        ↓
For each peer: one atomic nft transaction
    delete old element(s), add new element(s)
        ↓
Host record updated
```

Removing the old address takes priority over granting the new one. **Residual risk:** between the host leaving an address and peers learning about it, the old address keeps its permissions. If DHCP hands `10.10.2.10` to another device during that window, the device inherits the database's permissions. Mitigations: agents report interface events immediately; stable addresses or DHCP reservations are recommended for security-sensitive identities.

**Strict expiry (optional, off by default):** a group or policy can enable timeouts on set elements, so addresses that aren't refreshed drop out of peer sets. The UI warns that a long controller outage will then eventually **fail closed** for those relationships.

### 5.3.1 Propagation is per-peer atomic, not fleet-wide atomic

Each peer's update is one atomic nft transaction, but 40 peers means 40 transactions, and the controller may crash part-way through. Address sets are therefore a versioned domain (§9) with a **desired membership generation**:

```
database group membership generation: 82

peer01: 82 ✓
peer02: 82 ✓
peer03: 81 ⚠ stale
```

- The controller continuously converges peers to the desired generation, including after restart and when peers reconnect.
- Until convergence, the UI says so explicitly: **"Policy propagation incomplete: 17/40 peers current, 23 stale."**
- Stale peers are surfaced in Explain Host and policy simulation results.

### 5.4 SSH-only hosts with dynamic addresses

If an SSH-only host changes address and nothing reports it, **the platform cannot know where it went.**

SSH hosts that take part in identity-based firewall relationships must use one of:

- Static IP
- DHCP reservation
- Stable DNS name (resolved address verified by SSH host key)
- DHCP-integration plugin (e.g. router/Pi-hole/Kea lease import)
- Network discovery integration
- Manual address update

Whenever a new address is found for an SSH host, the **pinned host key is verified** before accepting that it's the same host.

---

## 6. Agent Architecture

### 6.1 Enrollment

```
Agent starts
  ↓ generates local keypair (private key never leaves host)
  ↓ presents single-use enrollment token
  ↓ verifies controller (pinned root CA, shipped with installer/token)
  ↓ submits public key + local capability ceiling
Controller creates host identity, issues certificate from intermediate CA
  ↓
Enrollment token invalidated
```

Tokens are random, short lived, single use, revocable, and optionally limited to a host group.

### 6.2 Mutual authentication and transport

- mTLS in both directions.
- **gRPC with bidirectional streaming** over mTLS. The agent connects **outbound** to the controller. No inbound management port by default (works through NAT, firewalls, VLANs, remote sites, VPNs).
- One protobuf schema covers registration, telemetry, commands, results, policy deployment, heartbeat, certificate renewal, address changes and agent updates.

### 6.3 Identity conflicts

Cloned VMs duplicate agent identities. When two sessions present the same identity at the same time:

```
IDENTITY CONFLICT
Host: web01
Session A: 10.10.10.20
Session B: 10.20.30.40
Management operations quarantined until resolved.
```

Default: quarantine (configurable). Limitation: detection only works while both clones are online. Future: TPM-backed keys.

### 6.4 Certificate rotation

Two layers:

```
Identity key     long-lived keypair generated at enrollment; private key never leaves the host
     ↓
Operational cert short-lived (default 30 days) certificate over that key; auto-renewed at ~2/3 lifetime
```

Admins can revoke an identity immediately, which invalidates all of its certificates.

**Expired-certificate recovery:** if the controller is offline long enough for a certificate to expire, a long outage must not unenroll the fleet.

```
Agent connects to restricted recovery endpoint (renewal RPC only)
        ↓
Presents expired cert + proof of possession of its identity key (signed challenge)
        ↓
Controller checks: identity known, not revoked, not in conflict, within grace period
        ↓
New operational certificate issued; event audited
```

- Grace period is configurable (default 180 days). Past it, the host must re-enroll.
- The recovery endpoint accepts nothing except renewal.

### 6.5 Controller endpoints and migration

Agents connect by DNS name and may hold several endpoints. Migration: controller sends a candidate endpoint → agent stores it as a candidate → agent tests it → agent confirms → candidate becomes primary. **A configuration change must never strand the fleet.**

### 6.6 Agent and controller outages

If the controller is unreachable, agents keep running with their **last committed state** indefinitely. No policy rollback, no fail-open, no reset to defaults (except during a pending firewall transaction, §17).

### 6.7 Capability ceiling

```
Local capability ceiling     (root-owned file on host; changed only by local action)
        ∩
Controller-requested caps    (may narrow, never widen)
        =
Effective capabilities
```

```yaml
# /etc/fleetmgr/agent.yaml  (root:root 0600)
capabilities:
  shell: false

  packages:
    enabled: true
    configured_repositories_only: true   # no controller-supplied repos

  files:
    enabled: true
    allowed_paths:
      - /etc/nginx/
      - /etc/myapp/

  services:
    allowed: [nginx, myapp]

  firewall: true
  users: false
  monitoring: true
```

**What each capability really grants:** unrestricted `files` (it can write `/etc/sudoers` or systemd units) and unrestricted `packages` (postinst scripts run as root) are **effectively root**. They only become meaningful limits when constrained, as above. The UI must label unconstrained capabilities as "root-equivalent".

Installer presets:

| Capability | Monitor Only | Managed | Full |
|---|---|---|---|
| monitoring | ✓ | ✓ | ✓ |
| services | ✗ | ✓ | ✓ |
| reboot | ✗ | ✓ | ✓ |
| packages (configured repos only) | ✗ | ✓ | ✓ (unrestricted) |
| firewall | ✗ | ✓ | ✓ |
| files | ✗ | ✗ (no arbitrary writes) | ✓ (unrestricted) |
| users | ✗ | ✗ | ✓ |
| shell | ✗ | ✗ | ✓ |
| | | | **ROOT-EQUIVALENT** |

Individual capabilities (e.g. `files.allowed_paths`) can be enabled locally after installation.

---

## 7. SSH Management

- The controller verifies and **pins** SSH host keys. A changed key raises an alert and is never trusted automatically.
- Credentials come from the secret store. Sudo is supported.
- Native Go SSH implementation.
- Multiple jump hosts (`via: [jump01, jump02]`) are modeled in the data model. The Technical Alpha implements one hop.
- Connection reuse/multiplexing is allowed for normal operations but **disabled for firewall verification** (§17.2).
- Capability ceiling: SSH hosts have no local agent, so restrictions come from the SSH account's own privileges (e.g. limited sudoers).

---

## 8. Management Paths and Hybrid Management

Management paths (how the platform administers a host) are separate from network policy (what normal traffic is allowed).

```yaml
management:
  preferred: agent
  fallback: ssh
  ssh:
    via: [jump01]
```

Management paths generate **required connectivity** (`controller → jump01:22`, `jump01 → web01:22`) that the policy validator protects (§18).

Hybrid fallback: agent unavailable → try SSH → inspect the agent service → offer restart or recovery.

---

## 9. State Versioning and Stale-Command Protection

Every piece of managed state is versioned **per host, per domain** (firewall, files, baseline, agent-config, ...):

```yaml
state_version:
  domain: firewall
  controller_epoch: a84f3c...      # random, controller-generated
  revision: 417                    # monotonic within epoch, controller-owned
  content_hash: sha256:...
```

Every command carries:

```yaml
operation_id: op_7f2a...
state_version: {...}        # when applicable
issued_at: 2026-10-06T14:00:00Z
expires_at: 2026-10-06T14:10:00Z
```

Rules enforced by the agent (and by the SSH executor, using a state file on the host at `/var/lib/fleetmgr/state.json`):

- Reject any operation past `expires_at`.
- Reject any operation whose `operation_id` was already executed (replay or duplicate).
- Within the same epoch, reject a revision ≤ the currently applied revision ("stale configuration").
- A different epoch is never applied automatically. It requires reconciliation (§10).

**Agents report state. They never advance controller-owned revision counters.** A compromised agent claiming revision 2⁶⁴−2 can only cause a reconciliation alert, not corrupt the version space.

This protects against retries, delayed queues, duplicate messages, races and restored controller databases.

---

## 10. Controller Backup, Restore and Reconciliation

### 10.1 Backup contents

Database (inventory, identities, policies, workflows, plugin config, audit config), encrypted secrets, intermediate CA material. The root CA is backed up separately by the admin.

### 10.2 Restore never re-enrolls agents

Agents still trust the root and hold valid certificates.

### 10.3 Host ahead of restored controller

```
Agent:               epoch A, revision 417, hash XYZ
Restored controller: epoch A, revision 400, hash ABC

→ HOST IS AHEAD OF RESTORED STATE
→ Controller refuses to push revision 401
→ Domain marked "Reconciliation required"
```

Admin choices (per host/domain or in bulk):

- **Adopt host state:** the controller imports what the host is running as its desired state.
- **Restore controller state:** push the controller's version.
- **Review diff**

Either way, reconciliation starts a **new epoch** (`epoch B, revision 1`), so old-epoch messages can never apply again.

After restore, the controller enters **reconciliation mode** and pushes no state until each host has reported in and been compared.

---

## 11. Inventory, Groups and Monitoring

### 11.1 Discovery

On enrollment or first SSH contact: distro, family, version, kernel, arch, hostname, CPU, RAM, storage, interfaces, addresses, package manager, init system, services, listening ports, container runtime, agent version.

### 11.2 Tags and dynamic groups

Manual tags (`environment:production`, `role:database`, `location:homelab`) and dynamic queries that can also be automation targets:

```
os:ubuntu updates:>10
service:docker state:failed
agent-version:<1.5
```

### 11.3 Monitoring

CPU, memory, load, disk/filesystem, uptime, interfaces, throughput, service health, failed services, package/update status, reboot required, agent/SSH connectivity, listening ports.

Agents stream telemetry. SSH hosts are polled; the polling interval is configurable and the scheduler spreads polls out.

---

## 12. Automation: Operation Engine

### 12.1 One engine, two front ends

```
                 Operation Engine
           (locking, retries, batching, state)
                       ↑
          ┌────────────┴────────────┐
   Imperative Actions        State Reconciler
   "Restart nginx"                 ↑
                             Desired State
                         "nginx must be running"
```

Both produce the same typed **Operations** (`ServiceOperation`, `PackageOperation`, `FileOperation`, `FirewallOperation`, `ScriptOperation`, ...).

The reconciler: observe → compare desired with current → no difference means no operation → otherwise generate a corrective operation.

Operations are idempotent wherever the domain allows it (raw scripts are explicitly not).

### 12.2 Services

```yaml
service: { name: nginx, state: running, enabled: true }
```

### 12.3 Packages

Semantic operations: install, remove, upgrade, upgrade system, security updates only, refresh cache, check reboot required. Backends: APT, DNF, (YUM), Zypper, Pacman. **Technical Alpha: APT and DNF only.**

### 12.4 Bulk execution

Targets: hosts, groups, or dynamic queries. Strategy: batch size, concurrency, failure threshold, canary hosts, reboot handling, verification steps, maintenance windows.

```yaml
job:
  name: production-updates
  targets: { group: production }
  strategy: { canary: 1, batch_size: 5, failure_threshold: 2 }
  actions:
    - update_packages
    - reboot: { if_required: true }
    - wait_for_host
    - verify_services
```

Canary: if the canary fails, the rest of the job stops.

### 12.5 Commands and scripts

```yaml
script:
  name: check-storage
  targets: { group: production }
  shell: bash
  when: { os_family: debian }
  run: |
    df -h
    free -m
```

OS-specific sections (`run: { debian: ..., redhat: ... }`) are supported. Scripts require the `shell` capability.

### 12.6 Workflows

Reusable, ordered steps (check disk → update → reboot if required → wait → verify network/services → health check → next batch).

### 12.7 Canonical schema, not arbitrary YAML

```
      Canonical Typed Model
            ↓
     ┌──────┴──────┐
  UI Forms /     YAML
  Visual Editor  (serializer)
```

- Both are editors and serializers of the same versioned schema.
- YAML that doesn't fit the schema is rejected with **"Schema validation failed"** and a precise location.
- We do not preserve comments, anchors or hand formatting across round trips.

### 12.8 Job state

Per-host states: Queued, Connecting, Running, Waiting, Rebooting, Verifying, Succeeded, Failed, Timed Out, Cancelled, Skipped, Rolled Back, **Interrupted** (the controller restarted mid-operation and the outcome is unknown; never automatically retried for non-idempotent operations). A job summarizes host states (e.g. "Succeeded 81 · Failed 4 · Offline 3 · Skipped 2") instead of a single pass/fail.

### 12.9 Errors

Structured categories: Connection, Authentication, **HostKey** (pinned key mismatch; never retried; raises a security alert), Authorization, Timeout, Execution, Configuration, Validation, Policy, PackageManager, Service, AgentProtocol, Capability, StaleState, Plugin. The UI turns them into actionable explanations (e.g. "Package manager locked by unattended-upgrades. Retryable: yes").

### 12.10 Retries

Per operation: timeout, attempts, delay, backoff, retryable error classes. Authentication, authorization and capability errors are never retried. Transient connection errors may be.

### 12.11 Circuit breakers

Fleet-wide failure patterns pause the job, e.g. "17 consecutive SSH authentication failures. Likely a shared credential problem. Job paused."

### 12.12 Host locking

No lock, shared lock or exclusive lock per operation. Conflicting jobs queue.

### 12.13 Maintenance mode

Per host or group: suppresses alerts, pauses scheduled automation and auto-remediation, records who enabled it, and expires automatically.

---

## 13. File Deployment and Change Diffing

Managed file deployment with backup, validation command (e.g. `sshd -t`) and reload. Each deployment stores the previous version, new version, diff, result and validation result. Users can preview, apply and roll back.

Restricted by the agent's `files.allowed_paths`.

---

## 14. Desired State, Compliance and Drift

Baselines (e.g. SSH root login disabled, required services, prohibited packages) are evaluated by the reconciler, with results of *Compliant* or *Drift Detected*.

Drift actions: **Accept current state** (becomes desired), **Restore managed state**, **Review difference**.

---

## 15. Network Policy Engine

### 15.1 Intent

```yaml
groups:
  web:      { members: [web01, web02] }
  database: { members: [database01] }
  jump:     { members: [jump01] }

policy:
  - name: web-database
    from: web
    to: database
    allow: [postgres]

  - name: management
    from: jump
    to: [web, database]
    allow: [ssh]
```

### 15.2 Named services

Built-in: ssh, dns, http, https, ntp, postgres, mysql, redis, smtp, smb, nfs, ... Custom services:

```yaml
services:
  jellyfin: { protocol: tcp, port: 8096 }
```

### 15.3 Special targets

- `internet`: the **inverse of `internal_ranges`**. `internal_ranges` is seeded with defaults (RFC1918, 100.64.0.0/10 CGNAT/Tailscale, IPv6 ULA, link-local, loopback) and fully editable, which covers routed LANs, VLANs and VPNs.
- DNS as a semantic service: compiles to UDP **and** TCP 53.

### 15.4 Compilation via sets

```
Policy Intent → Internal Policy Model → Firewall Compiler → nftables
```

Rules reference **named nftables sets** that hold group member addresses (IPv4 and IPv6):

```
set web_hosts_v4      { type ipv4_addr; elements = { 10.10.1.20, 10.10.1.21 } }
set database_hosts_v4 { type ipv4_addr; elements = { 10.10.2.10 } }
```

- **Policy change** → recompile and apply rules via a firewall transaction (§17).
- **Address change** → atomic set-element delta on affected peers. No rule recompilation.

Set-element updates go through generations (§9, §5.3.1) but don't need the full dead-man transaction unless they touch a management path.

### 15.5 Backend independence

The internal model doesn't depend on nftables syntax. nftables is the first backend.

### 15.6 Future: WireGuard identity backend

```
Normal LAN:      Identity → IP → nftables set
Secure overlay:  Identity → WireGuard public key → encrypted peer
```

This gives cryptographic enforcement and addresses the L2 limitation (§4.5).

---

## 16. Firewall Coexistence Rules

### 16.1 Never modify rules we don't own

We never edit, flush or reorder tables, chains or rules owned by Docker, firewalld, UFW, Tailscale, libvirt, Kubernetes CNIs or any other manager. We never run `nft flush ruleset`.

### 16.2 Own tables only

```
Detect host networking environment
        ↓
Select enforcement strategy
        ↓
Create fleetmgr-owned tables / base chains
        ↓
Attach at appropriate hooks and priorities
```

### 16.3 Docker specifics

- Docker can use an iptables backend or an (experimental) nftables backend.
- With the nftables backend there is **no `DOCKER-USER` chain**. Docker documents that custom rules belong in separate tables/base chains and that its own tables must not be modified.
- Traffic to published container ports goes through forward/NAT paths and does not pass the host's INPUT expectations. This is the well-known "UFW doesn't block my container" problem.
- The container enforcement strategy must therefore be backend-aware (§20).

### 16.4 Refuse when precedence is unknown

```
Firewall enforcement unavailable

Detected:
  Docker nftables backend
  firewalld
  Custom forward chains

Reason:
  Unable to safely determine rule precedence.

Monitoring remains available.
```

The detected environment is shown on the host page.

### 16.5 Compatibility matrix

The matrix is expanded deliberately, one explicitly tested strategy at a time.

| Environment | First Beta |
|---|---|
| Plain nftables, no other firewall manager | **Supported** |
| Docker (iptables backend) | Refuse. **First expansion target**, since most of our audience runs Docker |
| Docker (nftables backend) | Refuse |
| firewalld | Refuse |
| UFW (active) | Refuse |
| Tailscale | Refuse |
| Legacy iptables rules present | Refuse |

Monitoring and policy simulation work in every environment. Only enforcement is gated.

---

## 17. Firewall Transaction Protocol

The **managed host owns the transaction**, because the controller is exactly the thing that may become unreachable. The transaction must survive four failure cases: the controller vanishing, the network path breaking, the agent process crashing, and **the host rebooting**.

### 17.0 On-host state

```
/var/lib/fleetmgr/firewall/
  committed.nft               # last committed fleetmgr-owned ruleset
  candidate.nft               # pending candidate (only during a transaction)
  transaction.json            # durable journal
```

```json
{
  "transaction_id": "fwtx_01J...",
  "state": "pending",
  "previous_hash": "sha256:abc...",
  "candidate_hash": "sha256:def...",
  "state_version": { "domain": "firewall", "controller_epoch": "a84f...", "revision": 418 },
  "deadline": "2026-10-06T14:02:00Z"
}
```

Files are written with write-temp → fsync → rename → fsync directory.

**Boot loader:** a small `fleetmgr-firewall.service` unit, ordered before `network-pre.target`, loads policy at boot:

- If no journal exists, or the journal is `committed`: load `committed.nft`.
- If the journal is `pending`: load `committed.nft` (never the candidate), mark the journal `rolled_back`, and report it on the next connection.

We never write to the distro's `/etc/nftables.conf`. Our rules persist only through our own unit. SSH hosts get the same unit and files installed when firewall management is first enabled.

### 17.1 Agent hosts

```
Agent receives candidate policy (with state_version)
        ↓
Validate (schema, version, capability, nft -c -f candidate.nft)
        ↓
Ensure committed.nft reflects the current live fleetmgr rules
        ↓
Write journal: state=pending (durable)          ← BEFORE touching live rules
        ↓
Arm LOCAL rollback timer (systemd-run --on-active=120s)
        ↓
Apply candidate (single atomic nft -f transaction)
        ↓
Terminate existing management connection
        ↓
Establish completely NEW mTLS connection
        ↓
Report transaction_id + candidate revision + hash
        ↓
Controller verifies and sends COMMIT
        ↓
candidate.nft → committed.nft; journal state=committed (durable)
        ↓
Cancel rollback timer
```

The journal is written **before** the live rules change. If the order were apply-then-journal, a crash between the two steps would leave an applied candidate with no record that it's pending.

No COMMIT before the deadline (for any reason: rules block the path, controller crashed or lost power, network failure, agent crashed, host rebooted) → **the previous policy is restored**: by the timer while the host is up, or by the boot loader after a reboot. The rollback is reported on reconnect.

**Why a new connection:** rulesets almost always allow `ct state established,related`, so an existing session can survive rules that would block a new one. Verifying over the old session proves nothing.

**Why a systemd timer:** it survives the agent process crashing. The rollback action is idempotent (load committed, mark rolled back), so the timer, the agent and the boot loader can all safely race to perform it.

### 17.2 SSH hosts

```
Preflight: fresh-path connectivity test (see 17.4)
        ↓
Upload candidate.nft; verify committed.nft matches live fleetmgr rules
        ↓
Write journal: state=pending (durable)
        ↓
Arm local rollback: systemd-run --on-active=120s fleetmgr-fw-rollback
        ↓
Apply candidate atomically
        ↓
Open a BRAND-NEW SSH connection through the full jump chain
   (no ControlMaster, no multiplexing, no reused transport)
        ↓
Success → promote candidate, journal=committed, cancel timer
Failure → do nothing; the timer (or boot loader) restores committed.nft
```

`fleetmgr-fw-rollback` is a small root-owned script installed with the boot unit. Hosts without systemd are not supported for firewall enforcement in the Beta.

### 17.3 Locking and reboots

- A firewall transaction holds an **exclusive lock on the host's firewall domain and a reboot-blocking lock** on the host.
- Controller-initiated reboots, and jobs that may restart networking, are **refused while a transaction is pending**.
- A reboot from outside the platform during the window is handled by the boot loader (§17.0).

### 17.4 Timer duration

- Default **120 seconds**, configurable per host or group. No automatic adjustment.
- **Preflight:** before arming the timer, the controller measures a fresh-path connection (a new mTLS session, or a new SSH session through the full jump chain). If the configured timer is clearly too short (e.g. less than 3× the measured time), the transaction is rejected with a warning. Admins with `firewall.write` can override.

### 17.5 After commit

Controller disappears? **Nothing happens.** The last committed policy stays in place indefinitely and is reloaded at every boot.

---

## 18. Management Path Protection

The validator computes required connectivity from every host's management path (agent → controller endpoint(s); controller → jump → target over SSH). A policy that would remove it is rejected:

```
Policy would remove the configured management path for web01.
```

Authorized admins may use an explicit emergency override (audited). Even with the override, the dead-man transaction (§17) still applies.

---

## 19. Network Learning, Simulation and Explain

### 19.1 Learning mode: agent required

| Host type | Capability |
|---|---|
| Agent | Full continuous learning (conntrack / eBPF) |
| SSH | **Current connection snapshot only** (`ss`, conntrack dump). Not labeled "learning" |

Modes: **Monitor** (observe), **Guided** (observe, suggest, admin approves), **Enforce** (apply, detect drift). Observations are aggregated (e.g. "web01 → database01:5432, 12,482 connections"). Unclassified flows are highlighted.

### 19.2 Policy simulation

"Can web01 reach database01 on TCP 5432?" → YES/NO with the matching policy chain, or the configured management path.

### 19.3 Explain Host

For each host: inbound and outbound allowances, each with its reason (policy name, management path, DNS policy), plus explicit denies.

---

## 20. Container Integration

### 20.1 Containers in the policy model

Containers can be group members alongside hosts:

```yaml
groups:
  reverse-proxy: { containers: [caddy] }
  apps:          { containers: [paperless] }
  databases:     { containers: [postgres] }
```

Container addresses feed the same address observation stream (§5.2). Recreating a container is a normal set delta.

### 20.2 Topology preference

Prefer network design that limits connectivity: `proxy_net` (Caddy ↔ app), `backend_net` (app ↔ Postgres). Caddy isn't on `backend_net` and Postgres isn't on `proxy_net`. The platform recommends topology changes. Applying them is subject to ownership rules (§20.5).

### 20.3 Exposure auditing

Published ports are classified as Expected, Review or Unexpected Exposure (e.g. 5432 → Postgres), with concrete recommendations.

### 20.4 Providers

Native Docker API, Dockhand (optional, where its API supports it; never required), future Podman.

### 20.5 Resource ownership

Each resource records its owner (e.g. Compose stack: Dockhand; firewall policy: Fleet Manager). Changes to resources owned by another tool are shown as proposals and never applied silently.

---

## 21. Plugins

- **Core owns:** hosts, agents, SSH, jobs, secrets, RBAC, scheduling, networking, firewall, audit, logging, notifications. Plugins use these and never reimplement them.
- **Isolation:** separate processes communicating with the core over gRPC/protobuf. A plugin crash never crashes the controller. No in-process loading.
- **Topology:** plugins may *describe* relationships (Sonarr → Prowlarr TCP/9696). The core decides enforcement. Plugins never touch firewall rules.
- **Permissions:** declared in the manifest (`hosts.read`, `jobs.execute`, `secrets.use`, `network.inspect`, ...). Sensitive permissions (`hosts.execute_root`, `files.write`, `firewall.write`, `secrets.read`) are highlighted at install time.
- **Secret broker:** plugins ask the core to make calls using a named credential. They never receive raw secrets.
- **UI:** structured components (status cards, tables, forms) first. Isolated frames later. No arbitrary JS injection.
- **API compatibility:** plugins declare a version range (e.g. `>=1.3 <2.0`) and use only the documented plugin API.
- **Trust levels:** Official, Verified, Community, Unsigned. Unsigned plugins require explicit confirmation.
- **Storage:** namespaced data (`/data/plugins/<id>/`), logs (`plugin.<id>.log`) and audit events (`plugin.<id>.*`).

Example Arr plugin capabilities: health, version, updates, queue status, indexer status, API connectivity, config backup, service restart, dependency mapping, policy suggestions.

---

## 22. Configuration, Logging and Operations

### 22.1 Configuration precedence

Built-in defaults → config file → environment variables → runtime/UI settings. The UI shows where each value came from.

### 22.2 Ports

Nothing hard-coded. Configurable listeners: web/API (default 8080), agent gRPC (9443), metrics (9090).

### 22.3 Logging

Structured JSON. Separate streams: application, agent, job, audit, security, plugin. Built-in rotation (size, count, compression, age). Stdout mode for container deployments.

### 22.4 Job output limits

Configurable per-host cap (default 10 MB). Output past the cap is truncated with a marker.

### 22.5 Secret redaction

Passwords, API keys, tokens, auth headers, private keys and enrollment tokens are always redacted. Users can add their own patterns.

### 22.6 Audit

Append-oriented record of who, what, when, where and result for logins, failed auth, enrollment, revocation, certificate recovery, host deletion, jobs, firewall changes, capability changes, plugin installs and permissions, credential, user and config changes, reconciliation decisions and break-glass use.

Integrity (see §4.2 limitation):
- **Technical Alpha:** append-only table, with no update or delete through the application.
- **1.0:** hash-chained records (each record includes the hash of the previous one) plus an optional remote sink (syslog, webhook). Verifying the chain is part of diagnostics.

### 22.7 Health and diagnostics

`/health`, `/ready`, `/metrics`. The support bundle contains version, sanitized config, recent logs, schema version, agent version summary, failed job metadata and plugin inventory, with secrets removed.

### 22.8 Event timeline

The dashboard shows an event stream where every event links to its object.

---

## 23. Dependency Awareness

Users and plugins may declare dependencies (`web requires database, dns, redis`). The platform warns before operations that would take down every provider of a dependency at once. It feeds future availability-aware rolling updates.

---

## 24. Secrets, RBAC and Controller Authentication

- **Secrets:** SSH keys, sudo credentials, API credentials, plugin credentials, proxy credentials, CA material. Encrypted at rest. Permissions: use / view / modify / delete. Most automation needs only *use*.
- **RBAC:** roles (Administrator, Operator, Auditor, Viewer) built from capability permissions (`hosts.read`, `hosts.execute`, `jobs.create`, `firewall.write`, `credentials.use`, `plugins.install`, `audit.read`, ...).
- **Authentication:** local accounts, MFA (TOTP/WebAuthn), re-auth for destructive actions, session expiry, login rate limiting.

---

## 25. APIs and Protocols

```
Browser / external integrations  →  REST/JSON  (/api/v1/...)
Agents                           →  gRPC/protobuf over mTLS (bidi streaming)
Plugins                          →  gRPC/protobuf (local process boundary)
```

- The web UI uses the same public REST API. No privileged internal shortcuts.
- Raw gRPC is not exposed as the public API.
- Groups: hosts, groups, jobs, workflows, policies, plugins, events, secrets, users.
- OpenAPI spec generated from or checked against the canonical schema.

---

## 26. Scheduling

Immediate, one-time, recurring, or within maintenance windows.

---

## 27. Break-Glass Recovery

Temporary firewall rollback, console-initiated agent reset, emergency admin role, SSH fallback, recovery token, restoring the previous firewall policy. Every use is audited.

---

## 28. Agent Updates and Compatibility

Controller-managed rollout (canary → 10% → 50% → rest). The previous version is kept for rollback. The compatibility matrix flags unsupported agent/controller combinations.

---

## 29. IPv6 and Proxies

- IPv6 is first class in identity, addresses, policy and compilation (dual `ipv4_addr`/`ipv6_addr` sets).
- HTTP/HTTPS proxy support for agents and package operations. Credentials come from the secret store.

---

## 30. Technology Stack

| Component | Choice |
|---|---|
| Controller backend | Go |
| Agent | Go (single static binary) |
| Frontend | React + TypeScript |
| Database | SQLite (WAL) first; PostgreSQL optional later |
| Agent protocol | gRPC + protobuf, mTLS, bidirectional streaming |
| Public API | REST/JSON + OpenAPI |
| SSH | Native Go implementation (`golang.org/x/crypto/ssh`) |
| Firewall | nftables |
| Plugins | Separate processes over gRPC |

Using Go on both sides shares models and protocol code and gives easy static deployments.

---

## 31. Core Data Objects

```
Host, HostIdentity, HostAddress, AddressObservation, ManagementPath, HostGroup, Tag
Agent, AgentCapabilityCeiling, SSHHostKey, SSHCredential
StateVersion (domain, epoch, revision, hash)
Operation, Job, JobTarget, JobExecution, JobResult, Workflow, DesiredState
Service, Package, ManagedFile
NetworkPolicy, NetworkGroup, NamedService, PolicySet, FirewallTransaction, ObservedConnection
Container, ContainerNetwork, ContainerProvider, ResourceOwnership
Plugin, PluginPermission, PluginInstance
Secret, User, Role, Session
AuditEvent, SystemEvent
```

---

## 32. Architecture

```
                          Browser
                             │ REST/JSON
                             ▼
                      Web UI / Public API
                             │
       ┌──────────────┬──────┴───────┬───────────────┐
       ▼              ▼              ▼               ▼
   Inventory    Operation Engine  Policy Engine   Secret Broker
   & Identity   (jobs, reconciler)    │
       │              │               ▼
       │              │        Firewall Compiler
       ▼              ▼         (rules + sets)
   Monitoring    Execution Layer
                      │
          ┌───────────┴───────────┐
          ▼ gRPC/mTLS             ▼ SSH (+jump)
        Agent                   SSH Executor
          │                       │
          ▼                       ▼
        Hosts                   Hosts

                Plugin Runtime (separate processes, gRPC)
                ├── Arr      ├── Proxmox      └── Dockhand
```

---

## 33. Release Plan

### Technical Alpha: SSH only
- SSH management, pinned host keys, single jump host
- Host inventory, tags, static groups
- Run command / run script with per-host results (stdout/stderr, exit code, timeouts, retries)
- APT + DNF updates, package install/remove
- systemd service operations, reboot
- Operation engine with locking and job history
- Audit log, structured logging
- SQLite, REST API, basic web UI, local auth

### Alpha 2: Agent
- Agent, enrollment, PKI (offline root + intermediate + trust manifest), mTLS gRPC
- Permanent cryptographic identity, identity conflict detection, cert rotation, expired-cert recovery
- Agent telemetry and monitoring
- Agent → SSH fallback
- Local capability ceiling
- State versioning (§9)

### Beta: The differentiator
- Host firewall policies, named services, host groups
- nftables set-based compilation, address observation stream, propagation generations
- Coexistence detection and refusal (§16)
- Management-path awareness and protection
- Local dead-man firewall transactions with durable journal and boot loader (agent + SSH)
- Policy simulation, dry run, drift detection
- Controller backup/restore with reconciliation

### 1.0
Beta plus hardening, scheduling, batch/canary updates, file deployment with diffs, MFA, hash-chained audit with remote sink. **Firewalling ships before 1.0.** Without it we're competing directly with Semaphore/Rundeck without our main differentiator.

### Later
Desired-state baselines, workflows UI, dynamic groups, maintenance windows, agent self-update, network visualization, Docker discovery/segmentation/exposure audit, Dockhand, learning mode, Explain Host, dependency awareness, plugin ecosystem (Arr, Jellyfin, Proxmox, Caddy, Tailscale, WireGuard, Syncthing, ZFS), WireGuard identity backend.

---

## 34. Explicitly Out of Scope (initially)

Kubernetes replacement, SIEM, log analytics, vulnerability scanner, full configuration management language, Ansible compatibility layer, general endpoint management, Windows. Also deferred: the enterprise identity features listed in §1.1.

---

## 35. Positioning

Not "Ansible with a web UI." Instead:

> A web-first Linux fleet operations platform that understands not only how to manage systems, but how those systems are supposed to communicate, and that changes them safely.

**Prior art:** Salt (agent + SSH), Rundeck / Semaphore / AWX (web jobs), Cockpit (single-host web admin), Foreman (inventory/provisioning). None of them combine intent-based segmentation, management-path awareness and safe firewall transactions.

**Differentiators:** web-first automation · agent or agentless · cryptographic host identity · management topology · network intent · safe firewall automation · unified host and container segmentation · plugins · explainability.

---

## 36. Resolved Questions (v0.3) and Remaining

| Question | Decision | Section |
|---|---|---|
| Dynamic-address expiry | Off by default. Optional per-group/policy "strict expiry" with a fail-closed warning | §5.3 |
| Definition of `internet` | Inverse of editable `internal_ranges`, seeded with sensible defaults | §15.3 |
| Beta coexistence matrix | Plain nftables only. Refuse everything else; Docker (iptables backend) is the first expansion | §16.5 |
| Root CA on single-box installs | Strongly encourage export-and-remove; not required; persistent warning while online | §4.3 |
| Dead-man timer | 120 s default, configurable, no auto-adjust, preflight rejects obviously short timers | §17.4 |
| Agent presets | Monitor Only / Managed / Full (root-equivalent) | §6.7 |

**Remaining:** product name.

---

## 37. Key Architectural Decisions

1. Host IP addresses are attributes, not identities. Addresses are a continuous observed stream.
2. Agents use mutual cryptographic authentication under an offline-root PKI. Revocation of intermediates travels in a root-signed trust manifest.
3. SSH and agents are both first-class management methods.
4. Management paths are modeled explicitly and protected.
5. Network policy represents intent and compiles to rules plus address sets.
6. **The managed host owns firewall transactions.** Rollback is local and durable across reboots, and confirmation uses a new connection.
7. **We never modify firewall state we don't own**, and we refuse to enforce when precedence is uncertain.
8. **The controller can only narrow agent authority**, never widen it beyond the local ceiling.
9. **All managed state is versioned by controller-owned `{epoch, revision, hash}`.** Stale, replayed and expired commands are rejected.
10. A restored controller never overwrites newer host state without explicit reconciliation.
11. One operation engine serves both imperative actions and desired-state reconciliation.
12. One canonical typed schema. UI, YAML, CLI and API are all editors of it.
13. Destructive changes are validated and recoverable.
14. Plugins run out of process with narrowly scoped capabilities and brokered secrets.
15. The core manages Linux infrastructure. Plugins understand applications.
16. Distributed updates are never assumed to be atomic fleet-wide. Convergence is tracked and visible.
17. Local audit is not evidence after full controller compromise. Integrity comes from chaining and off-box sinks.

---

## 38. Next Step

Stop adding features. The next document is **ARCHITECTURE-ALPHA.md**: the concrete internal architecture and data model for the SSH-only Technical Alpha (Go package layout, SQLite schema, operation/job model, SSH executor, REST API surface, UI pages).
