# DROOL: Internal Architecture (implementation record, Technical Alpha → 1.0)

**v0.15 · 2026-10-10 (v0.1.0-alpha + M6 … M16 + M17: the 1.0 release candidate)** · Implements the *Technical Alpha* slice of `DESIGN.md` (§33), M6 file management and single-file deployment (DESIGN §13), M7, the DROOL agent and its lifecycle (DESIGN §4, §6, §8, §9, §28; §23 here), M8, firewall policy and safe enforcement on plain-nftables hosts (DESIGN §15–19; §24 here), M9, the Firewall Designer and an application-wide Light/Dark/System theme (§25 here), M10, network connectivity monitoring and enrichment (DESIGN §19.1, §19.4; §26 here), M11, system monitoring, service inventory and hardware health (DESIGN §11.3; §27 here), M12, alerts, notifications and maintenance (DESIGN §11.4, §12.13, §22.8; §28 here), M12.1, monitoring health overrides (DESIGN §11.3; §29 here), M13, scheduling, fleet automation and workflows (DESIGN §12.4, §12.6, §12.13, §26; §30 here), M14, desired state, drift and reconciliation (DESIGN §14; §31 here), M15, the UI/UX and information-architecture consolidation (DESIGN §2.7; §32 here), M16, core security, recovery and platform hardening (DESIGN §10.3, §7, §24; §33 here), and M17, supported platforms, packaging and release engineering for 1.0 (§34 here).

The file keeps its historical name: it is the implementation record that started with the Technical Alpha, its section numbers are referenced throughout the code (including the append-only migrations), and they don't change (§22, M17).

Sections 1–21 describe the implementation as built. Where an implementation decision superseded the original plan, the prose was updated and the decision and its reason are in §22.

This document is where we argue about concrete things: schema, operation semantics, SSH handling, job execution, package abstractions and API contracts. New product features go in `DESIGN.md`, not here.

---

## 1. Scope

### In scope
- SSH management: pinned host keys, at most one jump host
- Host inventory, tags, static groups, discovered facts
- Lightweight status polling (reachable, uptime, disk, failed units, pending updates, reboot required)
- Operations: run command, run script, package install/remove/upgrade, system update (APT, DNF), systemd service state, reboot with wait
- Operation engine: jobs, per-host state, locking, retries, timeouts, error classes, circuit breaker, cancellation
- Job history with stdout/stderr per host
- Local users and sessions, API tokens, secrets encrypted at rest
- Append-only audit log, structured logging with rotation and redaction
- SQLite, REST API, embedded React web UI
- Backup and restore
- **M6:** file manager for one host (browse, upload, download, text edit, mkdir, rename/move, delete, chmod, chown; login user or explicit elevation) and managed single-file deployment to many hosts (preview, diff, validation, service action, history, rollback) from a content-addressed artifact store (§10.6, §10.7)

- **M7:** the `drool-agent` binary and the agent Driver: enrollment, PKI with an offline-capable root and trust manifest, mTLS gRPC, certificate renewal/recovery/revocation, clone quarantine, local capability ceiling, state versioning, telemetry, agent-side audit, protocol negotiation, signed self-update with automatic rollback, staged rollouts, endpoint migration; Agent preferred / SSH fallback (§23)

- **M8:** intent-based firewall policy (hosts, groups, named services, internet/internal, IPv4/IPv6), compilation to nftables via named sets, environment detection and refusal, the dead-man transaction protocol over the agent and SSH (durable host journal, systemd rollback timer, boot loader, commit only over a new connection), management-path protection with an audited emergency override, set-element propagation, drift detection, Monitor/Guided/Enforce, connection learning (agents) and snapshots (SSH), simulation and Explain Host (§24)
- **M9:** the Firewall Designer: one canonical firewall document (named networks, groups, custom services, ordered policy) edited as a topology, as rules or as YAML; live server-side check of every draft with the real compiler (validation, per-host preview, management-path analysis, apply blockers), simulation and Explain on drafts, a read-only DROOL management-path overlay, layout persistence; named network objects in the policy model; set updates held near management paths; Light/Dark/System theme across the whole UI (§25)
- **M10:** network connectivity monitoring: the M8 observation store extended into one flow model (local address, last ports, process, scope, collection source), continuous agent collection from conntrack events when connection tracking is active (socket sampling otherwise, said so), SSH snapshots with process names, correlation of every flow with the firewall policy by the M8 compiler (covered, management, unclassified, denied, applied vs desired), local GeoIP/ASN enrichment of public addresses (MaxMind-format databases, cached, optional), a host Network connections card, a fleet Network activity page with server-side filters and paging, new-destination views, the Designer's Configured / Observed / Configured + Observed topology, and "Create policy from observed connection" as a designer draft (§26)
- **M11:** system monitoring on every host: CPU (per core), load, memory, swap, pressure, uptime, filesystems (capacity, inodes, read-only), disk IO, interfaces (state, throughput, errors), temperatures with the hardware's own thresholds, GPUs (NVIDIA, AMD, Intel); continuous from agents within an enforced resource budget that degrades fidelity under pressure, polled over SSH by the same collectors; history on the controller in three tiers (1 h/24 h/7 d/30 d views) with bounded retention; service inventory with transitions; storage device health (SMART, NVMe, eMMC, USB bridges, SD cards honestly limited, kernel evidence, an optional flash write/read probe); a health evaluation with durations and hysteresis (issues, no notifications); helper tools installed only with approval through package jobs; bulk SSH → agent conversion as a staged rollout (§27)
- **M12:** alerts as occurrences of M11's Warning/Critical issues (opened, escalated and resolved in the issue's own transaction), acknowledgement with notes, immediate maintenance for hosts and groups that suppresses notifications only, email/Discord/Slack/webhook destinations with tests and health, a default route plus optional filter routes, a durable notification outbox with leased workers, bounded retries and per-destination limits, reminders (off by default), the controller event timeline, Alerts/maintenance/notification UI and fleet/host/dashboard integration (§28)
- **M13:** schedules (run once, every *N* hours, daily, selected weekdays, weekly, monthly; explicit IANA zones with deterministic DST rules) that create ordinary jobs or ordinary maintenance through durable, unique occurrences; missed-run, late-run and overlap policies; selectors resolved per occurrence; owner re-authorization before every run; rollouts in the engine (canary wave, deterministic batches, a failure threshold that pauses); workflows of typed steps with immutable revisions pinned by schedules, templates, and controller-side checks (wait, host reachable, services running, health); recurring maintenance windows and maintenance-aware scheduled runs; Jobs → Runs / Schedules / Workflows (§30)
- **M14:** desired state: baselines of typed resources (systemd services, packages, managed files) with immutable revisions and an impact preview; global → group (explicit priority) → host precedence computed on the server with provenance and first-class conflicts; transport-neutral observation (M11 services with proof, a read-only targeted package query, file metadata and on-host hashes); statuses with honest *Unknown*; transition-only bounded history with truthful resolutions; Review difference, Accept current state (host-specific by default, files imported into the artifact store) and Restore managed state as one aggregated ordinary job per correction with the M13 rollout and post-job proof; optional per-baseline automatic restore owned by an administrator, re-authorized before every job, maintenance-aware, backing off and pausing with attention; drift warnings in New job; Desired state pages, host/group/dashboard integration (§31)
- **M15:** the web UI's structure and wording: grouped navigation, navigation search, page triage, states and permissions, the responsive shell (§32)
- **M16:** core security, recovery and platform hardening: full restore reconciliation with a new controller epoch, just-in-time SSH with an OpenSSH user CA, re-authentication and two-factor sign-in, a tamper-evident audit chain with an optional remote copy, crash-safe restore, backup verification, pre-upgrade backups, master key rotation, Shepherd compatibility statuses, upgrade and crash tests, soak tests, container hardening (§33)
- **M17:** the 1.0 release engineering: build provenance (version, channel, commit, modified, schema, protocol), one reproducible release pipeline (archives, `.deb`/`.rpm`, a multi-platform OCI image, SBOMs, notices, checksums, offline signing), the supported-platform matrix and its tests (every supported distribution in containers and VMs, arm64, rootless Docker and Podman), upgrade and rollback from real older builds, older Shepherds, the security checklist, documentation (§34)
- **M12.1:** sparse, inherited health overrides for M11's evaluator at global, group, host and subject scope with explicit group priorities and visible conflicts, stable subject identities, server-side provenance for every effective value, an impact preview, optional high-GPU-utilization health (off by default), truthful issue resolution reasons that M12 reports as "cleared", never "recovered" (§29)

### Out of scope (but the design must not block them)
Docker/container firewalling, WireGuard identity networking, templated files and directory sync, SSO/OIDC/SAML/LDAP, plugins, multi-hop jumps (TOTP two-factor sign-in arrived with M16, §33.3). (YAML editing of the firewall policy was added in M9, §25.) For network monitoring (M10): automatic or geographic blocking, threat scores, threat-intelligence feeds and IP reputation, packet capture and payload inspection, IDS signatures, SIEM features, alerting beyond new-destination visibility, eBPF collection. For system monitoring (M11): dashboard designers and query languages, external time-series databases, SMART self-tests, GPU driver installation, log collection, Windows hosts (a deferred future agent; DESIGN §2.6). For alerting (M12): on-call rotations, escalation trees and paging, incident grouping and correlation, anomaly or baseline thresholds, rule expressions, ticketing, SLA/MTTR reporting, automatic remediation, log alerting, notification plugins or executable hooks (recurring maintenance windows arrived with M13). For health overrides (M12.1): rule expressions or a monitoring language, anomaly or ML baselines, per-group subject selectors, GPU driver/Xid error collection, scheduled overrides. For scheduling (M13): DAGs, branching or a workflow language, approval pipelines, job-result notification routing (PagerDuty and the like), dynamic groups, scheduled firewall changes (firewall policy is applied from the Firewall page), cron syntax. For desired state (M14): compliance expressions and benchmarks (CIS, STIG), a DSL, remediation scripts, DAGs, package version policy, a package-registry abstraction, firewall desired state outside the Firewall (and Docker firewalling), Proxmox/Synology resources, SIEM, dynamic groups, Windows, plugins.

### Supported managed hosts
Supported (tested every release, §34.7): Debian 12 and 13, Ubuntu 22.04, 24.04 and 26.04, Rocky Linux 9 and 10, AlmaLinux 9 and 10, Fedora 44 (dnf5). Expected to work, not tested: RHEL 9 and 10, other Debian and RHEL derivatives, newer Fedora. The container matrix (§18) and the VM matrix (`TestVMPlatform`) run every supported distribution; Shepherds run on amd64 and arm64.
Requirements on the host: `sshd`, POSIX `sh`, coreutils, and `sudo` if the login user isn't root. **No Python required.** systemd is required for service operations and reboots; without it, commands, scripts, package operations and system updates still work, and the connection test reports the host as `unsupported` with that explanation.

---

## 2. Process and Deployment

```
┌───────────────────────── drool (single Go binary) ─────────────────────────┐
│                                                                               │
│  HTTP server ── REST /api/v1 ── embedded UI (go:embed) ── /health /ready      │
│       │                                                     /metrics          │
│       ▼                                                                       │
│  Services: auth · hosts · groups · secrets · jobs · audit                     │
│       │                                                                       │
│       ▼                                                                       │
│  Operation Engine ── Lock Manager ── Worker Pool ── Status Poller             │
│       │                                                                       │
│       ▼                                                                       │
│  Driver interface ──► SSH Driver ──► SSH Connection Manager (pool, jump, pin) │
│                       Agent Driver ──► drool-agent (mTLS gRPC, M7 §23)        │
│       │                                                                       │
│  Store (SQLite WAL) · Secret Box (AES-256-GCM) · slog (JSON, rotation)        │
└───────────────────────────────────────────────────────────────────────────────┘
```

- **One process, one binary.** Subcommands: `serve`, `healthcheck`, `migrate`, `backup` (and `backup verify`), `restore` (and `--recover`), `setup code`, `user create`, `user mfa-reset`, `secrets rotate-key`, `agent-ca …`, `release keygen|sign|verify`, `version [--json]`. Run as root on a data directory that belongs to another user, they refuse (§34).
- Runs as an unprivileged `drool` user. It never needs local root.
- Data directory: `/var/lib/drool/` (`drool.db`, `master.key`, `tls/`, `logs/`, `backups/`, `drool.lock`). `serve` holds `drool.lock` (flock) for its lifetime so a restore can't replace files under a running controller. Database files are created owner-only (0600) whatever the umask.
- Packaging (§34): `.deb` and `.rpm` (systemd, the `drool` account, `/usr/bin/drool`, the Shepherd binaries in `/usr/lib/drool`), a release archive with the static binary and the unit for `/usr/local`, and a multi-platform OCI image (distroless; data on a volume) with `compose.yaml` and a Podman Quadlet unit; for `linux/amd64` and `linux/arm64`.
- Single controller instance only. The lock manager and worker pool are in memory, and the alpha does not support HA.

---

## 3. Repository Layout

```
cmd/drool/                 main, subcommands
internal/
  config/                     defaults → file → env; records where each value came from
  logging/                    slog setup, streams, rotation (lumberjack), redaction
  store/                      SQLite open/pragmas, migrations, sqlc-generated queries
    migrations/               0001_init.sql, ...
    queries/                  *.sql for sqlc
  model/                      canonical typed model (Operation, TargetSpec, Strategy, Host, ...)
  schema/                     JSON Schema generated from model/ (served at /api/v1/schema)
  auth/                       passwords (argon2id), sessions, API tokens, CSRF, RBAC checks
  audit/                      append-only writer
  secrets/                    Secret Box: encrypt/decrypt, key versions, redaction registry
  inventory/                  hosts, groups, tags, facts parsing, target resolution
  engine/                     jobs, per-host state machine, scheduler, retries, circuit breaker
    lock/                     per-host lock manager
  ops/                        operation implementations (one file per op type)
    pkg/                      package manager backends: apt.go, dnf.go
  driver/                     Driver interface + shared result types
    sshdriver/                turns typed operations into remote shell execution
  sshx/                       connection manager, host key pinning, jump dialing, keepalive
  poller/                     periodic status checks
  firewall/                   policy, compiler, nftables backend, document, host tool, observations (M8, M9)
  netmon/                     network monitoring (M10): address scopes, settings
    collect/                  the agent's connection collector (conntrack events, socket sampling)
    geoip/                    local MMDB reader, GeoIP/ASN enricher and cache
    netsvc/                   queries, correlation views, topology, pruning
  monitor/                    system monitoring (M11): types, keys, CPU/memory/network/disk parsers and rates, filesystems, sensors, GPUs
    source/                   where collectors read from: the local machine (agent) or an SSH dump; helper allowlist
    storage/                  device inventory, SMART/NVMe/eMMC parsing, health judgement, kernel evidence
    services/                 systemd unit inventory and transitions
    collect/                  collection of everything for a host; the SSH script; helper status
    agentmon/                 the agent's engine, outbox, governor (budget/degradation), flash probe
    monsvc/                   controller side: ingestion, points and rollups, health evaluation, queries, fleet, helpers;
                              health configuration (M12.1): policy.go (checks, Recommended, resolution), subjects.go (identities), healthcfg.go (overrides, views, preview)
  alerts/                     alert lifecycle on M11's issue transitions, maintenance, routing, the notification outbox and workers, events (M12)
  notify/                     notification providers: SMTP, webhook, Discord, Slack; message formatting; result classification and redaction (M12)
  sched/                      scheduling (M13): recurrence and time zones, workflows and revisions, schedules, occurrences, the scheduler loop, previews
  verify/                     controller-side workflow steps (M13): wait, verify host / services / health
  api/                        HTTP handlers, problem+json errors, SSE, OpenAPI
  backup/
web/                          React + TypeScript (Vite)
api/openapi.yaml              REST contract (source of truth for UI client generation)
test/integration/             distro matrix, fault injection, VM tests, release qualification (Docker CLI; see §18, §34.11)
Dockerfile                    the one build definition: web UI, binaries, notices, release files, image (§34.3)
compose.yaml                  Docker Compose (a release ships it with its version)
packaging/
  release/                    build.sh (the release pipeline), buildx, sign.sh, nfpm.yaml, package scripts,
                              relkit/ (notices, reproducible archives, SBOM normalization, OCI reading, checksums, release.json)
  systemd/drool.service       the unit (packages; the archive's copy says /usr/local/bin)
  podman/drool.container      the Podman Quadlet unit
  keys/allowed_signers        the DROOL release key's public half (signing, §34.6)
  config.example.yaml         the common settings with their defaults
RELEASING.md                  versions, building, qualifying, signing, publishing
```

**Dependency rule:** `api` → services → `engine` → `driver` → `sshx`. `model` is imported by everyone and imports nothing internal. Nothing imports `api`.

---

## 4. The Key Abstraction: Driver

The boundary between "what to do" and "how to reach the host" is the **typed operation**, not a shell string.

```go
// driver/driver.go
type Driver interface {
    // Execute runs one typed operation on one host. Output is streamed to sink.
    Execute(ctx context.Context, h *model.Host, op model.Operation, sink OutputSink) (*OpResult, error)
    // Probe checks connectivity, auth and privilege escalation without changing anything.
    Probe(ctx context.Context, h *model.Host) (*ProbeResult, error)
}

type OpResult struct {
    Changed   bool              // idempotent ops report whether anything changed
    ExitCode  *int
    Data      json.RawMessage   // op-specific structured result (e.g. upgraded packages)
    RebootRequired *bool
}
```

- **SSH driver (alpha):** each operation is translated into remote shell via `ops/`, run over SSH, and the output is parsed back into an `OpResult`.
- **Agent driver (M7, §23):** forwards the same typed operation over gRPC. The agent implements it natively and enforces its capability ceiling.

So the engine, API and UI don't change when the agent arrives. Errors returned from `Execute` are always `*model.OpError` carrying one of the DESIGN §12.9 error classes.

---

## 5. Canonical Model (alpha subset)

Defined as Go types in `internal/model`. JSON Schema is generated from them and the REST API uses the same shapes. YAML import/export later is just another serializer over these types.

### 5.1 Operations

```jsonc
// command
{ "type": "command", "command": "df -h", "become": true, "timeout_s": 300 }

// script
{ "type": "script", "interpreter": "bash", "body": "df -h\nfree -m",
  "when": { "os_family": ["debian"] }, "become": true, "read_only": false, "timeout_s": 600 }

// package
{ "type": "package", "action": "install" | "remove" | "upgrade", "names": ["htop"],
  "allow_removals": false }      // needed when the change would remove other packages

// system_update
{ "type": "system_update",
  "mode": "upgrade" | "full_upgrade" | "security",
  "allow_removals": false,       // needed when the update would remove packages
  "refresh": true,
  "reboot": "never" | "if_required" | "always" }

// service (idempotent states; restarted/reloaded are inherently imperative)
{ "type": "service", "name": "nginx",
  "state": "running" | "stopped" | "restarted" | "reloaded" | null,
  "enabled": true | false | null }

// reboot
{ "type": "reboot", "wait": true, "timeout_s": 900 }

// file_deploy (M6, §10.7): content is referenced by SHA-256, never embedded
{ "type": "file_deploy", "artifact": "<sha256>", "path": "/etc/nginx/conf.d/app.conf",
  "overwrite": "replace" | "create_only",
  "owner": "root", "group": "root", "mode": "0644",   // empty: keep existing / default
  "elevated": true,                                     // as root via the host's sudo
  "validate": "nginx -t -c {path}",                     // runs on the staged file
  "service": { "name": "nginx", "action": "reload" | "restart" },
  "timeout_s": 600 }
// rollback_of / remove / expect_current are set only by the controller's rollback endpoint

// M13: steps the controller runs itself, inside the host's job (§30.9)
{ "type": "wait", "seconds": 60 }                                   // 1–3600
{ "type": "verify_host", "timeout_s": 300 }
{ "type": "verify_services", "services": ["ssh", "jellyfin"], "timeout_s": 120 }   // .service added; ≤50
{ "type": "verify_health", "policy": "no_critical" | "no_warning", "timeout_s": 120 }

```

Discovery is not a job step: it is an inventory operation run by the poller and by `POST /hosts/{id}/refresh` (§22, M3).

Each operation type declares static metadata used by the engine:

| Op | Lock | Idempotent | Requires become | Default timeout |
|---|---|---|---|---|
| command / script | exclusive (shared if `read_only`) | no | per spec | 300 s / 600 s |
| package | exclusive | yes | yes | 1800 s |
| system_update | exclusive | yes | yes | 3600 s |
| service | exclusive | yes (except restarted/reloaded) | yes | 120 s |
| reboot | exclusive | no | yes | 900 s |
| wait / verify_host / verify_services / verify_health (M13, controller-side) | shared | yes | no | seconds + 30 s / timeout + 60 s / timeout + 180 s (backstops; the step's own timeout decides) |
| file_deploy | exclusive | yes (except with a service restart) | if `elevated` (service actions always) | 600 s |
| discover (inventory, not a job step) | shared, never waits | yes | no | 120 s |

### 5.2 Targets and strategy

```jsonc
"targets": {
  "hosts":  ["host_01J..."],
  "groups": ["grp_01J..."],
  "tags":   ["role:web", "env:prod"],     // OR across entries; see below
  "exclude_hosts": []
}
"strategy": {
  "concurrency": 10,           // hosts in parallel (inside a wave)
  "failure_threshold": 3,      // pause after N failures (null = never), §11.5
  "canary_count": 1,           // M13: the first N hosts by name run first (0–10), or
  "canary_hosts": [],          //      explicit canaries (≤10; not both)
  "batch_size": 5              // M13: hosts per wave after the canaries (0 = all at once)
}
```

Target semantics: union of `hosts ∪ groups ∪ tag matches`, minus `exclude_hosts`. Each tag entry matches hosts having that tag (OR across entries). AND queries come with dynamic groups later. **Targets are resolved once at job start, and the resolved host list is stored with the job.** A job never retargets mid-run. A schedule stores the selectors and resolves them at every occurrence (§30).

**Waves (M13).** `model.PlanWaves` splits the resolved hosts (sorted by name) into waves when the job is created and `job_hosts.wave` stores them: wave 0 holds the canaries when there is a canary wave (only if hosts remain after it; an explicit canary no longer among the targets falls back to the first hosts by name, with a note), then batches of `batch_size`. `wave = -1` marks a host skipped before the run started (a scheduled run's host in unrelated maintenance).

**Per-host steps (M14).** A desired-state correction is one job whose hosts need different subsets of the same typed operations. `engine.SubmitOptions.HostSteps` stores a JSON list of step indexes in `job_hosts.steps` (NULL = every step); `runHost` silently skips the other steps for that host, and the job page says "Step N — on X of Y hosts". Such jobs may carry up to `model.MaxCorrectiveSteps` (50) steps (`SubmitOptions.MaxSteps`); ordinary jobs keep `MaxSteps`.

### 5.3 Job

```jsonc
{
  "name": "Update web",
  "steps": [ { "type": "system_update", "scope": "all", "reboot": "if_required" } ],
  "targets": { ... },
  "strategy": { ... }
}
```

`steps` run sequentially per host, and a failed step stops that host. At most 10 steps; the New job page builds one, workflows (§30.8) up to 10. M13 adds `origin` to a job: the schedule and occurrence, or the workflow and revision, it came from.

---

## 6. Database

### 6.1 Engine choices
- **SQLite via `modernc.org/sqlite`** (pure Go, no cgo, so static binaries are trivial).
- Pragmas: `journal_mode=WAL`, `synchronous=NORMAL`, `foreign_keys=ON`, `busy_timeout=5000`.
- **One writer connection** (`MaxOpenConns=1` on the write pool) plus a separate read pool. All writes go through the writer, so `SQLITE_BUSY` storms can't happen.
- **sqlc** for typed queries over hand-written SQL. Migrations are embedded numbered SQL files run at startup (`schema_migrations` table). Migrations only ever go forward.
- **IDs:** prefixed ULIDs (`host_01J9...`, `job_...`, `sec_...`): sortable, readable in logs, and the type is obvious.
- **Time:** INTEGER Unix milliseconds, UTC.
- PostgreSQL later: keep SQL portable (no SQLite-only functions in hot paths). sqlc supports a second query set.

### 6.2 Schema (initial migration, abridged)

```sql
CREATE TABLE users (
  id TEXT PRIMARY KEY,
  username TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,              -- argon2id encoded
  role TEXT NOT NULL CHECK (role IN ('admin','operator','auditor','viewer')),
  disabled INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);

CREATE TABLE sessions (
  token_hash BLOB PRIMARY KEY,              -- sha256 of random token; raw token only in cookie
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  source_ip TEXT, user_agent TEXT
);

CREATE TABLE api_tokens (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  token_hash BLOB NOT NULL UNIQUE,
  created_at INTEGER NOT NULL, last_used_at INTEGER, expires_at INTEGER
);

CREATE TABLE secrets (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  kind TEXT NOT NULL CHECK (kind IN ('ssh_private_key','password','sudo_password','api_token')),
  ciphertext BLOB NOT NULL,
  nonce BLOB NOT NULL,
  key_version INTEGER NOT NULL,
  public_part TEXT,                          -- e.g. SSH public key; safe to display
  created_by TEXT REFERENCES users(id),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE hosts (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  description TEXT NOT NULL DEFAULT '',
  connect_address TEXT NOT NULL,             -- hostname or IP used to dial
  management_method TEXT NOT NULL DEFAULT 'ssh' CHECK (management_method IN ('ssh','agent')),
  ssh_port INTEGER NOT NULL DEFAULT 22,
  ssh_user TEXT NOT NULL,
  ssh_credential_id TEXT REFERENCES secrets(id),
  become_method TEXT NOT NULL DEFAULT 'sudo' CHECK (become_method IN ('none','sudo')),
  become_credential_id TEXT REFERENCES secrets(id),   -- NULL = NOPASSWD sudo
  ssh_needs_pty INTEGER NOT NULL DEFAULT 0,           -- legacy requiretty
  jump_only INTEGER NOT NULL DEFAULT 0,               -- inventory entry used only as a hop
  status TEXT NOT NULL DEFAULT 'pending_hostkey'
    CHECK (status IN ('pending_hostkey','ready','unreachable','hostkey_mismatch','auth_failed','disabled')),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

-- Multi-hop in the model; alpha enforces at most one row per host.
CREATE TABLE host_hops (
  host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  hop_index INTEGER NOT NULL,
  via_host_id TEXT NOT NULL REFERENCES hosts(id),
  PRIMARY KEY (host_id, hop_index)
);

CREATE TABLE ssh_host_keys (
  id TEXT PRIMARY KEY,
  host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  algorithm TEXT NOT NULL,                   -- ssh-ed25519, ecdsa-sha2-nistp256, rsa-sha2-512...
  public_key BLOB NOT NULL,
  fingerprint_sha256 TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('offered','pinned','rejected','retired')),
  first_seen_at INTEGER NOT NULL,
  decided_by TEXT REFERENCES users(id),
  decided_at INTEGER
);
CREATE UNIQUE INDEX one_pinned_key ON ssh_host_keys(host_id) WHERE status = 'pinned';

CREATE TABLE host_addresses (
  host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  address TEXT NOT NULL,
  family INTEGER NOT NULL CHECK (family IN (4,6)),
  interface TEXT,
  source TEXT NOT NULL CHECK (source IN ('configured','discovered','dns')),
  first_seen_at INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  PRIMARY KEY (host_id, address)
);

CREATE TABLE host_facts (
  host_id TEXT PRIMARY KEY REFERENCES hosts(id) ON DELETE CASCADE,
  collected_at INTEGER NOT NULL,
  os_family TEXT, distro TEXT, distro_version TEXT, kernel TEXT, arch TEXT,
  pkg_manager TEXT, init_system TEXT, hostname TEXT,
  cpu_count INTEGER, mem_bytes INTEGER,
  facts_json TEXT NOT NULL                    -- full parsed facts (disks, interfaces, services, ports)
);

CREATE TABLE host_status (
  host_id TEXT PRIMARY KEY REFERENCES hosts(id) ON DELETE CASCADE,
  checked_at INTEGER NOT NULL,
  reachable INTEGER NOT NULL,
  boot_id TEXT, uptime_s INTEGER, load1 REAL,
  mem_used_pct REAL, max_fs_used_pct REAL,
  failed_units INTEGER,
  updates_available INTEGER, security_updates INTEGER, updates_checked_at INTEGER,
  reboot_required INTEGER,
  last_error_class TEXT, last_error TEXT
);

CREATE TABLE host_tags (
  host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  tag TEXT NOT NULL,                          -- "key:value" or bare "key"
  PRIMARY KEY (host_id, tag)
);

CREATE TABLE groups (
  id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, description TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE TABLE group_members (
  group_id TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
  host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  PRIMARY KEY (group_id, host_id)
);

CREATE TABLE jobs (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  spec_json TEXT NOT NULL,                    -- canonical job spec (steps, targets, strategy)
  spec_version INTEGER NOT NULL,
  idempotency_key TEXT UNIQUE,
  status TEXT NOT NULL CHECK (status IN
    ('pending','running','paused','completed','cancelled','interrupted')),
  pause_reason TEXT,
  created_by TEXT NOT NULL REFERENCES users(id),
  created_at INTEGER NOT NULL, started_at INTEGER, finished_at INTEGER
);

CREATE TABLE job_hosts (
  job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  host_id TEXT NOT NULL REFERENCES hosts(id),
  host_name TEXT NOT NULL,                    -- snapshot; survives host rename/delete
  state TEXT NOT NULL CHECK (state IN
    ('queued','waiting_lock','connecting','running','rebooting','waiting_host','verifying',
     'succeeded','failed','timed_out','cancelled','skipped','interrupted')),
  current_step INTEGER NOT NULL DEFAULT 0,
  attempt INTEGER NOT NULL DEFAULT 0,
  changed INTEGER,
  error_class TEXT, error_message TEXT, retryable INTEGER,
  started_at INTEGER, finished_at INTEGER,
  PRIMARY KEY (job_id, host_id)
);
CREATE INDEX job_hosts_by_host ON job_hosts(host_id, job_id);

CREATE TABLE job_steps (
  job_id TEXT NOT NULL, host_id TEXT NOT NULL, step_index INTEGER NOT NULL,
  op_type TEXT NOT NULL,
  state TEXT NOT NULL,
  exit_code INTEGER, changed INTEGER, result_json TEXT,
  stdout_bytes INTEGER NOT NULL DEFAULT 0, stderr_bytes INTEGER NOT NULL DEFAULT 0,
  truncated INTEGER NOT NULL DEFAULT 0,
  started_at INTEGER, finished_at INTEGER,
  PRIMARY KEY (job_id, host_id, step_index),
  FOREIGN KEY (job_id, host_id) REFERENCES job_hosts(job_id, host_id) ON DELETE CASCADE
);

CREATE TABLE job_output (
  job_id TEXT NOT NULL, host_id TEXT NOT NULL, step_index INTEGER NOT NULL,
  seq INTEGER NOT NULL,
  stream INTEGER NOT NULL CHECK (stream IN (1,2)),   -- 1 stdout, 2 stderr
  ts INTEGER NOT NULL,
  chunk BLOB NOT NULL,
  PRIMARY KEY (job_id, host_id, step_index, seq),
  FOREIGN KEY (job_id, host_id, step_index) REFERENCES job_steps ON DELETE CASCADE
);

CREATE TABLE audit_events (
  id TEXT PRIMARY KEY,
  ts INTEGER NOT NULL,
  actor_type TEXT NOT NULL CHECK (actor_type IN ('user','token','system')),
  actor_id TEXT,
  action TEXT NOT NULL,                       -- e.g. host.create, hostkey.approve, job.create
  target_type TEXT, target_id TEXT,
  result TEXT NOT NULL CHECK (result IN ('success','failure','denied')),
  source_ip TEXT, request_id TEXT,
  details_json TEXT NOT NULL DEFAULT '{}',
  prev_hash BLOB                              -- reserved for 1.0 hash chain
);
CREATE TRIGGER audit_no_update BEFORE UPDATE ON audit_events
  BEGIN SELECT RAISE(ABORT, 'audit_events is append-only'); END;
CREATE TRIGGER audit_no_delete BEFORE DELETE ON audit_events
  BEGIN SELECT RAISE(ABORT, 'audit_events is append-only'); END;

CREATE TABLE settings (
  key TEXT PRIMARY KEY, value_json TEXT NOT NULL,
  updated_by TEXT REFERENCES users(id), updated_at INTEGER NOT NULL
);
```

M6 (`0006_files.sql`) adds `artifacts` (content-addressed index: sha256, size, name, is_text, created_by, created_at, last_used_at) and `file_deployments` (one row per host per deployment or rollback: job, host, path, kind, rollback_of, outcome, before/after sha256, artifact reference and owner/group/mode, validation and service results, error, the operation's settings without content). Deployment rows are not pruned with jobs (`job_id` becomes NULL); they are the managed file's history and keep previous versions referenced.

Retention (by setting): `job_output` and finished jobs are pruned after 30 days by default, and audit is kept indefinitely. Audit retention pruning (1.0) will go through a dedicated, audited maintenance path that temporarily drops the triggers. Reserved for later migrations: `state_versions`, `agents`, `trust_manifests`.

---

## 7. Secrets

- **Secret Box:** AES-256-GCM, a random 96-bit nonce per encryption, and **AAD = secret ID + kind**, so ciphertexts can't be swapped between rows.
- **Master key:** 32 random bytes in `/var/lib/drool/master.key` (0600, created on first start), or the file `secrets.master_key_file` (`DROOL_SECRETS_MASTER_KEY_FILE`) names. `key_version` allows rotation (`drool secrets rotate-key` re-encrypts all rows).
- No API ever returns plaintext. Responses include metadata and `public_part` only.
- Decrypted values live only as long as one operation, and are **registered with the redactor** for its lifetime (§12.3).
- **Controller SSH key:** on first start, generate an ed25519 key as secret `controller-default`. The UI shows the public key with an `authorized_keys` one-liner. This is the default credential for new hosts.
- Backups contain the DB (ciphertext) but **not** the master key unless `--include-master-key` is passed. The CLI warns either way.

---

## 8. SSH Connection Manager (`sshx`)

### 8.1 Dialing

```
dial(host):
  if host has hop:
     jc := pool.get(hop)                     // jump connection shared by all hosts behind it
     conn := jc.Dial("tcp", target:port)     // direct-tcpip channel
     client := ssh.NewClientConn(conn, cfg(target))
  else:
     client := ssh.Dial("tcp", target:port, cfg(target))
```

- The jump host and the target are **each** verified against their own pinned key.
- **Decision D8:** a jump host must itself be a normal inventory host (optionally `jump_only`) with its own pinned key, credential, health state and audit history. Inline jump definitions are not supported.
- Library: `golang.org/x/crypto/ssh`. No `ssh_config` parsing or ProxyCommand in the alpha.

### 8.2 Host key pinning

- `HostKeyCallback` compares the presented key to the single `pinned` row. Any mismatch → `HostKeyError`: the connection is aborted, the host is set to `hostkey_mismatch`, a security event and audit record are written, and it is **never retried**.
- `HostKeyAlgorithms` is limited to the pinned key's algorithm. Otherwise a server offering a different key type (e.g. RSA instead of ed25519) would look like a mismatch.
- **Enrollment flow:** `POST /hosts/{id}/hostkey/scan` connects without authenticating, records every offered key as `offered`, and returns their fingerprints. The admin compares them out of band (or pastes the expected fingerprint when creating the host), then approves one, which becomes `pinned`. We never trust on first use silently.
- **Key rotation on a host:** the admin approves the new offered key. The old one becomes `retired`. Both steps are audited.

### 8.3 Pooling

- One `*ssh.Client` per host, with sessions multiplexed over it.
- Concurrent sessions per connection are capped at **8** (OpenSSH `MaxSessions` defaults to 10).
- Keepalive (`keepalive@openssh.com`) every 30 s; idle connections close after 5 min.
- The connection is evicted on any transport error. The next use redials.
- A connection authenticated with an SSH certificate (M16), or tunnelled through one, is handed out only until 30 s before that certificate expires; the next use redials with a new certificate (§33.2). Key and password connections have no such limit.
- **Verification connections** (used for firewall transactions in the Beta) bypass the pool and the jump connection cache entirely: `DialFresh()` exists from day one.

### 8.4 Authentication and privilege escalation

- Auth methods: private key (preferred) and password (allowed, labeled as discouraged). Keyboard-interactive is not supported in the alpha.
- **Become:**
  - Root login: no wrapper.
  - NOPASSWD sudo: `sudo -n -- <cmd>`.
  - Password sudo: `sudo -k -S -p '' -- <cmd>`, with the password as the first line of stdin. `-k` guarantees sudo actually consumes it, so a cached sudo timestamp can't let the password leak into the command's stdin.
- **No PTY by default**, which keeps stdout and stderr separate. If sudo replies `sorry, you must have a tty`, that's classified as `AuthorizationError` with a hint to set `ssh_needs_pty`. Per-host `ssh_needs_pty` is the only PTY control (decision D2).
- `Probe` checks: TCP reachable → host key matches → auth → `become` works (`sudo -n true` / password variant) → `uname -s` is Linux → systemd present.

---

## 9. Remote Execution Conventions

All operations run through a single helper:

```
exec(host, script, {become, stdin, timeout, env})
```

- Scripts are sent on **stdin** to `sh -s` (or `bash -s`), wrapped with `set -eu` where the op owns the script. User scripts run as written.
- Locale fixed: `LC_ALL=C` for parsed output. `DEBIAN_FRONTEND=noninteractive` for apt.
- **Structured output from built-in ops:** built-in op scripts print delimited sections (`@@DROOL:section@@`) that the controller parses, so we don't need `jq` or Python on hosts.
- **Cancellation and timeouts (decision D5):** every execution first creates a private run directory: `mktemp -d "$base/drool.XXXXXXXXXX"` (mode 0700), where `$base` is `$XDG_RUNTIME_DIR`, then `/run/user/$(id -u)` if it exists and is owned by the user, then `${TMPDIR:-/tmp}`. Nothing uses predictable names. The wrapper prints the created path on a marker line so the controller learns it, then runs `setsid sh -c '...' & echo $! > "$dir/pid"; wait`, and removes the directory on exit. Cancel = a separate session reads `"$dir/pid"` (after checking the directory is owned by the login user and not a symlink) and sends `kill -TERM -<pgid>`, then KILL after 10 s. The state becomes `cancelled`, the result is "may be partial", and this is documented. (OpenSSH's handling of `session.Signal` is unreliable, so we don't depend on it.)
- **Exit codes:** for built-in ops, the parser decides success, not just the exit code (e.g. `dnf check-update` exits with 100 when updates exist).

---

## 10. Operation Implementations

### 10.1 discover

One POSIX script collects: `/etc/os-release`, `uname -srm`, hostname, `nproc`, `/proc/meminfo`, `df -PT` (excluding pseudo filesystems), `ip -j addr` (falling back to text), package manager detection (`apt-get`, `dnf`, `yum`, `zypper`, `pacman`), init system (`/proc/1/comm`), running services (`systemctl list-units --type=service --state=running --no-legend --plain`), listening ports (`ss -Htlnu`, with process names when become is available), container runtime presence (`docker`, `podman`), and `boot_id`.

`os_family` mapping: `ID`/`ID_LIKE` → `debian` | `redhat` | `suse` | `arch` | `unknown`. Addresses go into `host_addresses` with `source=discovered`.

### 10.2 Package backends

`internal/ops/pkg` has one backend per package manager (APT; DNF covering dnf4 and dnf5). A backend only builds command lines and parses output; the SSH driver orchestrates each operation as separate steps:

1. detect the package manager (`apt-get`, `dnf`/`yum`)
2. read installed versions of the named packages (`dpkg-query` / `rpm -q`, as the login user)
3. refresh metadata (system updates only, `refresh: true` by default; root)
4. **simulate** the transaction (`apt-get -s …` as the login user; `dnf -C --assumeno …` as root, since dnf4 needs it)
5. apply the removal rule (below); stop with `ValidationError` before changing anything
6. run the real transaction through the cancellable run-directory runner (§9), streaming output
7. read versions again, verify the end state, check whether a reboot is required

| | APT (Debian/Ubuntu) | DNF (RHEL 9+/Fedora, dnf4 & dnf5) |
|---|---|---|
| Environment | `DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a NEEDRESTART_SUSPEND=1 LC_ALL=C`, `--force-confdef --force-confold`, `DPkg::Lock::Timeout=120`. needrestart is suspended: services are not restarted during the update | `LC_ALL=C` |
| Refresh | `apt-get update` | `dnf makecache` |
| Install / remove | `apt-get install -y` / `apt-get remove -y` (APT's default recommends handling; there is no `--no-install-recommends` setting) | `dnf install -y` / `dnf remove -y` |
| Named upgrade | `apt-get install --only-upgrade`; a package that isn't installed is a `ValidationError` | `dnf upgrade` |
| `mode: upgrade` | `apt-get upgrade -y` (never removes; may hold back) | `dnf upgrade -y` |
| `mode: full_upgrade` | `apt-get full-upgrade -y` (may install or remove) | same as `upgrade` |
| `mode: security` | **Not supported** → `ValidationError` (decision D4) | `dnf upgrade --security -y` |
| Reboot required | `/var/run/reboot-required` (Ubuntu); otherwise newest kernel in `/boot` vs. running kernel (plain Debian has no reboot-required file) | `needs-restarting -r` (dnf4) / `dnf needs-restarting -r` (dnf5) |
| Lock | apt waits up to 120 s itself; then `PackageManagerError{retryable}` naming the holder | `PackageManagerError{retryable}` |

Pending-update counts for the dashboard come from discovery (`apt list --upgradable`, `dnf -C check-update` plus advisories), not from these backends (§22, M3).

- **Removal rule (decision D3, applied to every package operation):** a removal of the named packages themselves (`remove`) is expected; any *other* removal (dependents of a removed package, conflicts of an install, removals during a system update) needs `allow_removals: true`. Without it the host fails with `ValidationError` listing the packages, before anything changes.
- **Dry run:** `POST /jobs/simulate {targets, step}` runs steps 1–5 on each host (at most 8 at a time, 3-minute budget, shared host lock taken without waiting) from the host's current metadata. The UI's "Preview changes" uses it; the "allow removals" box appears only after a preview showed removals. A system update refreshes metadata first, so the real run can include more updates than the preview; new removals still stop it unless allowed.
- **Security updates on APT are detected and displayed only** (the `*-security` suites feed the host's security count); security-only execution is DNF-only in the alpha.
- **Lock contention:** apt waits up to 120 s on the dpkg lock; after that, or on a dnf lock, the error is retryable and the engine retries with its normal budget (§11.4).
- **Interrupted dpkg** (`dpkg was interrupted`) → `PackageManagerError{retryable: false}` with the fix (`sudo dpkg --configure -a`). DROOL does **not** repair it automatically.
- **Results:** `changed` = versions differ or the plan was non-empty (system update: plan non-empty, or a reboot happened). Results store the full plan, `changes` for the named packages, `reboot_required` and its reason.

### 10.3 service

Reads `systemctl show -p LoadState,ActiveState,SubState,UnitFileState,CanReload <unit>` (`.service` is added when no suffix is given).

- Start, stop, enable and disable apply only the transitions needed; `restarted` and `reloaded` always act.
- Refused with `ServiceError` before acting: unit not found, masked unit, reload on a unit without reload support, enable/disable on a `static`/`indirect` unit.
- After acting, waits up to 10 s for transitional states to settle, then verifies the requested state. A unit that fails to start is a `ServiceError`, and `systemctl status -n 15` is attached to the output.
- A host where systemd isn't running (or `systemctl` doesn't exist) gives `ConfigurationError`.
- Results store the unit, the state before and after, and the `systemctl` verbs run.

### 10.4 reboot

1. Record the current `boot_id`.
2. Run `systemd-run --on-active=2 systemctl reboot` (falling back to `nohup sh -c 'sleep 2; systemctl reboot'`), so the SSH command returns cleanly.
3. Evict the pooled connection. State becomes `rebooting`, then `waiting_host`, then `verifying`.
4. Poll with fresh connections (3 s, doubling, capped at 15 s) until SSH works **and the host key still matches** **and `boot_id` has changed**. Only a changed `boot_id` proves the reboot actually happened. A different host key fails immediately with `HostKeyError`; DROOL never trusts a new key because a reboot happened.
5. Timeout → `TimeoutError`, worded differently for "the host never went away (old boot id, reported as not changed)" and "went down but did not come back". The job host ends `timed_out`; the host's own status is updated by the poller.

`system_update` with `reboot: if_required` checks reboot-required after the upgrade and then does the above; `always` reboots regardless.

### 10.5 command / script

- `when` conditions are evaluated against stored facts **before** connecting. A mismatch → `skipped`, with the reason recorded.
- OS-specific bodies (`body: {debian: ..., redhat: ...}`) pick the body by `os_family`. No match → `skipped`.
- Never retried on `ExecutionError`. Retried only on `ConnectionError` raised **before** the command started.

### 10.6 Files: transport and file manager (M6)

**Interface.** `internal/files` defines a transport-neutral `FS` (Caps, Home, RealPath, Stat, List, Open, Hash, Stage, Mkdir, Rename, Remove, Chmod, Chown) with typed errors (`not_found`, `permission_denied`, `exists`, `symlink`, `changed`, `unsafe_directory`, `integrity`, …). Writes are two-phase: `Stage` streams content into a private temporary file and checks its SHA-256; `Staged.Validate` runs a command on it; `Staged.Install(Precondition)` atomically replaces the destination only if it still has the expected checksum (or still doesn't exist); `Discard` removes it. `Caps` reports the transport, whether the session is elevated, `AllowedPaths` (nil = unrestricted) and `RootEquivalent`. An agent will implement the same interface (§19).

**SSH implementation** (`sshdriver.FileSystem`), always on the pooled, pinned `sshx` connection (jump host included):
- **Login user:** SFTP (`github.com/pkg/sftp`) over the existing client. No second SSH stack, no separate trust path.
- **Elevated** ("Use administrator privileges"): there is **no root SFTP server**. Each action is a small typed POSIX `sh` script run through the host's become setting (`sudo -n`, or `sudo -S` with the stored password on stdin; root logins run it directly). Scripts take paths as positional arguments, never interpolated, and report `@@ERR <code> <message>` markers. Listings use `find -P -printf` with NUL-separated fields; reads stream `sudo cat`. Content never shares stdin with the sudo password: uploads go by SFTP as the login user into a private `mktemp -d` directory (`${TMPDIR:-/var/tmp}`, 0700), and the root script copies it next to the destination.
- **Writes:** temporary file `.drool-tmp-*` created with `O_EXCL` in the destination's directory → full content → checksum verified → owner/group/mode set → optional validation → precondition checked (`changed` if the destination moved on) → `mv -f`/rename (atomic, same filesystem) → re-stat and re-hash to verify. A cancelled request, a dropped connection or a failed check leaves the destination untouched and removes the temporary file (stale `.drool-tmp-*` older than 24 h are removed by later elevated writes). Replacing a file keeps its owner, group and mode unless new ones are given; new files get 0644 and the session's user.
- **Symlinks:** listed with their target; never followed by a destructive or privileged action. Elevated scripts refuse a final-component symlink and resolve the parent with `cd -P`/`pwd -P`, refusing when it differs from the requested path (a symlinked directory). Delete removes the link, not the target. Download refuses symlinks; the UI navigates to the target explicitly.
- **Unsafe directories:** elevated writes, chmod and chown are refused (`unsafe_directory`) in a directory not owned by root or the login user, or writable by group/others (e.g. `/tmp`), where another user could race the operation.
- Recursive delete of `/` and top-level directories is refused. Rename never overwrites an existing destination.
- **Locks:** interactive changes take the host lock shared without waiting (409 `host_busy` while a job holds it exclusively); reads take no lock.

**API** (§13): list, stat, download (streamed, `Content-Disposition`), upload (streamed `PUT`; the server's 60 s read timeout is lifted for that request only; `overwrite=false` fails on an existing file), text view/save (UTF-8 without NUL, ≤ 1 MiB; save carries `expect_sha256` and fails with 409 `file_changed` unless `force`), and mkdir/rename/delete/chmod/chown. Paths must be absolute and are cleaned; the API never touches controller-local paths. Elevation is a per-request flag that needs `files.elevate` in addition to the route's permission.

**Audit:** every change records host, path, action, result, before/after SHA-256, owner/group/mode and whether it was elevated. Elevated listings and reads are audited too. File content never enters the audit log.

### 10.7 File deployment and artifacts (M6)

**Artifact store** (`internal/artifacts`): `<data_dir>/artifacts/sha256/<ab>/<sha256>`, 0600, written via a temporary file and renamed under its checksum (≤ 1 GiB). Reads verify the checksum while streaming. Job specs and audit entries reference artifacts by hash only. Hourly GC deletes artifacts that no `file_deployments` row and no job spec references and that weren't used for 24 h.

**Sequence** (`internal/filedeploy`, transport-neutral, per host): inspect destination (refuse symlinks and non-regular files) → for rollbacks, check the file still has the content the deployment installed → identical content and metadata: `unchanged`, `changed: false` → capture the current version into the artifact store (≤ 64 MiB; larger files are deployed but can't be rolled back) → stage from the artifact → validate (`{path}` = staged file; failure → `validation_failed`, live file untouched) → install with precondition → verify → optional service reload/restart (through the service operation of §10.3, always as root). A service failure after a successful install is a `ServiceError` stating that the file was installed. Each host's result is a `file_deployments` row plus a `file.deploy`/`file.rollback` audit event written in one transaction.

**Preview** (`POST /file-deployments/preview`, ≤ 8 hosts at a time, 3-minute budget, shared lock without waiting, audited): per host `create`, `identical`, `replace`, `metadata`, `keep` (create-only) or `remove`, or an error (*inaccessible*, including a missing parent directory), with a unified diff for text up to 1 MiB.

**Rollback** (`POST /file-deployments/{id}/rollback`): a new audited job that restores the captured previous content with its owner, group and mode (or removes a file the deployment created), reusing the original elevation, validation and service settings; it fails if the file no longer has the deployed content.

---

## 11. Operation Engine

### 11.1 Lifecycle

```
POST /jobs
  → validate spec (schema + semantic: become required? targets non-empty?)
  → resolve targets → snapshot job_hosts (state=queued)
  → audit job.create
  → engine.Submit(job)

Engine (per job):
  for each host (respecting strategy.concurrency, global limit):
     acquire host lock (mode from op metadata; FIFO; wait timeout 30 min → skipped)
     for each step: Driver.Execute with retry policy
     release lock
     update job_hosts, stream events
  hosts run wave by wave (M13): a wave starts when the previous one is
  terminal; a failed canary (wave 0) pauses before the rest starts
  circuit breaker / failure_threshold pause the job: no new hosts start,
  running hosts finish
  → job completed (summary derived from job_hosts, never stored as one boolean)
```

### 11.2 Concurrency
- Global worker limit: **20** concurrent host executions (configurable).
- Per-job `strategy.concurrency` (default 10) is applied within the global limit.
- Per-jump-host cap: **10** concurrent channels through one jump host (configurable). This stops a large job from overwhelming one bastion.

### 11.3 Locks
- In-memory per-host lock manager: `none | shared | exclusive`, FIFO, so writers don't starve.
- A host in `waiting_lock` shows which job holds the lock ("Queued behind *System Upgrade* (job_…)").
- No persistence needed. A controller restart means nothing is running anymore (§11.6).

### 11.4 Retries

```go
type RetryPolicy struct {
    Attempts     int           // default 3 for ConnectionError
    InitialDelay time.Duration // 5s
    Backoff      string        // exponential, factor 2, max 2m, ±20% jitter
    Retryable    []ErrorClass
}
```

As implemented: 3 attempts in total, 5 s initial delay, exponential, capped at 2 min, ±20 % jitter.

| Error class | Retried? |
|---|---|
| ConnectionError (before op started) | yes |
| ConnectionError (mid-op: the connection dropped after the command started) | only if op is idempotent; reported as "outcome unknown" |
| HostKeyError, AuthenticationError, AuthorizationError | **never** |
| PackageManagerError marked retryable (lock, download failure, no metadata cache) | yes, same budget (apt also waits 120 s for its lock first) |
| TimeoutError | only if op is idempotent |
| ExecutionError, ValidationError, ServiceError, ConfigurationError | never |

### 11.5 Circuit breaker, canary and failure threshold
Per job: **5 consecutive** host failures with the same class among {Authentication, HostKey, Authorization} → the job becomes `paused` with a reason ("5 consecutive authentication failures; likely a shared credential problem"). `POST /jobs/{id}/resume` or `/cancel`.

M13 adds two more pauses with the same mechanics (no new host starts, running hosts finish, the reason is shown, resume or cancel): a failed **canary** ("Canary web-1 failed: …. 13 hosts have not started; resume to continue the rollout, or cancel") and the **failure threshold** ("Paused after 2 failures (the failure threshold is 2). 13 hosts have not started; resume to continue, or cancel"). Before M13 the threshold skipped the remaining hosts; it now pauses, for manual jobs too. Resuming renews the allowance (the threshold counts failures after the resume); failed hosts stay failed. Canary and threshold pauses record a `job.paused` event (§30.11).

A failure is counted (`jobRun.failures`) *before* the host's failed state is written (M14 fix): the dispatcher checks the threshold after taking a slot, and a host finishing successfully in the window between the write and the count could otherwise start one more host past the threshold (seen under `-race` with parallel load).

### 11.6 Controller restart
On startup, every job in `running`/`pending` and every host in a non-terminal state is marked `interrupted` (outcome unknown). Nothing is resubmitted automatically. The UI offers "Re-run on interrupted hosts".

### 11.7 Output
- Output stays in SQLite for the alpha (decision D1). Each host/step buffers output in memory and flushes one row when the buffer reaches **64 KiB** or **250 ms** have passed, whichever comes first. Rows are written through the single writer. We benchmark this in M2 and move to files only if measurements show write contention or database growth is a problem.
- Per-host per-step cap (default **10 MB**, combined streams). After the cap: drain and discard, set `truncated=1`, and append a marker line.
- Live view: SSE (`GET /jobs/{id}/events`) sends state changes and output chunks. Clients resume with `Last-Event-ID`.

### 11.8 Status poller
- Every **5 min** per ready host (jittered): reachable, `boot_id`, uptime, load, memory, max filesystem %, failed units count, reboot required. Takes a shared lock and is skipped if the host is exclusively locked.
- Every **6 h**: update counts from the local cache, **without** refreshing (a refresh happens once a day, or within minutes when the count is unknown). After any job with package, system-update, service or reboot steps, the host's facts, status and updates are re-checked immediately.
- Work is dispatched as soon as a poll worker frees up, not only on the 15 s scheduler tick, so throughput is bounded by the workers.
- Polls are not jobs (no job history), but status transitions emit events (e.g. reachable → unreachable).
- **Polling yields to jobs (decision D6):** polls use a separate, small worker budget (default 4) and run only when job workers aren't saturated. They never wait on a host lock: if a host is locked, the poll is skipped. Under load, poll data goes stale rather than competing with jobs. `host_status.checked_at` drives a **"stale"** indicator in the UI (older than 3× the interval).
- A changed `boot_id` outside our own reboots is recorded as an event ("web01 rebooted").

---

## 12. Security Details

### 12.1 Auth
- Passwords: argon2id (m=64 MiB, t=3, p=2), with a minimum length of 12.
- Session cookie: random 256-bit value, `HttpOnly; Secure; SameSite=Strict`. Idle timeout 12 h, absolute 7 d.
- CSRF: SameSite=Strict **plus** a required `X-DROOL-CSRF` header carrying a token from `/auth/me` on state-changing requests.
- API tokens: `Authorization: Bearer drool_<id>_<secret>`. Only the hash is stored. Shown once.
- Login rate limit: 5 failures per username per 15 min, plus per IP. All attempts audited.

### 12.2 RBAC (alpha roles)

| Permission | admin | operator | auditor | viewer |
|---|---|---|---|---|
| hosts.read, jobs.read | ✓ | ✓ | ✓ | ✓ |
| hosts.write, hostkeys.approve | ✓ | | | |
| jobs.execute | ✓ | ✓ | | |
| credentials.use | ✓ | ✓ | | |
| credentials.manage | ✓ | | | |
| audit.read | ✓ | | ✓ | |
| users.manage, settings.write | ✓ | | | |
| files.read (browse, download, view text, deployment history) | ✓ | ✓ | | |
| files.write (upload, edit, mkdir, rename, delete, chmod, chown) | ✓ | ✓ | | |
| files.deploy (artifacts, deployment preview; running one also needs jobs.execute) | ✓ | ✓ | | |
| files.elevate (any file action with administrator privileges) | ✓ | ✓ | | |
| schedules.read, workflows.read (M13) | ✓ | ✓ | ✓ | ✓ |
| schedules.manage, workflows.manage (M13; running a schedule also needs what its steps need) | ✓ | ✓ | | |
| schedules.admin (M13: change anyone's schedule, reassign owners) | ✓ | | | |
| desired.read (M14: baselines, effective state, drift, history) | ✓ | ✓ | ✓ | ✓ |
| desired.restore (M14: refresh, restore preview and restore; also needs jobs.execute and the operations' own permissions) | ✓ | ✓ | | |
| desired.manage (M14: baselines, group priorities, Accept current state, host declarations) | ✓ | | | |
| desired.auto (M14: enable/disable automatic restore) | ✓ | | | |

File access is withheld from auditor and viewer because file contents are frequently secrets. Rollback needs `jobs.execute` plus `files.deploy` (and `files.elevate` for elevated deployments), checked on the generated job like any other.

Checks are written against permissions, not role names, so custom roles later are a data change only.

### 12.3 Redaction
- slog `ReplaceAttr` redacts known keys (`password`, `token`, `authorization`, `private_key`, ...).
- **Job output** is scanned for every secret value registered during that operation (e.g. sudo password) before storage and streaming, and matches are replaced with `«redacted:sec_…»`.
- Users can add their own regex patterns (settings).

### 12.4 TLS
- Default: listen on `:8080` with HTTPS using an auto-generated self-signed certificate (fingerprint shown on the console at first start).
- `tls.mode: provided` (cert/key paths), or `tls.mode: off` for a reverse proxy on the same machine.
- **Decision D7:** with `tls.mode: off`, the server refuses to start unless bound to a loopback address. Configuring `trusted_proxies` does **not** make a non-loopback plaintext listener acceptable. Running one requires an explicit `server.allow_insecure_plaintext: true`, which logs a security warning at startup and shows a persistent banner in the UI.
- `trusted_proxies` only controls whether `X-Forwarded-For`/`X-Forwarded-Proto` are honored.

### 12.5 First-admin bootstrap (setup claim)

Goal: `docker compose up -d` → `docker compose logs drool` → open `/setup` → dashboard, with no CLI step (DESIGN §2.5).

- **While no administrator exists**, each startup issues a fresh **6-digit code** (crypto RNG) valid for `setup.code_ttl` (default 60 min). The code replaces any earlier generated code. It's printed once in the startup banner on stderr (which `docker compose logs` shows) and is **never written to logs, audit or the database**. Only its SHA-256 hash is stored (`setup_codes`).
- **Operator-provided secret** (automation, Docker secrets): `DROOL_SETUP_SECRET` or, preferably, `DROOL_SETUP_SECRET_FILE`. At least 12 characters (shorter refuses to start), no expiry, hash-only storage, never displayed. When set, no code is generated.
- **`/setup` page** asks for setup code, admin username, password and confirmation. `POST /api/v1/setup/claim` creates the admin, **deletes every setup code**, writes the `setup.claim` audit record, and creates a session **in one transaction**, so concurrent claims yield exactly one admin. The new admin lands on the dashboard already signed in.
- **Closed for good** once any admin exists: claims return `409 setup_closed`; startup and `drool user create --role admin` delete leftover codes.
- **Rate limits** (failed codes only; field validation errors don't count): 5 per source IP and 20 globally per 15 min. With a 60-minute code that's at most ~80 guesses, about a 0.008% chance of guessing a 6-digit code. Codes don't regenerate on their own, so an abandoned, unclaimed controller becomes unclaimable after the TTL.
- **New code:** restart, or `drool setup code` (works while running, including `docker compose exec drool drool setup code`). It invalidates the previous generated code.
- **Audit:** every attempt is recorded as `setup.claim` with result and reason (`invalid_code`, `rate_limited`, `closed`, `validation`), never the code. Issuing a code via CLI records `setup.code_issue`.
- **Recovery:** `drool user create` remains available.
- Known limitation: inside a container the banner can't know the host's address and prints `https://<this-host's-address>:8080`.

---

## 13. REST API

Conventions: JSON; `/api/v1`; cursor pagination (`?limit=50&cursor=…`); errors as RFC 9457 `application/problem+json`, extended with `error_class`, `retryable` and `request_id`; `Idempotency-Key` header accepted on `POST /jobs`. Every response carries `X-Request-Id`.

```
Setup (public; only while no admin exists)
  GET    /setup/status
  POST   /setup/claim               {code, username, password} → admin created + session

Auth
  POST   /auth/login                 {username, password} → session cookie
  POST   /auth/logout
  GET    /auth/me                    user, permissions, csrf token

Users / tokens
  GET|POST          /users
  GET|PATCH|DELETE  /users/{id}
  GET|POST          /tokens
  DELETE            /tokens/{id}

Secrets (metadata only)
  GET|POST          /secrets
  GET|PATCH|DELETE  /secrets/{id}
  GET               /secrets/controller-ssh-key        public key + authorized_keys line

Hosts
  GET|POST          /hosts                             ?q= &tag= &group= &status= &os_family=
  GET|PATCH|DELETE  /hosts/{id}
  POST              /hosts/{id}/hostkeys/scan          → offered keys + fingerprints
  POST              /hosts/{id}/hostkeys/approve       {key_id} or {fingerprint}
  GET               /hosts/{id}/hostkeys
  POST              /hosts/{id}/probe                  → ProbeResult (step-by-step)
  POST              /hosts/{id}/refresh                collect facts and status now
  GET               /hosts/{id}/facts                  (status is part of GET /hosts/{id}; job history is GET /jobs?host=)

Groups
  GET|POST          /groups
  GET|PATCH|DELETE  /groups/{id}                       PATCH {host_ids} replaces members

Jobs
  POST   /jobs/preview               spec → resolved targets, skips (when), validation errors
  POST   /jobs/simulate              {targets, step} → per-host package plan, removals needing approval (dry run, §10.2)
  POST   /jobs                       spec → job
  GET    /jobs                       ?status= &host= &created_by=
  GET    /jobs/{id}                  job + summary counts per state
  GET    /jobs/{id}/hosts            per-host state
  GET    /jobs/{id}/hosts/{hid}      steps + results
  GET    /jobs/{id}/hosts/{hid}/output?step=&after=&stream=  JSON segments; ?format=text downloads raw
  GET    /jobs/{id}/events           SSE
  POST   /jobs/{id}/cancel
  POST   /jobs/{id}/resume
  POST   /jobs/{id}/rerun            {hosts: "failed" | "interrupted" | [ids]}

Files (M6; ?elevated=true needs files.elevate)
  GET    /hosts/{id}/files                 ?path=  → real path, parent, entries, caps
  GET    /hosts/{id}/files/stat            ?path=
  GET    /hosts/{id}/files/content         ?path=  streamed download
  PUT    /hosts/{id}/files/content         ?path= &overwrite=  streamed upload (octet-stream)
  GET    /hosts/{id}/files/text            ?path=  → content + sha256 (UTF-8, ≤ 1 MiB)
  PUT    /hosts/{id}/files/text            {path, content, expect_sha256, force}
  POST   /hosts/{id}/files/mkdir|rename|delete|chmod|chown
  GET    /hosts/{id}/managed-files         latest deployment per path
  POST   /artifacts                        ?name=  streamed body → {sha256, size, is_text}
  GET    /artifacts/{sha}  ·  GET /artifacts/{sha}/content
  POST   /file-deployments/preview         {targets, step} → per-host state + diff
  GET    /file-deployments                 ?host= &path= &job=
  GET    /file-deployments/{id}            with diff, can_rollback
  POST   /file-deployments/{id}/rollback   → job

Audit / system
  GET    /audit                      ?actor= &action= &target= &since=
  GET    /system/info                version, schema version, config sources
GET /health  GET /ready  GET /metrics   (unversioned)
```

`api/openapi.yaml` is the contract. The UI client is generated from it (`openapi-typescript`), and CI fails if the handlers and the spec drift apart (route/schema conformance test).

---

## 14. Web UI

**Stack:** React + TypeScript + Vite, TanStack Query (server state), React Router, Tailwind with a small set of local components, generated API types. Built into `web/dist` and embedded with `go:embed`. The visual design is deliberately plain for the alpha.

M15 reorganized the navigation and several pages (§32); the table describes the UI as it is now.

| Page | Contents |
|---|---|
| Setup | One-time first-admin claim: setup code, username, password, confirm (§12.5) |
| Login | — |
| Overview (`/`) | Fleet triage: tiles for hosts, alerts, runs, desired state and firewall (each "Loading…" until known), *Needs attention* across areas (critical alerts, paused runs, desired-state conflicts, failed restores and drift, paused automatic restore, firewall drift and refusals, failing notifications, per-host problems), alerts, runs, updates and reboots, unresolved security posture, event feed |
| Hosts | Filterable table (name, address, OS, health, managed via, connection, updates, reboot, tags); one card per host on phones |
| Add host (wizard) | 1) address, port, user, jump host · 2) credential (shows controller public key + copy snippet) · 3) **host key scan → compare fingerprint → approve** · 4) probe with step-by-step results · 5) discover |
| Host (`/hosts/:id`) | Triage: identity, status, quick actions; status help, maintenance and host-key approval in plain sight; *Needs attention* for the host; Health (reachability, monitoring tiles, updates, reboot); Management (path, SSH connection, pinned key, packs, tags, Shepherd problems; Shepherd details on demand); summaries of alerts, desired state, firewall, network activity, recent runs and deployed files, each linking to its page; system facts (filesystems, addresses and interfaces on demand); danger zone |
| A host's areas | Monitoring (`/hosts/:id/monitoring`), Desired State (`…/desired`), Firewall (`…/firewall`: mode, environment, enforcement, effective policy, observations, transactions, generated nftables), Network Activity (`…/network`: collection, listening sockets, snapshot, connections), Files (`…/files`); breadcrumbs back to the host |
| Packs (`/groups`) | Host groups: create, members (linked), delete, monitoring overrides, desired-state baselines and priority, links to their alerts, a run, network activity |
| Shepherds (`/agents`) | DROOL agents: enrolled agents, enrollment tokens, updates and rollouts, trust (PKI), controller address |
| Files (M6) | Per host: current path and breadcrumbs, go-to-path, **Use administrator privileges** toggle with a warning banner, table (name/type, size, owner:group, `rw-r--r--` + `0644` with a plain-language tooltip, modified), upload with progress and cancel, download, new folder, rename/move, delete (one confirmation), permissions editor (checkboxes + octal + description), owner/group; text editor with conflict handling |
| Deploy file (M6) | New-run kind: upload artifact, destination, overwrite policy, owner/group/mode, elevation, validation, service action; per-host preview with diffs; results with before/after, validation, separate service result and Roll back; the host page lists files deployed by DROOL |
| Runs (`/jobs`) | Every job, newest first, with origin (schedule, workflow, Desired State correction); `?host=` for one host's runs |
| New run | Command / Script / Packages / System update / Service / Reboot / Deploy file forms, "Preview changes" for package operations, targets (hosts, packs, tags) with a live preview (skips shown), rollout settings, **one confirmation stating the target count and the consequence** (removals, reboots). Host and pack pages link here pre-filled |
| Run detail | Summary bar (succeeded / failed / skipped / ...), per-host table updating live, host drawer with step results, stdout/stderr tabs, truncation notice, error class explanation and retry hint; Cancel / Resume / Re-run failed |
| Schedules, Workflows, Desired State, Monitoring, Alerts, Network Activity, Firewall | §30, §31, §27, §28, §26, §24–25 |
| Credentials | List (metadata), add SSH key / password / sudo password, controller public key |
| Audit Log | Filterable event list |
| Settings | Account (profile, appearance, password), API tokens, users, notifications, system (version, config value sources, backup commands). Monitoring settings live under Monitoring (`/monitoring/settings`) |

Error explanations are a frontend table keyed by `error_class` plus server `detail`. For example, `PackageManagerError` with "locked by unattended-upgrades" is shown as "Package manager is locked by unattended-upgrades. Retryable: yes".

---

## 15. Configuration

```yaml
# /etc/drool/config.yaml
server:
  bind: 0.0.0.0
  web_port: 8080
  tls: { mode: self_signed }        # self_signed | provided | off
  trusted_proxies: []
data_dir: /var/lib/drool
setup:
  code_ttl: 60m
  # secret_file: /run/secrets/drool_setup   # optional operator-provided setup code
engine:
  global_concurrency: 20
  per_jump_host_concurrency: 10
  output_cap_bytes: 10485760
poller:
  status_interval: 5m
  updates_interval: 6h
retention:
  jobs_days: 30
logging:
  level: info
  format: json                       # json | text
  output: file                       # file | stdout
  rotation: { max_size_mb: 25, retain_files: 10, compress: true, retain_days: 30 }
```

Environment overrides: `DROOL_SERVER_WEB_PORT`, and so on (path → upper snake case). `/system/info` reports where each effective value came from.

---

## 16. Logging

- `log/slog` JSON handler. Streams are attributes: `stream=app|job|audit|security`. Each stream optionally writes to its own file, rotated with lumberjack.
- Every log line in request scope carries `request_id`. Engine lines carry `job_id`, `host_id` and `op`.
- Security stream events: host key mismatch, auth failures, rate-limit trips, secret access.

---

## 17. Backup and Restore

- `drool backup [-o FILE] [--include-master-key]`: safe while the controller runs. Writes a gzip-compressed tar (`.tar.gz`; readable with standard tools) containing:
  - `manifest.json`: format and version, DROOL version and commit, schema version, creation time, source hostname, whether the master key is included, the master key id (a non-secret fingerprint), credential count, row counts, SHA-256 and size of every file, and the reserved `state_epoch`/`state_revision` fields (null in the alpha)
  - `drool.db`: a consistent snapshot (`VACUUM INTO`)
  - `tls/self-signed.*` when the self-signed TLS mode is used (provided certificates are the operator's files and are not copied; the manifest says so)
  - `config/drool.yaml`: the config file in use, if any, with `setup.secret` blanked
  - `master.key` only with `--include-master-key`
  - `artifacts/sha256/<ab>/<sha256>` (M6): every stored artifact, so deployment history and rollbacks survive a restore. Restore checks each artifact's content against its name and swaps the whole `artifacts/` directory (the previous one goes to `pre-restore-<time>/`). A backup reports artifacts it couldn't read as warnings
  The default output is `<data_dir>/backups/drool-backup-<time>.tar.gz` (0600); `-o -` writes to stdout. The CLI states plainly whether the backup is self-contained: without the key it is **not** a complete disaster-recovery backup, and it says which key file must be kept with it. It also checks that the installed key actually opens the stored credentials.
- `drool restore FILE` (`-` reads stdin) refuses to run while the controller holds the data-directory lock. It extracts into a staging directory and validates before touching anything: known member names only, regular files only, checksums and sizes against the manifest, format version, SQLite `integrity_check`, schema version (newer than the binary → refused), then runs migrations forward on the staged copy. It finds a master key that opens every stored credential (the backup's, `--master-key FILE`, or the one already installed) or refuses with an explanation; `--discard-credentials` instead restores everything else and deletes the credentials (hosts keep their keys and history and need a credential assigned again). Sessions and setup codes are cleared, the restore is recorded in the restored audit log, and only then are the live files moved to `<data_dir>/pre-restore-<time>/` and the restored ones moved in; a failure while swapping puts the previous files back.
- `serve` refuses to start when `master.key` is missing but the database holds credentials, or when the key doesn't open them, instead of silently generating a new key. The error explains the recovery path.
- In the alpha, restore has no reconciliation concerns (no managed state domains yet).

---

## 18. Testing

| Layer | Approach |
|---|---|
| Parsers (os-release, apt/dnf output, systemctl show, ss, df) | Table-driven unit tests with captured, sanitized fixtures from each distro |
| Engine | Fake executor with scripted results/latency/errors: retries, circuit breaker, locks, cancellation, failure_threshold, restart → interrupted, redaction, output cap |
| SSH | In-process `x/crypto/ssh` test server: host key mismatch, algorithm pinning, auth failures, jump dialing, simulated reboots (new boot id, no reboot, new host key) |
| Backup/restore | Round trip with and without the master key, wrong key, discarded credentials, corrupt/truncated/incomplete/tampered archives (installation must be unchanged), newer schema, running controller, config redaction |
| API | Conformance against `openapi.yaml`; authorization matrix (every route × anonymous and every role, CSRF on every state-changing route, session-only routes reject tokens); token ownership |
| Integration (`make test-integration`) | The real `drool` binary driven over its REST API against unprivileged containers (sshd only): Debian 12/13, Ubuntu 22.04/24.04/26.04, Rocky 9/10, AlmaLinux 9/10, Fedora 44. Connection test, discovery, commands and scripts, passwordless/password/wrong-password sudo, package operations and their errors, dpkg lock, interrupted dpkg, system updates, jump host (key approval, operations, cancellation, changed jump key) |
| Fault injection | Host down before the operation, connection lost during it, cancellation of root processes, output beyond the cap, controller crash mid-job, authentication failure, changed host key |
| Files (M6) | Unit: path/mode/diff helpers, artifact store (integrity, GC), filedeploy sequence with a fake FS, SFTP and elevated scripts against the in-process SSH server (atomic writes, zero bytes, 24 MiB, cancel and broken uploads, conflicts, symlinks, unsafe directories, validation). Integration on Debian and Rocky containers: browse, upload/download incl. zero-byte and 256 MiB with a controller memory check, cancelled upload and killed host mid-upload (destination unchanged), mkdir/rename/move/delete/chmod, symlink refusal, permission denied, NOPASSWD/password/wrong-password sudo, unsafe directory, text-edit conflict, jump host, changed host key, audit without content; deployment by tag and group with mixed success, no-op, preview states, validation pass/fail, rollback (content and metadata, refused after a later change, removal of a created file), service failure after install. VM: deployment + sshd reload and unit restart on real systemd |
| Scheduling (M13) | Injected clock against the real engine and real maintenance (DST, durability with crash hooks, missed/late/overlap, ownership, windows); real-process `TestSchedulerRestart` (SIGKILL mid-burst, an outage across due times); opt-in `TestSchedulerScale` (§30.14) |
| Desired state (M14) | An in-memory fleet (units, packages, files, failures) behind the real engine, filedeploy and maintenance with an injected clock (§31.13); API tests over real SSH; container `TestDesiredState` (APT and DNF); `TestVMDesired` on a Debian TCG VM (real systemd, packages and files over SSH and the agent); opt-in `TestDesiredScale` (§31.14) |
| systemd and reboots (`-vm`) | Cloud-image VMs under qemu with software emulation (TCG) inside an unprivileged container (no `--privileged`, no `/dev/kvm`): real service operations and their errors, reboots (direct, through a jump host, onto a changed host key), reboot-required after a kernel install, needrestart, cancelling a live transaction, unattended-upgrades contention. `TestVMPlatform` runs the SSH, SSH-certificate, Shepherd and firewall suites on one VM per supported distribution and on an emulated arm64 guest (§34.11) |
| Release qualification (`-release dist/<version>`, M17) | The release's own controller binary and Shepherds under the whole integration suite, plus the artifacts' contents and signing workflow, the image with its `compose.yaml`, the packages on every supported distribution (containers) and under real systemd (VMs), rootless Docker and Podman (VM), upgrades from v0.1.0-alpha and M16 and back, older Shepherds through a restore and an update (§34.11, RELEASING.md) |

Privileged containers are not used for testing (they interfered with the developer's desktop session; §22, M3).

---

## 19. Forward Compatibility Hooks

| Future | Alpha preparation |
|---|---|
| Agent (Alpha 2) | **Done in M7** (§23) |
| Agent file access | `files.FS` is the transport contract; `Caps.AllowedPaths` carries the agent's `files.allowed_paths` as a ceiling the controller may narrow, never widen; `Caps.RootEquivalent` drives the "root-equivalent" label for unrestricted access. `filedeploy` and the API depend only on `files.FS` |
| Multi-hop | `host_hops` table; dialer recursion is written but limited to one hop |
| Firewall Beta | **Done in M8** (§24); container/Docker strategies and the WireGuard backend reuse the backend-independent compiler (`internal/firewall/compile`) |
| Desired state | Idempotent ops already return `Changed` with before/after states |
| YAML | Firewall policy YAML **done in M9** (§25); other domains: canonical Go types + generated JSON Schema |
| Hash-chained audit | `prev_hash` column reserved |
| PostgreSQL | sqlc, portable SQL, writer abstraction |

---

## 20. Build Order

| Milestone | Deliverable |
|---|---|
| **M0 Skeleton** | Repo, config with source tracking, slog and rotation, SQLite with migrations, users/sessions/CSRF, audit writer, health endpoints, embedded empty UI shell, CI |
| **M1 SSH core** | Secret Box, controller key, host CRUD, host key scan/approve/pin, jump dialing, pool, Probe, add-host wizard |
| **M2 Engine** | Jobs, target resolution, per-host state machine, command/script ops, output storage + SSE, locks, retries, circuit breaker, cancel, interrupted-on-restart, job UI |
| **M3 Inventory** | discover op, facts and addresses, status poller, dashboard |
| **M4 System ops** | APT + DNF backends, package / system_update / service / reboot ops |
| **M5 Alpha release** | Backup/restore, integration matrix green, error explanations, docs, tag `v0.1.0-alpha` |
| **M6 Files** | File manager, artifact store, managed file deployment with preview/validation/rollback (§10.6, §10.7) |
| **M7 Agent** | `drool-agent`, enrollment, PKI, agent Driver and `files.FS`, management router with SSH fallback, lifecycle, self-update and rollouts (§23) |
| **M8 Firewall** | Policy model and compiler, nftables backend, coexistence refusal, dead-man transactions (agent + SSH), management-path protection, sets, drift, learning, simulation, Explain (§24) |
| **M9 Firewall Designer** | Canonical document + YAML, Topology/Rules/YAML views over one draft, management-path overlay and warnings, draft simulation/Explain, layout, theme (§25) |
| **M10 Network monitoring** | One flow model over the M8 store, conntrack collection with socket-sampling fallback, process attribution, policy correlation by the compiler, local GeoIP/ASN enrichment, host and fleet views, observed topology, drafts from observations (§26) |
| **M11 System monitoring** | Shared collectors (agent continuous, SSH polled), agent resource budget and degradation, memory-only buffering, tiered history, services, sensors and GPUs, storage health and the flash probe, health evaluation, helper installation, bulk conversion, host and fleet views (§27) |
| **M12 Alerts** | Alert occurrences on issue transitions, acknowledgement, maintenance, destinations (SMTP, webhook, Discord, Slack) with tests, default and filter routes, durable outbox with retries, event timeline, Alerts/maintenance/notification UI (§28) |
| **M12.1 Health overrides** | Sparse global/group/host/subject overrides of M11's health checks, group priorities and conflicts, stable subject identities, provenance API and UI, impact preview, GPU utilization health, truthful configuration resolutions carried into M12 (§29) |
| **M13 Scheduling** | Recurrence with explicit zones and DST rules, durable unique occurrences created with their jobs, missed/late/overlap policies, owner re-authorization, engine waves (canary, batches, threshold pause), workflows with pinned revisions and templates, controller-side checks, recurring maintenance windows and maintenance-aware runs, Jobs → Runs / Schedules / Workflows (§30) |
| **M14 Desired state** | Baselines with revisions and impact preview, precedence with provenance and conflicts, observation with proof, Accept / Restore as ordinary jobs, automatic restore (§31) |
| **M15 UI/UX** | Navigation, search, page triage, states, responsive shell (§32) |
| **M16 Hardening** | Restore reconciliation, JIT SSH, re-authentication and two-factor, audit chain and remote copy, backup/upgrade/key safety, compatibility, crash and soak tests, container hardening (§33) |

---

## 21. Resolved Decisions (v0.2)

| # | Question | Decision |
|---|---|---|
| D1 | Job output storage | SQLite, buffered and flushed at ~64 KiB / 250 ms; existing cap and retention kept. Benchmark in M2; move to files only if measurements require it (§11.7) |
| D2 | PTY | Off by default; per-host `ssh_needs_pty` only (§8.4) |
| D3 | APT upgrade semantics | `upgrade` → `apt-get upgrade`; `full_upgrade` → `apt-get full-upgrade` with removal preview, and `allow_removals` required to proceed when packages would be removed (§5.1, §10.2) |
| D4 | Debian/Ubuntu security-only | Deferred. Security updates are detected and displayed only; DNF uses native `--security` (§10.2) |
| D5 | Remote cancellation | Private `mktemp -d` run directory under `$XDG_RUNTIME_DIR` → `/run/user/$UID` → `/tmp`; no predictable names (§9) |
| D6 | Status polling under load | Separate small budget, yields to jobs, skips locked hosts, UI shows stale data (§11.8) |
| D7 | TLS default | Self-signed HTTPS. `tls.mode: off` needs loopback, or an explicit `allow_insecure_plaintext`; `trusted_proxies` alone is not enough (§12.4) |
| D8 | Jump hosts | Must be normal inventory hosts; no inline definitions (§8.1) |

The design is frozen. Changes from here on come only from implementation blockers, contradictions or security issues, and are recorded in §22.

## 22. Implementation Decision Log

Meaningful choices made during implementation, newest last.

| Milestone | Decision | Rationale |
|---|---|---|
| M0 | Go module path is `drool` | It's an application, not a library. Rename once hosting is decided |
| M0 | Standard-library `net/http.ServeMux` with method patterns; no router dependency | Enough for our routing; fewer dependencies |
| M0 | ~~The first administrator is created with `drool user create` (CLI), not a web setup page~~ **Superseded** by the setup claim below | — |
| M0 | `POST /tokens` requires an interactive session | A leaked API token must not be able to mint more long-lived tokens |
| M0 | Self-service password change at `POST /auth/password` (requires the current password); admins reset via `PATCH /users/{id}`. Password changes revoke the user's other sessions | Standard account hygiene |
| M0 | CSRF token stored per session (`sessions.csrf_token`), returned by login and `/auth/me`, required in `X-DROOL-CSRF` on session-authenticated state-changing requests | Implements §12.1; bearer-token requests are exempt |
| M0 | Login rate limiter is in memory (reset on restart). Locking out a known username is possible (5 failures → 15 min) | Acceptable for a single-instance alpha; revisit alongside MFA in 1.0 |
| M0 | A disabled user's login returns the same error as a bad password; audit records `disabled` | Don't reveal account state to unauthenticated clients |
| M0 | New permission `settings.read` (admin, auditor) gates config values and sources in `/system/info` | Config can reveal topology; viewers don't need it. Extends the §12.2 table |
| M0 | Audit actor type `anonymous` added for failed logins | Failed logins have no authenticated actor |
| M0 | If the audit write for a successful login fails, the session is revoked and login fails | Never grant a session we couldn't audit |
| M0 | ~~**Known gap:** for other mutations, the audit record is written after the change in a separate statement~~ **Fixed in M1** | — |
| M0 | `/metrics` is unauthenticated on the main listener | No sensitive labels (route patterns, status codes); a separate listener can come later if needed |
| M0 | HSTS only when `tls.mode=provided` | HSTS on a self-signed cert can strand users who later switch to a reverse proxy or another hostname |
| M0 | No HTTP `WriteTimeout` | M2 job output uses long-lived SSE streams; `ReadHeaderTimeout` and `IdleTimeout` are set |
| M0 | Migration `0001` contains only M0 tables; hosts, secrets and jobs come in later migrations. Until the `v0.1.0-alpha` tag, existing migrations may still be amended; after it they are strictly forward-only | Nothing is deployed yet |
| M0 | Frontend TypeScript pinned to 5.x | `openapi-typescript` needs the TypeScript JS compiler API, which TypeScript 7 (native port) doesn't ship |
| M0 | Frontend: React 19, React Router 8, TanStack Query 5, Tailwind 4, `openapi-fetch` with generated types. Visuals deliberately minimal: semantic CSS tokens + a few primitives | Easy to restyle after the planned UI review |
| M0.1 | **First-admin bootstrap is a one-time setup claim at `/setup`** with a code from the startup logs (or an operator-provided secret); `drool user create` stays as recovery. Details in §12.5 | DESIGN §2.5: setup must not need a CLI step. The race concern from the original decision is handled by requiring the console code, single use, expiry and rate limits |
| M0.1 | Setup codes are 6 digits, hashed with SHA-256 (not a slow hash) | Short codes can't be protected by any hash against an attacker holding the database; their protection is the short lifetime, single use and online rate limits. A DB reader before setup could equally just wait for the claim |
| M0.1 | Audit records can now be written inside the caller's transaction (`audit.Writer.RecordTx`); the setup claim uses it | Start of fixing the M0 known gap; remaining mutations are converted at the start of M1 |
| M0.1 | CLI subcommands persist audit records to the database only, without mirroring to the log stream | With `logging.output=stdout` (containers), mirrored JSON polluted command output such as `drool setup code` |
| M0.1 | Added `Dockerfile` (distroless, non-root, `/data` volume, logs to stdout) and `compose.yaml` | Makes the documented `docker compose up -d` flow real; minimal, no healthcheck yet |
| M0.1 | Config keys ending in `.secret` are shown as `«redacted»` in `/system/info` | `setup.secret` must not be displayed |
| M1 | Every successful mutation commits together with its audit record (`api.Server.inTxAudited`); services expose `XxxTx(ctx, q, ...)` variants. Expensive work (password hashing) happens before the transaction. Failed attempts are audited separately. Logout and the login session/audit pair stay as before (login fails closed if its audit write fails) | Closes the M0 gap; covered by a rollback test |
| M1 | Host statuses `untested` (key approved, no test yet) and `unsupported` (reachable but not Linux/systemd) added | The original set had no state between approval and a successful test, and no way to express "reachable but unusable" |
| M1 | Changing a host's address or port retires its pinned key and returns it to `pending_hostkey`. Other settings changes set `untested` | A new endpoint may be a different machine; the old key proves nothing about it |
| M1 | Host key endpoints are `/hosts/{id}/hostkeys/scan` and `/approve`; approve accepts `key_id` or an expected `fingerprint` | Fingerprint approval supports "verify out of band, paste, done" without a second step |
| M1 | A probe that sees a changed key records the presented key as `offered` (never pinned) and logs to the security stream; a jump host's changed key is reported on the jump host, not on hosts behind it | Admin decides; no silent trust |
| M1 | New hosts default to the `controller-default` credential, and to `become: none` when the user is root | Simplest path: paste one public key; DESIGN §2.5 |
| M1 | Group membership is replaced via `PATCH /groups/{id}` with `host_ids` (no separate `PUT /members`) | One endpoint, same semantics |
| M1 | Imported encrypted private keys are decrypted once with the supplied passphrase and stored unencrypted inside the AES-GCM box; the passphrase is not stored | Encryption at rest is the box's job; storing passphrases would add a second secret with no benefit |
| M1 | `POST /hosts/{id}/probe` requires `jobs.execute`; credential metadata requires `credentials.use`; the controller public key requires only `hosts.read` | Operators can test connectivity and pick credentials; the public key isn't secret |
| M1 | Replacing a credential's value evicts pooled connections of every host using it | Otherwise pooled connections keep using the old credential until idle |
| M1 | Password authentication also answers keyboard-interactive prompts with the stored password | Many distros' sshd only offer keyboard-interactive for passwords |
| M1 | Pool: concurrent first requests share one dial (single-flight); a tunnelled connection holds a reference on its jump connection for its lifetime | Found in testing: bursts opened one connection per request, and an idle jump connection could be reaped under active tunnels |
| M1 | Verified against real OpenSSH (Debian 12 containers): scan/approve through a jump host, passwordless sudo, host key rotation → `hostkey_mismatch`, unauthorized key → `auth_failed`, no systemd → `unsupported`; UI pages rendered in headless Chromium without runtime errors | The in-process test server can't catch OpenSSH-specific behavior. Note: Debian images bake host keys at build time, so containers from one image share keys, like cloned VMs |
| pre-M2 | **Renamed to DROOL.** Binary `drool`, Go module `drool`, `/etc/drool/`, `/var/lib/drool/`, `DROOL_*` environment variables, `drool.service`, container image and compose service `drool`, token format `drool_tok_…`, cookie `drool_session`, CSRF header `X-DROOL-CSRF`, metrics `drool_*`, problem type URNs `urn:drool:problem:*`. CSS design tokens became brand-neutral (`--ui-*`, `rounded-ui`). API paths unchanged. No migration was needed: the schema contained no product-specific names | Pre-alpha; nothing deployed, so no compatibility shims or migration baggage |
| M2 | `when` conditions and OS-specific script bodies deferred to M3 | Both need discovered facts (`os_family`), which arrive with the discover operation in M3. Shipping them earlier would mean every `when` skips |
| M2 | Remote execution exactly per D5: a prepare session writes the script into a private `mktemp -d` directory, a run session executes it under `setsid -w` (pidfile = process group), cancel/timeout kills the group from a separate session (TERM, then KILL after 10 s), cleanup removes the directory | Stdin carries only the sudo password; scripts get an empty stdin |
| M2 | Remote scripts must be dash-compatible: dash's `kill` rejects `--` before a negative group id. Found on real Debian, where cancelled password-sudo jobs survived; fixed and guarded by a test. The M5 integration matrix must run remote scripts under dash | Local tests ran under bash and couldn't catch it |
| M2 | A controller shutdown does **not** cancel running jobs: remote commands keep running and in-progress hosts become `interrupted` on the next start (verified with a real job). Their run directories stay behind (usually tmpfs) | Matches §11.6 "outcome unknown"; killing on shutdown would turn an unknown outcome into a deliberate one |
| M2 | Retries: only connection failures *before* the command started are retried for command/script (3 tries total, 5 s doubling backoff, ±20% jitter). Auth, host key, sudo and execution failures are never retried | §11.4; non-idempotent operations must not run twice |
| M2 | Output API returns JSON segments by default (paged by `after`), raw bytes with `?format=text`. Within one flush window (≤250 ms) the relative order of stdout and stderr isn't preserved; each stream's own order is exact | The UI needs stream-tagged segments; per-stream buffering keeps redaction and batching simple |
| M2 | Redaction applies to the host's own credential values (login password, sudo password) and catches values split across writes | A value only needs hiding where it's actually a secret for that host |
| M2 | Circuit breaker: 5 consecutive host failures of the same class among Authentication/HostKey/Authorization pause the job (no new hosts start; running ones finish). Resume resets the counter. Failure threshold counts failed + timed-out hosts and skips the rest | §11.5 |
| M2 | SSE replay buffer is bounded per job (2000 events / 2 MiB); slow subscribers are dropped and clients receive `reset` and refetch over REST. Streams linger 5 minutes after a job ends | Bounded memory regardless of output volume |
| M2 | Jump-only hosts cannot be targeted: explicit selection is a validation error, group/tag matches are left out with a warning | They're not managed hosts |
| M2 | Idempotency keys are scoped per user; re-runs are new jobs linked by `rerun_of`, and `"failed"` selects failed, timed-out, cancelled, skipped and interrupted hosts | |
| M2 | New error classes: `CancelledError`, `LockTimeoutError`, and `InterruptedError` (stored on hosts after a restart) | |
| M2 | sqlc pitfall: mixing `?` with `sqlc.narg()` misnumbers parameters in SQLite and silently matches nothing; queries use positional parameters only | Found by engine tests |
| M2 | UI: the CSRF token is now set inside the `/auth/me` fetch rather than in an effect, because child components' first requests ran before the provider's effect on a fresh page load (found in headless-browser testing) | |
| M2 | Verified against Debian 12 OpenSSH containers behind a jump host: passwordless and password sudo, stderr/exit classification, cancellation of root process groups via both sudo modes (~1 s), interrupted-on-restart and re-run, live output in the browser | |
| M3 | Discovery is an inventory operation, not a job step: it runs automatically (new hosts, after a passing connection test, daily) and on demand via `POST /hosts/{id}/refresh`. A bulk "discover" job step is deferred | Avoids flooding job history with background collection |
| M3 | Discovery, status and update checks run as the login user without privilege escalation; only the daily package metadata refresh uses root, and only where root login or sudo is configured. Listening-port process names are limited to what that user can see | Read-only collection shouldn't need root (least privilege) |
| M3 | Update counts come from the local package cache only (`apt list --upgradable`, `dnf -C`) and are unknown until a cache exists. When a count is unknown on a host with root access, the poller refreshes metadata within ~1–2 minutes instead of waiting for the daily refresh | Found on fresh Rocky containers: counts stayed unknown for up to 30 minutes |
| M3 | Security updates: APT via `*-security` suites; DNF counts pending packages that have a security advisory (dnf4 `updateinfo`, dnf5 `advisory`) | Found on real Rocky 9: advisories list several NEVRAs per package, so counting advisory entries gave 163 security updates out of 106 pending |
| M3 | Reboot required: Debian/Ubuntu `/var/run/reboot-required`; RHEL family `needs-restarting -r` (dnf4 or dnf5) from the update check; fallback newest installed kernel vs. running kernel | |
| M3 | Unknown is never zero: unavailable values are null in the API and "unknown"/"—" in the UI; systemd-dependent facts degrade with a warning instead of failing the host | Conservative parsing per the M3 brief |
| M3 | Status checks keep host connection status current: unreachable ↔ ready transitions, a changed host key is recorded for review (never trusted), authentication failures are surfaced. Untested hosts are not promoted by polls | |
| M3 | Stale = last status check older than 3× the status interval (default 15 minutes); computed server-side | |
| M3 | Facts and metadata refresh run every 24 h and are not configurable; only the existing status/updates intervals and worker count are | Keep the configuration surface small (DESIGN §2.5) |
| M3 | `when` (os_family, distro) and per-OS script bodies resolve distro id, then family, then `default`; unknown facts never match. A host where no step applies is `skipped`; partially skipped hosts succeed with a note (job_steps has no skipped state) | |
| M3 | Discovered addresses are informational: never overwrite configured addresses, and are removed when no longer seen | DESIGN §5.2 convergence is out of scope |
| M3 | **Test environment:** real-distro tests use unprivileged containers (sshd only, no systemd). Privileged systemd containers crashed the developer's desktop session and must not be used on a workstation. systemd-dependent parsing is tested with synthetic fixtures in real `systemctl`/`ss` output format; validation against a real systemd host (VM) is still pending. Fixtures are sanitized of host-inherited data | |
| M3 | Verified against Debian 12, Ubuntu 24.04, Rocky 9 and Fedora 41 containers: automatic discovery within seconds of adding hosts, polled status, update and security counts, early metadata refresh via sudo, per-OS script variants and distro conditions in a real job, dashboard and host detail in headless Chromium | |
| M4 | Typed operations (package, system_update, service, reboot) always run as root: directly for root logins, otherwise via the host's sudo setting (passwordless or stored password). A host without either fails with `ConfigurationError` before anything runs | §5.1; avoids half-applied changes from a partly privileged run |
| M4 | Package operations are orchestrated in Go as separate steps (detect manager → versions → simulate → removal check → execute → versions → reboot-required), with only the real transaction going through the D5 run-directory runner. The backend interface is command-builder + parser per step rather than §10.2's script-per-operation | Each step's output is parsed separately and cancellation still covers the long-running part |
| M4 | Every package operation simulates first (APT `apt-get -s`, DNF `dnf -C --assumeno`), not just full upgrades. Removals beyond the named packages (remove) or any removal at all (install, upgrade, system update) need `allow_removals`; without it the host fails with `ValidationError` listing them, before anything changes | A named remove can take dependents with it (found: removing libcurl4 removes curl; removing libpsl on Rocky removes wget). Same rule as D3, applied consistently |
| M4 | Dry runs use a dedicated `POST /jobs/simulate` (permission `jobs.execute`) instead of extending `POST /jobs/preview`. Preview stays a fast target/condition check; simulate connects to each host (≤8 at a time, 3-minute budget), takes the host lock shared without waiting, and reports busy/skipped/error per host | Simulation needs SSH and, on dnf4, root, so it doesn't fit preview's cheap read-only contract |
| M4 | Simulation uses the host's current metadata (no refresh). A system update refreshes first by default, so the real run may include more updates than the preview; any new removals still stop the job unless allowed. The UI says so next to the preview | Refreshing on every preview would make it slow and would itself change the host |
| M4 | APT runs with `DEBIAN_FRONTEND=noninteractive`, `--force-confdef --force-confold` (keep local config files), `DPkg::Lock::Timeout=120`, and `NEEDRESTART_MODE=a` (Ubuntu's needrestart restarts affected services instead of prompting). The `--no-install-recommends` setting from §10.2 is not implemented; APT defaults apply | Fully non-interactive; matches unattended-upgrades behavior on Ubuntu 24.04 |
| M4 | Lock contention: apt itself waits up to 120 s for the dpkg lock; if it still fails, or dnf reports a lock, the error is `PackageManagerError{retryable}` and the engine retries with its normal retry budget (3 attempts by default), not §10.2's 5 attempts / 10 min | One retry policy for the engine; the in-apt wait covers the common unattended-upgrades case |
| M4 | DNF: dnf4 and dnf5 share command lines; the parser handles both table formats, wrapped names, and `replacing` rows (a removal only if the replaced package has a different name). Protected packages and unresolvable changes give `PackageManagerError` with the first lines of dnf's problem report | Found on Fedora 41: removing libpsl is refused because dnf5 itself depends on it |
| M4 | Named upgrade of a package that isn't installed is a `ValidationError` (APT `install --only-upgrade` would silently do nothing). Install/remove of an already-installed/absent package is a successful no-op with `changed: false` | |
| M4 | `changed` for packages = versions differ before/after or the plan was non-empty; for system updates = plan non-empty (or a reboot happened). Results store the full plan plus `changes` (named packages only), `reboot_required` and its reason | The plan shows collateral installs/removals that `changes` doesn't |
| M4 | Service operations: `systemctl show` before, only the needed transitions for start/stop/enable/disable, always act for restart/reload, then wait (≤10 s) for transitional states to settle and verify. Not-found, masked, reload-unsupported and enabling static/indirect units are `ServiceError`; a failure attaches `systemctl status -n 15` to the output. A host without systemd running gives `ConfigurationError` | |
| M4 | Reboot polls with fresh dials from 3 s doubling to a 15 s cap (§10.4 says 5 → 30 s): faster detection, and a poll is one SSH handshake. A different host key after reboot fails immediately with `HostKeyError`. Timeouts distinguish "never went away" (old boot id; reported `changed: false`) from "went down and did not come back". The job host ends `timed_out`; the host's own status is left to the poller | |
| M4 | After a job with typed steps releases a host, the engine asks the poller to re-check its facts, status and updates immediately | Found during E2E: after "Install updates" the host still showed 56 pending until the next scheduled check |
| M4 | Output redaction holds back only a tail that could be the start of a secret, instead of a fixed `len(secret)-1` bytes | Found during M4 E2E with password sudo: the end of a line was held until the stream's next write and appeared out of order (M2 bug) |
| M4 | UI: one New job page with Command / Script / Packages / System update / Service / Reboot. Packages and system updates have "Preview changes"; the "allow removals" box only appears after a preview has shown removals, and resets whenever the step or targets change. One confirmation per job, stating the consequence (removals, reboots). Quick actions: host page (Install updates, Reboot), per-service and failed-unit restart links, dashboard Updates tile → system update for hosts with pending updates | Simple by default; destructive effects are explicit without stacked prompts |
| M4 | **Validation gaps (need a systemd VM / M5 matrix):** service operations against real systemd units, real reboots (boot id change, pinned key after reboot, `systemd-run` trigger, jump-host reboots), needrestart behavior on Ubuntu, kernel updates and reboot-required on real hosts, cancelling a running dpkg/dnf transaction, unattended-upgrades holding the lock at boot. Covered so far by fake `systemctl` fixtures and the in-process SSH test server (simulated restart with new boot id, no reboot, changed host key) | Unprivileged containers have no systemd and can't reboot (§22 M3 test-environment rule) |
| M4 | Verified against Debian 12, Ubuntu 24.04, Rocky 9 and Fedora 41 containers: install, idempotent re-install, not-found, upgrade of a not-installed package, removals needing `allow_removals` (APT and DNF), protected-package refusal (dnf5), security-only refused on APT and applied on Rocky, real dpkg lock wait, interrupted dpkg with hint, simulate/full upgrade/upgrade with unchanged re-runs, update counts refreshed right after the job, passwordless and password sudo, "systemd is not running" for service ops, "never went away" reboot timeout; UI flows rendered in headless Chromium | |
| M5 | Backups are gzip-compressed tar (`.tar.gz`), not `.tar.zst` as first planned | No new dependency, and an archive anyone can inspect or unpack by hand with standard tools in a disaster |
| M5 | A backup records a non-secret master key id and checks that the installed key opens every stored credential; the CLI states on every run whether the backup is self-contained. Restore picks the backup's key, `--master-key`, or the installed key, and verifies it opens every credential before changing anything | A backup without the key must not pass for a complete disaster-recovery backup (DESIGN §7, §17) |
| M5 | `restore --discard-credentials` restores everything except stored credentials (hosts are detached from them and marked untested) | The only way back after losing `master.key`; without it a lost key would make the whole backup useless |
| M5 | `serve` refuses to start when `master.key` is missing but credentials exist, or when the key doesn't open them | Found during review: it silently generated a new key, making every stored credential undecryptable |
| M5 | `serve` holds an flock on `<data_dir>/drool.lock`; restore requires it | Restore must never replace files under a running controller; also stops two controllers sharing a data directory |
| M5 | Restore clears sessions and setup codes, records `backup.restore` in the restored audit log, keeps replaced files in `pre-restore-<time>/` (only when something was replaced), and saves the backup's config file as `drool.yaml.from-backup` without applying it | Config usually lives outside the data directory (`/etc/drool`, environment); overwriting it silently would be surprising |
| M5 | Provided TLS certificates are not copied into backups (the manifest names them); self-signed material is | They are the operator's files, often renewed externally |
| M5 | `setup.secret` is blanked in backed-up config files | It is useless after setup and shouldn't travel in backups |
| M5 | Database files are created and kept 0600; the systemd unit sets `UMask=0077` | Found in the security pass: `drool.db` (password hashes, encrypted credentials, audit log) was created 0644 |
| M5 | `job.simulate` is audited | A dry run executes commands on hosts, as root for dnf |
| M5 | A connection that drops after a command started is `ConnectionError` "outcome unknown" with advice to check the host; a command killed by a signal names the signal | Found by fault injection: it was reported as `ExecutionError: exited with status -1` |
| M5 | Named package operations refresh metadata once when a package isn't in the local cache (APT "not found", dnf "no cache") | Found on a fresh Ubuntu cloud VM: installing anything failed with "package not found" until the daily refresh |
| M5 | APT reboot-required falls back to comparing the newest kernel in `/boot` with the running one; discovery uses `/boot` too (`/lib/modules` only as a fallback) | Plain Debian has no `/var/run/reboot-required`; `/lib/modules` can keep directories of removed kernels |
| M5 | Poller: dispatches again as soon as a worker frees (was: only on the 15 s tick, ~16 checks/min with 4 workers); a new host's first status check runs immediately; an update check never runs before the first status row exists; failed stores retry within a minute (were rescheduled 6 h / 24 h out) | Found while investigating slow discovery in the integration matrix |
| M5 | Reboot timeouts are judged by the latest contact: a host that answered with the old boot id after a failed poll is "never went away" | Found as a flaky test under load: one slow handshake was reported as "went down and did not come back" |
| M5 | A missing `systemctl` binary is treated like "systemd is not running" (`ConfigurationError`) | Found in the integration matrix on minimal containers |
| M5 | Error hints reviewed for every class users can hit (connection timeout/DNS/no route, authentication, host key, sudo, lock, removals, reboot timeouts, no systemd); the host page explains every non-ready status and what to do; stale status is explained in the status card | M5 usability goal: actionable plain language, raw detail still available |
| M5 | Integration tests drive the real binary over the REST API with the Docker CLI rather than testcontainers-go; systemd/reboot tests run cloud images under qemu TCG in unprivileged containers | No new Go dependencies; nothing privileged on the workstation (§22 M3 rule) |
| M5 | Correction to the M4 entry on APT: with `NEEDRESTART_SUSPEND=1` needrestart does **not** run during DROOL's updates, so services are not restarted automatically (`NEEDRESTART_MODE=a` only prevents a prompt if suspend isn't honored). Kept that way and documented | Verified on a real Ubuntu 24.04 VM ("needrestart is suspended"). Restarting services silently during an update is a disruptive action the user should choose explicitly |
| M5 | The controller's own SSH key (`controller-default`) can't be deleted or renamed (409 `controller_key`); the Credentials page shows its install one-liner and no Delete button for it | Found in the usability pass: deleting it broke the add-host default, and the next start would generate a different key than the one installed on hosts |
| M5 | Editing a host resets its status to `untested` only when a connection setting changes (user, credential, sudo method or password, PTY, jump role or route); tags, groups, name and description don't | Found in the usability pass: tagging a host made it look broken |
| M5 | Script jobs are named after their first meaningful line (not the shebang or `set -e`); the target preview says "no systemd: service operations and reboots will fail" instead of "unsupported system" for hosts without systemd; the add-host wizard says "Connected" when systemd is the only failed check | Usability pass |
| M5 | The job page shows the first failed host's output (or the first host's) without a click | Usability pass |
| M5 | Restore explains a permission-denied `--master-key` (Docker runs restore as the container's unprivileged user) | Usability pass with the Docker image |
| M5 | VM tests run Debian/Ubuntu guests with `-cpu max,la57=off,cx16=off` and RHEL guests with `-cpu max,la57=off`; qemu's translation cache is capped at 256 MB | Test environment only: newer kernels (6.12+) panic under TCG with LA57 or with TCG's `cmpxchg16b` emulation, and the default cache made containers thrash at their memory limit. RHEL 9 requires x86-64-v2 (cx16) |
| M5 | **Verified on real systemd (Debian 12, Ubuntu 24.04, Rocky 9 cloud VMs):** service start/stop/enable/disable idempotent, restart/reload always act and are verified, enable+start, reloading sshd over DROOL's own connection, `ServiceError` for no reload support, static, masked, missing and failing units (with `systemctl status` attached); reboots with a new boot id (70–140 s under emulation), reboots through a jump host, reboot-required after a newer kernel (Debian backports, Ubuntu HWE, RHEL needs-restarting) and `reboot: if_required` booting into it, a regenerated host key after reboot refused with `HostKeyError`, cancelling live dpkg and rpm transactions mid-unpack (the next operation succeeded; `dnf check` clean), unattended-upgrades starting during a DROOL install (it backed off with "lock could not be acquired"), needrestart suspended on Ubuntu | M4 validation gaps closed |
| M5 | A kernel install that outlived its step timeout was killed with its process group and left dpkg interrupted; the next operation reported `PackageManagerError` with the `dpkg --configure -a` hint instead of repairing it. A VM whose new kernel panicked on boot left the reboot in `waiting_host` until its timeout | Observed during VM testing; behavior as designed |
| M5 | Commands on a connection that goes silent (the host vanished without closing TCP) return at their timeout: opening a session and waiting for a closed session are bounded, and after a 5 s grace the connection is closed to unblock | Found in the VM tests: a reboot poll hit the moment the guest powered off and hung forever, leaving the reboot "waiting for host" though the host was back. Fresh connections (reboot polls) have no keepalive to rescue them; pooled ones were only rescued by keepalive |
| M5 | `drool backup` records its audit event in the database only, like the other CLI commands | Found by the clean-install test of the release image: with stdout logging (the container default) the audit log line was appended to `backup -o -` archives, which other tools then rejected |
| M6 | File access for the login user uses SFTP on the existing pooled `sshx` connection; elevated access uses small typed `sh` scripts through the existing become path. There is no root SFTP server and no `sudo sftp-server` | Requirement: no fake sudo-SFTP and no second trust path. Typed scripts keep each root action narrow and auditable, and work with password sudo (a root SFTP server would need the password on the same channel as the protocol) |
| M6 | Elevated uploads first go by SFTP as the login user into a private `mktemp -d` directory, then a root script copies them next to the destination | File content never shares stdin with the sudo password, and a broken upload never reaches the destination's directory |
| M6 | Root writes, chmod and chown are refused in directories not owned by root or the login user, or writable by group/others (`unsafe_directory`) | Another user could swap the temporary file or the parent for a symlink between checks; refusing is simpler and safer than trying to make every step race-free there |
| M6 | Destructive and privileged actions never follow symlinks: a final-component symlink is refused, a path whose parent resolves elsewhere (`pwd -P`) is refused, chown uses `-h`, delete removes the link. The UI follows a symlink only on an explicit click | Requirement: no silent symlink following in destructive or privileged operations |
| M6 | Elevated scripts report errors as `@@ERR code message` lines and capture command errors with `$(...)`; no files at predictable paths | Found in review: the first version wrote chown errors as root to `/tmp/.drool-err-$$`, a symlink attack |
| M6 | Previous versions are captured into the controller's artifact store (≤ 64 MiB) instead of `.bak` files on the host | Backup files on the host would be picked up by include directories (`sudoers.d`, `cron.d`, `conf.d`) and leak old secrets; the controller copy is checksummed, survives host changes, and is in DROOL backups |
| M6 | Artifacts are content-addressed by SHA-256, capped at 1 GiB, verified on every read; GC removes artifacts unreferenced by any deployment row or job spec and unused for 24 h | Job JSON and audit carry hashes only (requirement); the 24 h grace covers an upload whose job hasn't been submitted yet |
| M6 | `file_deployments` rows are not deleted with pruned jobs; they store the deployment's settings (`op_json`, no content) so a rollback reuses its elevation, validation and service action | They are the managed file's history and keep rollback targets referenced |
| M6 | Rollback is a new job with `expect_current` = the deployed checksum; if the file changed since, it fails without touching it. A deployment that created the file rolls back by removing it | Rolling back over someone's later edit would silently destroy it |
| M6 | A service action failing after a successful install is a `ServiceError` with "the file was installed, but …", and the deployment row records `installed` plus the service result | Requirement: the user must know the live file changed even though the job failed |
| M6 | A deployment preview whose destination directory doesn't exist reports the host as inaccessible ("the directory … does not exist"); deployments never create directories. Filesystem errors in a deployment are `ExecutionError` (`AuthorizationError` for permission denied) | Found in the integration tests: the preview promised "would create" and the job then failed as `InternalError` |
| M6 | Auditor and viewer roles get no `files.*` permissions; elevated listings, reads and previews are audited, not only changes | File contents (keys, configs) are often secrets; reading them as root is a privileged act |
| M6 | Interactive file changes take the host lock in shared mode without waiting (409 `host_busy`); reads take none | Don't interleave with an exclusive job such as a package upgrade, but never block the browser |
| M6 | Uploads lift the HTTP server's 60 s read timeout for that request only (`http.ResponseController`) | Large uploads would otherwise be cut off; the global timeout still protects every other route |
| M6 | Cancelling an SSH session request no longer closes a healthy pooled connection immediately; it waits the 5 s unresponsive grace first | Found by the large-upload test: a cancelled request tore down the shared connection under concurrent SFTP transfers |
| M6 | `files.FS` + `Caps{AllowedPaths, RootEquivalent}` is the transport contract for the future agent; `filedeploy` and the API depend only on it. SSH sessions report `AllowedPaths = nil` (bounded by the login user and its sudo rules) and `RootEquivalent` when elevated | DESIGN §6/§13: the agent's `files.allowed_paths` is a ceiling the controller may narrow but never widen, and unrestricted file access must be labelled root-equivalent |
| M6 | Temporary files are hidden and extension-less (`.drool-tmp-XXXXXXXXXX`) in the destination's directory | Include directories ignore them while they exist (`sudoers.d` and `cron.d` skip names with dots; `*.conf` globs don't match), and same-directory staging keeps the final rename atomic |
| M6 | Usability pass (headless Chromium, all ten steps: browse, download, upload, edit, elevated `/etc` change, multi-host deploy, diff, validation, apply, rollback). Fixed: file row actions were `group-hover` only, which Tailwind v4 applies only on hover-capable devices, so touch devices (and the headless browser) never saw Download/Rename/Delete; actions are now always shown. Symlinks showed a meaningless `rwxrwxrwx 0777` (now "link (the target's permissions apply)"). Permissions get a one-line plain-language summary in the table, not only a tooltip. A permission-denied save offers an explicit **Save with administrator privileges** button instead of losing the edit. Preview labels use the requested state names (missing/different/inaccessible). The deployment preview rejected the UI's step (`unknown field "type"`): the endpoint now takes the same operation shape as a job step, with an API test. The sidebar showed `vv0.1.0-alpha` | |
| M7 | Agent protocol bodies for operations and file requests are the canonical JSON already used by jobs (`model.Operation`, `files` requests) inside a small protobuf envelope; only lifecycle messages (hello, telemetry, renewal, updates, config) are typed protobuf | One canonical operation model (DESIGN §2.2); no second schema to keep in sync with the SSH driver |
| M7 | The agent verifies the controller with `VerifyPeerCertificate` against its pinned root and the newest accepted trust manifest, not the system trust store. The controller requests (doesn't require) client certificates at the TLS layer and authenticates each RPC itself | Enrollment and recovery must run over the same port without a valid client certificate; browser HTTPS trust must never authenticate the controller to agents (DESIGN §4.4) |
| M7 | All Agent CA keys are Ed25519 and sealed in the secret box (`agent_ca`); the root key can be exported and removed, leaving the root certificate. A persistent banner warns while the root key is online | DESIGN §4.3: offline root encouraged without making first setup harder (§2.5) |
| M7 | Intermediate rotation with `revoke_old` doesn't strand agents: recovery accepts a certificate from a revoked intermediate, authenticated by identity-key proof of possession plus the identity record, and an agent recovers proactively when its issuer appears revoked in a new manifest | Found by the rotation test: the first version left every agent unable to reconnect after an emergency rotation |
| M7 | Expired-certificate recovery is a separate RPC that only issues a certificate: challenge → signature with the identity key → checks (identity active, not revoked, not quarantined, expired less than `agent.recovery_grace` ago, 180 d default). Past the grace the agent must re-enroll | DESIGN §6.4 |
| M7 | Clone detection: a second session for a connected identity probes the first (heartbeat request, 10 s). If both answer, the identity is quarantined, both sessions are kept for display, and only an administrator resolves it (keep one session, or revoke) | Never auto-decide (DESIGN §6.3); a probe avoids quarantining a plain reconnect after a half-open TCP connection |
| M7 | Operations carry `op_id`, `issued_at`, `expires_at`, controller epoch and the relevant domain's state version. The agent admits in a fixed order (well-formed, duplicate, expired, future, epoch, stale, capability) and records acceptance in a durable ledger before replying "accepted" | Duplicates and replays are rejected durably, across agent restarts (DESIGN §9) |
| M7 | A channel lost after dispatch is resolved with `QueryOp(abandon)`: the agent tombstones an op it never started, so a late arrival can't run it, and returns the result of one that finished; otherwise the step is `ConnectionError` "outcome unknown". The router pins the transport per step once dispatched | Never switch transports after dispatch; the "lost during operation" test checks exactly-once execution |
| M7 | Fallback rules live in one place (`mgmt.Router.Decide`): revoked or quarantined → blocked, no fallback; not connected, incompatible protocol, epoch mismatch or not enrolled → SSH if a fallback is configured and SSH is set up; a `CapabilityError` is returned as is | A capability denial is the host owner's decision; SSH must not bypass it. Revocation must actually stop management through DROOL's agent path |
| M7 | SSH bypasses the agent's ceiling when it's used as a fallback, and the UI says so (`ssh_bypasses_ceiling`) | The SSH account's own rights are its boundary; hiding this would be misleading. Hosts that need the ceiling to be absolute should use fallback "none" |
| M7 | Typed host operations moved from `sshdriver` into `hostops` over a `hostexec.Runner`; the SSH driver and the agent both run the same code (packages, updates, services, reboot, simulate) | No second implementation of operation semantics; identical results and error classes on both transports |
| M7 | The agent's `files.FS` (`agent/localfs`) resolves every path with `openat2(RESOLVE_BENEATH | RESOLVE_NO_MAGICLINKS)` from an `allowed_paths` root and writes with `RESOLVE_NO_SYMLINKS`; DROOL's own agent directories are never reachable, and setuid/setgid modes are refused unless files are unrestricted. Deployments reuse `filedeploy` through a controller-side proxy | Kernel-enforced containment instead of string prefix checks; one deployment implementation |
| M7 | Agent file deployments bump the host's `files` state version; narrowing capabilities bumps `agent-config`. A host reporting a newer revision or another controller epoch is flagged `host_ahead` / `epoch_mismatch`, and versioned operations in that domain are refused until an administrator adopts the host's state | Minimal DESIGN §10.3 detection; full restore reconciliation is later work |
| M7 | Agent telemetry writes facts with source `agent` and addresses with source `agent`; the poller skips SSH polling for hosts whose agent is connected and healthy, and stale data stays marked stale | DESIGN §5.2, §11.3 |
| M7 | Protocol versions are integers independent of the product version (`agentrpc.ProtocolMin/Max`, currently 1–1). Hello carries both ranges; the highest common version wins. No overlap → `incompatible`, the session is refused, and SSH fallback applies | DESIGN §28; tested at both boundaries |
| M7 | Release signing: Ed25519 key generated with `drool release keygen`, kept off the controller. The manifest (version, channel, OS, arch, protocol range, SHA-256, size, `min_upgrade_from`) is signed with a domain-separated signature. Agents trust only `/etc/drool/release-keys.d/*.pub` (root-owned) plus keys compiled into the binary; keys registered on the controller are only for upload checks and for the install-time `--trust-release-key` | A compromised controller can't make agents run unsigned code (DESIGN §4.4) |
| M7 | Update install: `versions/<v>/` staged and verified (manifest, size, hash, `version` preflight of the new binary), `current`/`previous` symlinks swapped with rename, then a restart. A helper process from the old binary performs the switch; a guard run from `previous` (`ExecStartPre`, or the supervisor script without systemd) counts starts (max 3) and enforces the health deadline, and rolls back without executing the new binary. Health = the new agent completed a session with the controller | Rollback doesn't depend on the new binary; power loss leaves one complete version (DESIGN §28) |
| M7 | Rollouts: canary first, then batches; a rollout pauses at `max_failures` and never continues on its own; offline hosts stay pending and are offered the update on reconnect; a host that rolled back is never re-offered the same version in that rollout | One host's failure must not become fleet-wide |
| M7 | Endpoint migration: propose → each connected agent stores the candidate and test-connects with full verification → confirmations shown → admin commits → agents make it primary and keep the old endpoint as a fallback | Never strand agents (DESIGN §6.5) |
| M7 | Without systemd (containers), `drool-agent install` starts a small supervisor shell script that mirrors the unit (restart on exit, guard before each start) | Agents in unprivileged containers are a real use case and the test environment |
| M7 | The agent endpoint has its own `agent.bind` (default: all addresses) and port 9443; the integration tests bind it to the Docker network gateway only | Agents connect from other machines while the web UI may stay on loopback |
| M7 | Backups include agent CA material (sealed), manifests, identities, enrollment records, release metadata and state versions. A root key that was removed is not in the database, so it can't come back from a backup; the manifest records whether the root key was included | DESIGN §10.1; DR needs the offline root for rotation (README) |

| M7 | The agent is installed under `/usr/libexec/drool-agent` (`versions/<v>/`, `current`, `previous`), not `/usr/lib/drool-agent` | Found in the Rocky 9 VM test: with SELinux enforcing, systemd ran the `lib_t` binary in the confined `init_t` domain and every connection to the controller failed with "permission denied". `/usr/libexec` is `bin_t`, so the service runs as `unconfined_service_t` like packaged services, and staged updates inherit the label |
| M7 | An update's outcome (`result.json`) is written before `pending.json` is removed; the new agent reports the committed outcome on its live session, not only in the next hello | Found in the container tests: the healthy report never reached the controller (rollouts stayed open), and in the usability pass a reader could see "no pending update" next to the previous update's outcome |
| M7 | Update reports without a version (refused before the manifest was trusted) are resolved through the release id and applied only to the rollout for that version; the host page doesn't offer a version that failed or rolled back on that host again | Found in the usability pass: one refusal failed every open rollout for the host, and the host page kept offering the broken release |
| M7 | `drool-agent install --token` on an enrolled host enrolls again: the new identity is created beside the old one and only swapped in after enrollment succeeded; the old identity is moved to `replaced-<time>/`, never deleted. Without systemd, install stops a previous supervisor and agent first | Found in the usability pass: re-installing after a revocation kept the revoked identity, and a second supervisor ran next to the first |
| M7 | Usability pass (headless Chromium, all eight steps on an SSH host: install from the host page, Agent preferred / SSH fallback, command/package/service/files through the agent, agent stopped → SSH fallback shown per job, local ceiling change → CapabilityError with no fallback and the UI unable to widen it, signed update, broken signed update → automatic rollback and paused rollout, revocation → management blocked). Also fixed: preset cards' content was vertically centered; the CapabilityError text said the same thing three times; `AuthorizationError` was explained as "sudo refused" for blocked management; log and CLI messages pointed to a nonexistent "Settings → Agents"; agents and files pages load on demand (main bundle 539 → 358 KB) | M7 usability goal |
| M7 | APT's "Package 'x' has no installation candidate" is classified like "Unable to locate package" (`package not found`), so named package operations refresh metadata once and retry | Found in the Debian 12 VM test: a fresh cloud image with stale lists couldn't install `cron` (the same class of problem as the M5 fix) |
| M8 | One host-side tool, `drool-fw` (POSIX sh: dash, bash), implements the whole on-host transaction protocol for both transports: the agent runs it as root, the SSH path through sudo with the tool and boot unit sent along and installed when their hash differs. Files live in `/usr/libexec/drool-firewall` (bin_t), `/var/lib/drool/firewall` and `drool-firewall.service` | One implementation of the protocol that must survive crashes; testable without root under `sh` with fake `nft`/`systemd-run`; the M7 SELinux lesson (`/usr/lib` is `lib_t`) |
| M8 | The SSH executor's firewall state (DESIGN §9 "state file on the host") is the tool's own `committed.json`/journal: committed revision, epoch, hash; the tool refuses expired, duplicate, stale and foreign-epoch transactions itself | State lives with the domain's tooling, so a restore, an agent and SSH see the same thing |
| M8 | Management-path flows come from what the host saw over a fresh connection, re-measured at every transaction: `SSH_CONNECTION` (source address and sshd port as the host sees them, right behind NAT, port forwarding and jump hosts) and the agent's resolved controller endpoints. Their accept rules precede denies; a deny that matches one is a conflict; the emergency override drops only the conflicting accept | The configured address/port can differ from what the host sees (port forwarding in the VM tests, NAT in real networks); a stale observation would protect the wrong path |
| M8 | The agent commits only over a session that connected after the apply (`agentsvc.WithSessionAfter`); after a pending apply the agent ends that session itself and the controller also asks it to reconnect. The agent cancels only the apply-time session, never a newer one | Found in review: the delayed self-disconnect could have cut the new session the commit travels over |
| M8 | Any foreign nftables table with rules or a dropping base chain refuses enforcement, not only tables of recognised managers; empty accepting tables (Debian's default `inet filter`) are fine | A rule in another table can drop what DROOL accepts; precedence can't be proven (DESIGN §16.4) |
| M8 | Outbound traffic is allowed by default per host; `restrict` makes output default-drop with only policy allows, the management path and base ICMP/DHCP. Denies compile on both ends regardless | Default-deny outbound breaks package updates and DNS on most hosts; the design leaves the default open |
| M8 | Removing DROOL's policy is itself a transaction (candidate = an empty `inet drool`) | One path for every change, including the one that should be harmless |
| M8 | Address changes: the converger compares each enforced host's sets hash every 15 s and pushes `drool-fw sets` (flush and refill of the changed sets in one nft transaction, a generation number, refused unless the committed rules hash matches). Never on hosts applied with the emergency override | DESIGN §15.4, §5.3.1; the override removed management protection, so only a full transaction may change those hosts |
| M8 | `firewall_apply` jobs run one host at a time and stop at the first failure; the job timeout is the longest rollback window plus 10 minutes | A bad policy stops at the first host; the job outlives the transaction |
| M8 | Reboots, system updates, network-service actions and agent updates are refused while a transaction is open — at job submission (409) and again at dispatch | Found in the Debian VM test: checked only at dispatch, a reboot queued behind the transaction's host lock and ran right after the commit instead of being refused |
| M8 | Drift is the hash of `nft -s list table inet drool` against the one recorded at commit (and refreshed at boot); agents report it with status telemetry, SSH hosts are checked every 5 minutes; applying over drift needs `firewall.override` ("replace drifted rules"), and a rollback then restores DROOL's last committed policy, not the drift | DROOL must never silently overwrite or adopt changes it didn't make |
| M8 | Learning samples sockets with `ss` every 30 s on agents (aggregated by direction, protocol, service port and peer; new connections counted once), not conntrack or eBPF | No extra packages or kernel features, works in every supported environment; conntrack/eBPF can replace the sampler later without changing the model |
| M8 | The learning switch is its own message (`FirewallConfig`), re-sent on every session, not versioned agent-config | It is not managed state: losing it is harmless, re-sending is cheap |
| M8 | Agents announce optional features in the hello (`firewall-1`); the protocol version stays 1. Agents without the feature are managed over SSH for firewall purposes when SSH is configured | Additive change; old agents keep working for everything else |
| M8 | Transaction outcomes: committed, rolled_back (confirmed by the host), failed (nothing changed), unknown (not committed and not yet confirmed); unknown is resolved when the agent reconnects or by the converger over a fresh SSH connection | Never report an unconfirmed firewall operation as success |
| M8 | Rollback timeout: host value, else the smallest of its groups', else the default (120 s); 30–3600 s in settings; refused below 10 s and below 3x the measured fresh connection unless `override_timeout` | DESIGN §17.4 |
| M8 | Test-only controls: `DROOL_TEST_FW_CONTROL` (per-host delay or skip of the commit), `DROOL_TEST_FW_DELAY_COMMIT`, `DROOL_TEST_FW_SKIP_COMMIT`, and `DROOL_FW_DOCKER_SOCK` in the detect script; each logs "TEST MODE" | Lets integration tests kill the controller, the agent or the host inside the pending window deterministically |
| M8 | Minimal cloud images without nftables (Debian 12 genericcloud) are refused with "install the nftables package"; DROOL doesn't install it on its own | Found in the Debian VM test; installing packages implicitly would be a surprise change |
| M8 | UFW counts as active only when `ufw status` says so; `ufw.service` being active is used only when `ufw status` is unavailable | Found in the Ubuntu 24.04 VM test: `ufw.service` is a oneshot that is "active (exited)" with UFW disabled, so every Ubuntu host was refused |
| M8 | `ufw disable` leaves UFW's iptables-nft chains loaded until the next boot; DROOL keeps refusing (they are foreign rules) and says so: "UFW is disabled but its rules are still loaded: reboot, or remove them" | Found in the Ubuntu 24.04 VM test; refusing is right, an unexplained refusal was not |
| M8 | Removing DROOL's policy is allowed on hosts where enforcement is refused, gated only on the tooling (root, systemd, nft, required tools) | A firewall manager installed after DROOL enforced must not trap DROOL's rules on the host; removal deletes only `inet drool` |
| M8 | Side effects of a settings change (pushing the learning switch to the agent, waking the converger) run after the database transaction commits, never from inside it | Found in the container regression run: the learning push read the host's mode through another connection before the commit and sent "learning off"; it passed earlier only by winning the race |
| M8 | Check and SSH snapshots also record how the host sees DROOL's SSH connection (`SSH_CONNECTION`, unprivileged, over the connection in use); transactions still re-measure it over a fresh connection before compiling | Found in the browser pass: before the first transaction Explain had no management row and a snapshot showed DROOL's own connection as "would be blocked" |
| M8 | List-valued API fields are arrays, never `null` (`listResponse` marshals nil as `[]`; firewall results initialise their slices) | Found in the browser pass: `null` crashed the Firewall page and the policy preview |
| M9 | One canonical document (`internal/firewall/document`) is the only editable form of the firewall policy; Topology, Rules and YAML are views of one draft held in the browser. The browser never evaluates policy: every draft is POSTed to `/firewall/document/check`, which resolves it to a `compile.World` and compiles every host with the M8 compiler | Three editors must not become three configuration systems, and the canvas must not grow a second evaluator |
| M9 | Objects are referred to by name (`host:web1`, `group:web`, `network:admin-lan`, `cidr:…`, `internet`, `internal`, `any`); YAML writes bare names when they are unambiguous. Names are unique (hosts, groups, policies already were); network and service names can't be keywords | YAML has to be writable by hand; ids are not |
| M9 | Named network objects are a new selector kind (`network`, table `fw_networks`). The compiler resolves them like literal CIDRs (rules, not dynamic sets): changing a network's CIDRs is a policy change applied by transaction | The only change to the compiler: networks are needed as nodes on the canvas and as YAML names; keeping them static keeps M8's rule/set split intact |
| M9 | Saving a document replaces networks, custom services and policies with the document's (by name: kept policies keep their ids); groups the document lists get its members and descriptions (needs `hosts.write` too); groups it omits are kept, with a notice — groups are shared with jobs and the rest of DROOL | Deleting a DROOL-wide group from a firewall YAML edit would be surprising and destructive |
| M9 | Saves carry the revision the draft started from; a different stored revision is a 409 | Two editors (or the M8 policy API) can't silently overwrite each other |
| M9 | Canonical YAML: objects sorted by name, policy in order, `allow:`/`deny:` always lists, single CIDR as `cidr:`, single TCP/UDP entry as `protocol`/`port`. Comments, anchors and formatting are not kept | Stable round trips; the brief allows canonical serialization |
| M9 | Management-path analysis (`MgmtPath`) lays out each host's paths from the inventory (controller → jump hops → host; host → controller endpoints for agents) and marks each hop protected / blocked / unverified from the compiled draft's conflicts. The port a hop protects is the one the host observed (`SSH_CONNECTION`), not the dialled one | Found in the browser pass: behind a port-forward the dialled port (18601) differs from what the host's firewall sees (22); matching the dialled port missed real conflicts |
| M9 | Unmeasured hops whose host's compiled rules drop the dialled SSH port are flagged "at risk" (with the responsible rules) rather than shown as fine | Before the first Check DROOL can't know where its connection comes from; a deny of TCP 22 is worth a warning even then. Apply still re-measures and refuses |
| M9 | Removing an allow never cuts DROOL's management path: management accepts are compiled ahead of every deny and allow (M8). Only effective denies can — direct, or indirect through group membership, network CIDRs, service definitions or another rule — and all of those show up as compile conflicts on the draft | Tests assert both directions |
| M9 | The converger holds a set-only update that would create a management-path conflict (sets_error "held: …") instead of pushing it unattended | "Be conservative near management paths": a membership/address change shouldn't silently change behaviour next to DROOL's own access, even though management accepts still come first |
| M9 | Layout (positions, expanded groups, placed objects) is a separate settings blob (`firewall.layout`), never part of the document or its revision | Moving boxes is not a policy change |
| M9 | The theme is `data-theme` on `<html>` (light, dark, system) set before first paint by `/theme-init.js` (a static file: the CSP allows only same-origin scripts) and saved in localStorage; dark values are a separate token palette; System follows `prefers-color-scheme` live through CSS | No flash, no reload, no inline script exception in the CSP |
| M9 | Light-theme warning and danger tokens darkened (`#965a00`, `#b93232`) | The new contrast test found them at 4.48:1 and 4.43:1 on their tinted backgrounds, just under WCAG AA |
| M10 | Observations stay one store (`fw_observations`, rebuilt): flow identity is host + direction + protocol + local address + service port + remote address; the client's ephemeral port, the process and the pid are "last seen" attributes | One busy client is one row, not one row per connection; the firewall and the network views read the same data |
| M10 | Policy status comes only from the M8 compiler (`fwsvc.Classifier`): the desired policy, and on enforced hosts with unapplied changes the applied one. Outbound flows on hosts that don't restrict outbound are checked against the host compiled as if outbound were restricted, so "a policy names this connection" is answered without a second evaluator | The browser never decides firewall semantics; an outbound allow the compiler doesn't need on that host still covers the flow |
| M10 | Status values: management, covered (a policy or base rule), unclassified (the default), denied (a deny policy, or the default drop on an enforced host); `would_deny` separately marks what the desired policy would drop | A host in Monitor mode with an inbound flow nothing allows is unclassified *and* would be denied — both are true |
| M10 | One definition of internal: the firewall's `internal_ranges`. Scope is internal (in those ranges), public (globally routable unicast outside them) or special (loopback, link-local, multicast, documentation, private space removed from the ranges, ...). Only public addresses are ever looked up; the stored scope is recomputed when the ranges change | No second "LAN ranges" setting; nothing private is ever sent to a database lookup, even one on disk |
| M10 | Agents prefer conntrack: ctnetlink "new connection" events (read only, CAP_NET_ADMIN, no modules, sysctls or rules touched). It is used only after the kernel confirms it tracks the agent's own controller connection (a `CT_GET` for its tuple); otherwise, and after any failure, the agent samples sockets (`ss -Hntuap`) and reports why. Rechecked every minute while sampling, every five minutes on conntrack | Connection tracking is only active while something (a firewall) uses it; an honest fidelity label beats a silent gap |
| M10 | Socket sampling still runs alongside conntrack: it advances "last seen" for long-lived connections and names processes (scans at most every 2 s while conntrack announces unattributed connections). Inbound flows take the listening socket's process; outbound UDP from an unconnected socket its owner; otherwise "Unknown" | Process attribution from socket ownership is reliable or absent — never guessed |
| M10 | No bytes counters | conntrack counters exist only with `nf_conntrack_acct` (off by default; DROOL doesn't change sysctls) and socket samples have none: partial totals would mislead |
| M10 | Routed connections (containers on a bridge, a router) are counted, not shown | They aren't the host's own connections; container networking is out of scope |
| M10 | GeoIP/ASN behind `geoip.Provider`; the first backend reads MaxMind-format (MMDB v2) files from `network.geoip_dir` (default `<data_dir>/geoip`) with DROOL's own bounds-checked reader. Country and ASN databases are recognised by content (MaxMind GeoLite2/GeoIP2, DB-IP Lite, IPinfo Lite); the newest of each kind wins; a changed file is reloaded; a broken replacement keeps the last good copy; a 50 000-address LRU caches lookups and is cleared on reload. Enrichment happens when results are read, not stored | No vendor account in DROOL, no online lookups, no dependency on a vendor library; database updates are the operator's (geoipupdate, downloads), separate from collection; a new database applies to old observations |
| M10 | Retention (default 30 days) and a per-host cap (default 5 000 flows, least recently seen first) are pruned every 10 minutes in 2 000-row batches; ingestion writes each report in 500-row transactions | Bounded database; pruning interleaves with collection |
| M10 | Fleet queries filter in SQL first (host, group, direction, scope, protocol, ports, process, source, times, new destinations), then classify and enrich rows in Go for policy, country, ASN, organization, service, CIDR and text filters, scanning in 1 000-row chunks behind a keyset cursor (at most 500 per page, 200 000 rows per request). The total is returned only when every filter ran in SQL | Server-side filtering and paging that stays correct for filters the database can't evaluate |
| M10 | "New destination": a public address first seen (within retention) by that host, or by any host, after `since` | Situational awareness, not an alarm; no scores |
| M10 | The observed topology uses the designer's node keys and groups public addresses by ASN (top N networks, the rest as "Other external networks", or one box), expandable to at most 40 addresses per network; both ends of an internal connection are counted once | No node per address, no country nodes (country isn't a route); large fleets stay readable |
| M10 | "Create policy from observed connection" returns a document rule (and a custom service when no service matches); the designer adds it to its draft and nothing else happens | An observation never becomes a rule silently; validation, management checks, preview, save and the transaction all still apply |
| M10 | `network.read` for admins, operators and auditors (not viewers), `network.snapshot` for admins and operators, `network.settings` for admins; the M8 observations and suggestions routes moved to `network.read` | Observed connections are sensitive metadata |
| M10 | Agents announce `netmon-1` and send `netflows` reports (source, explanation, lost events, flows); M8 agents' `flows` reports are still accepted as socket sampling | Agents update after the controller; nothing is lost in between |
| M10 | The agent reads its session's peer with `peer.FromContext(stream.Context())` | Bug found by the VM conntrack test: `grpc.Peer` is only filled in when a stream ends, so the agent never knew its own connection (needed to prove conntrack) — and M7's operation log never recorded the controller's certificate fingerprint |
| M10 | Test mode: `DROOL_TEST_NETFLOWS_FILE` ingests reports from a file when it changes (browser tests need hundreds of public addresses) | Logged as TEST MODE, like M8's firewall test hooks |
| M11 | One set of collectors behind `source.Source`; the SSH path runs a script generated from the collectors' own file/command lists (one `grep -HZ` dump plus fixed commands, up to two follow-up passes), and a test requires agent and SSH results to be identical on a fixture machine | No second implementation to drift; an SSH poll is one or a few round trips, not hundreds |
| M11 | Rates are computed from raw counters and refused (no value, not zero) across a reboot (boot id), a counter going backwards, a replaced interface (ifindex) or disk (device number), or a rate beyond what the link or any disk could do | A wrapped or reset counter never shows up as a giant spike or as zero |
| M11 | Points are one-minute windows of average and maximum per key; a missing key is unknown. Stored as float32 pairs in a deduplicated key layout, three tiers (raw 26 h, 5 min 8 days, 1 h retention) | 1.5 MB per host for 30 days of 33 series; no external TSDB, no per-sample rows |
| M11 | The agent budget is set by systemd (CPUWeight 20, CPUQuota 25%, MemoryHigh 96 MB, MemoryMax 160 MB, IOWeight 20, TasksMax 256, OOMScoreAdjust 300), applied at runtime with `set-property --runtime` so existing installs and overrides need no unit rewrite. Defaults from measurement: 0.1% CPU / 25 MB on x86, 4.0% CPU / 14 MB peak at full fidelity on an emulated (TCG, ~20× slower) VM over 5 minutes including a full storage scan | Pi-safe ceilings with an order of magnitude of headroom; the cgroup is the backstop, internal bounds the mechanism |
| M11 | A memory guard restarts the agent when its live heap stays beyond Go's soft limit (¾ of `MemoryHigh`) for 2 minutes; the offline buffer must be at most ¼ of `MemoryHigh` | Found by the VM runaway test: `memory.high` throttles instead of killing, so a leaking agent would be wedged near the high-water mark forever rather than restarted |
| M11 | Operations (packages, scripts, firewall, services, files) run in `systemd-run --scope` outside the agent's budget; update checks in a low-weight scope | A capped monitoring agent must never starve or OOM-kill an `apt upgrade` it was asked to run |
| M11 | CPU throttling reduces fidelity only when the agent is held back more than 10% of the time, not on any throttled period | Found by the VM test: on a slow (emulated) CPU, one `systemctl show` burst is throttled in some 100 ms period; counting that kept the agent at minimal fidelity forever and starved the probe |
| M11 | The governor degrades in two levels on the agent's own CPU/throttling/memory signals or host PSI, and recovers one level per 5 calm minutes; connectivity, events and firewall/recovery are never deferred; every change is visible (state, event, info issue) | The managed workload wins, and the operator can see why data got coarser |
| M11 | Telemetry is never written to disk on the agent: a 2 MB point buffer (oldest dropped, gaps reported) and 1 000 events; only `monitor.json` (config) and the probe file are files | SD-card hosts must not wear out from monitoring; an outage longer than the buffer is an explicit gap |
| M11 | Agent logs: an in-memory 256-entry diagnostic ring (fetchable), Info+ to the journal, identical warnings/errors at most every 10 minutes, collector failures first-then-hourly; monitoring values never logged | No routine log writes; errors stay visible without flooding |
| M11 | Storage health is a capability level (Full / Partial / Vendor-specific / Limited / Unavailable) plus a state; SD cards and USB sticks are "Limited — no failure indicators detected", never healthy; SMART attributes are interpreted only where smartctl names them, never by ID across vendors; no self-tests, `-n standby` always | Honest, not invented; monitoring never wakes or exercises a disk |
| M11 | The flash probe is the only write DROOL monitoring makes: 512 bytes in a 0700 directory owned by the agent, `O_NOFOLLOW`, fdatasync, page-cache drop, O_DIRECT read back; hourly by default, shorter only with a wear warning; agent hosts only | Detects a card that silently stops persisting writes, at negligible wear |
| M11 | Health issues come from sustained conditions with hysteresis and gap resets (`mon_issues`, one active per host/check/subject); temperatures only against the hardware's own thresholds; no notifications | No universal "danger temperature"; spikes don't page anyone; M12 builds alerts on these rows |
| M11 | Helpers (smartmontools, nvme-cli, lm-sensors) are installed only by an ordinary package job after approval (host, selection, group or global standing approval by an admin); never uninstalled; GPU drivers never | One audited execution path; no hidden package changes |
| M11 | `InstallViaSSH` installs only the signed release of the controller's exact version (or its bundled build), never "the newest release"; failures disable the service, revoke the unused token and an identity that never connected | Found while testing: the newest signed release can be a different (even a test) build; a failed install must leave the host exactly SSH-manageable |
| M11 | The firewall status in the agent's hello (pending transaction, drift) runs as an operation, outside the monitoring budget | Found by the Rocky 9 VM test: inside the CPU quota the status check outlived its 20 s timeout on a slow host, the new connection carried no pending transaction, and a good candidate was rolled back. Recovery state is never subject to the monitoring budget |
| M11 | Inbound connections take the listening process even when the accepted socket belongs to another (fixes M10) | OpenSSH 9.8+ hands each connection to `sshd-session`; the service a policy names is the listener `sshd`, as M10's decision said but the code didn't do |
| M11 | Bulk conversion reuses agent rollouts (`kind='install'`, canary wave, batches, failure limit) | One staged-rollout machinery for agents |
| M11 | A read-only *virtual* device is information, not a warning; a read-only physical or flash device stays a warning (an SD card that locked itself read-only before a reboot must not look fine) | Found by the VM test: the cloud-init seed image (a read-only virtio disk) raised a storage warning |
| M11 | Containers: storage health is skipped (the devices belong to the container host) and runtime bind mounts are not filesystems | Found in the browser pass: a container showed the workstation's disks and `/etc/hosts` as a filesystem |
| M11 | Test mode: `DROOL_TEST_MONITOR_FILE` describes fixture hosts (SD card, failing disks, GPUs, USB bridges, a stale VM) | No isolated environment has that hardware; logged as TEST MODE |
| M12 | Alerts consume M11's issue transitions through a hook that runs inside the transaction recording the transition (`monsvc.SetIssueHook`); the alert, its timeline and its queued notifications commit with the issue. The hook runs in a savepoint: if alerting fails, only its part rolls back, the issue is still recorded, and a reconciliation (every minute, and at start) repairs it | No second evaluator, no window in which a crash loses an alert, and alerting can never stop monitoring from recording a problem |
| M12 | One alert per activation of a Warning/Critical issue (`alerts_one_open_per_issue`); Info issues never alert; title and value changes are silent; a rise above the alert's peak severity notifies once (escalation); falling back is silent | One opening and one recovery per activation; severity hovering at a threshold can't page anyone repeatedly |
| M12 | M11 now records `mon_issues.observed_at` when it knows a condition started before its duration was satisfied (CPU, memory, interfaces, temperatures, services, stale) | The alert timeline shows "first observed" only when monitoring actually knows it |
| M12 | Acknowledgement is a state (`acknowledged`), not resolution; there is no manual resolve | Only the source condition recovers an alert |
| M12 | Maintenance: immediate, for hosts and/or groups (membership evaluated live), with a duration, an end time (≤ 30 days) or until ended. Suppression is a flag on open alerts, applied when an alert opens, when maintenance starts or ends, every tick and again right before each send; queued notifications of newly suppressed alerts are cancelled. An alert still active when maintenance ends gets one opening after a 2-minute grace (re-queuing the cancelled one: deduplication keys); one that recovered inside maintenance is never notified; recoveries inside maintenance are not sent; acknowledged alerts get none | Notification suppression only, never hiding problems or touching safety mechanisms; no delayed noise |
| M12 | Routing: a seeded default route (`nrt_default`: Warning + Critical, recoveries) that a new destination joins unless "custom"; additional routes with AND-combined filters (severities; hosts ∪ groups; categories ∪ checks), no ordering — every matching route contributes, destinations are deduplicated, recovery is the OR and reminders the shortest interval of the routes that chose a destination | Simple setup is one form; advanced routing without first-match semantics or a rule language |
| M12 | Recoveries go only to destinations whose opening (or escalation) was delivered or is still pending; if the opening fails for good, its recovery is cancelled | A recovery for a message nobody received only confuses |
| M12 | Reminders off by default; per route 60 min to 7 days; at most 12 per alert; stopped by acknowledgement, suppression and recovery | Reminders exist for those who want them without becoming a storm |
| M12 | The outbox (`notify_deliveries`): a row per (alert, destination, notification) written in the alert's transaction, idempotent through `dedupe_key`; workers claim with one `UPDATE … RETURNING` (single writer), a lease and a fencing number (`attempts`), so a stale worker's result is discarded. At start every leased row is orphaned: the attempt is recorded as interrupted, the delivery marked ambiguous and re-queued. Per (alert, destination) notifications go in queue order (an opening before its recovery) | Durable, effectively-once in the UI, at-least-once on the wire; a crash can duplicate, never lose |
| M12 | Retries: 8 attempts, 30 s doubling to 30 min with ±20 % jitter (about an hour in all); `Retry-After` honoured up to an hour; HTTP 401/403/4xx and redirects, TLS/certificate errors, SMTP 5xx and authentication failures are permanent; timeouts, connection errors, DNS, 408/425/429/5xx and SMTP 4xx are transient | Configuration problems fail visibly at once; outages retry, never forever |
| M12 | Isolation: 4 workers, at most 2 concurrent sends per destination, rate limits per destination (Discord/Slack 30/min, email 60/min, webhook 120/min; the rest waits in the queue), every send bounded (dial 5 s, TLS 5 s, whole exchange the destination's timeout, default 10 s, max 30 s), response bodies read to 64 KiB at most, no database transaction held during network I/O | A hanging or slow endpoint can't stall ingestion, evaluation, jobs, agents or firewall transactions (tested with hanging receivers) |
| M12 | Destination secrets (SMTP password, webhook/Discord/Slack URL, webhook header value) are one blob sealed with the master key (AAD `notify-<kind>`), covered by master-key verification and by recovery's discard (destinations are then disabled until re-entered); the API returns only which secrets are set and `url_display` (scheme and host); audit records name changed secrets, never values; provider errors are redacted of every secret and its URL parts | The webhook URL *is* the credential for Discord and Slack and often for self-hosted receivers |
| M12 | Webhook URLs: http(s) only, no credentials in the URL, no fragment; private, loopback and link-local addresses allowed; at most 3 redirects, http(s) only, never https → http, and the secret header is dropped when a redirect changes host | Notifying internal services is a legitimate admin choice, not SSRF; redirects can't smuggle the secret elsewhere |
| M12 | SMTP via `net/smtp` on a deadline-bound connection: implicit TLS or STARTTLS (required when chosen; a server not offering it fails, no fallback); `PlainAuth` only over TLS; unencrypted delivery needs explicit confirmation and never carries a password; CR/LF stripped from every header value | Correct TLS by default; no silent downgrade; no header injection from host or service names |
| M12 | A notification's content is snapshotted when queued (so retries send the same thing) and dropped (`'{}'`) once the delivery finishes | Measured: halves the per-alert history size |
| M12 | Webhook payload `drool.notification.v1`: `event_id` (alert + event, the same for every destination), `delivery_id` (also `Idempotency-Key` and `X-DROOL-Delivery`), event, alert fields, plain-text summary; Discord: one embed with no mentions allowed; Slack: escaped mrkdwn text | Receivers can deduplicate; names can't ping a channel |
| M12 | Retention: resolved alerts and events 180 days, finished deliveries and attempts 30 days, ended maintenance 180 days (settings: 30–730 / 7–365); pruned hourly in batches of 500 | Measured: 200 hosts at a pessimistic 3 alerts/host/day keep ~1 MB per host; a full prune never blocks other writers more than ~30 ms |
| M12 | The open-alert indexes are on `state` (not partial): SQLite won't use a `state != 'resolved'` partial index for `state = 'active'` | Found by the scale test: the summary took 90 ms over 108 000 alerts; now under 0.2 ms |
| M12 | The controller event timeline is its own small table (`events`), written in the same transactions: alert opened/critical/acknowledged/recovered, maintenance started/ended, destination failing (once, after 3 consecutive failures or one permanent one) and recovered | DESIGN §22.8 without turning every retry into an event |
| M12 | Test sends (stored or unsaved destinations) are synchronous, audited, rate-limited to 10 a minute per user, create no alert and don't change the delivery health (kept as "last test") | Testing must not fake an incident or hide a real failure |
| M12 | Test mode: `DROOL_TEST_ALERT_TIMING=fast` shortens the maintenance grace and the tick for the browser tests | Logged as TEST MODE |
| M12.1 | Health configuration is a normalized table of sparse rows (`mon_health_overrides`: scope, group or host, check, optional subject, the overridden fields only as JSON), plus `mon_group_health` (priority). Field names are a small fixed vocabulary (`enabled`, `warning`, `critical`, `duration_min`, `clear`, `clear_duration_min`, `missed_intervals`); each check declares which it supports | New checks need no migration of existing rows; nothing copies a complete profile per host or sensor |
| M12.1 | Resolution order per field: subject (the subject's identities, strongest first) → host → groups → global → Recommended / hardware limit / derived. Clear levels not set anywhere derive from the effective warning level (and a set one that isn't below it is replaced by the derived one); a critical level below the warning level is raised to it | Sparse fields can come from different scopes and still form a sane rule |
| M12.1 | Groups: the highest `priority` among the host's groups that set the field wins; equal priorities with different values are a conflict — the field ignores the groups (next scope) and the conflict is reported in the provenance, the host summary and the UI; equal values agree. Order of membership, names, ids and creation times never matter (tested with shuffled memberships) | A host in two groups never gets a value chosen by accident |
| M12.1 | Subject overrides exist at host scope only; groups can't target "this mount on every member" | Subjects are identities of one machine; per-group selectors would be a rule language |
| M12.1 | Subject identities: `fsuuid:` from the storage report's partition UUID when the filesystem sits directly on it and no other mount shares it (else `mount:`); `disk:` the storage key (WWN, NVMe EUI, model+serial; `name:` is labelled weak); `gpu:` the NVIDIA GPU UUID or AMD `unique_id` (else `pci:` slot, else `index:` labelled weak); `sensor:` a drive's identity plus label, else chip@sysfs-device/label, else the collector id (weak); `iface:` name; `unit:` name; probes `mount:`. The UI receives the strength (stable / location / name / weak) | Overrides don't silently move to another device after a reboot; weak identities are honest |
| M12.1 | Collector additions for identity only: `GPU.uuid`/`pci` (nvidia-smi query gains `uuid,pci.bus_id`, 8-field output still parsed; sysfs `unique_id`), `Sensor.hw` (the hwmon device link's basename, already in the SSH dump); NVIDIA GPUs are matched to PCI slots by bus id instead of order | Stable GPU and sensor identity without new tools; older agents keep working with weaker identities |
| M12.1 | The evaluator holds one immutable configuration snapshot and compares a generation counter (`mon_health_gen`, bumped by triggers on overrides, priorities, group membership and group deletion, and by host-settings saves) at each evaluation; a new generation reloads and re-judges the affected hosts at once (`reconfigure`) | Membership changes from any path (groups page, enrollment tokens, the firewall designer) apply without every caller remembering to notify monitoring |
| M12.1 | Per host and check a signature of everything that can affect it (global, the host's groups with priorities, host, subjects, watched units) decides what changed. For a changed check: active issues are re-judged (still failing under the new rule: the same issue, severity/title updated; check off: resolved `disabled`; value below the new level: resolved `reconfigured`); pending conditions restart with `notBefore` = the change; firing ones restart their clear timer; service durations count from max(unit change, configuration change) | No new occurrence for a change, no retroactive opening, no fake recovery |
| M12.1 | `mon_issues.resolution` (`recovered`, `disabled`, `reconfigured`, `gone`) with a note travels in `IssueTransition`; filesystems unmounted and disks no longer reported end as `gone` | M12 can tell a repaired machine from a changed rule |
| M12.1 | M12 closes a non-recovered alert as resolved with `alerts.resolution`, a "cleared" timeline entry and event (`alert.cleared`), and sends the closing notification as message kind `cleared` to the destinations a recovery would reach (delivery kind stays `recovery`; the API shows it as `cleared`; webhook `event: cleared` with `resolution`): "CLEARED BY CONFIGURATION" / "CLOSED, NO LONGER REPORTED", never "RECOVERED" | One consistent behaviour: recipients of the opening learn it closed and why; history keeps the reason |
| M12.1 | Permissions: global and group overrides and priorities need `monitoring.settings`; host and subject overrides need `monitoring.manage`, except changes that weaken detection anywhere they apply (computed by comparing effective rules before and after for the host and each of its subjects: a check off, a higher level, a longer duration or interval, a higher clear level or a shorter clear duration), which need `monitoring.settings`; removing a stricter override counts as weakening | Operators can tighten monitoring; turning things off is an administrator's decision |
| M12.1 | Audit: `monitoring.health_override.create`/`update`/`reset`, `monitoring.health_check.disable` (a change that turns a check off) and `monitoring.health_group.priority`, each with scope, group/host, check, subject and the changed fields (before/after) — never telemetry | Who changed what monitoring considers healthy is always answerable |
| M12.1 | M11's custom profile (`settings.monitoring.profile = custom`) becomes global overrides of the fields that differed from Recommended, and a host's `disabled_checks` become host overrides (`enabled: false`), once at start; the settings API refuses a custom profile and a non-empty `disabled_checks` with a pointer to overrides | One configuration model; older clients sending an empty list still work |
| M12.1 | High GPU utilization (`gpu_utilization`) is evaluated from `gpu.util:<index>` points mapped to the GPU's identity through the current state, off in Recommended (95% for 30 minutes when enabled, clears below 85% for 5 minutes); NVIDIA GPUs report no temperature limit, so their temperature (`temperature`, subject `gpu:<id>`) is judged only against an explicit override | Busy is not unhealthy; no invented limits |
| M12.1 | Fixed bug: in M11, disabling a check for a host left an already active issue open forever (the check simply stopped being evaluated). Every disabled check now resolves its issues as `disabled` | Found while designing configuration-change semantics |
| M13 | A schedule never executes anything: each occurrence is an ordinary job submitted through `engine.Submit` (with an `Origin` and a per-host skip map) or ordinary M12 maintenance (`alerts.StartMaintenance` with `Until`) | "The schedule answers when; the job engine answers how" — locks, capabilities, management path, retries, output, cancellation and audit stay in one place |
| M13 | Occurrences: `schedule_occurrences` with a partial unique index on (`schedule_id`, `scheduled_for`) for non-manual rows; the occurrence (`INSERT … ON CONFLICT DO NOTHING`), its job (`engine.Submit` on the same transaction) and the schedule's next run (`UPDATE … WHERE next_run_at IS <the value read>`) commit together; the job is launched after commit | A crash before commit leaves nothing; after commit the engine's restart marks the job interrupted and the occurrence already exists, so there is never a second job, and nothing is re-run |
| M13 | One bounded loop: every 15 s (`Tuning.Tick`) an indexed query (`schedules_due`, partial on enabled and not archived) returns at most 100 due schedules, up to 50 batches per tick; each is handled in its own short transaction; no goroutine per schedule, no in-memory queue (queued runs are rows) | Measured with 6 000 schedules: a tick with nothing due takes ~0.2 ms; a burst of 3 000 due in the same minute dispatches at ~380/s while another writer's p99 stays ~3.5 ms |
| M13 | Recurrence kinds `once`, `hourly` (every N elapsed hours from a first run), `daily`, `weekly` (weekdays), `monthly` (day 1–31, clamped to the month's last day), with an explicit IANA zone (`""` and `Local` refused; `time/tzdata` embedded). Wall-clock kinds: a time in a spring-forward gap runs at the first instant after the gap (found by bisection, so 30- and 45-minute shifts work); a repeated time runs at its first (earlier) instant; one occurrence per local date | Deterministic and testable with a mocked clock (Los Angeles, Berlin, Lord Howe's 30-minute shift; a property test over three years in 13 zones, including midnight transitions in Havana and Santiago, finds exactly one run per local date) |
| M13 | Missed runs: an occurrence handled more than 5 min (`MisfireGrace`) after its time is missed. Recurring: all missed times become **one** `missed` row (`missed_count`, `missed_until`), then the schedule continues; `run_once` dispatches the most recent missed time once with a "started late" note. One-time: dispatched late with a note, or `expired` past `late_limit_minutes`. One aggregated `schedule.missed` event per tick. `Between` is capped at 100 000 times | A week of downtime is one history line and at most one run, never a replay |
| M13 | Overlap: an occurrence is skipped while the schedule's previous job is pending/running/paused; `queue_one` stores one `queued` row instead (further ones are skipped) and the tick dispatches it once the blocking job is terminal | Never an unlimited backlog |
| M13 | Ownership: before every occurrence the owner must exist, be enabled and hold `RequiredPermissions` (jobs.execute plus files.deploy/elevate or firewall.write/override by step; maintenance.manage for windows) — otherwise the occurrence is `blocked`, the schedule is disabled with an `attention` text, a `schedule.disabled` event is recorded and `schedule.blocked` audited. Jobs run as the owner (`created_by`); the audit actor is `system` named "schedule X" with a `summary` "system execution via schedule X, owned by Y". Editing by anyone else transfers ownership to the editor; enabling requires the owner to qualify; reassigning needs `schedules.admin` and a qualifying owner. Run now runs as the caller with the caller's permissions | Permissions are checked when work happens, not only when it was configured |
| M13 | Maintenance and scheduled runs: hosts covered by any maintenance other than the schedule's associated window are snapshotted into the job as `wave = -1`, state `skipped`, "skipped: maintenance active (reason)"; if every host is covered the occurrence is `skipped`. An associated schedule runs only while its window is active. A host covered by the associated window *and* another maintenance is skipped. Run now ignores maintenance (a deliberate act) | A window never lets a job past someone else's maintenance |
| M13 | Recurring windows: kind `maintenance`; hosts and groups only; 1 minute to 7 days and no longer than the shortest spacing between occurrences (no self-overlap). The maintenance row records `schedule_id`/`occurrence_id`, reason `Recurring window "name": reason`, actor "recurring window \"name\"". After an outage a window still in progress starts with its original end; one entirely elapsed is recorded missed and never started, so no suppression or recovery notification is ever sent late | M12's maintenance does the work; scheduling only decides when |
| M13 | Engine waves: `job_hosts.wave` fixed at submission by `model.PlanWaves`; `runJob` starts a wave only after the previous one's goroutines are done (`wg.Wait` barrier); after wave 0 of a canary rollout any failure pauses (`PauseCanary`); the failure threshold pauses (`PauseThreshold`) instead of skipping, and resume renews the allowance (`thresholdBase`); cancel marks every remaining host cancelled. `OnPause` lets `main` record a `job.paused` event (not for the circuit breaker, which the job page already explains) | Rollout safety belongs to the executor, so manual jobs, workflow runs and scheduled runs behave identically |
| M13 | Workflows: `workflows` + immutable `workflow_revisions` (steps and default strategy as JSON); a no-op edit stores nothing; names unique among non-archived; archiving keeps pinned schedules running; a schedule pins `{id, revision}` and shows `workflow_update_available`; adopting a newer revision is a schedule edit (audited with the previous revision). A schedule's rollout is its own (initialized from the workflow's default) | Reviewed behaviour can't change underneath a schedule |
| M13 | Controller-side steps (`model.IsLocal`): the engine runs them through a `LocalExecutor` (`internal/verify`) inside the host's job and lock, after the host's earlier steps, with state `verifying` (or `running` for wait); they never touch the host's shell. `verify_host` = agent connected, SSH fallback probe, or SSH probe; `verify_services`/`verify_health` read M11 data **collected after the step began**: an agent is asked to report (best effort; its regular reports count) and the step waits for a newer stamp (compared for change only — agent state carries the agent's clock); an SSH host is polled directly (the job holds the host, so the poller would skip it). Stale data never passes a check. Failures are `VerificationError`; monitoring off is a `ConfigurationError` | Checks must not report a host healthy from data taken before a reboot |
| M13 | Workflows and schedules refuse `firewall_apply` steps (firewall policy is applied from the Firewall page) | Firewall changes keep their review and simulation path; unattended runs don't apply them |
| M13 | Artifact garbage collection also keeps files referenced by any workflow revision or schedule configuration | Found while building M13: a file-deploy step saved in a schedule would have lost its file after 24 h, before its first run |
| M13 | Events (`events` table, M12): `schedule.disabled` (owner lost permission), `schedule.missed` (aggregated per tick), `job.paused` (canary, threshold), and M12's `maintenance.started`/`ended` for windows. Never per host or per tick; job results are not routed through alert notifications | The dashboard shows what needs a person; alert routing stays about host health |
| M13 | Retention: occurrences 400 days and at most 1000 per schedule (queued rows kept), pruned every 6 h in batches of 500; jobs follow the engine's retention and an occurrence whose job was pruned says so | An hourly schedule can't grow history without bound |
| M13 | Test mode: `DROOL_TEST_SCHED_TIMING=fast` ticks every second and counts runs more than 20 s late as missed | Real-process restart tests without five-minute outages; logged as TEST MODE |
| M14 | Desired state is one package (`internal/desired`, one `Service`) that observes, compares and proposes; the only path that changes a host is `engine.Submit` of ordinary typed operations (`service`, `package`, `file_deploy`) with `Origin.ReconcileID` | Locking, routing, capabilities, privilege, retries, rollouts, cancellation, maintenance, audit and history stay with the engine (DESIGN §12.1) |
| M14 | Resources are typed and keyed (`service:<unit>.service`, `package:<name>`, `file:<path>`); a declaration is decided as a whole at the most specific level (host declaration → baselines assigned to the host → group baselines by `ds_group_priority` → global); identical declarations (canonical form) agree, different ones at the deciding level are a conflict with no desired value; a host declaration may be *excluded* ("Not managed on this host") | Field-by-field merging across scopes makes provenance unexplainable; a conflict must never be resolved by accident |
| M14 | Persist declarations only: baselines, immutable revisions (`resources_json`, `assignments_json`, content hash), assignments, group priorities, host declarations. A trigger-maintained generation (`ds_gen`, as `mon_health_gen` in M12.1) makes the service reload an immutable `Snapshot` and resolve every host in memory; API calls reload first when the generation moved (`fresh`) | Membership changes from any path apply at once; effective state is never stored per host and can't go stale |
| M14 | Observed state per (host, effective resource) in `ds_resource_state` (status, desired and source JSON, observation, diffs and their signature, reconcile/job ids, automatic-restore counters); per host in `ds_host_state` (counts, check stamps, notes). Rows exist only for resources in effect; evaluation writes only changed rows, inside a write transaction guarded by the generation (a stale snapshot aborts, reloads and retries) | Bounded rows (hosts × managed resources); no write for an unchanged recheck |
| M14 | History (`ds_history`) only on a change of (status, diff signature); a host-wide unknown (unreachable, check failed) is one host-level entry instead of one per resource; first evaluations of never-checked resources write nothing; resolutions `restored` (a correction proven by a fresh observation), `configuration` (desired state changed or accepted), `observed` (the host changed back). Pruned hourly: 180 days, 100 per host and resource | Truthful, bounded history; no noise from rechecks |
| M14 | Services: M11 `mon_services` and `mon_current.services_at`; SSH data is fresh within 3× the poll interval + 1 min. Agent data: an agent sends its full list on every change (state or boot state) and at least every `agentmon.ServicesResend` (30 min), so while its identity is `connected` an unchanged list is current for 35 min; after a disconnect it counts for max(5 min, 4× the services interval) past `last_seen_at` ("the agent is not connected (last seen …)"). Monitoring off or no data → unknown. Proof (before restore, Accept, after a correction): `RequestRefresh` for agents (up to `ProofWait`, 45 s: the agent delivers every 15 s) / `PollSSH` for SSH hosts, then a stamp newer than before (compared for change only, as M13's verify). A masked or static unit can't be enabled/disabled: drift with a note, excluded from restore | Never compliant from old data; never claim a change that wasn't seen; an idle agent host isn't "unknown" just because nothing changed |
| M14 | Agent change (found by the VM test design, M11 behaviour): the agent re-sent its service list only on active/sub-state transitions or every 30 min, so enabling or disabling a running unit, and a requested refresh with nothing changed, produced no report; stopped units' boot state came from a unit-file cache refreshed only when nothing had been sent for 10 min. Now a boot-state change is sent at once, unit files are re-read every 10 min on their own clock, and a requested collection re-reads them and is always sent. Older agents still work (proof times out to *unknown* with "update the agent") | Desired state (and M13's service checks) must be able to prove the current state of an idle agent host |
| M14 | Packages: `hostops.PackageVersions` runs the backend's `dpkg-query -W`/`rpm -q` for just the declared names plus a sentinel (`dpkg`/`rpm` must read as installed, else the package database is unreadable → unknown), as the login user over SSH; agents get a new read-only purpose `query` (`agentrpc.StateQuery`, ≤ 500 names) admitted when monitoring *or* package management is allowed by the ceiling; an older agent's "unknown purpose" becomes "update the agent". The router tries the agent first, SSH only when the agent never started (no fallback for a connected agent's refusal) | A check must not be a package transaction, need root, or bypass the agent's ceiling |
| M14 | Files: `ObserveFile` = `Stat` without following symlinks + `Hash` on the host through `files.FS` (agent or SSH, elevated only when the resource says so); a symlink or non-regular file is drift that is never restored (DROOL won't follow or replace it); content is read only by Review difference (`files.read`, plus `files.elevate` for elevated files; audited `desired.review`) and Accept | Hash on the host instead of downloading; M6's symlink protections stay in force |
| M14 | Checks of packages and files: every 15 min per host at a stable FNV offset; `CheckEvery`-based window (2× + 5 min) after which data is stale; a fixed pool of 4 workers fed by one loop (1 s tick); brought forward (spread over a minute) by configuration changes that add or change package/file declarations, agent reconnects, corrective jobs and refreshes; monitoring's service hook marks a host dirty only when its service list changed; a sweep every 30 s catches staleness. One host is checked at most once at a time | Expensive checks spread out; no goroutine per baseline, host or resource |
| M14 | Restore: `plan` (selection = hosts, keys, baselines; options = allow removals) skips conflicts, unknown, reconciling, blocked hosts (agent epoch mismatch, quarantined/revoked path, `state_versions` files domain not `ok`) and corrections the agent's effective ceiling denies, with reasons; `buildJobs` makes **one** job of the distinct corrective steps (installs → files → services → removals; split only past `MaxCorrectiveSteps` = 50) with per-host step masks and, for > 1 host, the recommended rollout (canary 1, batches of 5, 5 at a time, pause after 2 failures); the preview's `hash` covers steps, strategy, host masks and diffs, and execute re-plans and refuses `ErrPlanChanged`. Each job commits with its `ds_reconciles` row and the resources marked `reconciling` (a resource in a correction is skipped by later plans) | Never one job per resource; never a second simultaneous correction; what was previewed is what runs |
| M14 | Settling: the engine's new `OnJobDone` hook (and, at start, every active `ds_reconciles` row — jobs interrupted by a restart) triggers a proof check of the job's hosts, then each item is judged: compliant → `restored`; still different → `failed` with the job's explanation (failed step, cancelled, skipped, interrupted "not re-run automatically"); no proof → `restore_unverified`. Jobs with `reconcile_id` can't be re-run from the job page (409): a correction is always re-planned from fresh state | A restart never re-runs a correction; nothing is marked restored without proof |
| M14 | Accept current state: proof check first; refuses resources whose state can't be verified, resources already compliant or being restored, and symlinks or other non-regular files; writes a host declaration (`origin = accept`) using only the fields the managed declaration manages (a conflict uses the competing declarations' fields); a file's content is imported into the artifact store (≤ `filedeploy.MaxCapture`, 64 MiB) with its current owner/group/mode for the fields that are managed; a missing file becomes an exclusion. Baselines never change; *Change baseline* goes through the editor's impact preview | Accept is host-specific by default and never silently widens or narrows a baseline |
| M14 | Automatic restore: per baseline (`auto_enabled`, optional rollout), owner = the administrator who enables it (needs `desired.auto`, `desired.restore`, `jobs.execute`). Every 30 s, outside the loop, never concurrently: owner re-checked (exists, enabled, `desired.auto` + `desired.restore` + `jobs.execute` + the plan's needs) else pause + audit `desired.auto_paused` + one event; at most 1 active automatic job per baseline and 3 fleet-wide; candidates are drifted resources whose effective source is that baseline (never host declarations), not paused, past their backoff, on hosts not in maintenance and not within 5 min after it; proof check first; never `allow_removals`. Per resource: failures back off 15 min, 1 h, then pause; 3 re-drifts within 24 h of a restore pause it; a canary/threshold pause of an automatic job pauses the baseline (`attention`). Jobs run as the owner; audit actor `system` with "system execution via automatic restore of baseline X, owned by Y" | Never fights a person; never acts on someone's lapsed authority; bounded |
| M14 | Warnings in `/jobs/preview` (`desired_warnings`) for steps that contradict an effective declaration (stop/start/enable/disable, install/remove, deploy of other content or metadata), grouped per message with up to three host names, with the automatic-restore sentence when an automatic baseline (not a host declaration) decides it; only for callers with `desired.read` | The person changing a host learns before running a job that desired state disagrees |
| M14 | Events (aggregated per loop step): `desired.drift`, `desired.conflict`, `desired.auto_paused`; up to three hosts are named, otherwise counts. No alert, no notification routing | The dashboard shows what needs a person, without a storm |
| M14 | `/desired/summary` adds firewall (`fw_host_state`: enforced hosts, drift) and agent state-version conflicts (`state_versions`) only for callers who may read them; the UI links to the Firewall and Agents pages | One summary without merging three implementations |
| M14 | Artifact GC keeps files referenced by any baseline revision or host declaration | A baseline's file must outlive the 24 h unreferenced-artifact window |
| M14 | Test mode: `DROOL_TEST_DESIRED_TIMING=fast` (500 ms tick, checks every 20 s, automatic restore every 2 s, 3 s maintenance grace, 10 s proof wait) | Real-process tests without 15-minute waits; logged as TEST MODE |
| M15 | The sidebar is data (`components/layout/nav.ts`): sections with optional headings (labels, not routes) and items with an optional permission, a conventional `hint` and a `match` function for the highlighted entry (Runs owns `/jobs/*` except `/jobs/schedules/*` and `/jobs/workflows/*`; Hosts owns `/hosts/*` and `/file-deployments/*`). Sections without a permitted item disappear | One source for the sidebar, its tests and the search's coverage check |
| M15 | Themed names are UI wording only: *Packs* (`/groups`, API `groups`), *Shepherds* (`/agents`, `drool-agent`), *Runs* (`/jobs`), *Overview* (`/`). Where the UI composes its own wording from structured fields (`groups[]`, `level`) it says "Pack"; display text the server builds says it too (see the later entry — first kept as "group", changed after the screenshot review showed both words side by side). Routes, API, database, Go types, YAML and protocol are unchanged; no redirects for themed URLs | No backend or domain churn for a vocabulary change; the conventional term stays visible and searchable |
| M15 | Navigation search over a static catalogue (`components/layout/destinations.ts`): each destination has a title, sidebar section, path (deep links like `/firewall?tab=simulate`), aliases and the permissions its page needs. `lib/navsearch.ts` scores exact > prefix > word prefix > substring > all words, aliases at 0.97 of the title, the section heading at 0.6, the description at 0.4; a stable sort keeps the catalogue order for ties; destinations the user can't open are removed before scoring | Deterministic, testable (every required alias is a unit test), no server round trip, never advertises an unpermitted page |
| M15 | The palette is a modal combobox (`role=combobox` + `listbox`, `aria-activedescendant`); Ctrl/⌘+K toggles it anywhere, "/" opens it unless focus is in a text field or another modal is open; Tab is held in the field; Escape closes and focus returns to where it was | Keyboard-first without breaking typing |
| M15 | The host page summarizes and links instead of embedding: the full firewall panel moved to `/hosts/:id/firewall`, the network card to `/hosts/:id/network`; the overview keeps compact summaries (`HostFirewallSummary`, `HostNetworkSummary`, `HostMonitoringSummary`). Its *Needs attention* list is computed in the browser from data already loaded for the summaries (status reasons, the alert summary, desired counts, the firewall plan, the management path) and shows "Checking…" until every permitted source has answered | Triage without a new endpoint; no "nothing needs attention" while loading |
| M15 | Provenance is one presentational component (`components/ui/Provenance.tsx`) fed by mappers that only reorder what the server returned: desired state — `source.shadowed` (overridden), the effective source or `source.competing` (conflict, nothing in effect), displayed broadest scope first; monitoring — the field's `inherited` value and source, a pack conflict when `inherited.conflict` is set, and the edited scope's own value. Scopes the server didn't report are never shown | The server stays the only resolver; the two areas explain inheritance the same way |
| M15 | Monitoring's settings page moved from `/settings/monitoring` to `/monitoring/settings` (the old route redirects); Settings keeps account, API tokens, users, notifications and system | Health thresholds are where people look for them |
| M15 | Narrow screens (< 1024 px): the sidebar is an off-canvas dialog (focus trap, Escape, backdrop, closes on navigation, focus back to its button); < 768 px tables marked `rtable` render as one card per row by CSS alone (`td[data-label]` → `::before`, the row's primary cell leads via `order: -1`), so the DOM, keyboard order and tests are the same at every width; tables whose columns belong together opt into sideways scrolling (`mobile="scroll"`); the Firewall Designer opens in Rules below 1024 px | One component tree for all widths; no hidden duplicate controls |
| M15 | The Agent Root CA warning moved from a full-width banner on every page to a persistent header indicator that expands to the explanation, plus a Security card on the Overview; the plaintext-HTTP banner stays full width (an active exposure) | Unresolved posture stays visible without occupying every page |
| M15 | User-facing firewall strings from the server say "automatic rollback timer" / "automatic rollback protection" instead of "dead-man" (environment refusal, apply hint, override refusal); docs and code keep the term. A host's stored detection result keeps the wording it was detected with until the host is checked again | Primary UI copy per the M15 vocabulary; no behaviour change, and no migration rewriting stored results for a wording change |
| M15 | Display text the server builds for the UI names a host group *pack*: schedule and maintenance targets ("pack linux-servers"), baseline assignments and "via pack …", desired-state and health provenance ("Inherited from pack: …", "Conflict between packs …"), health summaries ("1 inherited pack override"), the rollback-timeout source, the designer's notice about packs a YAML document omits. API fields, routes, validation errors, the policy YAML (`group:NAME`) and the firewall's rule explanations ("from group web") keep *group*; stored text (history entries, detection results) keeps the words it was recorded with | One name on screen without renaming the API; the explanations stay because an enforced host's apply preview diffs them against the policy stored at its last apply — renaming them would list every pack rule as changed |
| M16 | The audit chain hashes length-prefixed (netstring) fields exactly as stored, details byte for byte, in a documented order; never a re-serialization of JSON or a Go map | Anyone holding the records (the remote copy) can verify them without DROOL, and no encoder's choices can change a hash |
| M16 | The first chained record commits to every older record through a digest; checkpoints carry an HMAC under a key derived from the master key and never stored | Existing installations get the chain without rewriting history; a checkpoint can't be forged without the master key |
| M16 | Verification reports every problem it finds; an acknowledgement covers one problem signature, so a later break is still reported | Acknowledging one break must not hide the next |
| M16 | The remote copy's queue is the audit table and a cursor that moves only on a 2xx; at-least-once | Nothing extra to store or lose; a restart resumes; receivers deduplicate by seq and hash |
| M16 | Re-authentication is a timestamp on the session checked by the server per route (or by a handler when only some requests need it); API tokens never pass; the UI repeats the request after the prompt | Server-enforced, not a modal; one mechanism for every high-impact action |
| M16 | TOTP is implemented in-house (RFC 6238, SHA-1, 6 digits, 30 s, ±1 step, each step once) with the secret sealed and recovery codes hashed | A small, fully tested piece; no dependency for 60 lines |
| M16 | JIT SSH mints the certificate inside the SSH handshake for a fresh in-memory key, per connection; pooled connections are keyed by CA and principal and are handed out only until 30 s before their certificate expires (§33.2) | Nothing to store or leak; a certificate is used for one handshake |
| M16 (review) | A pooled connection authenticated with a certificate is handed out only until 30 s before that certificate's `ValidBefore`, and a connection tunnelled through one no longer than its jump connection; a lease that waited for a session slot is checked again; an operation already holding the connection finishes on it | OpenSSH checks a certificate only at authentication, so a pooled connection must not outlive the certificate a fresh connection would need (it was reused for up to an hour before); ending running operations at expiry would be worse, and sshd lets established sessions run too |
| M16 | Hosts are moved to certificates by an ordinary job step that verifies on a fresh certificate-only connection, and switched only after that; static credentials stay supported | No transition can strand a host; rollouts, history and permissions come from the engine |
| M16 | A restore gives the controller a new epoch and *sets aside* what was in progress (jobs interrupted, open firewall transactions closed as unknown, unsent notifications dropped, schedules, automatic restore and rollouts held) in the staged database, before the swap | The backup describes a world that moved on; nothing it records as in progress can be resumed or repeated safely, and doing it before the swap means the restored controller never starts in an unsafe state |
| M16 | Hosts move to the new epoch only by comparison: identical or controller-ahead → automatic; host-ahead, conflicting, foreign-epoch, identity changes → an administrator accepts the host's state (never rolls it back) | "DROOL knows what it knows": the controller's intent stays authoritative, a host's newer state is never silently overwritten or silently adopted |
| M16 | The epoch transition is all-or-nothing on the agent, idempotent, with the identity's epoch written last; the firewall moves with drool-fw `rebase` (tool v2), which changes the epoch and nothing else | A lost answer or a crash on either side is settled by sending the same transition again; no rule changes during a reconciliation |
| M16 | Reconciliation is bounded: 8 workers, one per host at a time, at most 64 hosts queued per 15 s round; Shepherd hosts are compared when they connect, never polled | 200 Shepherds reconnecting at once must not become a request storm; measured in `TestBoundedReconnect` and the soak |
| M16 | A Shepherd remembers a revocation and reports it until a controller moves it to a new epoch | A controller restored from an older backup can't know the revocation otherwise; older Shepherds don't report it (documented) |
| M16 | After a restore, the next trust manifest is numbered at least 1000 above the restored one (and above anything a Shepherd reported) | Agents refuse older or equal manifests, and those that can't connect can't say which version they have |
| M16 | Every restore file move is journaled before it happens; DROOL refuses a data directory with a journal; `drool restore --recover` undoes the moves | Before, a crash between moving the live database aside and moving the restored one in started an empty controller |
| M16 | Before migrating, an automatic backup without the master key (newest three kept); failure refuses to start | Migrations only go forward; the documented way back is restoring that backup with the older version |
| M16 | `schema_migrations.drool_version` is added by the runner itself, not by a migration file | It is the runner's own bookkeeping, and rows for migrations applied in the same run before such a migration would have none |
| M16 | Master key rotation is an offline CLI command (controller stopped) that re-seals every registered sealed value in one transaction; the previous key file is saved, not destroyed | The running controller seals with the key it loaded; older backups need the old key, and only the operator can decide where it goes |
| M16 | Replacing an in-use credential's value and revoking a host's only path need an explicit acknowledgement (409 with the consequence, then a flag); trust changes need re-authentication | Refuse or require an understood recovery path; a stolen session alone isn't enough for trust decisions |
| M16 | Compatibility is judged from announced features and the host's configuration (a missing feature the host needs → update required; otherwise reduced) | Capability negotiation instead of version equality; an operation is never sent to a Shepherd that can't do it |
| M16 | `query-1` is announced but judged by nothing; a missing feature counts only when an older Shepherd really lacks the capability | M14/M15 Shepherds answer Desired State queries without announcing it: counting it would have told every M15 Shepherd in a homelab that it can't do Desired State checks |
| M16 | Soak Shepherds run in the test process with a stub command runner | 200 real agents with real command execution would load the workstation; the controller under test is a real process and is what is measured |
| M17 | One build definition: the `Dockerfile` (BuildKit) builds the web UI, the controller, both Shepherd binaries, the notices, the release files (`--target artifacts`) and the image; `packaging/release/build.sh` runs it for `linux/amd64` and `linux/arm64` from a fresh repository holding exactly the release commit, then packages (nfpm), SBOMs (syft), checksums and `release.json` | Binaries built once and used everywhere (the pipeline checks the image's binaries are the archived ones byte for byte); nothing from a working tree can get in; the toolchain is pinned by digest. The final stage only copies, so arm64 needs no emulation |
| M17 | Reproducible: `SOURCE_DATE_EPOCH` = the commit's time, `-trimpath`, BuildKit `rewrite-timestamp`, no provenance attestations, deterministic tar/gzip (`relkit tar`), nfpm with a fixed build host, SBOMs with the commit time and a stable namespace | Anyone can rebuild a release and compare `SHA256SUMS`; checked by building twice, the second time without any cache |
| M17 | `.git` stays in the build context, so Go records `vcs.revision` and `vcs.modified`; `version.Get` trusts that over the stamped commit, and any modified or mismatching build is the development channel | Provenance from the toolchain, not only from what a build script claims; "a dirty tree accidentally became the release" can't look like a release |
| M17 | Channels from the tag only: `vX.Y.Z` stable, `vX.Y.Z-rc.N` prerelease, anything else development; `--version` names untagged candidates and never makes them a release | A channel is a statement about where a build came from |
| M17 | `drool-agent version` keeps printing exactly the version; details are behind `--json` | Every Shepherd so far runs the new binary's `version` during an update and requires exactly its signed version back: a richer output would have made every older Shepherd's update to 1.0 fail its preflight |
| M17 | `drool release sign` takes OS and architecture (and, for a build at a release tag, the version) from the binary's build information and refuses a mismatching manifest, another program, or a build from modified source | `--arch` used to default to amd64; a wrong manifest is only discovered on the host, after the download. `-trimpath` keeps `-ldflags` out of the build information, so untagged builds still need `--version` |
| M17 | Release integrity: `SHA256SUMS` over every file (including `release.json`), signed offline with an OpenSSH key (`ssh-keygen -Y sign`, namespace `drool-release`, `packaging/keys/allowed_signers`); ~~no project signature on Shepherd update manifests~~ (**superseded** before 1.0: official Shepherd releases, below and §42) | OpenSSH is everywhere and verifies without trusting DROOL itself; the release key is a separate trust domain from the operators' Shepherd release keys, which keep deciding what their hosts run; no key is ever in CI or on a controller |
| M17 | Native packages (nfpm) own only `/usr/bin/drool`, `/usr/lib/drool`, the unit, `/usr/share/drool` and the docs; `/var/lib/drool`, `/etc/drool` and the `drool` account are created by the scripts and never removed — by `remove` or by `purge` | Removing a package must not cost the database, the master key or the backups; deleting them is the operator's explicit act (the scripts say how) |
| M17 | Installing a package doesn't start the service; upgrading restarts a running one | The same on Debian and RHEL; no listener appears before the operator has seen the configuration; an upgrade picks up the new binary and its pre-upgrade backup at once |
| M17 | The controller finds the Shepherd binaries next to `drool` or in `../lib/drool` | Packages put them in `/usr/lib/drool`, the archive in `/usr/local/lib/drool`, the image next to the binary; no configuration needed |
| M17 | DROOL's commands refuse to run as root on a data directory owned by another user | Root-owned files written by a backup, a restore or a key rotation would break the service afterwards |
| M17 | The unit adds `ProtectHostname`, `ProtectClock`, `ProtectKernelLogs`, `ProtectProc=invisible`, `RestrictNamespaces`, `RestrictRealtime`, `RemoveIPC`, `MemoryDenyWriteExecute`, `SystemCallArchitectures=native`, `SystemCallFilter=@system-service`, `RestrictAddressFamilies` (with `AF_NETLINK` for the setup message's addresses) | Packaging must not weaken the sandbox; checked under real systemd (`systemd-analyze security`) |
| M17 | The image's base is distroless `static-debian13:nonroot`, pinned by digest | A longer support life and a newer CA bundle than Debian 12's; the binary is static, so nothing else changes |
| M17 | Podman is supported rootless through a Quadlet unit shipped with the release (`drool.container`); rootless Docker uses the same `compose.yaml` | Quadlet is Podman's own way to run a container as a service; one small file, tested, instead of a Compose emulation layer |
| M17 | `GET /system/security` reports only what the controller can tell (test switches, administrators without two-factor, the master key's place and leftover rotation files, backups in the data directory and their size, hosts on certificates); the checklist says what it can't see | A checklist that claims "backed up" from what it can't observe would be fake reassurance |
| M17 | A host held as `incompatible` after a restore is remembered (also across restarts) and compared again when its Shepherd connects speaking the protocol and announcing `epoch-1` | Found by the mixed-version qualification: `incompatible` counted as settled, so an updated M15 Shepherd was never compared again although the reason (and the README) said it would be |
| M17 | `ARCHITECTURE-ALPHA.md` keeps its name for 1.0 | 124 references in 107 files, including append-only migrations; it is the implementation record from the Technical Alpha to 1.0, and section numbers don't change |
| M17 | No package repository and no hosted update service | Downloadable, checksummed and signed packages are enough for 1.0; a repository is infrastructure to run and secure, and the controller doesn't update itself by design (§33.11) |
| M17 | A service operation that starts or restarts a unit looks at it again 2 s after it first reads active (`hostops.StartCheck`) and reports a `ServiceError` with `systemctl status` if it isn't running then | systemd reports a `Type=simple` service started as soon as its process exists, so a unit that exits at once read as active when the check came first: on Debian 13 under emulation a unit running `/bin/false` was reported started. M4's verification depended on timing; now a unit has to stay up. Costs 2 s per start or restart |
| After M17 | `drool restore` and `drool backup verify` take `--master-key -`: the key file comes on standard input, ahead of the backup when that is `-` too (the backup begins at gzip's magic bytes, which a key file never contains) | In a container the restore runs as the image's unprivileged user, which can't read the operator's `0600` key file; the documented workaround made the key world-readable for the restore. Piped in, it needs no permission change, no mount and no `sudo`, and works the same for Docker (rootful or rootless), Podman and native installations |
| After M17 | The README is the project's front door (what DROOL is, its status, a development quick start, platforms, limitations, links); step-by-step documentation lives in the user guide (`docs/wiki`, published as the GitHub wiki), including the commands the release tests run (§34.10 describes the M17 README) | The 2,000-line README had become a second manual next to the wiki; one place per explanation keeps them from contradicting each other |
| After M17 | Adding a host sets up DROOL's own SSH account by default: an account the administrator already has is used only to create a dedicated one, which signs in with `controller-default` and has passwordless sudo; the host switches to it only after a brand-new connection proved it (§36) | Every first host used to start with `useradd`, a sudoers file and a key paste by hand, and the administrator's own password or key stayed in DROOL for good. The setup account is never changed; what was typed for the setup is forgotten |
| After M17 | The setup is a typed, internal job step (`ssh_account`): exclusive host lock, never retried automatically, submitted only by `POST /hosts/{id}/ssh-account` (credentials.manage, hosts.write, re-authentication), refused by `POST /jobs`, workflows, schedules and re-runs | It changes how DROOL signs in, so it gets the strongest permissions and a recent sign-in; as a job it has the host lock, output, a result and a place in the run history, without a second execution path |
| After M17 | Credentials typed for the setup are stored as `secrets.temporary` and deleted in the switch transaction through `secrets.DeleteTx`, which refuses while any host (jump hosts included) references them: those are kept and reported | A credential shared by another host can't disappear under it; stored credentials the administrator chose are never deleted by the setup |
| After M17 | DROOL recognizes an account as its own only by a root-only record written before the account (`/var/lib/drool/ssh-accounts/<user>`: `controller-default`'s fingerprint and the account's user ID) | The name proves nothing: an existing `drool` account (the controller package creates one) must never be taken over |
| After M17 | `compose.yaml` and the Podman unit name DROOL's development image on GitHub Container Registry by default (`${DROOL_IMAGE:-ghcr.io/irnutsmurt/drool:main}`, `Image=ghcr.io/irnutsmurt/drool:main`); a release still pins its own version in both, and a source build runs its own with `DROOL_IMAGE=drool:local` | They used to default to `drool:local`, an image only a source build has: downloaded on their own they couldn't run (Compose: *pull access denied for drool*), and the guide needed a hidden `.env` file (Podman: a `sed` edit) to point them at GitHub |
| After M17 | Shepherds are told `agent.public_port` when it is set, else `agent.port`, which is only where the endpoint listens. One resolver, `agentsvc.Service.Endpoints(webHost)`, feeds enrollment tokens, installation over SSH (one host and bulk conversion), the endpoint shown before an installation and on Trust (PKI), and the startup log; the enrollment answer uses the same port rule. `agent.advertise` entries win; an entry, or a proposed controller address, without a port gets the same port (§37) | Dogfooding on a Synology NAS: Compose published the endpoint as `2043:9443`, the controller advertised `<host>:9443`, and the installation failed with *connection refused* (rolling back as designed). A controller in a container can't see Docker's port mapping, so the published port has to be configuration |
| After M17 | `compose.yaml` declares the two public ports once, in `x-drool-ports` (YAML anchors): `web` (8080) is published and given to DROOL as `DROOL_SERVER_PUBLIC_PORT`; `shepherds: ${DROOL_AGENT_PUBLIC_PORT:-9443}` is published onto the container's 9443 and given to DROOL as `DROOL_AGENT_PUBLIC_PORT`. The container ports stay 8080 and 9443 | One number to change, in the file, in the environment or in an optional `.env`, and no way to publish one port while advertising another. It works in Synology's Container Manager, where the YAML is edited in the browser (rendered with Compose v2.9.0, v2.20.1 and 5.5.1) |
| After M17 | A bulk conversion stores the web UI's host name with its rollout (`agent_rollouts.web_host`, migration 0022), and each host's installation resolves its endpoint from it | Conversions run after the request that started them; without it every converted host was told `localhost` unless `agent.advertise` was set |
| After M17 | A Shepherd's `effective` capabilities are an empty object until its first hello (enrollment records only the ceiling); the API schema says so (`ceiling` and `effective`: `AgentCaps` or `{}`), and the UI shows *Not reported yet* instead of the list | The host page polls every 5 s and counts an enrolled identity as live: a poll between `drool-agent install` enrolling and the service's hello rendered the capability list from `{}`, and the page failed ("Cannot read properties of undefined (reading 'enabled')") right after a successful installation (dogfooding). With the schema honest, the type checker finds every place that reads them |
| After M17 | A Pack is created with a name and an optional description alone and can stay empty; it lands on its own page (`/groups/:id`), which says *No hosts assigned yet* and has **Add hosts**, and removes hosts one at a time without deleting the Pack. Membership changes one host at a time: `POST /groups/{id}/members` (adds, keeps the other members) and `DELETE /groups/{id}/members/{host_id}`, from the Pack's page or a host's (**Change…** next to its Packs); `PATCH /groups/{id}` with `host_ids` still replaces the whole set | Dogfooding: creating a Pack opened a checkbox list of every host, so a Pack felt like a one-off selection instead of an object of its own. The schema already allowed empty Packs (`groups` and `group_members` are separate tables, and members were optional on create), so the fix is the UI and two routes; changing one member at a time also stops two people's edits from replacing each other |
| After M17 | The SSH account's sudo rule is minimal (`<user> ALL=(ALL:ALL) NOPASSWD: ALL`); a `requiretty` exception is added only if sudo, run as the account without a terminal, asks for one and the host's own visudo accepts it; the rule's effect is proven by that no-terminal sudo instead of root's `sudo -l -U`; a configuration with problems before is compared problem by problem (§36.2) | Dogfooding: on sudo-rs (Ubuntu's default since 25.10), which has no `requiretty`, visudo rejected the line DROOL wrote on every host, and the setup stopped safely. Root's `sudo -l -U` also fails without a terminal where `requiretty` is global, and with problems already on the host nothing caught a rule that made the configuration worse |
| Before the RC | Official Shepherd releases: the DROOL project signs each release's Shepherd binaries offline with a **Shepherd release key** (`drool release keygen` format), whose public half is built into DROOL and every Shepherd (`internal/release/official_keys.txt`); a separate key from the OpenSSH key over `SHA256SUMS` and from operators' keys. Shepherds trust it unless their host says `updates: {official: deny}` | Without it every operator had to create a key, make each host trust it, sign each binary and upload it before any Shepherd could update: automatic updates had nothing to install. A key of its own keeps the trust domains apart: rotating or losing one doesn't touch the other, and the controller still can't add a key to a host (§42.3) |
| Before the RC | Shepherds follow the controller: the official update is the Shepherd that ships with the controller, when the controller is itself an official release; the controller fetches only its signed description (`<source>/download/<tag>/drool-agent_<v>_linux_<arch>.release.json`, default the GitHub releases page) and verifies it against the built-in key, its own version and channel, its protocol range and its own binary. Never a binary from the internet, never newer than the controller | Keeps "update the controller first", needs no download of code, works without internet access (one small file uploaded by hand), and means a stolen project key plus the release page can't push code to a fleet: the binary is the one the operator installed with DROOL (§42.3) |
| Before the RC | Automatic stable updates are the default (canary 1, batches of 5, pause at the first failure, 120 s health window). Migration 0026 writes `manual` for installations that rolled out releases of their own without saving a policy; a saved policy is never changed | The expected behaviour of a new installation; an existing explicit choice, or an existing manual practice, is kept (§42.5) |
| Before the RC | A Shepherd's refusal before it changes anything (untrusted key, `updates.policy: deny`, failed before, downgrade, upgrade path, protocol, platform, busy) is a *skipped* host, not a failed update; 1.0 Shepherds say which (`UpdateReport.refusal`), older ones are known by their fixed words. 1.0 Shepherds also report their release trust in the hello, so the controller doesn't offer what they'd refuse | With automatic updates on, one host's local decision paused every rollout at the canary (§42.4) |
| Before the RC | The canary wave needs a host that takes the update: short of one, the next connected pending host becomes the canary; rollouts include only Shepherds behind the version, connected and trusting ones first | An offline or already-current canary let the batches start without any canary result (§42.4) |
| Before the RC | A version that failed on a host is tried there again only by a person's **Try again** (`UpdateOffer.retry`); the Shepherd still refuses it by itself | Before, a failed version was refused forever, even when a controller restart during the health window had caused the rollback (§42.4) |
| Before the RC | Release discovery is a check of the source every 6 h (hourly while the own release isn't published, 30 s after start, **Check now**), its result kept in `agent_release_check`; `agent.update_source` names a mirror or `off` | One small, bounded HTTP client like the GeoIP downloads; what the source says counts only after it verifies, so the source decides availability, never trust (§42.3) |

---

## 23. Agent (M7)

### 23.1 Components

| Package | Role |
|---|---|
| `cmd/drool-agent` | The agent binary: `install`, `enroll`, `run`, `status`, `presets`, `trust-release-key`, `version`, and the internal `update-apply` / `update-guard` |
| `internal/agent` | Agent runtime: state store, durable op ledger, op log, admission, execution, file server, session, telemetry, renewal, updates |
| `internal/agent/localfs` | `files.FS` on the host, confined by `openat2` |
| `internal/agentrpc` (+ `api/agent/v1/agent.proto`) | Wire types, protocol range and negotiation, enrollment bundle |
| `internal/agentpki` | Root / intermediate / leaf certificates, trust manifests |
| `internal/agentcaps` | Capability model, presets, ceiling parsing, narrowing (`Intersect`), checks |
| `internal/agentsvc` | Controller side: PKI store, gRPC service, enrollment, sessions, agent Driver, `files.FS` proxy, updates and rollouts, endpoint migration, install over SSH |
| `internal/mgmt` | Management router: picks agent or SSH per step |
| `internal/hostops`, `internal/hostexec` | Typed operations shared by both transports |
| `internal/release` | Release signing and verification |

### 23.2 Trust

```
Agent Root CA (offline after setup)   ── signs ──>  trust manifest v1, v2, …  (allowed + revoked intermediates)
   └─ Controller Intermediate (online, sealed)
        ├─ controller server cert  (URI urn:drool:controller)
        └─ agent leaf certs, 30 d  (URI urn:drool:agent:<identity>, key = agent identity key)
Release-signing key (never on the controller)  ── signs ──>  agent release manifests
```

Agents pin the root (fingerprint in the enrollment bundle, certificate fetched over the agent port and checked against it). They store the highest manifest version and reject older ones. On reconnect the controller sends the newest manifest before anything else. Keys are separate: Agent CA, release signing, browser HTTPS and the future OpenSSH CA.

CLI: `drool agent-ca status | export-root | remove-root | rotate | sign-manifest | import-manifest`, `drool release keygen | sign | verify`.

### 23.3 Enrollment

1. An admin creates a token (UI or API): random, hashed at rest, 1 h default TTL, single use, revocable, optionally bound to a host or a group, with a capability preset. The bundle `drool-enroll-1.<base64url>` carries endpoints, the root fingerprint and the token.
2. The agent generates its Ed25519 identity key locally, fetches and checks the trust material, and sends a CSR-equivalent signed with the identity key.
3. In one transaction the controller consumes the token, creates the host (or attaches to the token's host), creates the identity and issues the certificate. A hostname or IP that matches an existing host is never used to associate.

Install paths: `curl … /agent/install.sh` (pinned with `--pinnedpubkey` against the web certificate), **Install DROOL Agent** on an SSH host (binary and script over the pinned SSH connection, token on stdin), or manual (`drool-agent install --token-file -`).

### 23.4 Management model

`hosts.management_method` (`ssh` | `agent`) is the preferred path, `management_fallback` (`none` | `ssh`), `ssh_configured`. The API's `management` object reports preferred, fallback, active path, agent state and reason. Job hosts record `transport` and `transport_note` ("agent not connected; SSH fallback before dispatch").

### 23.5 Capabilities

`/etc/drool/agent.yaml` (root:root 0600) is the ceiling; the controller's requested set is intersected with it and can only narrow. Capabilities: `monitoring`, `shell`, `packages` (`configured_repos_only`), `services` (`allow` list), `reboot`, `files` (`allowed_paths`, `restricted`), reserved `firewall`, `users`. Root-equivalent capabilities (shell, unrestricted files, package installs, unrestricted services) are labelled in the UI. Presets: Monitor Only, Managed, Full; `drool-agent presets` prints exactly what each enables. Denials are `CapabilityError` (HTTP 403 for file requests).

### 23.6 Lifecycle

| Event | Behaviour |
|---|---|
| Renewal | At 2/3 of the 30-day lifetime, over the session, with a new certificate for the same key |
| Expiry | Recovery RPC within `agent.recovery_grace` (180 d) |
| Revocation | Identity revoked → sessions closed, renewal and recovery refused, management blocked without fallback |
| Conflict | Quarantine; both sessions shown; admin keeps one or revokes |
| Controller offline | The agent keeps its last committed state and retries with backoff |

### 23.7 Updates

On the host: `/usr/libexec/drool-agent/versions/<v>/drool-agent` with `current` and `previous` symlinks, state in `/var/lib/drool-agent` (identity, certificates, manifest, ledger, `update/`), ceiling in `/etc/drool/agent.yaml`, release keys in `/etc/drool/release-keys.d/`, logs in `/var/log/drool-agent/`. None of these directories is reachable through the agent's file operations.

States per host: current, update available, staged, updating, healthy, rolled back, failed, offline. Policy: manual or automatic (channel or exact version), canary size, batch size, max failures, health window. Release upload requires a registered release key; `min_upgrade_from` and version comparison refuse unsupported downgrades. Before the release candidate this became official releases, verified by the controller and every Shepherd, with automatic stable updates by default (§42).

### 23.8 Agent-side audit

`/var/log/drool-agent/operations.log`: JSON lines (op id, type, controller identity, timestamps, outcome, error class), 0600, rotated at 10 MB × 10 compressed. No command text, file contents or secrets. The controller has no API to change it.

### 23.9 API, RBAC, UI

New permissions: `agents.read` (all roles), `agents.enroll`, `agents.manage`, `agents.pki` (admin). Routes under `/api/v1/agents/…` and `/api/v1/hosts/{id}/agent…` (see `api/openapi.yaml`); public `/agent/install.sh` and `/agent/download/{platform}`. UI: an Agents page (fleet, tokens, PKI, releases, rollouts, endpoints), a management card on the host page, "Managed via" on the hosts list, "Via" on job results.

### 23.10 Testing

- **Unit:** PKI and manifests (pinning, rollback, rotation, revocation), release signing (altered manifest, altered binary, wrong key, platform, protocol, downgrade), capabilities and narrowing, `localfs` confinement, admission (duplicate, expired, future, stale, foreign epoch, capability), durable ledger and abandon, op log without secrets, staging (interrupted download resumes; a corrupted partial is discarded), apply/guard (healthy commit, crash loop, deadline, interruption before and after the switch, atomic links).
- **In-process end to end** (`internal/api`, real gRPC and mTLS): enroll/operate/fallback/revoke, token expiry/reuse/revocation, renewal and recovery inside and outside the grace, clone quarantine, manifest rotation, planned rotation, protocol compatibility at both boundaries, loss during an operation (exactly one execution), endpoint migration.
- **Containers, real binaries** (`test/integration/agent_test.go`, all seven distros): install over SSH, commands, packages, services, files and deployment through the agent, the op log, SSH fallback with the agent stopped, a local ceiling change that SSH must not bypass, manual enrollment with a token on stdin, token reuse, agent-observed addresses; updates: signed update with health confirmation, offline catch-up, a broken signed release rolled back with the rollout paused at the canary, an untrusted signing key refused by the agent, an unsigned binary refused by the controller.
- **VMs with real systemd** (`vm_agent_test.go`, Debian 12, Ubuntu 24.04, Rocky 9 with SELinux enforcing): the unit with its `ExecStartPre` guard, services, packages, a real update and an automatic rollback through systemd, reboot through the agent, revocation.

---

## 24. Firewall (M8)

Implements DESIGN §15–19 for plain-nftables hosts. Everything here is as built; deviations from the design and their reasons are in §22.

### 24.1 Components

| Package | Role |
|---|---|
| `internal/firewall/policy` | Intent model: selectors (host, group, internet, internal, any, cidr), built-in and custom services, `internal_ranges`, validation |
| `internal/firewall/compile` | Backend-independent per-host policy: ordered input/output rules with reasons, named address sets, management-path flows and conflicts, evaluation (simulation) and Explain |
| `internal/firewall/nftables` | The nftables backend: rendering, set-element deltas, environment detection (`nft -j list ruleset` and service checks) and the compatibility matrix |
| `internal/firewall/hostfw` | The on-host tool `drool-fw` (POSIX sh), the boot unit, detect and snapshot scripts, and the Go wrapper that runs them through any `hostexec.Runner` |
| `internal/firewall/observe` | Connection snapshots (`ss`) → aggregated flows |
| `internal/firewall/fwsvc` | Controller service: storage, plans, transactions over agent or SSH, converger (set propagation, drift, unresolved transactions), observations, suggestions, simulation |
| `internal/agent/firewall.go` | Agent side: runs `drool-fw` as root, reconnects after an apply, reports status and endpoints, samples connections when learning is on |

### 24.2 Intent and compilation

A policy is `from → to: services`, allow or deny. For host H:

- **Inbound** rules come from every enabled policy whose `to` covers H (host, group, or `any` = every managed host); the peers are the policy's `from` selectors.
- **Outbound** rules come from policies whose `from` covers H. Denies always compile; allows only when H's outbound mode is `restrict` (default `allow`).
- **Peers:** hosts and groups become *dynamic* named sets (`h_<hash>_4/6`, `g_<hash>_4/6`) filled with the members' validated addresses (configured, discovered and agent-observed; loopback and link-local excluded). `internal` is the static set `internal_4/6` from `internal_ranges`; `internet` is `!= @internal_X`; `cidr` is an inline set; `any` matches everything.
- **Services:** `dns` compiles to UDP and TCP 53; ICMP entries name a type and compile per family.
- **Order** in each chain: base rules (invalid drop, established/related accept, loopback, essential ICMP/ICMPv6 and DHCP), **management path**, denies, allows, default (input drop; output accept, or drop when restricted). DROOL owns exactly one table, `inet drool`, with base chains at `filter` priority.

Hashes: `RulesHash` covers everything except dynamic set elements; `SetsHash` covers only those. A policy or management-path change changes `RulesHash` (a transaction); an address change only `SetsHash` (a set update).

### 24.3 Management-path protection (DESIGN §18)

The required flows come from what the host itself saw, measured over a fresh connection at every transaction:

- **SSH path:** `SSH_CONNECTION` over a brand-new connection (through the jump host when there is one): the source address and sshd port as the host sees them, which is right behind NAT and port forwarding.
- **Agent path:** the controller endpoints as the agent resolves them (`Hello.firewall_json`), needed when outbound traffic is restricted.
- **Jump role:** a host other hosts are reached through allows outbound SSH to them.

These accept rules precede denies. A deny that matches a management flow makes compilation report a conflict, and the transaction is refused with the policy's name and the flow. The audited emergency override (`firewall.override`, reason required) drops just the conflicting accept; the dead-man transaction still protects the host.

### 24.4 Environment detection (DESIGN §16)

`detect` (read-only, runs everywhere) reports systemd, nft, required tools, firewall-related services, Docker and Tailscale processes, legacy iptables rule counts, resolvers and the full `nft -j list ruleset`. Enforcement is refused when any of these hold: no systemd; no nft; missing `systemd-run`/`flock`/`sha256sum`; firewalld, UFW, Docker, Tailscale or iptables/netfilter-persistent services active; legacy iptables rules; or **any foreign nftables table with rules or a dropping base chain** (named when recognised: Docker, UFW, firewalld, Tailscale, libvirt, Podman, Kubernetes CNIs, iptables-nft). Empty foreign tables with accepting base chains (Debian's default `inet filter`) are allowed. Monitoring, snapshots, simulation and Explain work regardless.

### 24.5 The host tool and its files (DESIGN §17.0)

```
/usr/libexec/drool-firewall/drool-fw          the tool (bin_t under SELinux)
/etc/systemd/system/drool-firewall.service    boot loader: Before=network-pre.target, After=nftables.service
/var/lib/drool/firewall/
  transaction.json   journal: tx id, state, previous/candidate hash, state_version, deadline
  committed.nft      the last committed policy (what boot and rollback load)
  committed.json     committed hash, revision, epoch, live-table hash (drift), sets generation, rules hash
  candidate.nft      only during a transaction
  seen               recent transaction ids (duplicates are refused)
/run/drool-fw.lock   flock: apply, commit, rollback, boot and sets never interleave
```

Writes go temp file → `sync FILE` → rename → `sync DIR`. Every nft file starts `table inet drool` / `delete table inet drool`, so it replaces DROOL's table atomically and touches nothing else. The tool never runs `nft flush ruleset` and never edits `/etc/nftables.conf`.

| Command | Does |
|---|---|
| `apply` | expiry → duplicate → pending → epoch/stale → write candidate, check its hash → `nft -c` → drift check (live table vs `committed.json`) → **journal `pending` (durable)** → arm `systemd-run --on-active=Ns drool-fw rollback` → `nft -f candidate` (failure: restore at once) |
| `commit` | only for the pending transaction, before its deadline, with the candidate's hash: promote candidate → `committed.json` → journal `committed` → stop the timer |
| `rollback` | the timer's action: load `committed.nft`, journal `rolled_back`. Idempotent; no-op after a commit |
| `boot` | pending journal → roll back (never the candidate); otherwise load `committed.nft` |
| `sets` | apply a set-element delta to the committed rules (same rules hash, newer generation, no pending transaction, no drift) and record the new committed file |
| `status` | journal, committed revision/epoch/hash, live-table hash, boot unit |

The SSH path sends the tool and boot unit with each mutating call and installs them when their hash differs; the agent carries its own copy.

### 24.6 Transactions (DESIGN §17)

A transaction is the job step `firewall_apply` (one host at a time, stopping at the first failure). Per host, under an in-process firewall-domain lock:

1. Resolve any unresolved earlier transaction; refuse while one is open.
2. Pick the path with the M7 router rules (agent if preferred and connected and it announces `firewall-1`, else SSH; blocked hosts get nothing; an agent whose ceiling lacks `firewall` fails with `CapabilityError` and never falls back to SSH).
3. `detect` → refuse unsupported environments. `status` → refuse a pending host transaction, a foreign epoch, a host revision ahead of the controller's (host_ahead, reconciliation) and drift (unless "replace drifted rules").
4. **Measure a fresh path:** agent: request a reconnect and time the new mTLS session; SSH: dial a new connection through the full jump chain (never the pool) and read `SSH_CONNECTION`.
5. Compile with the fresh management flows; refuse conflicts (unless overridden) and a policy that changed since the preview (`expect_rules_hash`).
6. Timeout: host → smallest group value → default (120 s). Refused below 10 s, and below 3× the measured connection time unless `override_timeout`.
7. Reserve the next revision (`state_versions`, domain `firewall`) and record the transaction (`preparing`).
8. `apply` on the host. The reboot-blocking marker is set from here until the end.
9. **Verify over a new connection:** agent: ask it to reconnect (it also drops the apply session itself); wait for a session that connected *after* the apply, check its hello's journal (transaction id, revision, candidate hash), then send `commit` only over that session (`agentsvc.WithSessionAfter`). SSH: dial a brand-new connection through the jump chain, check `status`, `commit` over it.
10. Not committed by the verification deadline (deadline − min(15 s, timeout/4)): wait past the deadline, then confirm the host's own rollback (`rolled_back` with its reason). If the host can't be reached, the state is `unknown`; the converger resolves it when the host is reachable or the agent reconnects.

States: `preparing`, `pending`, `committed`, `rolled_back` (the host restored its previous policy), `failed` (refused or failed before anything changed), `unknown`. Nothing that wasn't committed is reported as success.

While a transaction is open (and until its deadline + 2 min if unresolved), reboots, system updates, service actions on network units (`networking`, `NetworkManager`, `systemd-networkd`, `systemd-resolved`, `nftables`, `firewalld`, `ufw`, `iptables`, `docker`, …) and agent updates are refused.

### 24.7 Address sets (DESIGN §5.3.1, §15.4)

The converger (every 15 s, and on policy/setting changes) recompiles enforced hosts. Same rules hash, different sets hash → `sets` with a flush-and-refill delta for the changed sets in one nft transaction, a new generation, and the new full committed file. Hosts applied with the emergency override never get automatic set updates (a full transaction is required). The Firewall page shows "Policy propagation incomplete: N/M enforced hosts current" and names stale hosts; simulation and Explain note stale generations.

### 24.8 Modes, learning, drift

| Mode | Effect |
|---|---|
| Off | Nothing |
| Monitor | Learning on (agent) or snapshots (SSH); Explain shows what a policy would do |
| Guided | Monitor plus suggestions from unclassified flows; approving one opens the policy editor (and creates a missing custom service) |
| Enforce | Transactions can apply; drift is detected |

Agents sample `ss` every 30 s while learning is on (`FirewallConfig` message) and send aggregated flows (direction, protocol, service port, peer, new-connection count). SSH hosts get on-demand snapshots only and are labelled as such. Flows are classified against the desired policy (covered by a policy or the management path, or unclassified). Drift: agents report `status` every status interval; SSH hosts are checked every 5 minutes; the live-table hash is compared with the one recorded at commit.

### 24.9 Simulation and Explain (DESIGN §19.2–19.3)

`POST /firewall/simulate` evaluates a new connection between hosts or addresses on both ends (source output chain, destination input chain), for every address pair of the same family, with the desired policy: YES / NO / PARTLY, the deciding rule and reason per host (policy, management path, default, "not filtered in mode X"), and notes about unapplied changes, stale set generations and drift. Explain Host lists the compiled rules as allowances and denies with peers, members and reasons.

### 24.10 Agent protocol additions

Protocol version unchanged (1). `Hello` gained `firewall_json` (tool status and resolved endpoints) and `features` (`firewall-1`); `ControllerMessage` gained `FirewallConfig` (learning on/off); telemetry kinds `firewall` and `flows`; the internal operation type `firewall` carries one `drool-fw` action. Older agents don't announce the feature and are managed over SSH (if configured) for firewall purposes. The ceiling's `firewall` capability is no longer reserved; detection, status and snapshots need `monitoring`. `/var/lib/drool`, `/usr/libexec/drool-firewall` and the boot unit are protected from agent file operations.

### 24.11 API, RBAC, UI, backup

Permissions: `firewall.read` (all roles), `firewall.write` and `firewall.override` (admin). A `firewall_apply` step needs `firewall.write` whichever route submits it; the internal `firewall` operation is refused in job specs. Routes: `/api/v1/firewall/{settings,services,groups,policies,hosts,apply,transactions,simulate,propagation}` (see `api/openapi.yaml`). UI: a Firewall page (hosts with status and propagation, policies, "Can A reach B?", services, transactions, settings) and a Firewall card on every host page (mode, environment check, status, apply/remove with overrides, Explain, observations and suggestions, transaction timelines, generated nftables as diagnostics). Audit: `firewall.policy.*`, `firewall.service.*`, `firewall.settings.update`, `firewall.host.update`, `firewall.group.update`, `firewall.detect`, `firewall.snapshot`, `firewall.override`, `firewall.transaction.{start,commit,rollback,unknown}`, `firewall.sets.update`, `firewall.drift`, `firewall.state.adopt`. Backups include all `fw_*` tables (the database is backed up whole); hosts keep their committed policy across a controller restore, and a host ahead of the restored controller is flagged and must be adopted.

### 24.12 Testing

- Unit: `hostfw` (the tool's state machine under `sh` with fake nft/systemd: commit, timer rollback, boot with pending, late commit, duplicates, stale and foreign-epoch revisions, expiry, malformed candidates, hash mismatches, timer failure, apply failure, drift and replacement, crash after journaling, set updates), `compile` (IPv4/IPv6, groups and churn, internet/internal, DNS, outbound restriction, management conflicts and override, rendering, deltas, Explain), `nftables` (compatibility matrix), `observe`.
- In-process end to end (`internal/api/firewall_test.go`; real agent over mTLS, in-process SSH server, real `drool-fw` with fake nft and a fake `systemd-run` whose timer really fires): agent transaction with reconnect, agent death after apply, capability denial, SSH transaction over fresh connections, refused management-path deny, emergency override with a blocked path → timer rollback, reboot refusal while pending, drift, set updates, Docker refusal, host-ahead reconciliation, the 3× timeout rule, permissions.
- Containers (`test/integration/firewall_test.go`): refusal without systemd (nothing installed), SSH snapshots, agent continuous learning and Guided suggestions, refusal through the agent.
- In-process additions: removal next to Docker (apply refused, remove committed), the learning switch following mode changes (fails on the pre-fix code), management path recorded by Check, and every list-valued field of the firewall API being an array, never `null`.
- VMs with real nftables and systemd, only inside disposable qemu (TCG) guests (`vm_firewall_test.go`; `-run '^TestVMFirewall$' -vm debian-12,ubuntu-24.04,rockylinux-9`): plain-nftables detection (nftables installed with a DROOL package job where the image lacks it), SSH transaction committed over a fresh connection, outbound restriction with the management path intact, boot restore of the committed policy, a deliberately blocked management path rolled back by the host's timer, reboot refused while pending (409), hard reset while pending → boot loader rolls back ("rebooted during the transaction"), drift detected and replaced only on request, foreign nftables rules refused and untouched (Ubuntu: enabled UFW refused; disabled UFW with its rules still loaded refused with the hint), address change pushed as a set update without a transaction, agent transaction committed over a new mTLS connection, blocked agent path rolled back, agent killed while pending → systemd timer rolls back, removal.
- `TestVMFirewallControllerCrash` (Debian 12 behind a jump host): commit through the jump host over a brand-new SSH chain; the controller is killed while a candidate is pending; the host rolls back on its own; the restarted controller resolves the transaction as rolled back with the host's reason. The guest sees the jump host's real address, so the compiled management rule is exactly that address (the test's own side channel needs its own allowance).
- Browser pass (puppeteer, VM + container): Check, Enforce, refusal reasons, intent-first policy editor with live affected-hosts preview, apply dialog, transaction timeline, Explain, Can A reach B? (YES/NO with the reason), management-path refusal on the host card, observations, settings. It found two pages crashing on `null` arrays and the snapshot's own SSH connection shown as "would be blocked"; all fixed.

---

## 25. Firewall Designer and theme (M9)

The designer makes M8's policy visual without adding a second policy system or a second evaluator.

### 25.1 One canonical document

`internal/firewall/document` defines the document: `networks` (name → CIDRs, IPv4/IPv6), `groups` (name → member host names; DROOL's shared host groups), `services` (custom; built-ins are implicit) and the ordered `policy` (name, action, from, to, services, enabled, description). References are `host:NAME`, `group:NAME`, `network:NAME`, `cidr:PREFIX`, `internet`, `internal`, `any`. `Normalize` gives the canonical form; `Resolve` validates against the inventory and binds names to ids (new objects get provisional ids), producing the policies, groups, networks and services the M8 compiler reads. Problems carry a path and, for YAML, a line.

YAML (`RenderYAML`, `ParseYAML`) is a strict view of the same document: unknown fields, both `allow` and `deny`, ambiguous bare names and unknown names are errors with line numbers. Rendering is canonical, so YAML → document → YAML is stable; comments and formatting are not kept.

### 25.2 Server side

| Endpoint | Does |
|---|---|
| `GET /firewall/document` | The stored document, its canonical YAML and its revision |
| `POST /firewall/document/check` | Parses (document or YAML), validates, builds the draft world (`draftWorld`: stored hosts/addresses/ranges, the draft's networks, groups, services and policies) and compiles every host with the M8 compiler: per-host impact (new allowances, new denies, removed, refused hosts), management paths, apply blockers, notices. Stores nothing |
| `PUT /firewall/document` | Same validation in a transaction, revision-checked (409), audited (`firewall.document.save`), then the converger is kicked. Never applies anything to hosts |
| `POST /firewall/document/explain` | Explain Host and the host's management paths under a draft |
| `POST /firewall/simulate` | Accepts an optional `document`/`yaml`: the M8 simulation on the draft world |
| `GET/PUT /firewall/layout` | Presentation only |

Applying is unchanged: M8 transactions per host, refused on management conflicts, the audited override still dead-man protected.

### 25.3 Management paths

`managementPaths` walks each host's configured paths (inventory hops for SSH: controller → jump … → host; agent endpoints for agents) and marks each hop from the draft's compiled conflicts: **protected** (DROOL allows it ahead of every rule), **blocked** (a deny in the draft overlaps it; names the rules), **unverified** (not measured yet; "at risk" when the host's compiled rules drop the dialled SSH port). The designer shows these as a dashed read-only overlay, a blocking banner with the required path and the blocked segment, highlights the hosts and rules involved, and asks for confirmation before saving such a draft.

### 25.4 Web UI

`web/src/components/firewall/designer/`: `model.ts` (pure functions over the document: edits, references, `toGraph`), `useDesigner.ts` (the single draft, debounced checks, YAML edits replacing the draft once they parse, undo, revision-checked save, layout), `Topology.tsx` (React Flow; one edge per source/destination/action listing every rule and service with its protocol/port; groups collapsed by default and expandable into a grid of members; filter; "all hosts"; auto layout with dagre; positions persist; boxes on screen stay put), `RulesView.tsx` (ordered rules table with reorder, and networks, groups and services forms — full parity without drag-and-drop), `YamlView.tsx`, `RuleDialog.tsx` (shared by drawn connections and the Rules view; named services always show protocol/port; Custom protocol/port creates a service), `Panels.tsx` (management warnings, grouped preview, inspector with draft Explain, Can it reach…? on the draft). The Firewall page's Policies and Services tabs are replaced by the Designer (old links redirect to the Rules view).

### 25.5 Theme

Tokens in `web/src/styles/index.css`: a light palette on `:root`, a dark palette applied by `[data-theme="dark"]` and, for `system`, by `prefers-color-scheme: dark` on `:root:not([data-theme="light"])`. `web/public/theme-init.js` sets `data-theme` before the first paint; `src/lib/theme.ts` changes and persists it and re-renders subscribers (the canvas uses the resolved theme for React Flow's controls). The switch is in the header and under Settings → Account. Dialogs gained focus management (focus in, Tab trapped, focus restored).

### 25.6 Testing

- `internal/firewall/document`: YAML forms, canonical round trips, the brief's example, ambiguity, invalid YAML (with lines), validation problems (ports, protocols, CIDRs, deleted objects, duplicates, keywords), and `TestGolden`: `testdata/designer.json` ⇄ `testdata/designer.yaml`.
- `web/.../designer/model.test.ts` (vitest): the same golden JSON built through Topology operations and through Rules operations, the golden JSON drawn as a topology (and the Rules-built one draws the same graph), grouping, filtering, management overlay aggregation, edits.
- `internal/api/designer_test.go`: YAML → document → stored policies → YAML; stale revision; direct, jump-host and agent management paths; explicit deny; indirect loss through group membership, a network CIDR change and a service redefinition; removing an allow (no loss); invalid drafts; draft simulation and Explain; apply refused and the override; permissions; layout; forwarded ports; unmeasured hosts at risk; the converger holding a set update near the management path.
- Browser (`web/e2e`, run by `test/integration` `TestBrowser` against containers): Topology drag → Rules/YAML; YAML edit → Topology (and invalid YAML); Rules created with the keyboard only → Topology/YAML; management-path warnings, highlight and save confirmation; inspector, Explain and simulation; override confirmation; layout persistence; Light/Dark/System applied without reload, persisted, System following the OS live, the account page control; WCAG AA text contrast on 20 pages and a dialog in both themes.

---

## 26. Network connectivity monitoring (M10)

What hosts actually talk to, correlated with the firewall policy and enriched with where public addresses belong. Visibility only: nothing in M10 blocks, scores or alerts, and nothing changes a host.

### 26.1 What each kind of host can see

| | Agent host | SSH-only host |
|---|---|---|
| Collection | **Continuous.** `conntrack` when connection tracking is active (DROOL's firewall, or another, uses it) and the agent may read events; otherwise socket sampling every 30 s | **Snapshot only**, on request: the sockets open at that moment. Never called continuous |
| New connections | conntrack: every connection the kernel confirms, however short (events lost to a full buffer are counted and reported). Sampling: only connections open at a sample | Only those open during the snapshot |
| Long-lived connections | Seen at every sample (last seen advances) | Once per snapshot |
| Processes | From socket ownership (`ss -p`, root): inbound flows take the listening process; outbound ones the owning socket's, if it still exists when scanned; otherwise "Unknown" | The same through sudo, at the moment of the snapshot |
| Not seen | Packet contents (never read), DNS queries, byte counts, connections routed through the host (counted only), loopback | The same, plus everything between snapshots |
| Label in the UI | "Continuous: conntrack" or "Continuous: socket sampling (every 30 s)" with the reason conntrack isn't used | "Snapshot only: SSH" |

M8 agents (no `netmon-1`) keep sending socket-sampling `flows` reports; they are stored as socket sampling with an "update the agent" note.

### 26.2 Collection (`internal/netmon/collect`)

The collector runs for the agent's lifetime while collection is on (network settings: every agent host, only hosts whose firewall mode isn't off, or none). It samples `ss -Hntuap` every 30 s (listeners, established sockets, owners) and, when usable, subscribes to ctnetlink `NFNLGRP_CONNTRACK_NEW` (`collect/conntrack_linux.go`: a minimal netlink client parsing `CTA_TUPLE_ORIG`; 8 MB receive buffer; `ENOBUFS` counted). Conntrack is used only after a `IPCTNL_MSG_CT_GET` for the agent's own controller connection succeeds; the check repeats every minute while sampling and every five minutes on conntrack. Events become connections by the host's addresses (source local: outbound; destination local: inbound; neither: routed, counted). An aggregator (`internal/firewall/observe`) counts each connection once — from its event, or the first sample that sees it — and holds at most 20 000 flows between reports (dropped flows are counted). Reports go every two sample periods (and at least every five minutes, so the fidelity label stays current).

### 26.3 Storage

`fw_observations` (migration 0010 rebuilds M8's table): `host_id, direction, protocol, local_ip, port` (the service port) `, peer` identify a flow; `local_port`/`remote_port` (last seen), `process`/`pid`, `scope`, `source` (the best that ever saw it: conntrack > sockets > snapshot), `count`, `first_seen`, `last_seen` describe it. Snapshots raise counts (they see open connections, not new ones); continuous sources add. Every flow is validated before storage (direction, tcp/udp only, ports 1–65535, parseable non-loopback non-multicast addresses without zones, sane times, printable process names, at most 25 000 flows per report); rejected ones are counted in the log, never logged. `net_collectors` holds each host's source, explanation, lost events, dropped and routed counts, last report and last snapshot.

Growth, measured with SQLite as DROOL configures it: about 250 bytes per flow including indexes. A host that talks to a few hundred peers keeps a few hundred rows (≈100 KB); the per-host cap (default 5 000) bounds the worst case at ≈1.25 MB per host, ≈250 MB for 200 hosts at the cap. Ingestion handles ≈7 000 flow updates per second on a desktop SSD (a 200-agent fleet reporting a few hundred flows a minute each needs a few hundred). Pruning (retention, then the cap) runs every 10 minutes in 2 000-row batches.

Query cost at fleet scale (`internal/api/network_scale_test.go`, `DROOL_SCALE_TEST=1`: 200 hosts, 500 flows each, through the real ingestion path): a page of flows 10–50 ms with database filters, 35–200 ms with classifier/enrichment filters, new destinations 210 ms, the fleet summary 560 ms, filter values 300 ms, the topology 730 ms. "New destinations" uses one grouped pass (`(host_id, peer) IN (… GROUP BY … HAVING min(first_seen) >= ?)`); the first version, a correlated lookup per row, took 5.5 s and was replaced.

### 26.4 Correlation (`fwsvc.Classifier`)

One world snapshot per request; each host compiled at most once. A flow is evaluated with `HostPolicy.Evaluate` exactly like Explain Host: management (a protected DROOL path), covered (a policy, or a base rule), unclassified (the default), denied (a deny policy; or the default drop on an enforced host). On hosts that don't restrict outbound, outbound flows are also evaluated against the host compiled with outbound restricted, so a policy that names the connection still covers it (noted: "the firewall doesn't need it"). On enforced hosts whose rules differ from the desired ones, the applied policy (stored at commit) is evaluated too and a different verdict is reported ("not applied yet"). Modes, pending enforcement, pending set updates and drift become notes.

### 26.5 Enrichment (`internal/netmon/geoip`)

`Provider` is the interface; `Enricher` serves the directory's databases. The MMDB reader validates the metadata, tree and every data offset, bounds nesting and work per lookup, and never panics on corrupt input (tested by flipping bytes across a file). Lookups are only made for public addresses, cached (50 000-entry LRU, invalidated on reload), and never stored, so a database update applies to everything already observed. Missing directory, no files, unrecognised or corrupt files: monitoring is unaffected and the UI says "Unknown" / how to enable enrichment.

### 26.6 API (`api/openapi.yaml`, tag `network`)

| Endpoint | |
|---|---|
| `GET /network/flows` | Filtered, keyset-paged flows (≤500 per page), each with host, endpoints, process, source, counts, times, scope, peer host/groups or named network, GeoIP/ASN, policy correlation |
| `GET /network/flows/{id}` | One flow: collection fidelity, firewall summary, related flows, first seen by the host and by the fleet, and a draft rule when no policy covers it |
| `GET /network/summary` | Counts, how every host is observed, settings, last prune |
| `GET /network/filters` | Filter values (hosts, groups, processes, ports, services, countries, networks) |
| `GET /network/topology` | The observed graph for the designer (`expand`, `max_external`, filters) |
| `GET/PUT /network/settings` | Collection, retention, per-host cap, enrichment (audited: `network.settings.update`) |
| `GET /network/enrichment`, `POST …/reload` | Database status; reload (audited: `network.enrichment.reload`) |
| `GET /hosts/{id}/network`, `POST …/snapshot` | A host's summary (collection, listening ports from inventory with observed inbound use, counts); an SSH snapshot (audited: `firewall.snapshot`) |

### 26.7 Web UI

`web/src/components/network/`: `FlowTable` (server-paged, keyboard-operable rows), `FlowFilters` (presets and every filter; text filters apply after a pause), `FlowDialog` (Source/Destination/Country/Network/Protocol/Port/Process/times/connections, the firewall's reason, collection fidelity, related flows, "Create policy from observed connection"), `HostNetworkCard` (host page: collection, listening, inbound/outbound/internal/external/new/unclassified views; replaces the inventory card's port list and the firewall card's observation table) and `pages/network/NetworkPage` (fleet summary, fidelity, presets, filters, table; settings and enrichment status). The designer's topology gains Configured / Observed / Configured + Observed (`model.ts` merges observed edges onto the visible nodes — hosts, collapsed groups, named networks, the controller for management — and draws them dotted with a status symbol and dash pattern), status/scope/period/network-count filters, external networks expandable to addresses, an observed-connections list as the non-canvas equivalent, and observed edge and node details. Status is always words plus a symbol (✓ covered, ⛨ management, ⚠ unclassified, ✕ denied), never colour alone; country is text, never a flag only.

### 26.8 Testing

- `internal/netmon`: scope classification (RFC 1918, CGNAT, ULA, loopback, link-local, multicast, documentation, benchmarking, reserved, mapped IPv4, public IPv4/IPv6, custom ranges, most specific range), settings.
- `internal/netmon/geoip`: record sizes 24/28/32, IPv4 and IPv6 databases, IPv4 in IPv6 trees, pointers, corrupt and truncated files (byte flips everywhere), missing directory, unknown addresses, the cache (hits, bound), replacement and a broken replacement, IPinfo layout; checked once by hand against the real DB-IP Lite databases.
- `internal/netmon/collect`: `ss -p` parsing (dual-stack listeners, mapped addresses, owners, no owner), socket-sampling fallback for each reason (no conntrack, no permission, not tracking), conntrack events (short-lived connections counted, inbound attributed to the listener, UDP to its socket, routed and loopback skipped, ICMP ignored, no double counting with samples), losing conntrack, lost events, the pending bound; ctnetlink encoding/parsing (IPv4/IPv6, unknown attributes, truncation, garbage).
- `internal/api/network_test.go`: ingestion and validation, aggregation (first seen stable, last seen advancing, counts), every filter, paging with and without totals, details and the suggestion (accepted by the designer's check), management, covered, unclassified, would-deny, denied and stale (applied ≠ desired) correlation, internal_ranges changes, enrichment off, private addresses never looked up, facets, topology (ASN grouping, both ends counted once, expansion, folding), host summary with listening ports, retention and cap pruning, concurrent reports, M8 reports, permissions, missing and corrupt databases, the agent end to end (socket sampling, processes, collection off).
- `test/integration`: SSH snapshots in containers (sshd, management); agents in unprivileged containers fall back to socket sampling with the reason; `TestVMFirewall` (TCG VMs): with DROOL's firewall enforced the agent switches to conntrack and counts six sub-second connections, inbound SSH attributed to sshd.
- Browser (`web/e2e/network.test.mjs`, `TestBrowser`, synthetic observations and test GeoIP databases): fleet page, paging, presets and filters, details from the keyboard, new destinations, the draft from an observation (stored policy unchanged), the host card with a real snapshot, Configured/Observed/both with ASN grouping, expansion and the list equivalent, empty results and settings; the contrast pass covers the new pages, topology modes and the details dialog in Light and Dark.

---

## 27. System monitoring, service inventory and hardware health (M11)

Simple by default, powerful by choice: monitoring is on as soon as a host is added, with a recommended health profile; thresholds, intervals, agent budgets, helper approvals and raw details are there for those who want them. It extends the agent telemetry (§23) and the status poller (§11.8); there is no separate monitoring system and no external database.

### 27.1 One implementation, two fidelities

| | Agent host | SSH-only host |
|---|---|---|
| Collection | **Continuous**: CPU, memory, load, PSI, network and disk counters every 10 s; filesystems, sensors, GPUs every 60 s; services every 30 s; device inventory every 5 min; SMART/NVMe every 30 min; kernel messages as they happen (`/dev/kmsg`); the optional flash probe hourly | **Polled** (every 5 min, the poller's `monitor` task): everything above at that cadence; SMART/NVMe and the kernel log hourly, with sudo only for that pass (and only where the host allows it) |
| Rates (CPU %, throughput) | Between the agent's own samples; one-minute points carry average and maximum | Between two polls (a 5-minute average); the first poll after a controller restart has none |
| Offline | Buffered in memory (bounded), gaps reported | Nothing to buffer: missed polls are gaps |
| Label | "Continuous: the agent samples every 10 s" (and "reduced to save resources" when it is) | "Polled over SSH every 5 min (not continuous)" |

Collectors (`internal/monitor`: system, filesystems, sensors, GPUs; `storage`; `services`) see only the `source.Source` interface. `source.Local` reads the agent's machine (bounded reads; helpers through an allowlist with checked arguments, one at a time, a timeout, capped output and their own process group, killed as a whole). For SSH, `collect.Poll` runs a script generated from the collectors' declared files, links and commands (`source.Spec`): one `grep -HZ` dumps every file, one loop the symlinks, then fixed commands; `source.Dump` answers from that output and records what else the collectors ask for (capacity per mount, a SMART query per disk, a SAT retry for an unknown USB bridge), which up to two more passes run. A test runs both paths over the same fixture machine and requires identical results.

Containers: `/sys` shows the container host's block devices and the container's own storage is an overlay, so storage health is skipped ("belongs to the machine it runs on"), as are runtime bind mounts (`/etc/hosts`, …).

### 27.2 The agent's budget (the managed workload wins)

`drool-agent.service` (new installs) and a runtime `systemctl set-property --runtime` at every start (existing installs, and per-host or fleet overrides pushed in `MonitorConfig`) set:

| Property | Default | Why (measured) |
|---|---|---|
| `CPUWeight` | 20 | One fifth of a default service's share under contention |
| `CPUQuota` | 25% of one CPU | Measured: 0.1% average and 2% peak per second on a desktop x86 container (12 ticks in 120 s, including a full collection); 4.0% average at full fidelity on a TCG-emulated VM (roughly 20× slower than the host CPU, so a fair stand-in for a slow ARM board) over 5 minutes including a full storage scan. 25% leaves an order of magnitude of headroom for bursts (TLS reconnects, SMART scans, update verification) while capping a runaway agent at a quarter of one core of a Raspberry Pi |
| `MemoryHigh` | 96 MB | Measured RSS 25 MB steady (16 threads); helpers like `nvidia-smi` run inside the budget. Reclaim and throttling start here |
| `MemoryMax` | 160 MB | Hard ceiling, ~6× steady state; on a 512 MB Pi it is a ceiling, not use |
| `IOWeight`, `TasksMax`, `OOMScoreAdjust` | 20, 256, 300 | Background IO; bounded threads/helpers; under global OOM the agent goes before the workload |

Go's soft memory limit is set to ¾ of `MemoryHigh`, so the garbage collector works harder before the kernel has to. `memory.high` throttles a cgroup's allocations rather than killing it, so a leaking agent would otherwise sit near its high-water mark indefinitely — slowed to a crawl, silent, never reaching `MemoryMax` (measured: 96 → 128 MB took 45 s and kept slowing). The agent's **memory guard** reads the live heap after each garbage collection every 10 s; when it stays beyond the soft limit for 2 minutes it logs an error and exits, and systemd (`Restart=always`) starts a fresh agent. A normal agent's live heap is about a tenth of the soft limit, and the offline buffer may be at most a quarter of `MemoryHigh` (validated with the budget), so only unbounded growth trips it. **Operations are not monitoring**: package installs, scripts, firewall transactions and the firewall status the agent reports, service changes and file validations run through `systemd-run --scope` outside the budget (checked at startup; without systemd scopes they run as before), and package update checks in a low-priority scope (`CPUWeight=20`, `IOWeight=20`): a capped agent must never OOM-kill an `apt upgrade`. The agent reads its own cgroup (`cpu.max`, `cpu.weight`, `memory.high/max/current/peak`, `cpu.stat`, `memory.events`) and reports the effective budget, use, throttling and OOM kills; the UI shows them and whether they are enforced (not on hosts without systemd/cgroup v2, which the UI says).

Internal bounds (the cgroup limit is the last net, not the mechanism): offline buffer 2 MB of points (configurable 64 KB–64 MB), 1 000 important events, latest-only state/services/storage reports, 512 series per point, 64 filesystems/interfaces/disks/devices, 128 sensors, 16 GPUs, 2 000 units, 60 kernel events per minute (the rest counted), 256-entry diagnostic ring, 1 MB helper output, one helper at a time; M10's flow aggregator keeps its 20 000-flow bound.

**Degradation** (`agentmon.Governor`, every 30 s): triggers are the agent using more than 70% of its CPU quota, being held back by the quota more than 10% of the time (`throttled_usec`; a burst throttled within one 100 ms period is the quota working, not a sign the work doesn't fit), nearing (85%) or hitting its memory high-water mark, exceeding its internal heap budget (48 MB), or the host itself under pressure (PSI CPU > 50%, memory > 20%: the workload comes first). Level 1 samples 3× less often (services 2×) and defers SMART and GPU helper queries; level 2 samples 6× less often and also defers device inventory. The flash probe is never deferred: it is one 512-byte write and read, and on a host that is always busy it would otherwise never run. Controller connectivity, events (service transitions, storage evidence, probe results) and firewall/recovery state are never deferred. One level is regained per 5 calm minutes. Each change is an event, the state's fidelity carries the reasons, and the controller opens an info-level "reduced fidelity" issue.

### 27.3 Memory first, logs sparing

The engine runs for the agent's lifetime (not a session). `agentmon.Outbox` holds routine points by bytes and drops the oldest first, merging dropped windows into gaps that are sent with the next delivery; events are kept up to 1 000 (older ones counted as lost). On reconnect the outbox is delivered in 120-point messages; anything unsent is requeued in order. Nothing routine is written to disk: the only files are `monitor.json` (the controller's last config, written when it changes, so monitoring resumes at boot) and the probe's file. The agent's slog handler keeps every record (debug included) in a 256-entry ring that the UI can fetch, writes only Info and above (connections, applied changes, updates: infrequent lifecycle events), and writes an identical warning or error at most once per 10 minutes (with a "repeated" count next time). Collector failures are logged on the first failure, then hourly; recovery once.

### 27.4 Storage on the controller (migration 0011)

| Table | |
|---|---|
| `mon_points` (WITHOUT ROWID, PK host/tier/ts) | Tier 0: raw points (agent minutes, SSH polls) for 26 h; tier 1: 5-minute for 8 days; tier 2: hourly for the retention (default 30 days, 7–400). A point is little-endian float32 average/maximum pairs in the order of a deduplicated key layout (`mon_layouts`); NaN means no value |
| `mon_current` | One row per host: the latest state, SSH raw counters (for the next poll's rates), storage report, latest probe per mount, kernel-message dedup (boot id + kernel timestamp) |
| `mon_services`, `mon_service_events` | Units (written only when something changed) and transitions (agent events with exact times; SSH from the poll diff; `unknown` when a host goes stale and back) |
| `mon_events`, `mon_gaps`, `mon_issues` | Kernel evidence, probe results, fidelity changes, gaps, reboots; health issues (one active per host/check/subject) |
| `mon_host_settings`, `mon_helper_installs`, `mon_helper_approvals` | Watched services, probe mounts and interval, disabled checks, agent budget; helper requests and standing approvals |

Rollups run every minute from per-host watermarks (closed windows only; late points — an agent's buffer after an outage — lower the watermark so windows are recomputed). Pruning runs every 10 minutes per host by primary-key range, events in 5 000-row batches, with per-host caps of 20 000 events and 20 000 service transitions. Telemetry from agents goes through a 2 000-item queue, so an agent's stream never waits for the database (overflow is counted and logged).

Measured (`TestMonitoringScale`, `DROOL_SCALE_TEST=1`; 33 series per host, a full retention of history in every tier): **1.54 MB per host** — 8.6 MB for 5 hosts, 78 MB for 50, 308 MB for 200 (264 bytes of values per point). At 200 hosts: one minute of live points for every host ingested in 29 ms; history queries 2–10 ms (1 h raw, 24 h five-minute, 7 d and 30 d hourly); fleet summary 2 ms; host overview 1 ms; a prune pass 50 ms. History requests are bounded: five ranges, at most 32 series and 2 000 points each.

### 27.5 Services, hardware, storage

- Services: `systemctl show -p … '*.service'` (loaded units) plus `list-unit-files` (installed, never started; the agent re-reads them every 10 minutes and when asked, M14); the agent sends the list on every state or boot-state change and at least every 30 minutes; monotonic timestamps converted with `btime` (no localized date parsing); `NRestarts` as automatic restarts since the last manual start. Default view: enabled units, running ones (not systemd's own plumbing), anything failed; templates, transient, masked and static noise hidden. Control stays the typed service operation.
- Sensors: hwmon (`tempN_input/label/max/crit/emergency`, storage sensors mapped to their block device), thermal zones only without hwmon, `sensors -j` only without either. GPUs: PCI display devices from NVIDIA, AMD and Intel (virtual adapters and BMCs ignored); NVIDIA through `nvidia-smi --query-gpu` (`[N/A]` is no value), AMD through `gpu_busy_percent`, `mem_info_vram_*` and hwmon, Intel temperature/power only (utilization isn't in sysfs). A failing vendor collector doesn't hide the others.
- Storage (`internal/monitor/storage`): device inventory from sysfs with stable keys (WWN, NVMe EUI/NGUID, model+serial, by-id), partitions, UUIDs and device-mapper layers to mounts; classification by transport (NVMe, MMC — SD vs eMMC from the card type —, virtio/Xen, USB from the device path, SATA, SAS/SCSI); `smartctl -j -a -n standby` (never wakes a sleeping disk, never starts a self-test); for an unknown USB bridge one retry with `-d sat`; NVMe through smartctl or `nvme smart-log -o json` (both nvme-cli JSON styles); eMMC `life_time`/`pre_eol_info`; `ioerr_cnt`. ATA attributes are normalized only by smartctl's names (so a vendor's reuse of an ID stays raw); endurance from ACS device statistics when present, otherwise vendor wear attributes marked as such; bytes written only from device statistics. `storage.Judge` decides the state: failing (SMART failed, NVMe critical warning bits, spare below threshold, eMMC pre-EOL urgent or life exhausted), warning (reallocated/pending/offline/uncorrectable/media errors, endurance ≥100%, failed self-test, read-only device), ok (only with data), no indicators (limited), unknown (unavailable). The controller judges again on ingestion with the same code.
- Kernel evidence: a fixed set of patterns (IO and medium errors, timeouts and link resets, USB resets and disconnects, MMC errors, filesystem errors and read-only remounts, CRC errors) mapped to the disk (partitions, NVMe controllers, MMC hosts, ATA ports, USB ports through sysfs paths); only matching lines, cut to 200 characters, ever leave the host.
- Flash probe (`agentmon.Prober`): `/var/lib/drool-agent/probe/probe` when that is on the probed filesystem, otherwise `<mount>/.drool-agent-probe/probe`; the directory must be the agent's own (mode 0700, not a symlink); `O_NOFOLLOW`; 512 random bytes written in place, `fdatasync`, `FADV_DONTNEED`, read back with `O_DIRECT` where supported, compared. One probe per mount at a time; a hung device is reported as such after 30 s. Agent hosts only. Interval default hourly; shorter is allowed with the wear warning.

### 27.6 Health evaluation (`monsvc/health.go`)

Conditions are tracked per host/check/subject with a start time, a clear time and a firing flag: a condition must hold for its duration (gaps in the data restart the clock), and clears only below a lower level for a clear duration. Recommended: CPU ≥95% for 5 min (clears <85% for 3 min); memory ≥90% for 5 min, ≥97% critical (clears <85% for 3 min); filesystems >85%/>95% after two observations (clears 2 points lower); inodes >90%/>97%; filesystem or block device turning read-only after being writable this boot (or the kernel saying so); default-view units failed for 1 min (watched ones critical), watched units stopped for 2 min or missing; physical interfaces down for 1 min after being up this boot; temperatures at the hardware's own high/critical thresholds for 2 min (none without thresholds); storage health failing/warning; new media errors (decays after 7 quiet days); kernel storage errors (decay after 24 quiet hours) and 3 USB resets within an hour; probe failures (resolve on the next pass); stale hosts (no data for 3 expected intervals; units become unknown); reduced fidelity (info). Since M12.1 the numbers come from the Recommended profile and sparse overrides at global, group, host and subject scope (§29); M11's custom profile and per-host disabled checks are migrated into overrides at start. Issues are rows (`mon_issues`) written only when they open, change severity/title or resolve — what M12's alert lifecycle will consume. M11 notifies nobody.

### 27.7 Helpers and conversion

`HelperStatus` decides per host which allowlisted helper (smartmontools, nvme-cli, lm-sensors) is installed and which would add capability (a device that needs it; lm-sensors only on bare metal without sensors). `PreviewHelpers` lists, per host, the package manager's names (lm_sensors on dnf); `InstallHelpers` submits one ordinary package job per distinct package list (the engine's locks, capability checks, simulation and audit apply; plus `monitoring.helpers.install`), and records the request. Standing approvals (`global` or a group, admin only, audited) let `AutoInstallHelpers` (every 5 minutes) install where needed, as the approver, once per host and package; a failed installation is not retried automatically and leaves that host's monitoring reduced. Nothing is ever uninstalled. GPU drivers are not in the allowlist.

SSH → agent conversion keeps M7's `InstallViaSSH` and tightens it: the host must report Linux and a supported architecture; the binary is the signed release of exactly the controller's version (re-verified against the trusted release keys) or the controller's own build — never "the newest release"; the upload's checksum is checked on the host; the token is short-lived, single-use, sent on stdin and redacted from output; success means the new agent connected over mTLS; on any failure the agent service is disabled again, an identity that never connected is revoked, the unused token is revoked, the temporary directory is removed and the failure is audited — the host stays exactly as SSH-manageable as before. Bulk conversion is an agent rollout of kind `install` (`agent_rollouts.kind`, `preset`): the compatible hosts (preview: compatible / already agent-managed / unsupported / unreachable) in a canary wave and bounded batches; "offering" a host runs `InstallViaSSH` under the engine's host lock; the rollout pauses at the failure limit (default 1).

### 27.8 API (`api/openapi.yaml`, tag `monitoring`), RBAC, UI

`GET /monitoring/summary` (fleet categories, filtered/sorted/paged rows, top issues), `GET /monitoring/issues`, `GET/PUT /monitoring/settings` (audited), `GET/POST/DELETE /monitoring/approvals`, `POST /monitoring/helpers/preview|install`, `GET /hosts/{id}/monitoring` (tiles, state, issues, probes, helpers, agent budget, gaps), `…/history` (bounded), `…/services`, `…/services/{unit}/history`, `…/service-events`, `…/storage`, `…/hardware`, `…/events`, `…/helpers`, `PUT …/settings` (audited; budgets need `monitoring.settings`), `POST …/refresh`, `GET/POST …/diagnostics`; `POST /agents/conversions/preview` and `/agents/conversions`. Permissions: `monitoring.read` (every role), `monitoring.manage` (admins, operators), `monitoring.settings` (admins); helper installs need `jobs.execute`, conversions `agents.enroll`. Agent protocol: `ControllerMessage.monitor_config` (JSON `agentmon.Config`), telemetry kinds `metrics`, `monitor_state`, `services`, `storage`, `events`, `diagnostics`; feature `monitor-1`.

UI: the host page's Monitoring card (tiles: CPU, Memory, Storage, Temperature, Network, Services, Storage devices, Active health issues — each a link to details); `/hosts/:id/monitoring` with tabs Overview, Metrics (hour/6 h/24 h/7 d/30 d; CPU, memory, load, filesystems, network, disk IO, temperatures, GPU; per-core now), Services (filters, all units, watch, history, control through the service job), Storage health, Sensors & GPU, Collection & helpers (fidelity, collectors, helpers with Install, the agent budget and its override, per-host checks, diagnostics); `/monitoring` (fleet: categories with counts, search, sort, paging, bulk helper installation and agent conversion, active issues); Settings → Monitoring (profile, thresholds, retention, intervals, the default budget, SSH sudo, standing approvals). Charts are SVG with no library: averages as lines with distinct dash patterns and markers, window maxima dotted, missing windows as breaks, reported gaps hatched, a "Show as table" equivalent; statuses are icons and words; unknown values render as "—"/"Not available"/"Unknown".

### 27.9 Testing

- `internal/monitor`: CPU/memory/load/swap/PSI calculations, MemAvailable vs. estimate, counter resets, reboots, wraps, replaced interfaces and disks, implausible rates, hotplugged CPUs, mountinfo (bind, btrfs-style roots, octal escapes, read-only flags), capacity semantics, unresponsive filesystems, hwmon with and without thresholds, thermal zones, GPUs (nvidia-smi with `[N/A]`, missing, failing, malformed; AMD; Intel; none).
- `storage`: healthy/failing SATA, vendor-specific attributes, missing smartctl, permission denied, standby (not woken, last reading kept), USB thumb drive, SATA and NVMe behind USB bridges, NVMe critical warnings/spare/endurance/media errors through smartctl and both nvme-cli formats, malformed output, SD cards (limited, never "healthy", no invented wear), read-only SD, eMMC life time and pre-EOL, virtual disks, partitions through LVM, stable keys, kernel patterns and noise, device mapping.
- `services`: show/list-unit-files parsing, timestamps, unknown restarts, default view, no systemd, diffs. `collect`: the SSH script and local collection agree on a fixture machine; the script is one grep; unsafe helper arguments are refused.
- `agentmon`: the outbox (bounds, gaps, events, requeue order), the governor (levels, calm recovery, host pressure, heap budget), cgroup parsing, the probe (success, reuse, fsync failure, mismatch, write failure, hung device, symlinks, foreign directories), the engine end to end on a fixture machine.
- `internal/api/monitoring_test.go`: health evaluation (spikes ignored, sustained CPU, hysteresis, memory critical, gaps, filesystem debounce, read-only flips, temperatures only against hardware thresholds, unknown never zero), services (default view, filters, watch, Running → Failed → Running → Stopped, stale → unknown → running), storage (pending sectors, media-error increases, SMART failure, read-only device, kernel events deduplicated across agent restarts, probe failure and recovery), history (tiers, holes, late data, bounds, gaps, pruning, invalid points), permissions, settings validation and audit, fleet categories/filters/paging, helpers (preview, allowlist, permissions, the package job, standing approvals, automatic installation once with distro names), the SSH poll path through the real scripts, an in-process agent end to end (config push, continuous data, a controller outage buffered in memory and delivered, transitions during the outage, nothing on disk, diagnostics, turning monitoring off), bulk conversion (preview, canary, a failed host left on SSH with its agent stopped and identity revoked, the token never on a command line, audit). `monitoring_scale_test.go` for 5/50/200 hosts.
- `test/integration`: `TestVMMonitoring` (TCG VM with an emulated NVMe drive, plus SATA, USB and SD where the guest kernel has drivers): SSH polling of real systemd, QEMU's disks reported as virtual (health unknown, no helper recommended — every emulated disk carries a QEMU model, so physical-drive parsing is covered by fixtures captured from real smartctl/nvme-cli output instead), the agent's budget applied by systemd and operations in their own scope, the agent's measured cost, service transitions, the probe (including the guest's NVMe controller removed under a mounted filesystem: probe failure and kernel IO errors), fidelity reduced under CPU pressure, and a runaway build (`-tags droolrunaway`) held at the CPU quota and, when its memory grows without bound, restarted by its memory guard (or OOM-killed at the ceiling) while the host stays responsive. Browser: `web/e2e/monitoring.test.mjs` (fixture hosts from `DROOL_TEST_MONITOR_FILE`) and the contrast pass over the new pages in Light and Dark.

---

## 28. Alerts, notifications and maintenance (M12)

M11 answers "what is happening?"; M12 answers "what needs my attention, and how should DROOL tell me?". Detection stays in one place: thresholds, durations, hysteresis, SMART interpretation, service-state interpretation and temperature limits are M11's (`monsvc/health.go`). M12 owns occurrences, acknowledgement, suppression, routing, delivery, retries, delivery history and recovery messages. Package `internal/alerts` (lifecycle, maintenance, routing, outbox, queries) and `internal/notify` (providers).

### 28.1 From issue transition to alert

```
metric / service / storage state → M11 evaluator → mon_issues transition ─┐ one transaction
                                                   alerts hook (savepoint) ┘ alert + timeline + events + outbox rows
                                                        → workers → providers → delivery result, destination health
```

- `monsvc` writes every issue change (open, severity/title change, resolve) in a transaction and calls the installed `IssueHook` inside it (`SetIssueHook`; `alerts.Service.Attach`). A transition is `{kind: opened|changed|resolved, issue, host, check, subject, severity, previous severity, title, detail, value, observed_at, started_at}`. M11 only gained the hook and `mon_issues.observed_at`; an issue whose write fails stays "not recorded" in memory and is retried by the next evaluation, as before.
- The hook works in `SAVEPOINT alerting`. If alerting fails it rolls back only its own part, logs, and asks for reconciliation; the issue commits regardless.
- **Reconciliation** (at start, every minute, and at once after a hook failure): active Warning/Critical issues without an open alert get one (timeline: "the health issue was already active" — this also covers upgrading to M12 with issues already open), and open alerts whose issue is no longer active resolve (with the issue's `resolved_at` when known).
- **Opened** (Warning/Critical): a new `alerts` row (`alerts_one_open_per_issue` guarantees one open alert per issue), timeline entries "Condition first observed" (only with `observed_at`), "health issue active", "Alert opened", an `alert.opened` or `alert.critical` event, then suppression (§28.3) or queueing (§28.4). Info issues are ignored; an issue that changes from Info to Warning opens an alert then.
- **Changed**: severity, title, detail and value follow the issue silently, except that a rise above `peak_severity` (Warning → Critical) is an escalation: one timeline entry, one `alert.critical` event, one escalation notification per destination (deduplicated by severity). Falling back and rising again doesn't notify again.
- **Resolved**: the alert resolves at the issue's time; timeline "Condition recovered" and "Alert resolved after 7m 42s" (from first observation, else issue start); queued reminders cancelled; recovery notifications (§28.4).
- **Acknowledged** (API, `alerts.acknowledge`): `state = acknowledged`, who/when/note (≤ 500 characters), queued reminders cancelled, timeline and event, audited in the same transaction. There is no unacknowledge and no manual resolve.

### 28.2 Schema (migration 0012)

| Table | Purpose |
|---|---|
| `alerts` | One row per occurrence: host (cascade), issue id (plain text; issues are pruned sooner), check, subject, category (M11's check category), severity and peak severity, state (`active`, `acknowledged`, `resolved`), title, detail, value, `observed_at`, `issue_started_at`, `opened_at`, acknowledgement, `resolved_at`, `suppressed`/`suppressed_by`/`ever_suppressed`, `notify_after` (opening due after maintenance), `next_reminder_at`, `reminders_sent` |
| `alert_events` | The alert timeline (≤ 200 per alert; recovery and resolution are always kept) |
| `maintenance`, `maintenance_targets` | Who, why, start, end (`NULL`: until ended), ended by; hosts and groups (with names kept) |
| `notify_destinations` | Kind (`email`, `webhook`, `discord`, `slack`), enabled, non-secret config JSON, sealed secrets, delivery health (last success/failure/error, consecutive failures, failing since), last test |
| `notify_routes`, `notify_route_destinations` | Routes and their destinations; `nrt_default` is seeded and can't be deleted |
| `notify_deliveries`, `notify_attempts` | The outbox and its history (§28.4) |
| `events` | The controller event timeline (DESIGN §22.8) |

Indexes serve the list views (`alerts_state(state, opened_at)` — not partial, see §22), history (`opened_at, id`), per host and per subject (occurrences), due work (`notify_after`, `next_reminder_at`), and the outbox (`status, next_attempt_at`; `alert_id, destination_id`).

### 28.3 Maintenance

Immediate, for hosts and groups; exactly one of a duration (1 minute – 30 days), an end time (≤ 30 days) or "until ended". A host is covered by a window if it is a target or a current member of a target group; the earliest-started covering window is named on the alert. `applySuppression` runs when a window starts or ends and on every tick (15 s): newly covered open alerts become suppressed and lose their queued notifications; alerts no longer covered are unsuppressed and, if still active and unacknowledged, get `notify_after = now + 2 min`; the tick then queues one opening (re-queuing a cancelled one through its deduplication key). Every send re-checks coverage, so a race can't leak a notification. Recoveries during maintenance are not sent. Maintenance changes nothing else in DROOL: no other package reads it.

### 28.4 Routing and the outbox

- `targets(alert, severity)`: all enabled routes whose filters match (severities; host ∈ hosts or member of a group; category ∈ categories or check ∈ checks; empty filters match everything) contribute their enabled destinations; per destination, recovery is the OR and reminders the minimum of the contributing routes. Sorted by name.
- Each notification is a `notify_deliveries` row with `dedupe_key = alert|destination|kind[|severity or reminder n]` (`INSERT … ON CONFLICT DO UPDATE … WHERE status = 'cancelled'`), the message snapshot (`notify.Message`) and `recovery_wanted`. Recoveries are queued only for destinations whose opening/escalation is delivered or pending and whose routes want recoveries.
- **Workers** (4): `UPDATE … SET status = 'sending', attempts = attempts + 1, lease_until = now + 2 min WHERE id = (next due row, not behind an earlier row for the same alert and destination, not for a destination at its limits) RETURNING …`. Before sending, the destination is loaded and decrypted, and the alert re-checked (maintenance, reminders only while unacknowledged, recoveries only after a delivered opening); skipped rows are `cancelled` with the reason on the alert timeline. An attempt row is written as `interrupted` before the network call and completed after it.
- **Recording** is fenced on `(id, status = 'sending', attempts)`: a worker whose lease expired can't overwrite a newer attempt. Delivered/failed rows drop their message (`'{}'`). Transient failures go back to `queued` at `now + max(backoff(n), Retry-After)`; after 8 attempts or a permanent failure the row is `failed`, a pending recovery for it is cancelled, and the timeline says so. Destination health is updated in the same transaction; the first failure that makes it "failing" (3 consecutive, or 1 permanent) and the first success after it each write one event.
- **Restart**: every `sending` row is orphaned at start (one controller owns the outbox): its open attempt becomes `interrupted` ("the receiver may or may not have it"), the row is marked `ambiguous` if the attempt had started, and it is re-queued (or failed at the attempt limit). Expired leases are recovered the same way on every tick.
- **Limits**: 2 concurrent sends and a token bucket per destination (Discord/Slack 30/min, email 60/min, webhook 120/min, burst half a minute's worth); a busy destination's rows are skipped by the claim, not failed.
- **Reminders**: routes may ask for 60 min – 7 days; the tick queues reminder *n* for unacknowledged, unsuppressed active alerts to destinations whose routes want reminders, at most 12; acknowledgement, suppression and recovery stop them.

### 28.5 Providers (`internal/notify`)

| | Transport | Success | Permanent | Transient |
|---|---|---|---|---|
| Email | `net/smtp` over a deadline-bound connection; implicit TLS or STARTTLS (required when chosen); PLAIN auth only over TLS; RFC 5322 message, quoted-printable UTF-8, `Message-ID: <delivery id@helo>`, `Auto-Submitted`, `X-DROOL-Event`; CR/LF stripped from headers | 250 after DATA | auth (530/534/535/538, or any AUTH failure), 5xx, no STARTTLS offered, no AUTH offered, TLS/certificate errors | 4xx, 454 during auth, timeouts, connection errors |
| Webhook | `POST` JSON `drool.notification.v1`; `Idempotency-Key`/`X-DROOL-Delivery`/`X-DROOL-Event`; optional secret header | 2xx | 401/403 (auth), other 4xx, unfollowed 3xx, redirect policy, TLS | 408, 425, 429 (with `Retry-After`), 5xx, timeouts, DNS, refused |
| Discord | one embed (title, body, colour, link), `allowed_mentions: {parse: []}` | 2xx (204) | as webhook (404 = deleted webhook) | as webhook |
| Slack | `text` in mrkdwn with `&`, `<`, `>` escaped | 2xx | as webhook (403 `invalid_token`, 404, 410) | as webhook |

Every send has a deadline (the destination's timeout, default 10 s, at most 30 s) covering DNS, connect (≤ 5 s), TLS (≤ 5 s) and the exchange; response bodies are read to 64 KiB at most (512 bytes kept for the error). A timeout after the request was fully written is reported as *ambiguous*. Results carry a category (`ok`, `timeout`, `tls`, `auth`, `rejected`, `rate_limited`, `server_error`, `network`, `config`) used by the UI's test results. Every message passes through redaction of the destination's secrets (and their URL path and query) before it is stored, returned or logged.

Content is plain and bounded: severity and host, the issue title, its detail (≤ 300 characters), the current value with its unit where M11 has one (`99%`, `85 °C`), "First detected" (the observed time, else the issue start, in the configured time zone), acknowledgement, duration on recovery, the link. Never metric dumps, command output or logs.

### 28.6 Destinations, settings and secrets

`notify.Normalize` validates and fills defaults per kind and returns warnings (unencrypted email, `http://` webhooks). Secrets are one JSON blob sealed with the master key, AAD `notify-<kind>` bound to the destination id; the kind can't change after creation. `secrets.VerifyBox` (start, backup, restore key selection) opens them too, and `DiscardAllTx` (restore without the key) clears them and disables those destinations. Settings (`settings` key `alerts`): link base URL (set from the browser's `Origin` when the first destination is created and none is set), notification time zone, alert retention (30–730 days, default 180) and delivery retention (7–365, default 30).

### 28.7 API, RBAC and audit

Tags `alerts` and `notifications` in `api/openapi.yaml`: `/alerts` (views, filters, keyset cursor, ≤ 200), `/alerts/summary`, `/alerts/{id}` (timeline, deliveries — error texts only with `notifications.read` — source issue, occurrences), `/alerts/{id}/acknowledge`, `/events`, `/maintenance` (+ `/{id}`, `/{id}/end`), `/notifications/destinations` (+ `/{id}`, `/{id}/test`), `/notifications/test`, `/notifications/routes` (+ `/{id}`, `/preview`), `/notifications/deliveries` (cursor, ≤ 200), `/notifications/settings`. Permissions: `alerts.read` (all roles), `alerts.acknowledge` and `maintenance.manage` (admin, operator), `notifications.read` (admin, operator, auditor), `notifications.test` (admin, operator), `notifications.manage` (admin). Audited: `alert.acknowledge`, `maintenance.start|end`, `notifications.destination.create|update|delete|test`, `notifications.route.create|update|delete`, `notifications.settings.update` — with configuration before/after and the *names* of changed secrets.

### 28.8 UI

- **Alerts** (navigation item with the unacknowledged count and an icon for "some are critical"): Active / Acknowledged / Resolved-history / Maintenance tabs (a keyboard tablist), filters in the URL, severity as icon + word, age or duration, state, maintenance, notification status; "Load more" paging.
- **Alert page**: timeline, details (value, first observed, opened, duration), source issue, acknowledgement form (note), notifications with attempts and "may have arrived twice", other occurrences.
- **Maintenance**: a dialog from host pages and the Maintenance tab (hosts and groups, 1/2/4/24 h, until a time, until ended, reason); a banner on the host and host-monitoring pages with *End maintenance*.
- **Settings → Notifications**: destinations with health (*Delivering*, *Delivery failing*, last success/failure) and *Send test*; the add dialog picks a kind, asks only what it needs, tests before saving and defaults to Recommended. *Advanced* (also `?advanced=1`): routes, delivery history, settings.
- Integration: dashboard *Alerts* and *Recent events* cards; the fleet Monitoring page's alert strip and per-host alert column with maintenance marks; the host page's *Alerts* card.

### 28.9 Testing

- `internal/notify`: a local fake SMTP server (STARTTLS, implicit TLS, AUTH PLAIN, hangs, 4xx/5xx, missing STARTTLS) and httptest receivers: success and message format, certificate errors, authentication failure, timeouts (bounded), refused connections, rejections and deferrals, plaintext refused/confirmed and never with a password, header injection, webhook payload and headers, every status class, `Retry-After`, ambiguity after a written request, response-size bound (a 64 MiB answer), redirects (followed without the secret header across hosts; other schemes, downgrades and loops refused), private/loopback URLs allowed, invalid URLs, secret redaction (URL path, query and header echoed back), Discord (no mentions, colours, deleted webhook) and Slack (escaping, revoked token) payloads, test messages.
- `internal/api/alerts_test.go` (real M11 evaluation for CPU; `OpenIssueForTest` for the other checks through the same transaction and hook): lifecycle (one alert per activation, 50 more samples → no new alert or notification, acknowledgement with note by an operator and refusal for a viewer, the issue unchanged, audit, recovery after acknowledgement with duration, a new linked occurrence), all 14 actionable checks plus Info ignored, escalation once under flapping with a bounded timeline; maintenance (issue before, during, recovering during, persisting after with one opening after the grace, expiry, group, overlapping host + group, validation, audit); routing (default, severity, host, group, category, check, multiple routes, duplicate destination once, recovery off, preview, disabled route and destination, default route undeletable, validation, reminders and acknowledgement stopping them); delivery failures (retries with backoff, permanent 401, destination failing and recovered events once, attempt limit, no recovery after a failed opening, 429 `Retry-After`); durability (crash before delivery → new service delivers, crash mid-delivery after the receiver accepted → re-delivered once with the same delivery id and marked ambiguous, restart during retry, 16 workers on two services delivering 30 rows exactly once, idempotent reconciliation, a failing hook leaves the issue recorded and reconciliation repairs it); endpoint isolation (hanging receiver: ingestion, evaluation and API stay fast; concurrency bounded); RBAC matrix for every endpoint and role, secrets never in responses, audit or plaintext storage, CSRF; test results by category, test audit, link base URL.
- `TestAlertScale` (`DROOL_SCALE_TEST=1`) — measured on the workstation:

| Measurement | Result |
|---|---|
| One alert lifecycle with 2 destinations (open, 2 deliveries, resolve, 2 recoveries) | 6.7 ms; 5.4 KB before finished deliveries are pruned |
| History at 3 alerts/host/day for 180 days, deliveries 30 days | 5 hosts: 3 276 alerts, 9.5 MB · 50 hosts: 27 600, 58 MB · 200 hosts: 107 715, 208 MB (about 1 MB per host) |
| Queries at 200 hosts | history page 0.9 ms, by host 1.0 ms, deep page 0.9 ms, active 0.1 ms, summary 0.14 ms, detail 0.4 ms |
| Full prune (107 715 alerts, 70 854 deliveries) | 6.1 s in batches; longest wait for another writer 29 ms |
| Burst: 200 hosts fail at once, 2 destinations | 200 alerts in 0.2 s; 400 notifications by 4 workers in 2.5 s; receiver peak concurrency 4 |
| Default chat rate limit | 16 at once, then 30 a minute; the rest queued |
| Flapping: 500 activations of one condition | 500 alerts, 1 000 notifications (1 opening + 1 recovery each), 3.4 KB per activation |
| Outage: every destination refusing connections | 101 alerts and 6 metric minutes recorded in 0.24 s while deliveries failed |

- Real processes: `TestAlertsControllerCrash` (container suite) SIGKILLs the controller while a notification is in flight — before the local receiver took it (delivered once after the restart, the first attempt recorded as interrupted) and after it took it (delivered again with the same delivery id, marked ambiguous) — and checks the alerts are untouched; the hosts are monitoring fixtures (`crash-01`, `crash-02`). `TestVMMonitoring` now also requires the real systemd failure in the TCG guest to arrive at a local webhook as one opening and one recovery with a duration, and the broken disk's probe failure as a critical alert.
- Browser (`web/e2e/alerts.test.mjs`, a local receiver only): the Active list with words and icons and the navigation count, server-side filters, keyboard tabs; an alert's timeline and acknowledging from the keyboard; maintenance from a host page (banner, suppressed alerts, health issues still visible, Maintenance tab, End now); the notification setup's empty state, a webhook added, tested (the receiver gets a test, not an alert) and saved with the URL never shown; advanced routing; a failing destination (401) visible on the settings page and the alert list while the alert stays active; dashboard and fleet counts and recent events. The contrast test covers every new page and the Advanced view in Light and Dark.

## 29. Monitoring health overrides (M12.1)

M11 stays the only health evaluator and M12 the alert lifecycle. M12.1 makes M11's judgement configurable where fleets differ, with sparse inheritance and no rule language (DESIGN §11.3).

### 29.1 Model (migration 0013, `monsvc/policy.go`)

A check (`CheckSpec`) declares the fields it supports (`FieldSpec`: bool, percent, °C, minutes, count, with ranges) and, for subject overrides, its subject kind. Recommended values live in code; temperatures have none (the hardware's limits apply); clear levels derive from the warning level by a per-check margin. An override row stores only its fields. The resolver walks subject → host → groups → global → Recommended per field and returns the value with its provenance (`kind` and a human label) and any group conflict; `rule()` turns the result into what the evaluator applies (≈2.3 µs for a host in three groups with overrides at every scope).

| Check | Fields | Recommended | Subject |
|---|---|---|---|
| `cpu` | on, warning, duration, clear, clear duration | on, 95%, 5 min, <85%, 3 min | — |
| `memory` | on, warning, critical, duration, clear, clear duration | on, 90%/97%, 5 min, <85%, 3 min | — |
| `filesystem`, `inodes` | on, warning, critical, clear | on, 85/95% and 90/97%, two readings, 2 below | filesystem |
| `filesystem_readonly` | on | on | filesystem |
| `storage_health`, `media_errors`, `device_readonly` | on | on | disk |
| `kernel_storage` | on | on | — |
| `probe` | on | on | probe target |
| `service_failed`, `service_stopped` | on, duration | on, 1 min / 2 min | service |
| `interface_down` | on, duration | on, 1 min | interface |
| `temperature` | on, warning, critical, duration, clear, clear duration | on, hardware limits, 2 min, 3 below, 2 min | sensor (or an NVIDIA GPU) |
| `gpu_utilization` | on, warning, duration, clear, clear duration | **off**, 95%, 30 min, <85%, 5 min | GPU |
| `stale` | on, missed reports | on, 3 | — |
| `fidelity` (info) | on | on | — |

### 29.2 Subjects (`monsvc/subjects.go`)

`hostSubjects` lists what a host reports now (filesystems, disks, GPUs, sensors, physical interfaces, watched/failed/active units, probe mounts) with its stable key, identity text and strength; configured subjects the host no longer reports are added as unavailable and keep their overrides. The issue subject (a mount, a unit, a sensor id, a GPU id) and the page's row reference (`ref`) are included so pages can open a subject without knowing identities.

### 29.3 Applying changes (`monsvc/health.go`)

`current()` compares `mon_health_gen` (one-row read) at every evaluation entry; a new generation loads a new immutable snapshot under a mutex and runs `reconfigure(old, new)`. Writes inside transactions use `peek()` (no reload, no re-evaluation inside the writer's transaction). The API calls `ApplyHealthConfig` before validating (current rules) and after committing (immediate effect). Re-judging an active issue uses the latest value the evaluator kept (points-based checks) or the cached state (filesystems, temperatures), never a guess; checks without values (events, services) keep their issue unless turned off. At start every active issue is re-judged against the stored configuration (changes made while the controller was down, or restored backups).

### 29.4 API (`api/openapi.yaml`, tag `monitoring`)

`GET /monitoring/health/checks`; `GET /monitoring/health/global`, `/groups`, `/groups/{id}`, `/hosts/{id}/monitoring/health[?check&subject]` return a `HealthView`: per check, every field with its effective value and source, this scope's own value (`own`) and what it inherits (`inherited`, what "Reset to inherited" restores), conflicts, and for hosts a summary ("Using recommended defaults", "3 inherited pack overrides, 2 host overrides"), the host's groups and its subjects. `PUT /monitoring/health/overrides` sets one key's complete sparse field set (empty removes it); `DELETE /monitoring/health/overrides/{id}`; `PUT /monitoring/health/groups/{id}/priority`; `POST /monitoring/health/preview` computes affected hosts, how many are above the previous and the new warning level now, which active issues would close or stay — for a field change or a priority change — without changing anything. Lists are bounded (overrides ≤1000 in responses, preview hosts ≤50).

### 29.5 UI

Settings → Monitoring keeps Recommended as the visible default ("Using the Recommended profile") with **Customize** opening the per-check table, and an *advanced* list of every group/host/device override plus group priorities (with a live preview of a priority change). Groups show **Customize monitoring** (or "Monitoring overrides (n)") on each group. A host's monitoring page has a **Health settings** tab: the summary, its groups and priorities, host-level checks, and every subject with its identity strength. Filesystem, disk, sensor, GPU and service rows offer **Customize health check**. The editor shows, per field, the inherited value with its source badge (a hardware limit is visibly different from a custom one), a Custom toggle, **Reset to inherited** per field and for the whole check, and — before saving — the impact preview. All values and sources come from the server. Alerts closed by configuration show "Cleared: check turned off / threshold changed" or "Closed: no longer reported" and "(not a recovery)".

### 29.6 Testing

- `monsvc/policy_test.go`: precedence and sparse inheritance at every scope, inherited values for reset, strong identity before mount path, group priority and priority changes, equal-priority conflicts (and order independence over shuffled memberships), agreement, host settling a conflict, membership changes, GPU utilization off by default and independent of temperature, hardware limit vs explicit temperature, filesystem thresholds vs disk checks, mixed-scope critical/warning, field JSON and validation per capability, subject identities (UUID uniqueness, LVM, btrfs subvolumes, WWN, weak names, GPU UUID across reordering, PCI, sensors by drive and device), weakening directions; a benchmark.
- `internal/monitor` collectors: NVIDIA UUID and bus id, AMD `unique_id`, PCI addresses, sensor device identity.
- `internal/api/health_test.go` (M11's real evaluator and M12's real alerting with a local receiver): CPU disabled for one compute host closes its issue as `disabled`, the alert "cleared" with a `cleared` webhook event and delivery and no `recovered` anywhere, CPU still alerting on another host, memory still judged, re-enabling counts the duration from the change; an issue still failing keeps its id and alert and escalates when the critical level drops; raising a threshold closes it as `reconfigured`; lowering a threshold opens only after the full duration from the change; a pending condition restarts on a rule change; a unit failed for 8 minutes isn't backdated under a new 1-minute rule; per-unit disable; groups with a conflict (evaluator uses the next scope), a priority settling it (and restarting the duration), a host override beating both, joining a group through the groups page applying at once; GPU utilization off by default for an hour of 99%, enabled for a group, the same issue after the GPU's index changes, disabled for that GPU while its custom temperature limit and an AMD sensor's hardware limit still alert, a sensor's custom duration; a filesystem bound by UUID (90% on `/srv/media` with 92/98 vs `/` with 85/95), SMART failure still alerting, a disk override following a rename, the override following a remount, the subject disappearing (`gone`, override kept as unavailable, not inherited by a new filesystem) and returning; provenance labels, reset per field and whole, group and global views, capabilities, validation, RBAC for viewer/auditor/operator/admin (stricter vs weaker host changes, removal of a stricter override, groups, global, priorities), audit details, impact preview for enabling and disabling GPU utilization across a 5-host group, routes untouched; migration of M11's custom profile and disabled checks (idempotent).
- `internal/notify`: "cleared" headline, text and webhook/Discord/Slack/email payloads for every reason never say "recovered".
- M11 and M12 suites (`TestMonitoring*`, `TestAlert*`) re-run unchanged except M11's settings test, which now uses overrides.
- Browser (`web/e2e/health.test.mjs`): Recommended by default; a global change with the impact preview, its source and reset, audited; a host's Health settings tab reached with arrow keys, a GPU customized by keyboard (Tab, Enter, Escape returning focus) with its UUID identity, "off by default" and the subject source; a sensor's hardware limit shown as such; a group's overrides only when chosen, with "2 hosts affected" and "turns a check off" before saving, and the host showing the group as source; an alert closed by configuration reading "cleared", never "recovered". The contrast test adds the health tabs, the fleet's check table and the editor in Light and Dark.
- Real machines: `TestVMMonitoring -vm debian-12` (TCG) gained two subtests: over SSH the real root filesystem binds as `fsuuid:` + the UUID `findmnt` reports; a subject override with a 1% warning opens an issue after two real polls, and removing it closes the issue as `reconfigured`; after the agent is installed the same filesystem has the same identity. The whole VM test passed (10 subtests; agent cost unchanged at 3.5% of an emulated CPU, 13 MB peak) — the collector additions (nvidia-smi fields, `unique_id`, sensor device) changed nothing on hardware without GPUs. Results: Go unit tests all pass (race-clean for monitor, alerts, notify and the health/alert/monitoring API tests), container integration suite 241 subtests pass (incl. `TestAlertsControllerCrash`), browser 36/36.

## 30. Scheduling, fleet automation and workflows (M13)

Schedules decide *when* and *with which rollout*; the job engine decides *how*. Every run is an ordinary job (or ordinary M12 maintenance) and appears in job history; nothing new executes on hosts (DESIGN §12.4, §12.6, §12.13, §26).

### 30.1 Model (migration 0014)

- `jobs` gains `schedule_id`, `occurrence_id`, `workflow_id`, `workflow_revision` (the job's `origin`); `job_hosts` gains `wave` (§5.2).
- `workflows` (name unique among non-archived, `latest_revision`, `archived_at`) and `workflow_revisions` (PK workflow + revision; steps and default strategy as JSON; author).
- `schedules`: kind `job` | `maintenance`, `enabled`, `archived_at`, `owner_id` (FK, `ON DELETE SET NULL`), `revision`, `config_json` (recurrence, zone, workflow ref or inline steps, targets, strategy, missed/late/overlap policies, associated window, window duration and reason), `workflow_id`/`workflow_revision`, `window_id`, `next_run_at`, `attention`. Index `schedules_due(next_run_at) WHERE enabled = 1 AND archived_at IS NULL`.
- `schedule_revisions`: every configuration, so an occurrence names the configuration that produced it.
- `schedule_occurrences`: `scheduled_for`, `manual`, `status` (`queued`, `dispatched`, `skipped`, `missed`, `expired`, `blocked`, `failed`), `reason`, `missed_count`/`missed_until`, `dispatched_at`, `job_id`/`maintenance_id` (FK `SET NULL`), `triggered_by`, `skipped_hosts`. Unique (schedule, scheduled_for) for non-manual rows.
- `maintenance` gains `schedule_id`, `occurrence_id`.

### 30.2 Recurrence and time zones (`sched/recur.go`)

`Recurrence{kind, at, every_hours, time, weekdays, day_of_month}` with `Normalize`, `Next(after, loc)`, `Between(from, to, loc, limit)`, `Describe(loc)` ("Every Sunday at 03:00 (Europe/Berlin)", "Monthly on day 31, or the month's last day, at 03:00 (UTC)"), `MinSpacing` (for windows) and `Shifted(t, loc)` ("at 03:00 instead of 02:30: the clocks skip 02:30 that day"). `Wall(y, m, d, hh, mm, loc)` returns the earliest instant showing that wall time, or — in a gap — the first instant after the gap (bisection over the transition). The API and UI never send cron; times are `HH:MM` and `YYYY-MM-DDTHH:MM` in the schedule's zone.

### 30.3 The scheduler (`sched/scheduler.go`)

`Run` ticks (15 s; 1 s in test mode). `Tick` (serialized by a mutex) reads due schedules through `schedules_due` in batches of 100 (≤ 50 per tick) and `process`es each: it computes the due time and every time missed since (`Between`), applies the missed-run policy, then `dispatchJob` or `runWindow`. A dispatch is one transaction: authorize the owner, check overlap, resolve targets and maintenance covers, insert the occurrence, `engine.Submit` (job rows, `job_hosts` with waves, skipped hosts with `wave = -1`), audit, advance `next_run_at` with an optimistic `WHERE next_run_at IS ?` — then launch. After the batches, `queued` dispatches waiting `queue_one` runs whose blocking job ended, `reportMissed` writes one event, and every 6 h `prune` trims history. Test hooks (`BeforeCommit`, `NoLaunch`) simulate crashes before and after commit.

### 30.4 Durability and restart

- Crash before commit: nothing was written; the next tick (or the restart) handles the occurrence normally (within the misfire grace) or as missed.
- Crash after commit: the occurrence and job exist; the engine's restart marks the job (and its hosts) interrupted; the occurrence's outcome reads "Interrupted: the controller restarted during the run (not re-run automatically)". No path re-runs it.
- A restart at any moment cannot create a second job for an occurrence: the unique index and the shared transaction guarantee it, and the real-process test kills the controller in the middle of a 200-schedule burst to show it.

### 30.5 Targets, preview and next runs (`sched/preview.go`)

Selectors are resolved at every occurrence (`engine.ResolveTargets`); hosts are skipped for maintenance and steps' OS conditions as in any job; the job snapshots its hosts. `POST /schedules/preview` returns what saving would do now: next five runs (with DST notes), today's targets with their wave or why they'd be skipped, the waves, concurrency and threshold, step summaries, reboot behaviour, verification steps, the associated window's relationship to the next run, policies in words, notes (selectors resolved per run; package metadata changes by next week) and warnings. Nothing is saved.

### 30.6 Ownership and authorization

`RequiredPermissions(kind, steps)` is checked for the creator, an editor, the caller of Run now, a new owner, an enable, and — before every occurrence — the owner (`MissingPermission` against the owner's current role; deleted or disabled owners block). A blocked occurrence disables the schedule with an `attention` text and an event; enabling again is the owner's (or an admin's) explicit act. Route permissions: reads need `schedules.read`/`workflows.read`; changes need `schedules.manage`/`workflows.manage`; changing someone else's schedule or its owner needs `schedules.admin` (`mine()` in the handlers); anyone with `schedules.manage` may *disable* a schedule (stopping is always allowed). Running a workflow needs `jobs.execute` and its steps' permissions.

### 30.7 Rollouts in the engine

See §5.2 (waves) and §11.5 (pauses). The job page shows each wave's progress, the pause explanation, and for scheduled runs that cancelling doesn't disable the schedule; the schedule page says disabling doesn't cancel the run in progress.

### 30.8 Workflows and templates (`sched/workflows.go`)

Create, update (new revision only when steps, strategy, name or description changed), archive, get with revision summaries, get one revision. Templates (`GET /workflows/templates`): *Scheduled system update* (update rebooting only if required, verify host), *Safe fleet update* (the same with one canary, batches of five, pause after two failures), *Update and verify services* (plus services to name and `no_critical` health). `POST /workflows/{id}/run` runs a revision now as an ordinary job (origin workflow + revision, idempotency key honoured).

### 30.9 Controller-side steps (`internal/verify`)

`engine.SetLocal(verify.New(…))` in `main`: `AgentConnected` from the management router's decision, `Probe` from the inventory's connection test (ready or unsupported count as reachable), `PollSSH` the poller's monitoring poll. Each step loops with a 10 s interval until its own timeout and writes progress lines to the step's output ("waiting: jellyfin.service is activating/start"). Results carry data for the UI (`services` with each unit's state, `reachable` with a detail, `issues`).

### 30.10 API (`api/openapi.yaml`, tag `automation`)

`/workflows` (list, create), `/workflows/templates`, `/workflows/{id}` (get, update, archive), `/workflows/{id}/revisions/{rev}`, `/workflows/{id}/run`; `/schedules` (list with `kind`, `archived`, `owner=me`; create), `/schedules/preview`, `/schedules/next-runs`, `/schedules/{id}` (get, update, archive), `/schedules/{id}/enable`, `/disable`, `/run-now` (409 while busy), `/owner`, `/schedules/{id}/occurrences` (paged by scheduled time). Jobs gain `origin`, job hosts `wave`; `/jobs/preview` accepts a `strategy` and returns `waves`; maintenance gains `schedule_id`; operations gain `wait`, `verify_host`, `verify_services`, `verify_health`. Lists are bounded (workflows 500, schedules 1000, occurrences 200 per page). The browser uses only this API (types generated with openapi-typescript).

### 30.11 Audit and events

Audited: `workflow.create`/`update`/`archive`; `schedule.create`/`update` (with the previous revision, an ownership transfer and a workflow revision change)/`enable`/`disable` (naming a job that keeps running)/`archive`/`run_now`/`reassign`/`blocked`; every scheduled `job.create` and `maintenance.start` with `via: schedule`, owner, occurrence and time. Events as in §22.

### 30.12 UI

No new sidebar entries. **Jobs** has tabs *Runs*, *Schedules*, *Workflows* (links with `aria-current`). *Schedules*: name and attention text, state (enabled, disabled, needs attention, archived), next run (local, with DST note), when, targets, what runs (with "newer revision"), last result, owner. The editor's basic part is *When* (pattern pills, time, zone; next runs computed by the server), *Targets* and *What to run* (steps, or a saved workflow pinned to a revision with "Use revision N"); *Advanced* (collapsed, summarized in one line each) holds rollout, missed runs, overlap and the maintenance window. A sticky preview shows the server's resolution. The detail page shows attention, the newer-revision banner, the run in progress, settings in words, history with outcomes and links, Run now, enable/disable, edit, archive and Change owner. *Workflows*: a list with step summaries, revision and pinning schedules; a template chooser; an editor of ordered steps (add, move, remove; the typed forms of New job plus the checks) with a default rollout, the revision list and a revision viewer, Run now (targets, rollout, waves) and Schedule. New job gains the rollout fields, the recommendation, the waves, *Schedule this job* and *Save as workflow*. The job page shows the origin, every step, rollout progress per wave, pause explanations and the cancel/disable note. Alerts → Maintenance lists recurring windows. Dashboard events link to their job, schedule or maintenance.

### 30.13 Testing

- `internal/sched` (real engine with a fake executor, real M12 maintenance, injected clock): ordinary jobs and audit; targets resolved per run (a host added to a group later joins; a running job isn't retargeted); DST (spring forward and fall back, never duplicated); durability (a restart before the occurrence; a crash while the occurrence is being created — rolled back, then created once; a crash right after the job was created — never a second job, never re-run silently; two schedulers at once); missed runs (five days down: skipped and recorded once; run one missed occurrence; a quick restart still runs; one-time late and expired); overlap (skip; queue one and only one); edit/enable/run-now semantics; pinned workflow revisions; owner authorization (disabled, demoted then reassigned, deleted; file and firewall steps need their own permissions); maintenance windows (ordinary maintenance ending on time, restart inside a window, a window missed entirely with no maintenance and no events, unrelated maintenance skipping scheduled hosts but not manual runs, a workflow associated with its window); canary from a schedule; preview and next runs; `recur_test.go` (DST incl. Lord Howe, hourly across DST, monthly on the 31st, once per local date over three years in 13 zones, validation, descriptions). Opt-in `TestSchedulerScale` (`DROOL_SCALE_TEST=1`).
- `internal/model`: wave planning (canary count and explicit, fallback, batches), workflow step validation.
- `internal/engine`: threshold pauses then resumes or cancels; canary and batches (barriers, pause before the rest).
- `internal/verify`: wait and cancellation, host reachability (SSH, agent, fallback, deleted host), services (running/failed/missing/unknown, a service starting in time, old data never passing, monitoring off), health (policies, other hosts, an issue clearing during the check).
- `internal/api/sched_test.go`: ownership and audit end to end, run now while busy and disable-not-cancel, validation (zones, cron, firewall refusal), workflows (revisions, pinning, explicit update, templates, runs with origin, archive), a workflow with checks running over SSH, preview waves and maintenance skips, next runs; route permissions for every role in `TestAuthzMatrix`.
- `internal/artifacts`: GC keeps files referenced by workflows and schedules.
- Integration `TestSchedulerRestart` (real controller process): SIGKILL during a 200-schedule burst, and a 75-second outage across one-time, skip, run-once and two recurring windows.
- Browser (`web/e2e/automation.test.mjs`): the Jobs tabs from the keyboard and no new sidebar entry; a workflow from a template (a step moved with the keyboard, a new revision, revision 1 unchanged) and scheduling it with today's package dry run; New job → recommended rollout → *Schedule this job* with the preview's exact waves, Run now (origin and rollout progress on the job page), history and disable; a schedule kept on its workflow revision until *Use revision 2*; a recurring window from Alerts → Maintenance; a failing canary pausing with three hosts not started, then cancelled; a failed service check explained. The contrast test adds the schedule, workflow and window pages, the editors and jobs with rollouts in Light and Dark.
- `engine`: two timing-based tests (`TestExclusiveLockSerializesJobs` from M2 and the threshold test) waited fixed times and flaked under `-race` with parallel load; they now wait for the states they need.
- Results: Go unit tests all pass race-clean (42 packages; the scheduler's 30+ scenarios, verification, API, engine); `TestSchedulerScale` as in §30.14; container integration suite 244 tests and subtests pass, none fail (incl. `TestSchedulerRestart`: SIGKILL with 5 of 200 occurrences committed — 5 jobs interrupted and never re-run, 195 run after the restart, no duplicate; the outage case for every policy and both windows); browser 43/43; web unit tests 25/25. The VM tests weren't re-run: M13 changes no host-side execution (its checks run on the controller).

### 30.14 Scale (`TestSchedulerScale`, `DROOL_SCALE_TEST=1`)

Measured on the workstation with 6 000 schedules (3 000 due in the same minute, 2 000 not due, 1 000 disabled) and another writer inserting a row every 2 ms throughout:

| What | Result |
|---|---|
| Creating a schedule through the service | 0.8 ms |
| A tick with nothing due | ~0.2 ms; plan `SEARCH schedules USING INDEX schedules_due (next_run_at<?)`, no temporary sort |
| The burst: 3 000 occurrences, each with its job | dispatched in 7.8 s (~385/s) in one tick; the instant fake executor finished every job with the burst |
| The other writer during the burst | p50 0.56 ms, p99 3.5 ms, max 6–9 ms |
| Goroutines and heap (instant hosts) | 6 before, peak 25, back to 4; heap ≤ 15 MiB |
| The next day with slow hosts (200 ms each, 20 at a time) | dispatched in 3.9 s; the engine finished all 3 000 jobs after 31 s; peak 2 663 goroutines (one per queued job, inside the engine), heap 32 MiB |
| A second tick at the same time, and a restart | no new job |

The scheduler holds no per-schedule state; a burst becomes ordinary queued jobs, which the engine runs at its global concurrency (20 hosts). A burst of thousands of jobs therefore costs one parked goroutine per queued job until it runs (measured above: 3 000 queued jobs, 32 MiB heap), bounded by the number of due occurrences.

---

## 31. Desired state, drift and reconciliation (M14)

Desired state observes, compares and proposes; ordinary jobs change hosts. *Observe → compare → no difference means no operation → drift produces corrective typed operations* (DESIGN §12.1, §14). Package `internal/desired`, wired in `cmd/drool/main.go`.

### 31.1 Model (migration 0015)

- Configuration (declarations only): `ds_baselines` (name unique among non-archived, `revision`, `auto_enabled`, `auto_strategy_json`, `owner_id`, `attention`/`attention_at`, `archived_at`); `ds_baseline_revisions` (PK baseline + revision; `resources_json`, `assignments_json`, `content_hash`, author — immutable); `ds_assignments` (scope `global` | `group` | `host`, unique per baseline and target); `ds_group_priority` (−1000…1000); `ds_host_overrides` (PK host + key; `resource_json` or `excluded`, `origin` accept | manual, note, version). `ds_gen` is bumped by triggers on every table that changes resolution (baselines' revision/name/archive/auto/attention, assignments, priorities, overrides, `group_members`, group deletion, host insert/delete).
- Observed state: `ds_host_state` (effective hash, host status, counts, package/file check stamps and notes, `unknown_since`) and `ds_resource_state` (PK host + key; kind, status, desired, source, observation and its time, diffs and signature, `last_known`, note, `since`, `reconcile_id`/`job_id`/`last_job_id`, automatic-restore counters `auto_attempts`, `auto_next_at`, `auto_paused`, `restored_at`, `redrifts`, `redrift_since`).
- History and corrections: `ds_history` (transitions with desired/observed/diffs, source baseline + revision, job, actor, resolution) and `ds_reconciles` (`manual` | `auto`, baseline, requester, job, `active` | `done`, the items and a summary).
- `jobs.reconcile_id` (origin) and `job_hosts.steps` (per-host step mask, §5.2).

### 31.2 Resources and resolution (`resource.go`, `resolve.go`)

`Resource{kind, name | path, state, enabled, artifact, owner, group, mode, elevated, validate, service}`: services (`.service` units; `state` running/stopped/unmanaged and/or `enabled`), packages (installed/absent), files (artifact SHA-256 required; owner/group/mode optional; validation, reload/restart). `Normalize` validates and canonicalizes; `Same` compares canonical forms; `Want`/`Describe` give the words; `Corrective(allowRemovals)` returns the typed operation that restores it. `Snapshot.Resolve(host)` walks the levels (host declaration → assigned → groups by priority → global) and returns `HostState{Resources: key → Effective{Resource | Conflict, Source{level, label, baselines, groups, competing, shadowed, auto}}, Hash}`; the labels ("Required by Global baseline …", "Inherited from pack: … (baseline …)", "Baseline …, assigned to this host", "Custom for this host", "Not managed on this host", "Conflict between packs a and b (same priority 0)") are produced here and only here.

### 31.3 The loop (`service.go`)

`Run` starts a fixed pool of check workers (4) and one loop (1 s tick, woken early by hooks). `Step`: reload the snapshot when `ds_gen` moved (hosts whose effective hash changed become dirty; new or changed package/file declarations bring their check forward, spread over a minute), evaluate dirty hosts, dispatch due checks, settle finished corrections, every `AutoEvery` (30 s) sweep stale data and start one automatic-restore pass on its own goroutine (never two), prune hourly, flush events. Hooks: `monsvc.SetServicesHook` (a host's service list changed), the agent session tee (`OnHostConnected`), `engine.OnJobDone`, the engine's pause hook (`OnJobPaused`).

### 31.4 Observation (`observe.go`, `check.go`)

`Compare(resource, observation)` returns a `Judgement` (status, diffs `{field, desired, current}`, blockers, note). Services are read from M11 (`loadServices` with the agent identity's `connected`/`last_seen_at`; freshness from monitoring's settings, cached 10 s, §22 M14); `freshServices` asks agents to report (`RequestRefresh`; the agent re-reads unit files and always answers) or polls SSH hosts (`PollSSH`) and waits up to `ProofWait` (45 s) for a newer stamp. Packages and files go through `Prober` (`mgmt.Router`): `PackageVersions` (agent purpose `query`, SSH `dpkg-query`/`rpm -q` with the sentinel; no sudo) and `OpenFS` + `ObserveFile` (no-follow `Stat` + `Hash`). A host check (`runCheck`) observes every package and file of the host in one connection; its result is applied by the next evaluation in its transaction. `CheckNow` (refresh, restore preview, Accept) runs checks for up to 8 hosts in parallel and waits for them.

### 31.5 Evaluation (`eval.go`, `state.go`)

`evaluate` loads the host's rows, judges each effective resource (conflict → `conflict`; check data older than the window or a failed check → `unknown` with the reason; otherwise `Compare`), keeps `reconciling` while a correction runs, and writes changed rows, history entries, counters and events in one write transaction guarded by the generation. Host status: failed > conflict > drift > reconciling > unknown > compliant (`none` with nothing managed). `diffSig` decides whether drift *changed*. Resources that leave the effective state are deleted with a "no longer managed" entry (resolution `configuration`). Automatic-restore bookkeeping (failure backoff, re-drift counting, pause) happens here, on the transitions that prove it.

### 31.6 Restore (`restore.go`)

`PreviewRestore` = `CheckNow` on the selected hosts + `plan` + `planWarnings` (maintenance, a service to start whose package isn't installed and isn't being installed, removal simulations through `Removals` = `router.Simulate` for up to 50 removals, 8 at a time) + `planHash`. `Restore` re-plans from the stored state (no new check) and refuses on a different hash. `submit` creates, per job and in one transaction, the `ds_reconciles` row, the job (`engine.Submit` with `HostSteps`, `MaxSteps`, `Origin.ReconcileID`, the plan's strategy), `reconciling` rows and `restore_requested` history, and audits `desired.restore` (+ `job.create` with `via: desired_state`); the jobs launch after commit. Excluded resources carry their reason (conflict, unknown, reconciling, a capability the agent's ceiling denies, a blocked host, a symlink, a static or masked unit). Removals use `allow_removals` only when the caller asked; otherwise the package job's own safeguard refuses a removal that would take more (the item ends `failed` with that reason).

### 31.7 Settling (`settle.go`)

Corrections to settle come from `OnJobDone` and, at start, from every `active` `ds_reconciles` row. `settleOne` (at most two at a time) proof-checks the job's hosts (since now), evaluates with the job's outcome per item (from `job_hosts` and `job_steps`: succeeded, failed step with its message, cancelled, skipped, interrupted) and finishes the correction: compliant → `restored` (resolution `restored`), different → `failed` (the job's explanation; "interrupted by a controller restart; not re-run automatically"), no fresh data → `restore_unverified`. Resources still marked reconciling afterwards become unknown ("the correction finished; waiting for the next check") and are re-checked.

### 31.8 Accept and host declarations (`accept.go`)

`Accept(host, keys, note)` proof-checks, then writes host declarations from the observation (§22 M14). `SetOverride`/`ResetOverride` set or remove a host declaration explicitly (`PUT`/`DELETE /desired/hosts/{id}/overrides`, versioned). Each is audited (`desired.accept`, `desired.override.set`/`remove`) and triggers a re-evaluation with resolution `configuration` (history "Current state accepted" / "Resolved by configuration").

### 31.9 Automatic restore (`auto.go`)

`autoRestore` lists baselines with `auto_enabled` and no `attention`, re-checks each owner (`ownerProblem`), counts active automatic jobs (≤ 3 fleet-wide, ≤ 1 per baseline), collects candidates from the in-memory effective state (drifted resources whose source is that baseline with `auto`, not host declarations, not paused, past `auto_next_at`, hosts not in maintenance or in its 5-minute grace — `recentlyInMaintenance`), proof-checks those hosts, plans without removals and submits as the owner with kind `auto`. `SetAuto` (audited `desired.auto`) enables or disables it, records the owner and resets the baseline's per-resource counters. `pauseAuto` sets `attention`, audits `desired.auto_paused` and records one `desired.auto_paused` event.

### 31.10 Baselines, revisions and impact (`baselines.go`, `impact.go`)

`CreateBaseline`/`UpdateBaseline` validate (resources, artifacts present in the store, groups and hosts exist; jump hosts can't be assigned) and store a new revision only when resources, assignments, name or description changed (audited with the previous revision). `ArchiveBaseline` stops it applying. `PreviewBaseline`/`PreviewPriority` resolve the fleet twice — current and with the change — and compare per host and key (`valueHash`), predicting status changes from the current observations: hosts affected, resources added/removed/changed, compliant → drift, drift → compliant, new conflicts, resolved conflicts. Group priorities are audited (`desired.group_priority`).

### 31.11 API (`api/openapi.yaml`, tag `desired`), RBAC and audit

`/desired/summary` (counts by status for hosts and resources, baselines restoring automatically and those paused, firewall and agent state-version counts when readable), `/desired/hosts` (list with status, name and baseline filters; with a baseline, each host's status, counts and problems cover that baseline's resources only), `/desired/hosts/{id}` (effective state with provenance, observations, diffs, groups, baselines, firewall line), `…/resource?key=` (one resource with its history), `…/review?key=` (files: state, metadata, diff when allowed), `…/refresh`, `…/accept`, `…/overrides` (PUT, DELETE); `/desired/refresh`; `/desired/restore/preview` (plan + `missing_permissions`) and `/desired/restore` (with `plan_hash`; 409 when the plan changed); `/desired/history` (filters host, key, baseline, kind; paged); `/desired/baselines` (list, create), `/desired/baselines/preview`, `/desired/baselines/{id}` (get with revisions and counts, update, archive), `…/revisions/{rev}`, `…/auto`; `/desired/groups`, `/desired/groups/{id}/priority` and its preview. Jobs gain `origin.reconcile_id`/`reconcile_kind`/`baseline_id`/`baseline_name`, job hosts `steps`, and `/jobs/preview` `desired_warnings`. Permissions in §12.2; route-level checks in `TestAuthzMatrix`. Audited: `desired.baseline.create`/`update`/`archive`, `desired.group_priority`, `desired.accept`, `desired.override.set`/`remove`, `desired.refresh`, `desired.review`, `desired.restore` (+ `job.create`), `desired.auto`, `desired.auto_paused`.

### 31.12 UI

A **Desired state** sidebar entry (after Jobs) with tabs *Overview*, *Baselines*, *History*. Overview: tiles for drift, conflicts, failed restores, unknown and paused automatic restore (healthy counts recede), firewall and agent lines linking to their pages, the hosts that need attention (or all hosts) with what differs in words, *Check all now* and *Restore managed state…*. Baselines: list with what each applies to and its resources; editor (simple path: name, *Applies to* everywhere/groups/hosts, resources by type with typed fields; *Advanced*: rollout of automatic restore) with the server's impact preview before saving; detail with revisions (read-only revision viewer), usage counts, automatic restore (enable/disable, owner, attention, the rules in words), archive. A host's **Desired state** page (`/hosts/:id/desired`): status, where its desired state comes from (baselines with revisions, group priorities, precedence and check cadence in words), managed resources with desired → current and status; a resource opens its details: what differs, where the value comes from and what it replaces, competing declarations for a conflict, automatic restore, history, and the actions **Review difference**, **Restore managed state**, **Accept current state**, **Change baseline** (and *Reset to inherited* for a host declaration). Host detail shows a Desired state card; Groups show their baselines and priority (with *Change priority* and its impact); the dashboard shows a compact Desired state summary; New job shows drift warnings in its preview; a corrective job's page shows its origin and per-host steps, without *Re-run*.

### 31.13 Testing

- `internal/desired` (in-memory fleet `fleet_test.go`: units, packages with dependents, files with owners, modes, symlinks and permissions, unreachable hosts, failing or non-starting services; the real engine, real `filedeploy` and real maintenance; injected clock):
  - resolution — `TestResolvePrecedence` (global; group over global; equal priorities in conflict, then a priority, then a host declaration; agreeing groups; assigned over groups; two global baselines; exclusion; automatic restore follows the deciding baseline; shuffled memberships), `TestResourceValidation`, `TestCompare` (incl. masked and static units);
  - services — `TestServiceDriftRestoreAndProof` (exact drift text, provenance, no history for unchanged reports, one job, dedup while restoring, restored only with proof), `TestRestoreNeedsProofNotExitCode`, `TestUnreachableAndStaleNeverCompliant` (failed checks, the stale window, one host-level unknown entry, recovery), `TestMonitoringOffMakesServicesUnknown`;
  - packages — `TestPackagesOneAggregatedJob`, `TestPackageRemovalSafeguards`, `TestUnsupportedPackageManager`; files — `TestManagedFiles` (content, owner/group/mode, review, accept with import), `TestFileSymlinkAndPermissions`;
  - conflicts and accept — `TestConflictBlocksRestoreUntilResolved`, `TestAcceptResolvesConflictForOneHost`, `TestAcceptCurrentIsHostSpecific`, `TestAcceptPackageAndMissingFile` (exclusion, reset);
  - restore guards — `TestRestoreRefusesBlockedHosts` (epoch mismatch, blocked path, files ahead, agent ceiling), `TestRestoreRefusesAChangedPlan`, `TestCorrectionsSplitPastTheStepLimit`, `TestRestartSettlesInterruptedCorrection`, `TestBroadChangeIsOneControlledJob`;
  - `TestRevisionsAndImpactPreview`, `TestJobWarnings`, `TestEventsAreAggregated`, `TestHistoryIsBounded`;
  - automatic restore — `TestAutoRestoreOffByDefaultThenBounded` (off by default, owner, maintenance and grace, manual restore with a warning, backoff then pause), `TestAutoRestoreStopsFighting` (re-drift pause), `TestAutoRestoreOwnerLosesPermission` (demoted, disabled), `TestAutoRestoreRolloutPausesOnCanary`.
  - Opt-in `TestDesiredScale` (`DROOL_SCALE_TEST=1`, §31.14).
- `internal/hostops`: the package query is read-only (no transaction) and detects an unreadable database (dpkg and rpm). `internal/agent`: the `query` purpose's admission (names validated, monitoring or packages capability). `internal/engine`: per-host steps, `MaxSteps`, `OnJobDone`. `internal/artifacts`: GC keeps baseline and host-declaration files.
- `internal/api/desired_test.go`: the whole flow through the API with real SSH (fixture host): baseline, drift, review with and without file permissions, restore preview/execute with a real file deployment, accept, history, permissions per role; New job's drift warnings. `TestAuthzMatrix` and OpenAPI conformance cover every new route.
- Integration `TestDesiredState` (APT and DNF containers over SSH): a prohibited package and a missing file restored by one job (file proven, removal refused by the safeguard, then allowed explicitly), content drift reviewed (unified diff) and accepted for the host without touching the baseline, and a controller SIGKILL during a correction (the job is interrupted, settled from a fresh check, never re-run).
- VM `TestVMDesired` (Debian 12 under TCG, real systemd): service, package and file drift and restoration over SSH and through the agent (§31.15).
- Browser (`web/e2e/desired.test.mjs`): the sidebar entry and tabs from the keyboard; a baseline created in the editor with its impact preview; a host's effective state with provenance (compliant, drift, unknown); a group conflict explained, then resolved by a priority with its impact; Review difference, Restore managed state (an ordinary job, then a check) and Accept current state on a real container; automatic restore controls; the drift warning on New job. The contrast test adds the desired pages, a conflict's and a drift's details and Review difference in Light and Dark.
- Found while testing M14: the engine counted a failure only after writing the host's failed state, so under load one more host could start past the failure threshold (now counted first, §11.5); the monitoring-settings cache and the registry of corrections being settled were package-global, leaking between services (now per service); `AutoRestoreNow` could overlap the loop's own pass (now serialized by the same guard, so two passes never run at once); timing-based tests that flaked under `-race` with the whole suite in parallel now wait for states instead of fixed times (`internal/sched` `TestEditEnableRunNow`, `internal/verify` `TestVerifyHost`, M11's `TestAgentBulkConversion`, the agent's new services-report test), and M8's firewall apply helper allows 6 minutes for the agent-death case that waits out the rollback timer.
- Found by the M15 screenshot inventory (after the M14 commit, fixed in their own commits): the dashboard stated "0 hosts", "Nothing needs attention" and "No jobs yet" while still loading; the overview counted a paused automatic restore as "restoring automatically" (`auto_baselines` now counts only baselines on and not paused); a baseline's host list showed each host's overall status and problems, including drift other baselines decide (`/desired/hosts?baseline=` now scopes status, counts and problems to that baseline's resources); impact previews judged hosts by their overall status only, so a conflict resolved on a host dominated by a failed restore counted as "unchanged" (conflicts resolved or introduced are now found per resource, and a resolved conflict whose winning value differs counts as drift).
- Results: Go unit tests all pass race-clean (43 packages; the desired-state tests also repeated with `-cpu 1,2,8`, the agent services-report test under parallel load); `TestDesiredScale` as in §31.14; container integration suite 253 tests and subtests pass, none fail (incl. `TestDesiredState` on Debian 12 and Rocky 9); browser 50/50 (7 desired-state tests; contrast in Light and Dark including the desired pages, a conflict's and a drift's details and Review difference); `TestVMDesired` on Debian 12 passes (service, packages and file restored over SSH and through the agent, proven on the guest; an intentional change accepted); web unit tests 25/25; OpenAPI conformance and the authorization matrix cover every new route. The other VM suites weren't re-run: M14's host-side changes are the read-only package query and the agent's service reporting, which `TestVMDesired` and the agent-monitoring tests cover.

### 31.14 Scale (`TestDesiredScale`, `DROOL_SCALE_TEST=1`)

Measured on the workstation with the in-memory fleet: a global baseline of 20 resources (6 services, 10 packages, 4 files) on every host plus a group baseline with one package on half of them; another writer inserting a row every 2 ms while every host drifts at once:

| What | 5 hosts | 50 hosts | 200 hosts |
|---|---|---|---|
| First check and evaluation of every host | 29 ms | 242 ms | 914 ms (4.6 ms/host) |
| An unchanged recheck of every host (writes only each host's check time; no history) | 4 ms, 5 row changes | 35 ms, 50 | 145 ms, 200 |
| State rows | 102 | 1 025 | 4 100 |
| Database size | 1.7 → 2.1 MiB | 3.6 → 5.1 MiB | 5.0 → 7.0 MiB |
| Host list / host detail / summary | 0.29 / 0.43 / 0.19 ms | 1.4 / 0.60 / 0.25 ms | 5.2 / 0.85 / 0.70 ms |
| Every host drifting at once (a prohibited package appears everywhere), checked and evaluated | 7 ms (other writer p99 1.1 ms) | 51 ms (other writer p99 1.5 ms) | 203 ms, 200 history rows (other writer p50 0.5 ms, p99 1.8 ms) |
| Automatic restore of that drift | 1 job, 2 waves, 28 ms; settled 19 ms later | 1 job, 11 waves, 197 ms; settled 145 ms later | 1 job, 41 waves, dispatched in 746 ms (incl. the proof check of 200 hosts); all compliant 539 ms later |
| Goroutines; heap | 5 → 15 → 5 | 5 → 20 → 5 | 5 → 22 → 5; 1.8 → 1.9 MiB |

A pathological configuration — 200 hosts × 20 resources × 10 groups at equal priority, every declaration different — resolves in 84 ms, evaluates in 1.2 s and yields 4 000 conflicts and nothing restorable (each excluded with the reason). Corrections never become one job per resource or host: the 200-host fleet is one job whose rollout (canary, batches of five, five at a time, pause after two failures) the engine runs at its global concurrency. `TestBroadChangeIsOneControlledJob` (always run) checks the same for a baseline change that makes 200 hosts drift: one job, 41 waves.

### 31.15 Restore reconciliation boundary

Desired state is not DESIGN §10.3. It refuses to restore a host whose agent reports another controller epoch, whose management path is blocked (quarantined or revoked identity), or whose `files` state-version domain is not `ok`, and says so; it never writes state versions or starts epochs. Services and packages have no state-version domain: after a controller restore they are simply observed again and compared with the restored desired state, so a restore of an older controller shows drift (to review, accept or restore) rather than silently reverting hosts.

---

## 32. UI/UX and information architecture (M15)

M15 changed the web UI's structure and wording, not its backend: no new endpoints, no schema change, no change to job, desired-state, firewall, monitoring, notification or agent semantics. The visual language (tokens, components, Light/Dark) is the M9 one.

### 32.1 Navigation

The sidebar lists direct destinations under non-clickable headings — Overview · **Fleet** Hosts, Packs, Shepherds · **Observe** Monitoring, Alerts, Network Activity · **Automate** Runs, Schedules, Workflows, Desired State · **Protect** Firewall, Credentials, Audit Log · **Settings** — each heading's items in a list labelled by it (`ul[aria-labelledby]`). Packs and Shepherds show their conventional term beside the name and describe themselves on hover *and* keyboard focus (a fixed-position tooltip, plus `aria-describedby`). Runs, Schedules and Workflows are peers (the M13 tab bar is gone); pages have at most one local view level (`LocalNav`, links with `aria-current`). Detail pages carry breadcrumbs (`PageHeader crumbs`).

Routes kept despite new labels: `/groups` (Packs), `/agents` (Shepherds), `/jobs` (Runs), `/jobs/schedules`, `/jobs/workflows`, `/firewall?tab=designer` (the *Policy* tab, formerly *Designer*). New routes: `/monitoring/settings` (from `/settings/monitoring`, which redirects), `/hosts/:id/firewall`, `/hosts/:id/network`; `/jobs?host=` filters Runs by host (the existing API parameter); New run accepts `?groups=`.

### 32.2 Navigation search

`components/layout/CommandPalette.tsx` over `destinations.ts` (§22, M15). 36 destinations: every sidebar entry plus places inside pages (Add host, Shepherd enrollment/updates/trust/controller address, monitoring health settings, maintenance, network collection, New run/schedule/workflow, baselines and their history, Firewall policy/simulation/transactions, the Settings pages). Permission filtering mirrors the routes' `RequirePermission`.

### 32.3 Pages

- **Overview:** fleet triage (§14). Loading is never shown as zero or healthy (the M14 fix, kept for every tile and card).
- **Host:** triage (§14) with *View monitoring / desired state / firewall / network activity / runs* and *Open file manager*. Primary action: *Run command* on a working host, *Test connection* otherwise.
- **Monitoring:** local views *Fleet health* and *Health checks and settings*; health checks and overrides come first on the settings page. **Alerts** link to notification settings (and say so when nobody is notified yet); **Notifications** link back to Alerts and to the health checks.
- **Network Activity ↔ Firewall:** headers link to each other (and to *Can A reach B?*); the Firewall's hosts tab links each host to its firewall and observed traffic; a host's firewall and network pages link to each other and to the fleet view; flow details keep *Create policy from observed connection*. The designer's Topology / Rules / YAML is a view switcher (a button group with `aria-pressed`) over one document.
- **Packs:** members as links; pack-level monitoring overrides, desired-state priority, and links to alerts, a run and network activity.

### 32.4 States and permissions

`Loading` / `LoadError` (`components/ui/States.tsx`), `EmptyState` (what it is, why, the next step only if permitted), `ReadOnlyNote` on pages a role can view but not change (monitoring settings, notifications, firewall settings and a host's firewall, network settings). A direct link to a page the role can't view shows *Not permitted* with the role and permission (`RequirePermission`); navigation and search never offer it.

### 32.5 Responsive shell and accessibility

Breakpoints: `lg` (1024 px) switches between the sidebar and the off-canvas menu; `md` (768 px) between table rows and cards, and between the header's inline theme switcher / user / sign-out and an account menu. Content padding 16 px on phones; a table whose columns only make sense side by side scrolls inside its own box, which also holds its visually hidden labels. Dialogs fit the viewport (`100dvh` − 1 rem) and scroll inside; every dialog has a close button last in the tab order. Global `:focus-visible` outline; skip link to the content; icon-only buttons have labels; status always has words or a symbol besides its colour.

### 32.6 Testing

- Web unit tests (vitest): `src/lib/navsearch.test.ts` — every alias the M15 brief lists lands on the right destination as the first result, punctuation and case are ignored, a section heading lists its destinations, an empty query lists everything allowed, the viewer role never sees Credentials, Audit Log, Network Activity, Users, Notifications or create actions (a destination needing two permissions needs both), every sidebar destination is searchable; the sidebar's headings and order, the highlighted entry for nested pages, and empty headings dropped. `src/components/ui/provenance.test.ts` — desired-state layers (overridden declarations broadest first, then the one in effect; a conflict's competitors with nothing in effect; no invented scopes) and health layers (scope names, inherited vs own, a pack conflict between the fallback and the edited scope).
- Browser (`web/e2e/navigation.test.mjs`, new): the sidebar's headings and destinations, headings not links, the conventional term visible beside Packs and Shepherds and their description announced and shown on keyboard focus, the highlighted entry on nested pages; the search from its button, Ctrl+K and "/" (but not while typing in a field), 17 aliases from the brief, arrow keys with `aria-activedescendant`, Enter, deep links, "no match", Escape returning focus; a viewer created for the test — hidden destinations, search results limited to permitted pages, *Not permitted* with role and permission on five direct links, no actions on a host, read-only notes on monitoring settings and the firewall policy; at 390×844 in Light and Dark — the menu (dialog, focus moved in and trapped, Escape, focus back, closes on navigation), header controls not overlapping, tables as cards (`::before` labels, hidden header), no sideways scrolling on 19 pages (including a host's Metrics, Services, Storage and Hardware tabs, measured once the fixture's data is drawn), a dialog inside the viewport scrolling internally, the firewall opening in Rules with Topology explained, the search fitting; cross-links (a host to its monitoring, desired state, firewall, network activity and runs; runs for one host; Alerts → notification settings; Monitoring → health checks; an alert → its host and monitoring; Network Activity ↔ Firewall at fleet and host level; the old monitoring-settings address redirecting); loading honesty by holding API answers back (no "0 hosts", "Nothing needs attention", "No open alerts" or "Nothing running" on the Overview, no "Nothing needs attention on this host" while a host's sources load).
- Existing browser tests updated to the new structure, not weakened: Runs/Schedules/Workflows as sidebar peers (formerly a tab test), *Desired State* sidebar entry and tabs, Packs wording in the baseline editor, the host network test now starts from the host's summary and its *View network activity* link to the host's page, the designer's view switcher (`aria-pressed`) and the emergency override from the host's firewall page, the provenance wording in the health editor and the source chips (*Pack: …*, *Device: …*), the canonical monitoring-settings route (and the redirect), Shepherd wording in monitoring. Links in tests are scoped to the page content where the sidebar now has a same-named destination. The contrast test adds the host firewall and network pages, runs for one host, the navigation search, the security indicator, five pages and the menu at 390 px, in Light and Dark (`lowContrast` moved to `e2e/lib.mjs`).
- Found while testing M15: on a phone, with the data the other browser tests leave behind, the Overview was 17 px wider than the screen. The narrow-screen test then named the element: a single-column grid's implicit `auto` column grows to the widest content any of its cards can't wrap — here a run name truncated with `nowrap`; a 100-character run name made the review environment's Overview 687 px wide. Grid children in the content area may now shrink (`main .grid > * { min-width: 0 }`) and long words break inside their box (`main { overflow-wrap: break-word }`); the assertion lists the element that sticks out.
- Results (final, at 9d5911e): `go vet` and `gofmt` clean, generated code current; `go test -race ./...` passes (43 packages; 21 have no tests) — one earlier full run had `agentmon`'s `TestEngineProducesPointsStateAndServiceEvents` (unchanged since M14) miss its 20 s deadline under the suite's parallel race load; it passes 5/5 alone and the rerun was clean; web typecheck clean, unit tests 81/81 (25 before M15); the container integration suite passes, 253 tests and subtests, none fail (the 6 VM suites skip without `-vm`; M15 changes nothing on hosts), including `TestBrowser` 57/57 (10 files; the 7 navigation tests are new; contrast in Light and Dark including the new surfaces and phone layouts). Go changes are display text only: the firewall's automatic-rollback wording and the server-built labels that now say *pack* (decision log); no API, schema, protocol or behaviour change. The post-M15 screenshot inventory has 368 screenshots covering every route, with no console or page errors on 133 desktop and 136 phone page visits.
- Found by the post-M15 screenshot inventory and fixed before it was taken again: firewall rules squeezed into seven narrow columns on a phone (now one card per rule); in a phone card, a value made of several blocks (a rule's second service, a disk figure's mount point, an alert's maintenance mark) wrapped under the label instead of staying in the value column; the global focus outline, written outside Tailwind's layers, overrode fields' own focus rings and drew a second outline (global defaults now live in the base layer); a card's action button wrapped its label when the card title was long ("New pack" beside *Packs (host groups)*; the title wraps now); the monitoring table's row checkbox stood alone in a phone card (it now says *Select*); a tooltip that repeated its first word ("Groups: Groups of hosts …"); one "group priorities" left in the monitoring overrides summary. A scan of 80 pages at 390 px then found a host's monitoring tabs wider than a phone: the filesystems, interfaces, temperature-sensor and services tables now become cards (the raw SMART attributes, whose columns belong together, scroll inside their box), and a sideways scroller now contains its absolutely positioned content — visually hidden labels in the wide services table had escaped it and made the whole page 539 px wide. The narrow-screen test opens those tabs now; without the fixes it fails in both themes. Reviewing that capture found three more: a host's System card said "0 filesystems, 0 addresses" while its inventory was still loading (it now says loading, and the loading-honesty test holds that request back too); a read-only role saw a host's credential as an internal ID (it now reads "a stored credential — your role can't see its name") and a *Customize* button on health checks it can't change (*Show checks*); and "group" left in text the server builds for the UI (see the decision log). Page headers keep their actions beside a long description instead of below it. The desired-state browser tests now wait for the monitoring fixture themselves, so they also pass when run on their own (they had relied on running after other files).


## 33. Core security, recovery and platform hardening (M16)

M16 made DROOL fail safely and recover predictably when authentication, credentials, controller state, agents, upgrades, backups, the network or the controller itself fail, without adding a new product area. The M15 information architecture is unchanged: everything new lives on existing pages (§33.13). The established principles hold throughout: the controller is the root of trust; host identity is separate from its address; an agent's identity key is long-lived while its certificates rotate; the controller epoch invalidates stale controller state; agents report state but never advance the controller's counters; firewall rollback stays host-independent; unknown outcomes stay unknown; one engine executes operations; authorization is decided on the server.

### 33.1 Restore and reconciliation

DESIGN §10.3, which M14 implemented only partly. A restore is *restore durable intent and identity → new controller epoch → reconnect and re-observe → reconcile → expose discrepancies → resume only what is proven safe*.

**At restore time** (`backup.PrepareRestored`, one transaction on the staged, migrated database before anything is swapped in; migration 0019): a new random `controller_epoch` (the backup's is kept as the restore's `previous_epoch`); unfinished jobs → `interrupted` with a restore-specific message, unfinished job hosts → `interrupted`/`InterruptedError` ("unfinished when the backup was taken …; not run again automatically"), running steps → interrupted; open firewall transactions → `closed_reason = 'restore'` (preparing/pending become `unknown`; `ListOpenFwTransactions` ignores closed ones, so nothing resolves them against a host that moved on); queued/sending notifications → cancelled, `ambiguous`; enabled job schedules → disabled with `attention` "Held after a controller restore …" plus one `missed` occurrence at the backup time standing for the gap (enabling counts from then; nothing is made up); queued occurrences → skipped; automatic Desired State restore → paused through `attention`; running agent rollouts → paused; an earlier reconciling restore → superseded; a `controller_restores` row (backup time, version, host, epochs, who, `summary_json`) and a `pending` `host_reconciliation` row per host. The audit record `backup.restore` carries the restore id and both epochs. `drool restore` prints the summary.

**Agents refuse** every command from another epoch (§23) and the firewall tool every transaction, so until a host is reconciled nothing of the restored controller reaches its Shepherd or its firewall; the controller doesn't even send (`agentsvc.epochRefusal`, the router's `epoch_mismatch` decision → `ConfigurationError` "still answers to the controller epoch of the backup …", nothing dispatched). A host with SSH as its fallback keeps running ordinary jobs over SSH (no versioned state is involved; automatic restore and schedules are held, so only work an administrator starts runs). Observational data (telemetry, monitoring, facts) keeps flowing: re-observation prefers the host's truth; declared intent stays the controller's. A confirmation from another epoch (`ConfigAck`) never advances the controller's record.

**Reconciliation** (`internal/recovery`): loads the latest restore whose epoch is the controller's; a fixed pool of workers (8) with a de-duplicated queue, one worker per host at a time (a host queued again while in flight runs after); hosts are queued when their Shepherd connects (`RecoveryHooks.OnSession`) and, for SSH hosts and retries, by a 15 s tick that queues at most 64 due hosts per round. Per host:

- *Shepherd hosts:* quarantined → `blocked`; not connected → `pending` ("waiting for its Shepherd to connect", checked again when it connects); protocol incompatible or no `epoch-1` feature → `incompatible` (compared again when the Shepherd connects updated, §34.12); the agent's epoch neither the backup's nor this one → `attention` (another controller may have managed it). Each domain the agent reports (agent-config, files) and its committed firewall (from its latest report) is compared with the restored `state_versions` row: `same`, `controller_ahead` (the restored controller has newer intent the host never got: safe), `host_ahead` (changes after the backup), `conflict` (same revision, other content), `foreign_epoch`. Hashes compare without their `sha256:` prefix. Any `host_ahead` → host state `ahead`; any conflict or foreign epoch → `attention`; otherwise an `EpochTransition` (protocol: `transition_id`, `from_epoch`, `to_epoch`, every agent domain with the expected epoch/revision/hash, and the firewall). The agent (`applyEpochTransition`) checks everything before changing anything — every one of its domains must be listed, each must still match, a pending firewall transaction refuses — then rebases the firewall with drool-fw tool v2's `rebase` (§33.1.1), moves each domain to the new epoch, and finally its identity's epoch (the commit point); the same transition answers the same way again. On success the controller records every domain in the new epoch (keeping its own revision and intent when it is ahead) in one transaction, pushes agent-config the agent hadn't got, and the host is `reconciled`. Refusals: `pending` (a firewall transaction) → retried in a minute; `changed`/`epoch` → compared again after 10 s, up to five attempts, then `attention`; no answer → `pending` and sent again (it is idempotent).
- *SSH hosts:* `fwsvc.SSHStatus` over a fresh connection; a changed host key → `blocked`; refused credential → `attention` (an SSH CA or key changed after the backup?); unreachable → `unreachable` with exponential backoff (30 s … 30 min); no DROOL firewall and none recorded → `recovered`; otherwise the same comparison and `fwsvc.SSHRebase` under the host's firewall lock → `reconciled`.
- *Hooks:* `MachineChanged` (the identity is presented from another machine id) and `Revoked` (the Shepherd reports a revocation, §33.1.2) block a pending host synchronously, before its hello is recorded, and the outcome of a worker that started earlier is written only if the row is still what it read.

**Decisions** (`/hosts/{id}/reconciliation/accept`, `settings.write`, re-authentication): *accept the host's state* for `ahead`, `attention` and the two blocks above — nothing on the host changes; the controller adopts the host's revision and hash for the domains that differ (`VerdictAccepted`), so its own next change supersedes them, and the comparison runs again. `/retry` compares a host again. `/system/restore/finish` closes the reconciliation by hand; it finishes by itself when every host is reconciled or recovered. Hosts not reconciled keep refusing commands, and their Shepherds are still compared whenever they connect.

**What can't be known is said so:** jobs, firewall transactions and notifications set aside are counted under *Unknown outcome*; held schedules, paused automatic restores and rollouts under *Held until you resume it*, each with whether it was resumed (resuming uses the existing controls: enabling a schedule clears its attention and counts from now; *Resume automatic restore*; rollout resume).

#### 33.1.1 drool-fw rebase (tool version 2)

`rebase --from E --epoch E2 --revision N --hash H` under the tool's lock: refuses a pending journal, requires `committed.json`, refuses when the committed epoch/revision/hash isn't the expected one (`err changed`), is a no-op when already at E2 with the same revision and hash, and otherwise rewrites `committed.json` with the new epoch and everything else unchanged (`@@state rebased`). No rule changes; the boot loader and rollback timer are untouched.

#### 33.1.2 Identities the restored database gets wrong

- *Enrolled after the backup:* the certificate chains to the root but the identity isn't in the database. `agentsvc.refused` reads the hello (10 s), records the identity in `unknown_agents` (at most once a minute each; listed again a day after dismissal) and disconnects with code `unknown` (the agent backs off 10 minutes). Listed on the Shepherds page; enroll again.
- *Certificate from an intermediate rotated after the backup* (identity known and active): disconnect code `recover`; the agent renews through certificate recovery (proof of possession of the identity key on record, DESIGN §4.3.1), at most once per 5 minutes.
- *Revoked after the backup:* the Shepherd remembers a `revoked` disconnect (`revoked.json`) and reports it in every hello (`Hello.revocation`) until a controller moves it to a new epoch; the restored controller blocks the host ("revoke it here too, or accept"). Shepherds older than M16 don't report it: repeat revocations made after a backup (the remote audit copy lists them, §33.5).
- *Trust manifest newer on agents:* `/agents/pki` reports `newest_agent_manifest`; the Trust tab warns; a new manifest (rotate, recover) is numbered above any version a Shepherd reported and, after a restore, at least 1000 above the restored one, because agents that can't connect can't say which they have and agents never accept an older or equal version.

### 33.2 Just-in-time SSH (OpenSSH user certificates)

`internal/sshca`, migration 0018. DROOL's own user CA (Ed25519, sealed with the master key; states `next → active → previous → retired`) certifies a fresh in-memory Ed25519 key for every SSH connection to a host in certificate mode, minted inside the handshake (`PublicKeysCallback`): principal = the host's `ssh_principal` or login user, valid from `ssh_ca.clock_skew` (5 min) before to `ssh_ca.cert_ttl` (10 min) after issue, key id `drool:<controller>:<host>:<serial>`, serials allocated in a transaction; every issuance is audited (serial, validity, principal, CA fingerprint), never the key. Pooled connections are keyed by CA fingerprint and principal and are handed out only until `sshx.CertReuseMargin` (30 s) before the certificate that authenticated them expires (its `ValidBefore`, recorded at the handshake), because OpenSSH checks a certificate only when a connection authenticates; a connection tunnelled through one (a host behind a jump host in certificate mode) inherits that limit, and a lease that waited for a free session slot is checked again. The next lease dials with a fresh key and certificate and the old connection closes once idle; an operation that already holds the connection finishes on it, as a session would on sshd. Key and password connections are pooled as before. There is no silent fallback to a stored credential. Trust is an ordinary job step (`ssh_ca_trust`, controller side): it writes `/etc/ssh/drool_user_ca.pub` and `/etc/ssh/sshd_config.d/40-drool-user-ca.conf`, checks `sshd -t`/`sshd -T`, reloads sshd only on change (systemctl, or HUP without systemd), restores the previous files on any problem, refuses a host that already trusts another CA file, then proves each issuing CA on a fresh certificate-only connection (retrying while sshd re-execs). Switching a host to certificates verifies with the requested principal first; removing its last stored credential needs re-authentication. Rotation: introduce `next` → trust both on every certificate host → activate only when all accepted it → retire (key destroyed); the issuing CA can't be retired. Static SSH credentials remain supported.

### 33.3 Re-authentication and two-factor sign-in

Migration 0017. `sessions.reauth_at`: signing in counts as confirming; `POST /auth/reauth` (password, plus a TOTP or recovery code when two-factor is on; failures count toward the sign-in limit) sets it. Routes flagged `reauth` (and handlers that need it only sometimes) answer `403 reauth_required` with the window when it is older than `auth.reauth_window` (5 min; 15 min since the final After-M17 batch, §38.11); API tokens never pass. The UI's request layer shows the prompt and repeats the request once confirmed. Re-authentication guards: users (create, change, delete, two-factor reset), API tokens, two-factor enroll/disable/new recovery codes, audit break acknowledgement and remote copy settings, the SSH CA (create, rotate, activate, retire) and a host's SSH sign-in mode, the agent CA (export/remove the root key, rotate, import a manifest), Shepherd revocation and quarantine release, release-signing keys, the controller-address commit, restore decisions (accept a host's state, finish), and conditionally: replacing an in-use credential's value, firewall overrides, approving a changed host key, removing a certificate host's last credential.

Two-factor (`internal/auth/mfa.go`): one RFC 6238 TOTP authenticator per user (SHA-1, 6 digits, 30 s, ±1 step, each step usable once), the secret sealed with the master key and shown once (QR code drawn in the browser, and the key to type), active only after a code confirms it; ten single-use recovery codes stored as SHA-256, regenerating replaces them; sign-in becomes a two-step challenge held in memory; an administrator (re-authenticated) or `drool user mfa-reset` on the controller turns it off. Nothing about the secret reaches logs or the audit log. No SSO/OIDC/SAML.

### 33.4 Tamper-evident audit history

Migration 0016, `internal/audit/chain.go`, `verify.go`, `integrity.go`. Every record is a link: `seq`, `prev_hash`, `hash` = SHA-256 over netstrings of the format name (`drool-audit-v1`), the previous hash in hex and every stored field in a fixed order (details byte for byte as stored, never re-serialized). The first link (`audit.chain.start`) commits to all older records through a digest in id order. Checkpoints (`audit.checkpoint`) seal the head hourly while there are new records, at start and at shutdown with an HMAC under a key derived from the master key (HKDF, "drool audit checkpoint v1"; never stored). Verification (at start in the background, daily, on request) reports every problem it finds — missing, duplicated, changed, reordered or forged links, unchained records written outside the chain, a truncated end, bad checkpoints — not just the first, writes an evidence file, and raises the header's security indicator; checkpoints made with a retired master-key version are checked structurally only. A break is acknowledged (re-authenticated, with a note) by its signature; later breaks are still reported. The history is called tamper-evident, never immutable. What local verification can't show: whoever controls the controller and its master key can rewrite all of it; without the key, write access to the database is enough to rewrite the records after the newest checkpoint (at most an hour's worth while DROOL runs; the shutdown checkpoint seals the rest) or, while DROOL is stopped, to remove the newest records together with their checkpoints (a running controller notices records missing below the newest one it wrote). The remote copy, which already has those records, is for exactly these cases.

### 33.5 Remote audit copy

`internal/audit/sink.go`: one optional HTTPS endpoint (optional CA certificate, one header whose value is sealed with the master key), batches of up to 500 records with their chain fields, the chain id and the controller epoch (`drool-audit-export-v1`). The queue is the audit table itself: `cursor_seq` advances only on a 2xx, so a restart resumes, a failing receiver only grows the backlog (backoff up to 10 minutes; the security indicator says when it fails or falls behind), and delivery is at least once (receivers deduplicate by seq and hash). After a restore, the restored controller's records continue from the backup's head: the copy receives sequence numbers it has seen before with other contents, under the new epoch, and keeps both branches. No arbitrary hooks, no SIEM.

### 33.6 Backup and restore hardening

- *Writing:* `backup.CreateFile` writes to a temporary file in the destination's directory, syncs it, renames it into place and syncs the directory; temporary files older than a day are removed. The manifest records the controller epoch (`state_epoch`) and `key_verified` (the key file opened every sealed value).
- *Reading:* extraction checks the gzip trailer after the tar end blocks (a file missing its last byte used to pass, its members' checksums still being right) and refuses trailing bytes.
- *`drool backup verify FILE [--master-key FILE]`* (`backup.Verify`): archive and checksums, database integrity, schema (refuses a newer one), the migrations restoring it would apply (run on a temporary copy), which key opens its sealed values, and the audit chain (with checkpoints when a key opened); exit 0 complete, 2 restorable without its key, 1 not restorable. Nothing outside a temporary directory is touched.
- *The restore journal* (`backup/journal.go`): before each rename the swap records it in `<data_dir>/restore.journal` (written atomically and synced); the master key's backup copy and installation are recorded the same way; a completed swap marks the journal committed, then removes it. A failure inside the swap undoes every recorded move and the key. A crash leaves the journal: every command that opens the data directory refuses it (`ErrInterruptedRestore` — before M16 the controller would have created an empty database and run as a new installation), and `drool restore --recover` undoes the recorded moves newest first (a move whose source was the staging directory that no longer exists is discarded: the archive has that data) and puts the master key back, or finishes a committed restore.
- *Pre-upgrade backups* (`backup.PreUpgrade`, called by every command that migrates): when the database has a schema and this binary would apply migrations, a backup without the master key goes to `backups/drool-pre-upgrade-<time>-schema<N>.tar.gz` first (newest three kept; `backup.before_upgrade: false` skips it); if it fails, nothing is migrated and DROOL doesn't start.
- *Older schemas:* the sealed-value registry skips tables an older schema doesn't have yet (a backup made before an upgrade no longer warns that the key doesn't match).

### 33.7 Credential and key lifecycle

| Material | Created | Stored | Used | Rotated | Revoked / deleted | Backup / restore | Failure and recovery |
|---|---|---|---|---|---|---|---|
| Master key | first start (`master.key`, 0600; `secrets.master_key_file` / `DROOL_SECRETS_MASTER_KEY_FILE`) | key file, versions `N:base64` | seals every value in `sealedLocations`; derives checkpoint keys | `drool secrets rotate-key`: previous file saved → new version added → every value re-sealed in one transaction → new version alone verified → old version removed (`--keep-previous` keeps it); every step leaves a key file that opens everything; controller stopped | — | not in backups unless `--include-master-key`; restore tries the backup's, `--master-key`, the installed one | missing or wrong key: DROOL refuses to start; restore offers `--discard-credentials` |
| Controller SSH key | first start | `secrets` (sealed) | default credential for hosts | create a new credential, test it on a host, switch hosts, delete the old (or move hosts to certificates) | refused while hosts use it; the default can't be deleted | with the database | regenerated after `--discard-credentials` (hosts need it authorized again) |
| SSH credentials (keys, passwords, sudo passwords) | Credentials page/API | `secrets` (sealed) | host connections | as above; replacing the value of one hosts use is refused unless acknowledged, then needs re-authentication | deletion refused while in use | with the database | `--discard-credentials` detaches hosts (they need a credential again) |
| SSH user CA | Credentials page | `ssh_user_cas` (sealed) | per-connection certificates (§33.2) | next → trust → active → previous → retired | the issuing CA can't be retired; a certificate host's last credential needs re-authentication to remove | with the database | a host that stopped trusting it: `attention` after a restore; trust step again with a working credential |
| Agent root CA | first start | `agent_ca` (sealed) until exported and removed | signs intermediates and manifests | — | export → remove (fingerprint typed); offline copy only | in backups only while on the controller | needed to recover an intermediate (`drool agent-ca rotate --root-key`) |
| Agent intermediate CA | first start, rotation | `agent_ca` (sealed) | issues agent and controller certificates | rotate (planned: old stays trusted; compromise: revoked); manifest N+1 (§33.1.2) | revoked in the manifest | with the database | recovered with the root key; agents move by certificate recovery |
| Agent certificates | enrollment, renewal at 2/3 | agent's state directory | mTLS sessions | renewed over the same identity key | identity revocation (immediate) | not in backups (on hosts) | expired: recovery within `agent.recovery_grace` (180 d); after that re-enroll |
| Agent identity key | enrollment | agent's state directory, never sent | proof of possession | — (long-lived) | revocation; re-enrollment creates a new one | — | lost: re-enroll |
| Release-signing keys | `drool release keygen` (offline) | public keys only on the controller | verify agent updates | add a new key, sign with it, remove the old | removal (re-authentication) | with the database | — |
| API tokens | Settings → Tokens (re-authentication, session only) | hash only | API | create new, delete old | deletion; user deletion | with the database | — |
| Notification / audit-sink secrets | settings pages | sealed columns | outgoing requests | replace the value | delete the destination | with the database | unreadable after a lost key: destination disabled until re-entered |
| TOTP secrets, recovery codes | enrollment | sealed / SHA-256 | sign-in, re-authentication | new authenticator after turning it off; new codes replace old | turning off; admin or CLI reset | with the database | lost authenticator: recovery code, or reset |

Revoking a Shepherd that is a host's only management path (no SSH) is refused unless acknowledged ("DROOL can't reach it until someone with access to the host enrolls its Shepherd again"); DROOL can't know about other ways in and doesn't claim to.

### 33.8 Shepherd/controller compatibility

The protocol range is negotiated at every hello (no session when incompatible). Within it, optional capabilities are announced as features (`firewall-1` since M8, `netmon-1` M10, `monitor-1` M11, `epoch-1` M16; M16 Shepherds also announce `query-1`, which gates nothing because M14 and M15 Shepherds answer Desired State queries without announcing it), recorded per identity (`features_json`, migration 0020) and checked before anything is sent: a missing feature means SSH (when allowed) or a refusal that says to update the Shepherd (`ConfigurationError`, nothing sent); an older Shepherd rejecting an operation type it doesn't know is reported the same way, never as an ordinary failure; unknown protobuf fields and messages are ignored both ways. `agentsvc.Judge` gives each identity a status for the Shepherds page and the host card: `trust_failure` (revoked, quarantined, or a certificate expired beyond the recovery grace), `incompatible`, `update_required` (missing a feature this host's configuration needs: firewall management when its firewall mode isn't off; `epoch-1` while the host awaits restore reconciliation), `reduced` (other features missing, or none announced: older Shepherds), `update_recommended` (a newer stored release for its platform), `compatible`. Mid-upgrade agents keep their update state; a controller restart is just a reconnect with backoff; a stale manifest is replaced at connect; an expired certificate is recovered on reconnect within the grace.

### 33.9 Upgrades, migrations and downgrades

Each migration runs in its own transaction together with its `schema_migrations` row, which now records the DROOL version that applied it (`drool_version`, added by the runner itself). A database newer than the binary is refused with `NewerSchemaError`: which schema, which DROOL upgraded it, that downgrades aren't done in place, and the pre-upgrade backups found. Downgrading means running the newer version again or restoring a pre-upgrade backup with the older one. Tests: an upgrade from every schema version 1–18 with rows written in that version's shape (row counts kept, integrity and foreign-key checks clean, every migration's version recorded); a migration failing part-way leaves nothing; a process killed in the middle of a migration leaves the previous version intact and the next start is ordinary; backups from schemas 5, 8, 12, 15 and 18 restore.

### 33.10 Crash and restart semantics; soak

| Subsystem | Process death at an awkward point |
|---|---|
| Jobs (engine) | pending/running jobs and unfinished hosts become `interrupted` at start; never re-run; the agent's ledger answers an operation id it already received instead of running it twice |
| Scheduled occurrences | one row per nominal time (unique), created with its job in one transaction; missed runs follow the schedule's policy; a crash in a burst creates no duplicate (M13 test) |
| Workflows | runs are ordinary jobs (as above) |
| Desired State corrections, automatic restore | corrections whose jobs were interrupted are settled at start: outcome unknown, the host re-checked, never re-run; automatic restore re-checks before each job |
| Notification outbox | leased rows: a crash leaves them to be (re)delivered; an attempt that may have reached the receiver is marked ambiguous |
| Audit chain | each record and its link are written in the caller's transaction; a checkpoint is a record |
| Audit sink | the cursor moves only after a 2xx: a crash resends a batch (at least once) |
| Firewall transactions | the host's own journal, rollback timer and boot loader decide; the controller resolves open transactions from the host's status; after a restore they are closed as unknown |
| File deployments | recorded per host from the agent's or SSH result; an interrupted deployment is an interrupted job host |
| Agent requests | the agent's ledger (accepted, completed) answers a re-asked operation id; an operation whose acceptance wasn't seen is resolved by asking, never by resending |
| Epoch transitions | idempotent on the agent; the controller re-sends after a crash and records the move (test: a crash after the agent moved) |
| Monitoring, alerts | state is recomputed from new observations; alert transitions happen in the issue's transaction |
| Restore | the restore journal (§33.6) |
| Master key rotation | every step leaves a key file that opens everything (test: a stop after each step) |
| Migrations | one transaction each (test: a killed process) |

Soak (`test/integration/soak_test.go`, opt-in: `-soak 5,50,200 -soak-duration 3m`): a real controller process and a swarm of real Shepherds (the agent library in the test process, commands answered by a stub so nothing runs on the test machine, a shell-only ceiling, collection off) under a job on every host every 20 s, scheduled runs every minute, and a tenth of the Shepherds reconnecting every 30 s; the controller's goroutines, open files and heap are read from `/metrics`; then every Shepherd stops and the controller must drain back. Measured (3 minutes of load each, this workstation):

| Shepherds | Jobs (host runs, failed) | Reconnects | Controller goroutines: idle → connected → peak → drained | Open files: idle → loaded → drained | RSS loaded |
|---|---|---|---|---|---|
| 5 | 12 (60, 0) | 5 | 47 → 70 → 74 → 45 | 25 → 30 → 25 | 62 MiB |
| 50 | 12 (600, 0) | 25 | 45 → 295 → 295 → 45 | 25 → 75 → 25 | 76 MiB |
| 200 | 12 (2 400, 0) | 100 | 45 → 1 048 → 1 048 → 45 | 25 → 225 → 25 | 99 MiB |

About five goroutines and one file per connected Shepherd, flat during load, back to the idle baseline when they leave: no leak in sessions, schedules, jobs, locks or reconnects. (Heap in use swings with garbage collection — 44 to 126 MiB peaks — and isn't a trend.) The M13 limitation stands unchanged: a burst of thousands of queued jobs parks one goroutine per queued job inside the engine (§30).

### 33.11 Self-update

Shepherds update themselves only to releases signed with a release key the operator holds (separate from the agent CA), staged, health-checked and rolled back automatically, through controller-managed rollouts (§23, unchanged; official releases signed by the DROOL project followed before 1.0, §42). The controller doesn't update itself: there is no controller update path in the UI, the API or the binary; upgrading is replacing the binary or image, with the automatic pre-upgrade backup (§33.6). Nothing half-built was left behind.

### 33.12 Container deployment

The image is distroless, runs as `nonroot`, listens on 8080 and 9443 (unprivileged ports) and writes only `/data` (and `/tmp`, for `drool backup verify`). `compose.yaml` runs it `read_only`, with a tmpfs `/tmp`, `cap_drop: [ALL]` and `no-new-privileges`, and a health check (`drool healthcheck`, which asks its own `/health`; the image has no shell or curl; also a `HEALTHCHECK` in the Dockerfile). No host network, Docker socket, devices or host mounts; host firewalls are managed through SSH or the Shepherd, never the controller host's. `TestRestrictedContainer` ran this build's static binary in the image exactly that way and checked setup, the API, the health check, a backup and its verification; M17 replaced it with `TestContainerImage`, which runs the built image itself with the shipped `compose.yaml` (§34.5).

### 33.13 Security UX

New status and actions live where their subject already is: two-factor on Settings → Account and Users; re-authentication as a prompt wherever an action needs it; audit integrity and the remote copy on the Audit Log page; the SSH CA on Credentials and a host's SSH sign-in on its page; Shepherd compatibility on the Shepherds page and the host's Shepherd card; unknown Shepherds and newer trust manifests on the Shepherds page; restore reconciliation on Settings → System (plus a notice on each affected host's page); backup verification and pre-upgrade backups in Settings → System's backup card; security posture (audit history failed or behind, two-factor off for an administrator, plaintext HTTP, the root CA key online, a restore being reconciled) in the existing header indicator and the Overview. Plain language first ("Audit history verification failed", "Ahead of DROOL"), the technical detail under it.

### 33.14 Testing

New: `internal/audit` (links, concurrent writers, the canonical encoding; every kind of tampering — changed, changed and rehashed, deleted, deleted and renumbered, cut off at the end, reordered, inserted, written outside the chain, rewritten with the checkpoint left alone, rewritten without the key — each reported where it is), `internal/sshca` (certificate lifetime and principals, clock skew both ways, untrusted and retired authorities, the host key still pinned with certificates, unique serials under concurrency, no key material in the audit log, jump-host forwarding only on jump hosts, the trust script against fake sshd), `internal/recovery` (comparison rules; 200 Shepherds reconnecting at once — at most 8 at a time, each moved exactly once, no job; every host decision; refusals and retries; a crash after a transition), `internal/agent` (the transition: all-or-nothing, idempotent, refusals), `internal/backup` (restore preparation; a crash after every file move and recovery; a completed restore finished by recovery; cuts at every offset; write failures and a real file-size limit; old-schema backups; pre-upgrade backups and retention; the manifest's epoch and key check; verify's verdicts incl. a tampered audit history; master key rotation and restoring a pre-rotation backup), `internal/store` (§33.9), `internal/secrets` (rotation stopped after each step), `internal/agentsvc` (compatibility verdicts), `internal/api` (re-authentication: the window, a wrong password, tokens never pass, guessing bounded; two-factor: enrollment, sign-in, a code and a recovery code each used once, guessing bounded, regeneration, turning it off, an administrator's reset; audit integrity and acknowledgement; the remote copy against a local receiver, including a backlog while it is down; SSH certificate migration and rotation; in-process restores with real agents: unchanged, ahead + accept, controller ahead (the missed configuration follows in the new epoch), revoked and enrolled after the backup; with real drool-fw on Shepherd and SSH hosts; manifest numbering; last-path and re-authentication guards; a Shepherd without `firewall-1` is sent no firewall transaction and shown as needing an update), integration (`TestRestoreReconciliation`: real `drool backup`/`drool restore` with real Shepherds in containers, and the restore UI in Chromium — Settings → System, the host's notice, the header indicator, accepting a host's state after re-authenticating; `TestSSHCertificates`: the trust step, certificate-only work, principals, a jump host and a rotation against real sshd in containers; `TestUpgradeSafetyCLI`; `TestRestrictedContainer`; `TestSoak`; `TestBrowser` now runs `security.test.mjs` too: two-factor set-up and sign-in, re-authentication, an administrator's reset, audit integrity), VM (`TestVMHardening`, QEMU/TCG, on Debian 12, Ubuntu 24.04 and Rocky Linux 9: the SSH CA trust step against the distribution's own systemd-managed sshd — Ubuntu's starts from `ssh.socket` — with `sshd -T` and the unit checked afterwards, work with the static key removed (sshd logs `ED25519-CERT` and `ID drool:…`), and a full CA rotation with the host on certificates only; on the first VM, a backup, a restore and reconciliation with a real Shepherd and a committed nftables policy: the ruleset is byte for byte unchanged, `committed.json` is in the new epoch, no rollback timer is armed, and the next transaction through the Shepherd is revision 2).

Results (final, at c2cc9d5): `go vet` (also with the integration tag) and `gofmt` clean, generated code current; `go test -race ./...` passes (47 packages; 19 have no tests); web typecheck clean, unit tests 84/84; the container integration suite passes, 257 tests and subtests, none fail (8 skip without their flags: the VM suites and the soak), including `TestBrowser` 61/61 (11 files, `security` new) and the restore UI in `TestRestoreReconciliation` (3/3); `TestVMHardening` passes on Debian 12, Ubuntu 24.04 and Rocky Linux 9 (14 min; run on the tree before three display-only changes in 0d88859); the older VM suites pass again on Debian 12 with this build (`TestVM`, `TestVMAgent`, `TestVMFirewall`, `TestVMFirewallControllerCrash`; 37 tests and subtests, 44 min) because M16 changed host-facing code (drool-fw v2, the Shepherd's epoch transition, the SSH layer); `TestSoak` as in §33.10 (at 51f791e). The final runs found three problems in tests and four in what DROOL says, and nothing in its behaviour. In tests: a finished VM's certificate host stayed in the shared controller, and the CA activation guard rightly refused an authority that host had never accepted; the Shepherd install raced the poller's inventory read after a package job ("host busy, try again", which the M14 VM test already retries); and a browser test expected its own events among the Overview's newest eight while other tests' stopped Shepherds went stale. In DROOL's display: M15 Shepherds were labelled as unable to do Desired State checks (`query-1`); the restore notices implied every held host has a Shepherd; reconciliation reasons used internal domain ids; and the Audit Log card claimed every change to stored records was detectable (§33.4 says what sealing doesn't cover). Checking the completion report's claims afterwards added test cases in two test-only commits: an SSH host that refuses DROOL's credential after a restore (attention), the retry backoff of an unreachable one, and a Shepherd the restored controller is ahead of getting the configuration it missed (each fails with its code path removed); `internal/recovery` and `internal/api` pass under `-race` with them.

### 33.15 Gap audit

What M16 found before changing code, and what became of it:

| Area | Found | Done |
|---|---|---|
| Restore | the restored controller kept the backup's epoch; no reconciliation states; no agent epoch transition or firewall rebase; open firewall transactions resolved against hosts that had moved on; queued notifications re-sent; schedules could replay; automatic restore could roll hosts back to older intent; unknown/revoked identities unhandled; a crash mid-swap ran an empty database; no bounded reconnect handling | §33.1, §33.6 |
| JIT SSH | none | §33.2 |
| Authentication | no re-authentication, no two-factor | §33.3 |
| Audit | append-only table only | §33.4, §33.5 |
| Backup | no verify command, no directory sync, no pre-upgrade backup, trailer unchecked | §33.6 |
| Keys | no master key rotation (the docs promised a command that didn't exist); in-use credential replacement and last-path revocation unguarded | §33.7 |
| Compatibility | protocol range only; no status model; unknown operations reported as failures | §33.8 |
| Migrations | no applying version recorded; terse downgrade refusal; only fresh → latest tested | §33.9 |
| Crash, soak | systematic tests missing for the new durable steps; no soak | §33.10 |
| Self-update | agent updater complete; no controller updater (and none suggested) | §33.11 |
| Containers | no health check, no hardening in compose, no restricted-run test | §33.12 |

---

## 34. Platforms, packaging and release engineering (M17)

M17 made the software M1–M16 built installable, upgradable, recoverable, supportable, reproducible and releasable as one product, without a new product area. Everything here is about how DROOL is built, shipped, installed, upgraded and qualified; the only behavior changes are the build information it reports, a guard on root in the data directory, the Shepherd binaries' second location, the security checklist on Settings → System, and two fixes the new tests found (§34.12).

### 34.1 Gap audit

What the repository had before M17, and what became of it:

| Area | Found | Done |
|---|---|---|
| Provenance | `drool version`: version, short commit, date; no channel, no "modified", no protocol or schema; `drool-agent version` the version only; Docker builds had no VCS information | §34.2 |
| Release pipeline | none: the Makefile built the host's architecture; the Dockerfile used unpinned bases, a remote BuildKit frontend, no `SOURCE_DATE_EPOCH`, one platform; no archives, packages, checksums, SBOMs, notices, signatures or release notes | §34.3, §34.6 |
| License | the repository has no `LICENSE`: DROOL can't be distributed responsibly until its owner chooses one | §34.6 (a stable build refuses without it) |
| Packaging | a systemd unit for `/usr/local/bin` and manual `useradd`; Shepherd binaries had to sit next to `drool` | §34.4 |
| CI | one job without the race detector, the integration tests, the image or a release build | §34.11 |
| Container | `compose.yaml` built from source; `TestRestrictedContainer` mounted a test binary into a pre-existing `drool:local` image (and skipped without one): the real image was never tested | §34.5, §34.11 |
| Rootless Docker, Podman | "work the same way … not part of the automated tests" | §34.5 |
| Support claims | README "Tested in this release" with "expected to work" for Ubuntu 22.04 and Alma/RHEL; §1 claimed "Debian 12+, Ubuntu 22.04+, RHEL/Rocky/Alma 9+, current Fedora", more than was tested. Real systemd (VMs) only on Debian 12, Ubuntu 24.04 and Rocky 9; Debian 13, Ubuntu 26.04, Rocky 10 and Fedora 44 only in containers (no systemd, reboots, nftables or Shepherd under systemd); arm64 cross-compiled and never run; no arm64 controller or image | §34.7 |
| Shepherd releases | operators sign their own (unchanged); `drool release sign` defaulted to amd64 and didn't look at the binary | §34.2, §34.6 |
| Mixed versions | M16 found the `query-1` label problem by reading code; no older Shepherd binary was ever run against a newer controller | §34.9 |
| Upgrades | migrations tested schema by schema in-process and with fabricated databases; never a real older build upgraded to a real newer one, nor the rollback; pre-upgrade backup disk use undocumented; a manual installation would shadow a package | §34.8 |
| Master key | on the data volume next to the database; "keep a copy" without where or how per deployment; no Podman procedure; restore procedures never run as written | §34.10 |
| Test isolation | one shared controller for the integration tests; M16's final runs hit three leakage failures | §34.11 |
| Docs | README "Status: v0.1.0-alpha" and "on main" notes; this header listed milestones through M15; §2's subcommand list stale; `DROOL_MASTER_KEY_FILE` documented, but the variable is `DROOL_SECRETS_MASTER_KEY_FILE` | README, §2, §7, §33.7, header |
| Security posture | problems in the header indicator and on the Overview; nothing that tells problems, recommendations and options apart | §34.10 |
| Test switches | `DROOL_TEST_*` are honored by every build (logged as warnings); the release binary is what the tests run, so they stay | §34.10: the checklist reports them as a problem |

### 34.2 Versions, channels and provenance

`internal/version` holds the stamped `Version`, `Commit`, `Date` and `Channel`; `version.Get()` adds what Go recorded (`vcs.revision`, `vcs.modified`), the Go version and the platform. The recorded revision is authoritative: a stamp that isn't a prefix of it, any modified source, or a channel other than `stable`/`prerelease` makes the build `development`. `drool version [--json]` and `GET /system/info` (Settings → System) report version, channel, full commit, modified, build date (the commit's time for releases), platform, schema version, the agent protocol range and the Shepherd features the controller uses. `drool-agent version` prints exactly the version — every Shepherd's update preflight depends on it (§22) — and `--json` the details. Product version, database schema, Shepherd protocol and backup format are numbered independently (RELEASING.md).

`drool release sign` reads `GOOS`, `GOARCH`, `vcs.modified` and, for builds at a release tag, the module version (Go takes it from the tag; `-trimpath` keeps `-ldflags` out of the build information, so an untagged build's stamped version isn't readable) from the binary itself (`release.Inspect`, `release.CheckAgentBinary`).

### 34.3 The release pipeline

`packaging/release/build.sh` (= `make release`):

1. Resolves the commit; the tag decides the version and channel (`--version` names an untagged candidate, still `development`). A stable or candidate build refuses uncommitted changes in the working tree and a source without `LICENSE` (and, since §42.7, without a real maintainer or the Shepherd release key).
2. Creates a fresh repository holding exactly that commit (`git init` + `fetch` of the commit and the tags), so nothing uncommitted or untracked can get in, and derives `SOURCE_DATE_EPOCH` from the commit.
3. Runs BuildKit through `packaging/release/buildx` (the docker plugin, or a pinned standalone buildx: dockerd's own BuildKit, nothing installed) twice on the same build graph: `--target artifacts` → the binaries and notices per platform; the image → an OCI layout with both platforms, `rewrite-timestamp`, no provenance attestations.
4. Checks: every file is there; the image's three binaries per platform are byte for byte the archived ones (`relkit oci`); the notices match across platforms; the host's controller binary reports the version, channel and commit, unmodified, and its Shepherd the version.
5. Archives (`relkit tar`: sorted, fixed times and owners, gzip without name or time), packages (nfpm in a pinned container with a fixed hostname and every file's time set to the epoch), `compose.yaml` and `drool.container` with the version filled in, SBOMs (syft, pinned; Go module licenses from a module cache filled with the same `go.sum`; normalized by `relkit sbom-normalize`), `release.json` and `SHA256SUMS`.

The Dockerfile's stages: `web` (Node, `npm ci`, the UI, `npm-licenses.mjs`), `source` (Go, with `.git`), `agents` (both architectures, independent of the target platform so BuildKit builds them once), `controller` (the target architecture), `notices` (`relkit notices`: every Go module in the three binaries' build information with its license files from the module cache, the Go distribution's license, the npm packages from the lockfile that aren't development-only with theirs; a component without a license file fails the build), `artifacts` (FROM scratch) and the image (distroless `static-debian13:nonroot`, OCI labels for version, revision and creation time).

A second build of the same commit with `--no-cache` produced identical `SHA256SUMS` (§34.13).

### 34.4 Native packages

| Path | Owner | Package owns it |
|---|---|---|
| `/usr/bin/drool` | root, 0755 | yes |
| `/usr/lib/drool/drool-agent-linux-{amd64,arm64}` | root, 0755 | yes (the directory too, so rpm removes it) |
| `/usr/lib/systemd/system/drool.service` | root, 0644 | yes |
| `/usr/share/drool/` (notices, `config.example.yaml`, `LICENSE`) | root | yes — outside `/usr/share/doc`, which minimal systems (Ubuntu's container images) leave out |
| `/usr/share/doc/drool/` (`README.md`, `PACKAGE.md`) | root | yes |
| `/var/lib/drool` | drool:drool, 0750 | no: created by `postinst` (and `StateDirectory=`) |
| `/etc/drool` | root, 0755 | no: created by `postinst` (and `ConfigurationDirectory=`) |
| the `drool` system account | — | no: created by `preinst` when missing (`groupadd`/`useradd --system`, shell `nologin`, home `/var/lib/drool`) |

Scripts (one set for deb and rpm, branching on their arguments): install creates the account and directories and prints how to start DROOL (it isn't started or enabled); an upgrade (deb `configure <old>`, rpm `2`) runs `systemctl try-restart`, so a running controller restarts with the new version and its pre-upgrade backup; removal (deb `remove`, rpm `0`) disables and stops the service; `remove` and `purge` keep `/var/lib/drool`, `/etc/drool` and the account and say how to delete them. Leftovers of a manual installation (`/etc/systemd/system/drool.service`, `/usr/local/bin/drool`) are pointed out, because they shadow the package. Without systemd (containers) the scripts do the rest and skip `systemctl`. The release archive has the same layout under `/usr/local` (the unit's `ExecStart` rewritten by the pipeline).

The unit (`packaging/systemd/drool.service`, the one source for packages and the archive): `User=drool`, `StateDirectory=drool` (0750), `ConfigurationDirectory=drool`, `UMask=0077`, no capabilities, `NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`, `PrivateDevices`, `ProtectKernelTunables/Modules/Logs`, `ProtectControlGroups`, `ProtectHostname`, `ProtectClock`, `ProtectProc=invisible`, `RestrictNamespaces`, `RestrictRealtime`, `RestrictSUIDSGID`, `RemoveIPC`, `LockPersonality`, `MemoryDenyWriteExecute`, `SystemCallArchitectures=native`, `SystemCallFilter=@system-service`, `RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK`. DROOL's CLI commands run outside the unit, as the `drool` user (root is refused on its data directory, §22).

### 34.5 The container image and runtimes

The image keeps M16's properties: distroless, `nonroot`, ports 8080 and 9443, writable `/data` only (plus `/tmp` for `drool backup verify`), and the health check. `compose.yaml` runs a released image (`${DROOL_IMAGE:-drool:<version>}` in a release, `drool:local` from a checkout via `make image`) read-only, with a tmpfs `/tmp`, `cap_drop: [ALL]` and `no-new-privileges`; the release ships it with the version filled in. Nothing needs the host network, the Docker socket, devices or host mounts.

Runtimes (§34.13 for the evidence): Docker Engine rootful (the release tests' own Docker) and **rootless Docker** (docker-ce's rootless mode, same `compose.yaml`) are supported; **rootless Podman** is supported through `packaging/podman/drool.container`, a Quadlet unit (Podman ≥ 4.4: `systemctl --user`, `loginctl enable-linger` to survive logout) with the same restrictions, also shipped per release with the version filled in. Rootful Podman and other OCI runtimes are expected to work and not tested; there are no Kubernetes manifests.

### 34.6 Integrity, signing, SBOM, notices, license

- `SHA256SUMS` covers every file, `release.json` included; `release.json` lists every file's hash and kind, the version, channel, commit, source date, schema, protocol range and features, the pinned toolchain and the image's index, manifest and config digests (the index digest is what a registry serves when the archive is pushed unchanged: `skopeo copy oci-archive:… docker://…`).
- Signing: `packaging/release/sign.sh` signs `SHA256SUMS` offline with the DROOL release key (OpenSSH Ed25519, `ssh-keygen -Y sign -n drool-release`) after checking every file, and verifies against `packaging/keys/allowed_signers`; users verify with `ssh-keygen -Y verify` (README). The key never enters the repository, CI or a controller; until its public half is in `allowed_signers`, nothing verifies and no build is official. It is separate from the DROOL Shepherd release key (official Shepherd updates, §42.3) and the operators' own Shepherd release keys (§23.7), the Agent Root CA, the SSH user CA and the master key. `release.json` says how a release is signed; a build never is.
- SBOMs: SPDX 2.3 JSON (syft) for each platform's binaries (Go modules with licenses from the module cache, and the npm packages bundled in the web UI — development-only ones excluded) and for each platform's image (adding the base image's Debian packages).
- `THIRD_PARTY_NOTICES.txt` (in the archives, the packages, the image's `/usr/share/doc/drool/` and the release) carries every bundled component's license texts.
- DROOL's own license: the GNU GPL version 3, since its owner added `LICENSE` on 2026-10-17, with the SPDX id `GPL-3.0-only` in `packaging/release/license` (§42.1). Packages, the image's `org.opencontainers.image.licenses` label and DROOL's own packages in the SBOMs carry it. Without `LICENSE`, packages and SBOMs say `NOASSERTION` and a stable or candidate build refuses; with `LICENSE` but no SPDX id, every build refuses.

### 34.7 Support matrix, and why

Supported means tested by the release qualification for every release; *expected to work* means built from the same binaries or packages as something supported, not tested.

| | Supported | Evidence | Expected to work |
|---|---|---|---|
| Managed hosts | Debian 12, 13; Ubuntu 22.04, 24.04, 26.04; Rocky Linux 9, 10; AlmaLinux 9, 10; Fedora 44 | containers: the integration matrix (SSH, packages, files, Shepherds, firewall detection, Desired State); VMs (`TestVMPlatform`): systemd services, reboots, file deployment with service actions, system updates and reboot-required, SSH certificates with the distribution's sshd, the Shepherd under systemd with updates and rollback, nftables transactions over SSH and through the Shepherd | RHEL 9, 10 (the same packages as Rocky and Alma; RHEL itself needs a subscription), other Debian and RHEL derivatives, newer Fedora |
| Controller packages and archive | `.deb` on the Debian and Ubuntu releases above, `.rpm` on the RHEL-family and Fedora releases above; the release archive on the same distributions | `TestReleasePackages` in containers on all ten; `TestVMReleasePackage` under real systemd on Debian 12, Rocky Linux 9 and Debian 12 arm64 (a manual M16 installation moved to the package, the hardened unit at exposure 1.5, documented restores and key rotation, an older package upgraded and downgraded, and the archive's `INSTALL.md` as written — the archive's binary is the packages') | other systemd distributions with the release archive |
| Container image | Docker (rootful, rootless), rootless Podman with the Quadlet unit (Podman ≥ 4.4) | `TestContainerImage` and `TestContainerImageUpgrade` (Docker); `TestVMContainerRuntimes`: rootless Docker 29.9 and rootless Podman 4.9 on Ubuntu 24.04, the arm64 image under rootless Docker on Debian 12 arm64 (setup, the restrictions as inspected, the documented backup, key copy and restore) | rootful Podman |
| Architectures | amd64, arm64 for the controller, the image and the Shepherd | arm64: an emulated aarch64 Debian 12 VM runs the arm64 package and the archive under systemd, the arm64 image under rootless Docker (Debian 12's Podman 4.3 has no Quadlet), and the arm64 Shepherd installed by the controller with the VM platform suite | SSH-only hosts of any architecture |

Not supported: Windows, macOS and BSD (controller and hosts), Kubernetes for the controller, other Linux distributions for package operations (APT/DNF only), Shepherds other than amd64/arm64. Ubuntu 22.04 and AlmaLinux became supported because M17 added them to both test matrices; RHEL stays "expected to work" because it can't be tested without a subscription. Fedora's promise is the Fedora release current when DROOL is released.

### 34.8 Upgrades and going back

`TestReleaseUpgrade` builds the tagged `v0.1.0-alpha` and M16 (`3079311`) from this repository's history (`git archive`, nothing written to the repository) and upgrades them to the candidate on the same data directory, as an operator replaces a binary:

- *From the alpha* (schema 5): the candidate writes `drool-pre-upgrade-<time>-schema5.tar.gz` before migrating (and says so); the alpha's administrator, host, pinned key, credential (sealed with the alpha's master key) and job history are there; a job runs; `drool backup verify` calls the pre-upgrade backup restorable and complete with the key; the alpha refuses the upgraded database without changing it; the documented way back — the alpha restores the pre-upgrade backup — works, and the alpha runs jobs again.
- *From M16* (schema 20, no schema change): no pre-upgrade backup (one is made only when migrating); M16's Shepherd reconnects and runs jobs; a host on SSH certificates only signs in with a fresh certificate; Desired State finds drift made after the upgrade; monitoring arrives from the Shepherd; the Shepherd updates to the candidate's, signed with `drool release sign`; going back is starting M16 again, and the updated Shepherd works with it.

`TestVMReleasePackage` does the same with packages under real systemd: a manual M16 installation (its README's commands) moved to the package by the README's steps; a package upgrade from an older package (the alpha's binary in this release's packaging) and the documented downgrade with the pre-upgrade backup restored; and, on a clean machine, the release archive installed by running its `INSTALL.md` commands as written (the controller under the archive's unit hands out the Shepherds from `/usr/local/lib/drool`), then removed the way it says, keeping the data.

`TestVMFirewallUpgrade` covers the firewall, which the alpha didn't have: M15, whose host tool is `drool-fw` version 1, enforces a policy on a VM, over SSH in one run and through its own Shepherd in another; the controller is replaced by the candidate on the same data. The committed policy is recognized (same revision, not drift); over SSH the next revision commits with the candidate's tool (version 2); through the Shepherd, M15's Shepherd keeps applying with its own tool for the upgraded controller, and after a signed update to the candidate's Shepherd, with version 2; after a reboot the boot loader brings back the last committed policy and the host is *in sync*.

Disk use: a pre-upgrade backup is a full compressed copy of the database (about a `drool backup`'s size); the newest three are kept in `<data_dir>/backups`; `backup.before_upgrade: false` turns them off; the checklist (§34.10) shows what the data directory's backups take.

### 34.9 Mixed versions

The 1.0 promise (README "Shepherd versions"): a 1.x controller works with every protocol-1 Shepherd, older or newer; capabilities are negotiated, never inferred from a version; the M15 and M16 builds are tested, older pre-1.0 Shepherds aren't; upgrade the controller first; after a controller rollback, already updated Shepherds keep working.

`TestMixedVersionShepherds` (M15 and M16 Shepherds built from history, enrolled by hand with host-bound tokens, next to the current one): all connect, run jobs and send monitoring; Desired State checks go through M15's (which answers without announcing `query-1`); the statuses are M15 *reduced* (no `epoch-1`), the others *compatible*. After a backup and a restore, the current and M16 Shepherds reconcile; the M15 hosts wait as *incompatible* (no transition sent) and *update required*; with SSH as the fallback an M15 host's jobs run over SSH, without one they are refused (`ConfigurationError`, nothing run). A signed rollout of the candidate's Shepherd updates them in the old epoch (update offers aren't epoch-gated); they reconnect announcing `epoch-1`, are compared again (the fix in §34.12), reconcile, and run jobs through the Shepherd; every Shepherd ends *compatible*. That closes the path M16 left untested.

### 34.10 Secure your installation; the master key

*Settings → System → Secure your installation* (`SecurityChecklist`, needs `settings.read`; reachable by navigation search and from the Overview's Security card) lists checks as **problem** (plaintext HTTP on a network address, `DROOL_TEST_*` set, audit history verification failed), **recommended** (two-factor for every administrator, the Agent Root CA key offline, a remote audit copy), **optional** (an own TLS certificate, SSH certificates instead of stored keys), **done**, or **check yourself** (a copy of the master key kept apart from the backups; backups kept off the machine). `GET /system/security` supplies what only the server can tell (§22): it never claims to know about copies elsewhere. Nothing on it blocks using DROOL; MFA and the remote audit copy stay recommendations.

The master key's place and handling per deployment (native `/var/lib/drool/master.key`, containers `/data/master.key` in the volume), how to copy it out, what `--include-master-key` changes, rotation's `master.key.previous-*`, a lost key, and keeping the Agent Root CA key separately are in the README's *Backup and restore*; the restore commands there are the ones the release tests run: Docker Compose (`TestContainerImage`), rootless Docker and Podman (`TestVMContainerRuntimes`), native on the same machine and with a kept key file, plus key rotation (`TestVMReleasePackage`).

### 34.11 Release qualification and test isolation

`go test -tags integration ./test/integration -release dist/<version>` qualifies a release: setup checks every file against `SHA256SUMS` and `release.json`, extracts the host's controller archive, and the whole suite then runs that binary, with the release's Shepherds handed out (`agentV1` becomes the release's version). New suites: `TestReleaseArtifacts`, `TestReleaseSigning`, `TestContainerImage` (replacing `TestRestrictedContainer`), `TestContainerImageUpgrade` (a Compose installation of the alpha's own image upgraded to the image under test on the same volume, and back with the alpha's image restoring the pre-upgrade backup), `TestReleasePackages`, `TestReleaseUpgrade`, `TestMixedVersionShepherds`, `TestVMPlatform`, `TestVMReleasePackage`, `TestVMFirewallUpgrade`, `TestVMContainerRuntimes`. The VM matrix gained Debian 13, Ubuntu 22.04 and 26.04, Rocky Linux 10, AlmaLinux 9 and 10, Fedora 44 and an emulated arm64 Debian 12 (`qemu-system-aarch64`, UEFI, TCG, still an unprivileged container; `VM_FWD` forwards extra guest ports).

Isolation: `freshController(t)` gives a test a claimed controller of its own and points the helpers at it; every serial suite starts with one (the upgrade suites run a controller per step), so each runs the same alone or after anything else, and nothing one suite leaves behind reaches another; the release suites were run both ways (§34.13). RELEASING.md lists the qualification layers and which run in CI (`.github/workflows/ci.yml`: checks, race tests, web, the release build and its Docker-only release tests; `release.yml` on tags: the build twice for reproducibility and the whole integration and browser suite on the artifacts, uploaded unsigned) and which before a release on a machine you control (the VM layers).

### 34.12 Fixes the qualification found

**Restore reconciliation: updated Shepherds.** The mixed-version qualification found that a host held as `incompatible` (its Shepherd can't move to the new epoch) counted as settled: when its Shepherd came back updated, nothing compared it again, although the recorded reason said it would be. `recovery.Service` now remembers incompatible hosts (loaded at start with the pending ones) and, when such a host's Shepherd connects speaking this protocol and announcing `epoch-1`, puts it back to `pending` and compares it; a Shepherd that is still too old stays held without another attempt or audit record per reconnect. Unit test `TestIncompatibleShepherdUpdated` (fails without the fix); end to end in `TestMixedVersionShepherds`.

**Services that exit right after starting.** On Debian 13 (`TestVMPlatform`) a service operation reported a unit running `/bin/false` as started: `systemctl start` returns as soon as a `Type=simple` process exists, and DROOL's single look afterwards (M4) came before the process exited. Whether DROOL noticed depended on timing. A start or restart now looks again after `hostops.StartCheck` (2 s) and reports a `ServiceError` with `systemctl status` when the unit isn't running any more (failed, stopped, or restarting itself). The same code runs over SSH and in the Shepherd. Unit test `TestServiceStartedUnitThatExitsRightAway` (fails without the fix).

### 34.13 Results

The candidate qualified is **v1.0.0-rc.1**, built by `build.sh` at `c14381f` (untagged, so the development channel; 260 s on a 12-core workstation). Every later commit changes tests or documentation only, so its binaries are the ones the last commit ships; the release was rebuilt at the end and checked again (below). Commit hashes in this document are those of the published history, which was rewritten once before its first push (author identity and message trailers only; every tree is unchanged); the rc.1 and rc.2 artifacts were built before that and record the earlier hashes (`baa5c38` for `c14381f`, `39ce470` for `f3adba6`).

**Fast layers.** `make fmt-check vet check-generated` and `go vet -tags integration` clean; `make test-race` passes (50 packages with tests; 726 s while two emulated VMs ran — `internal/api` alone needed 606 s under that load, past Go's default 10-minute limit, so the target sets `-timeout 30m`); web typecheck clean, 88/88 unit tests, the production build.

**Release suites on the candidate, each in a process of its own:** TestReleaseArtifacts (42 s, with the Shepherd archive installed by hand), TestReleaseSigning (7 s), TestContainerImage (29 s), TestContainerImageUpgrade (22 s), TestReleasePackages (127 s, ten distributions), TestReleaseUpgrade (67 s), TestMixedVersionShepherds (68 s), TestRestoreReconciliation (42 s), TestSSHCertificates (32 s), TestAgentUpdates (40 s): all pass.

**Everything in one process** (the container suite with `-skip TestBrowser`, 26 min): 331 tests and subtests pass, 12 skip (the VM suites and the soak without their flags), 6 failed — all the AlmaLinux 9 test host, whose base image stopped shipping `passwd` on 2026-10-02; with the test image fixed, TestMatrix, TestAgentMatrix and TestFileManager pass on all ten distributions. **Browser tests:** 62/62, run while no other test process started or stopped (a run beside starting and stopping processes failed two tests on `net::ERR_NETWORK_CHANGED`).

**VM qualification** (QEMU TCG in unprivileged containers, two VMs at a time; about eleven hours, reruns included; counts are subtests):

| | `TestVMPlatform` (SSH, SSH certificates, Shepherd, firewall over SSH and through the Shepherd) | `TestVMReleasePackage` | `TestVMContainerRuntimes` |
|---|---|---|---|
| Debian 12 | 30/30 (40 min) | 3/3 | — |
| Debian 13 | 29/29 (40 min) | — | — |
| Ubuntu 22.04 | 28/29, then both firewall suites 16/16 | — | — |
| Ubuntu 24.04 | SSH (with the unattended-upgrades lock and the HWE kernel) and certificates pass; then the Shepherd and both firewall suites 22/22 (one vCPU) | — | rootless Docker 29.9 and rootless Podman 4.9 (Quadlet): pass |
| Ubuntu 26.04 | SSH, certificates and Shepherd pass, then both firewall suites 16/16 (one vCPU) | — | — |
| Rocky Linux 9 | 28/29, then both firewall suites 16/16 | 3/3 | — |
| Rocky Linux 10 | 29/29 (113 min) | — | — |
| AlmaLinux 9 | 29/29 (87 min) | — | — |
| AlmaLinux 10 | 29/29 (94 min) | — | — |
| Fedora 44 | 29/29 (90 min) | — | — |
| Debian 12 arm64 | 28/29 (52 min), then both firewall suites 16/16 | 3/3 | rootless Docker with the arm64 image: pass; Podman skipped (4.3, no Quadlet) |

On Debian 12 also: `TestVMFirewallUpgrade` passes over SSH and through a Shepherd (11 min; M15's revision in sync after the upgrade, tool 1 → 2, the boot loader restoring the last committed policy); the older suites `TestVMMonitoring`, `TestVMDesired`, `TestVMFirewallControllerCrash` and `TestVMHardening` pass (44 min).

Where a row says "then", the first run failed only in what a test fix or an emulator setting below changed, and the affected suites were run again on the same candidate. On Rocky Linux 9 the first run's "agent path blocked" apply failed before a transaction began: the Shepherd reported the operation interrupted, its process id afterwards (1000) suggests the VM restarted underneath it, and DROOL reported the outcome as unknown without sending anything again — as designed; the rerun passed, and the cause wasn't captured (the test now prints the Shepherd's journal and the VM's console in that case).

**What the qualification found.** In DROOL, two bugs, both fixed (§34.12): a host held as incompatible after a restore was never compared again once its Shepherd was updated (M16), and a service that exits right after starting could be reported started (M4; seen on Debian 13). In the tests: the post-cancel "next operation" had been a no-op since M5 (the package was already installed), so it never showed what DROOL says about an interrupted dpkg — now it does (`PackageManagerError`, "run `dpkg --configure -a`"); M17's per-suite controllers broke the firewall tests' agent-port rules (they denied the shared controller's port, so a transaction meant to roll back committed); the platform suite chains suites written for separate hosts (a revoked identity and a deliberately changed host key blocked what came next — the host is now left as an operator would leave it); Compose's log prefix and Podman's journald blank lines hid the setup code; the Podman check relied on an earlier `apt-get update`. In the environment: Ubuntu's 7.0 kernel (26.04's, and the HWE kernel the tests install on 24.04) oopses on an int3 in kernel text, or doesn't come back from a reboot, under two-vCPU multi-threaded TCG — an emulator race — so both Ubuntu VMs run with one vCPU; on that slower VM a firewall detection can exceed DROOL's one-minute limit, which DROOL reports as retryable, and the test checks again as an operator would.

**Final rebuild.** At `f3adba6` (the commit before this paragraph, which adds only it) `build.sh` built **v1.0.0-rc.2** in 184 s, and again with `--no-cache` in 308 s: the two `SHA256SUMS` are identical, every artifact byte for byte. On it, each release suite alone — TestReleaseArtifacts (40 s), TestReleaseSigning (6 s), TestContainerImage (26 s), TestContainerImageUpgrade (19 s), TestReleasePackages (105 s), TestReleaseUpgrade (87 s), TestMixedVersionShepherds (73 s), TestRestoreReconciliation (35 s), TestSSHCertificates (28 s), TestAgentUpdates (35 s) — passes; formatting, vet, generated code, the web typecheck and its 88 unit tests are clean at that commit.

---

## 35. Dogfooding backlog (after M17)

Product issues the user guide and dogfooding exposed (from 2026-10-12), recorded here instead of being
explained away in the documentation. Size and risk were estimates from the code. Every item is done:
most in the final batch before the 1.0 freeze (§38), the rest in the correction batch that reopened it
once (§40).

| # | Finding | Assessment | Proposed |
|---|---|---|---|
| 1 | Deleting a Pack leaves broken references: firewall rules (`group:` in the policy) and schedule targets point at a Pack that no longer exists, and foreign keys silently delete what hangs off it (Desired State assignments, health overrides, enrollment tokens, helper approvals) | Medium. References live in several places: JSON in policy revisions, schedule and workflow targets, alert routes (`group_ids_json`); cascades in five tables. Needs a reference query per subsystem, a `409` with the list from `DELETE /groups/{id}`, and an impact dialog with explicit confirmation | Done: refused while anything depends on the Pack (§38.4) |
| 2 | Removing a Shepherd from a host is a manual sequence (`systemctl`, `rm`) | Medium to large: a supported uninstall touches the controller (an action that revokes the identity and switches the host to SSH), the host (a `drool-agent uninstall` or an SSH script removing the unit, `/usr/libexec/drool-agent`, `/etc/drool`, `/var/lib/drool-agent`, `/var/log/drool-agent`) and, done through the Shepherd itself, the protocol and its compatibility rules | Done: **Uninstall Shepherd…** over SSH (§38.1) |
| 3 | An administrator can't reset another user's password in the UI; deleting and recreating the user loses owned schedules | Small: `PATCH /users/{id}` already accepts `password` (users.manage, re-authentication, audited as `user.update` with `password_changed`, the user's sessions revoked). Missing: a **Set password…** action on Settings → Users, and a `drool user set-password` command for a locked-out controller | Done (§38.5) |
| 4 | CLI output predates the M15 names: *Agents → Trust*, *Agents → Updates*, *the Agents page*, "agents take it", "groups", "jobs" in `drool backup`, `restore`, `agent-ca` and one security log line | Small: about ten human-facing strings in `cmd/drool`, `internal/agentsvc` and `internal/desired`; the API, JSON fields and log keys keep their names. The user guide quotes some of this output and changes with it; tests that match the text change too | Done (§38.5) |
| 5 | Docker Compose names the data volume after the project folder (`<folder>_drool-data`): renaming or copying the folder makes DROOL look new | Small change, real migration risk: `volumes: drool-data: {name: …}` makes the name stable, but existing installations use `<folder>_drool-data`, and the integration tests run several projects at once. A default that keeps the documented folder's volume (`drool_drool-data`) plus an override in `.env` avoids moving anyone's data; it changes a release artifact, so it belongs before 1.0 | Done: that default, `DROOL_INSTANCE`, a warning on an empty volume (§38.6) |
| 6 | `internal/recovery` TestHostDecisions fails now and then under full-suite load: `MachineChanged` for the cloned host is called after the service has started, so the host can be reconciled before the conflict is noticed | A test-harness race, not seen in the product; it passes on its own (8 of 8). Fix the ordering in the test without skipping it, weakening the assertion or adding sleeps | Done (§38.10) |
| 7 | Three UI hints still say *see the README* (the remote audit copy's format, *Configuration* on the security checklist, the restore commands on Settings → System) although those details moved to the user guide | Small: three strings; the README links each place, so nothing is unreachable meanwhile. The wiki's `system-backup-commands.png` shows one of them and needs capturing again with the change | Done (§38.8) |
| 8 | Adding a host required preparing an account on it by hand (`useradd`, a sudoers file, pasting DROOL's key), and the administrator's own password or key otherwise stayed in DROOL | Done: DROOL's own SSH account (§36) | Done (after M17) |
| 9 | `internal/sshx` TestPoolCertificateJumpPaths fails now and then under `-race -count=10`: its clock is real time plus an offset, and the test leaves the jump certificate one second of validity, so a slow run (1.03 s observed) sees it expire and the path re-dialled | A timing margin in the test, not a product fault; it passes on its own and in the normal suite. Fix by freezing the test clock rather than by sleeps or a looser assertion | Done (§38.10) |
| 10 | With Compose publishing the Shepherd endpoint under another port (`2043:9443` on a Synology NAS), Shepherds were told the port inside the container, and installation failed with *connection refused* | Done: a public port separate from the listen port, set once in `compose.yaml` (§37) | Done (after M17) |
| 11 | Creating a Pack required picking hosts at once; a Pack felt like a one-time grouping | Done: a Pack is created empty and gets its own page, where hosts are added and removed afterwards (§22) | Done (after M17) |
| 12 | DROOL's own SSH account wrote `Defaults:<user> !requiretty` everywhere; sudo-rs's visudo rejects it | Done: a minimal sudo rule, the exception only where the host's sudo needs and accepts it (§36.2) | Done (after M17) |
| 13 | Re-authentication asked from inside a dialog opened *behind* it, so switching a host to DROOL's own account looked hung | Every dialog shared one z-index; a modal layer stack fixes it everywhere (§38.9) | Done (§38.9) |
| 14 | A DietPi host over SSH was reported as "no systemd", with no services | systemctl without D-Bus fails for non-root accounts and was read as "no systemd"; detection from PID 1, permission-limited reported as such (§38.12) | Done (§38.12) |
| 15 | Services could only be started and restarted from the Services tab; intentionally stopped services kept alerting | Seven service actions fitted to the unit's state; expected state Running, Stopped or Ignored per host, Pack or every host (§38.2, §38.12) | Done |
| 16 | Network Activity couldn't say what traffic is, or recognize DROOL's own | Classification from DROOL's evidence and user classifications (§38.3) | Done (§38.3) |
| 17 | Setup-only credentials of an abandoned Add host stayed forever | Deleted with the host or when it signs in another way, swept at start; kept while a setup can be resumed (§38.7) | Done (§38.7) |
| 18 | No Telegram notifications | A first-class provider; the bot token treated as secret in every path (§38.13) | Done (§38.13) |
| 19 | Every failed unit alerted (`rtl433.service`, `smartmontools.service`, `varnish.service`…) and every service row was a policy editor | Automatic judged any failed unit a problem; the expected-state dropdown sat on every row | Done: monitoring a service is opt-in, one **Monitor** box per row, the rest under **Customize…**; old automatic issues closed as reconfigured (§40.1) |
| 20 | Alerts could only be acted on from an alert's own page | Acknowledge, maintenance and health settings existed; the list had no way to them | Done: **Actions…** per open alert (§40.2) |
| 21 | Network Activity said *country unknown / network unknown* for every public address when no GeoIP/ASN database was installed | The page didn't tell "not looked up" from "no answer" | Done: per-kind enrichment state, one explanation, "unknown" only for a real miss (§40.3) |
| 22 | Removing a host left DROOL's own SSH account, with passwordless sudo, on it | Only "remove from DROOL" existed; the guide listed manual commands | Done: **Clean up DROOL access and remove the host**, proven by the account's marker (§40.4) |
| 23 | Uninstalling a Shepherd required a working SSH path | The removal ran only over SSH | Done: a typed self-uninstall request; SSH only to verify, or as the fallback (§40.5) |

---

## 36. DROOL's own SSH account (after M17)

A dogfooding improvement after M17 (§35 #8), not a milestone. A host used to be added with an account
the administrator had prepared by hand. Now the account the administrator already has (their own,
with a password or key, sudo with or without a password, or root) is used once, to bootstrap a
dedicated account that DROOL then signs in with. Implementation: `internal/sshaccount` (plan, host
script, fresh verification, switch), the internal job step `ssh_account`, `POST /hosts/{id}/ssh-account/plan`
and `POST /hosts/{id}/ssh-account`, `setup_credentials` on `POST /hosts`, migration 0021
(`secrets.temporary`, `hosts.ssh_account_at`).

### 36.1 Invariants

1. **Bootstrap credentials may be temporary.** Passwords and keys typed in Add host are stored
   encrypted as `secrets.temporary` and assigned to the host; nothing else creates temporary
   credentials.
2. **The switch happens only after a brand-new connection with the new identity succeeded.** It is
   dialled through every hop again (`sshx.Dialer.Dial`, never the pool and never the setup
   connection), checks each pinned host key, authenticates as the new account with
   `controller-default` alone, and runs `sudo -n -- id -u` (answer `0`) over that same connection.
   A host that other hosts reach through must also forward a connection to each of them (their SSH
   banner is read).
3. **The original Linux account is never deleted or modified automatically.** The setup only runs
   as it; its password, keys, groups and sudo rights stay.
4. **A full-management account is root-equivalent through sudo, and DROOL says so.** The rule is
   `ALL=(ALL:ALL) NOPASSWD: ALL` (plus a `requiretty` exception for that user only where the host's
   sudo requires a terminal and its visudo accepts the exception): no allowlist that would pretend
   otherwise. The plan, the form, the result and the user guide say it in the same words.
5. **A shared credential is never deleted while anything references it.** The switch transaction
   deletes a temporary credential only through `secrets.DeleteTx`, which checks every host (jump
   hosts are hosts); a referenced one is retained and the result names the hosts.
6. **Partial provisioning never replaces a known-good path with an unverified one.** Until the switch
   transaction commits, the host's stored sign-in is untouched; any failure, and an unknown outcome
   (a connection lost during the setup), leaves the setup account and its credentials in place. The
   switch also refuses when the host's sign-in settings (user, credentials, sudo, address, port, jump
   host, pinned key, PTY, mode) changed during the run: the edit wins.

### 36.2 The setup on the host

One `sh` script, run as root through the setup account; in *check* mode (the plan) it stops after
reading. It needs `useradd`, `usermod`, `getent`, `visudo` and `sudo`, and an `includedir` for
`/etc/sudoers.d`; it installs nothing.

- **Ownership.** An existing account counts as DROOL's only if its record
  `/var/lib/drool/ssh-accounts/<user>` (root, 0600, in a 0700 folder) names `controller-default`'s
  fingerprint and the account's user ID; if the user ID wasn't recorded yet (a crash between
  `useradd` and the record), the account's comment must carry the record's random token. Otherwise:
  refused before any change (`exists`, `foreign`, `replaced`), as is a free name whose group exists.
- **Creation.** The record first, then `useradd -r -m` (shell `/bin/bash`, else `/bin/sh`), then the
  user ID. The password field becomes `*`: no password sign-in, and not `!`, which OpenSSH treats as a
  locked account when `UsePAM` is off.
- **Key.** `~/.ssh` (0700) and `authorized_keys` (0600), owned by the account; `controller-default`
  added once (lines others added to DROOL's own account stay); `restorecon` where SELinux has it.
  The same key for every host: no per-host keys.
- **sudo** (`sudoPolicy`, revised after M17 dogfooding). The policy is minimal and follows the
  host's actual sudo, never its distribution or version: `<user> ALL=(ALL:ALL) NOPASSWD: ALL`, and a
  `Defaults:<user> !requiretty` line only when the host needs and accepts it. Each proposed file goes
  to a dot-named temporary file in `/etc/sudoers.d` (sudo skips names with a dot) and is checked
  alone with the host's `visudo -cf`; a rejected one is deleted and never installed. An accepted one
  is moved to `/etc/sudoers.d/drool-account-<user>` (0440), keeping a copy of a file it replaces.
  Then: (1) `visudo -c` must report no problem it didn't report before (problems already on the
  host, such as another file's mode, are passed on as a warning and not counted against DROOL); (2)
  sudo must run `id -u` as root for the account without a password and without a terminal
  (`setsid -w runuser -u <user> -- sudo -n -- id -u </dev/null`, judged by its exit status and
  output, not by warnings such as *unable to resolve host*). If that test says a terminal is required
  (*must have a tty*), the exception is proposed, checked with `visudo -cf` and tested again; a
  host that rejects it, or still wants a terminal with it, is refused (`sudotty`, a configuration
  error) with its rule taken out. Any failure puts back the file DROOL replaced (or removes its own),
  and leaves no proposal behind. The no-terminal test replaced root's `sudo -l -U <user>`, which
  itself fails without a terminal on hosts with a global `requiretty`. Found on a host with sudo-rs
  (Ubuntu's sudo since 25.10): it has no `requiretty` setting, and its visudo rejected the line
  DROOL used to write on every host. The fresh connection (invariant 2) stays the final proof.
- **Interruptions.** The script ignores `SIGHUP` and `SIGPIPE`, so a dropped connection can't stop it
  halfway; running it again finds DROOL's record and continues (it reports `account existing`).

### 36.3 The switch and afterwards

One transaction: compare the sign-in snapshot; set user, `controller-default`, sudo without a
password and `ssh_account_at`; delete or retain the temporary credentials; audit `host.ssh_account`
(host, account, setup account, account created or existing, verified, switched, each credential's
name, kind and outcome; never a value) and a `credential.delete` per deleted credential, with the
requesting user as actor. After the commit the pool drops the host's connections (and those of hosts
that ride through it), a connection test runs as the new account, and the engine's host-changed hook
refreshes the inventory when the job releases the host. A failed setup is audited as a failure with
the step it reached.

`ssh_account_at` is cleared when an edit changes the host's user or credential; the UI offers the
setup again only for hosts without it (and not for jump-only hosts or hosts in certificate mode).

### 36.4 Boundaries

- **Certificate mode.** Refused for a host that signs in with certificates (switch back, set up,
  switch again). The account is an ordinary account, so certificates for its name work afterwards;
  no certificate authority is created for the setup.
- **Jump hosts.** A host behind one is set up and proven through it; the jump host's own row and
  credentials are untouched. A *jump host only* gets no account.
- **Shepherds.** The account can be a Shepherd host's SSH path or fallback; the Shepherd neither uses
  nor needs it.
- **No automatic undo.** An account created by an attempt that failed later stays, recorded as
  DROOL's, and the next attempt continues with it; removing it is documented (user guide, *DROOL's
  Own SSH Account*).

### 36.5 Tests

`internal/sshaccount` (an in-process SSH server with per-user keys plays the host: switch only after
a new connection, sudo checked over it, temporary credentials deleted, shared and jump-host
credentials retained, stored ones kept, every failure leaving the host as it was, a lost connection
reported as unknown, resuming, a concurrent edit winning, no secret in output, results or audit),
`internal/model` (internal step, name rules), `internal/api` (permissions, re-authentication,
`setup_credentials`, plan JSON), integration `TestDedicatedSSHAccount` (real sshd, `useradd` and
`visudo` on Debian 12 and Rocky Linux 9 in unprivileged containers: a refused fresh sign-in, then
the setup again; a stored credential kept; an existing `drool` refused and untouched; a refusing
sudo creating nothing; a host behind a jump host; the minimal rule), and the browser test
`web/e2e/addhost.test.mjs`. The sudo policy has its own tests: `TestSudoPolicy` runs `sudoPolicy`
against stand-in `visudo`, `sudo`, `runuser` and `setsid` (no terminal needed; a visudo that rejects
`requiretty`; a terminal required with the exception accepted, and rejected; a rejected rule never
installed; a rule sudo doesn't honour; an earlier file put back exactly; problems already on the host
not blamed; a configuration made worse, with and without earlier problems; no proposal left behind),
and integration `TestSSHAccountSudoPolicy` runs the real script on real sudo-rs (Ubuntu 26.04 with
`sudo-rs`, whose visudo rejects the old line: a first attempt failing at the sudo rule after the
account was created, then the setup again continuing with that account and installing the minimal
rule) and on a Rocky Linux host whose sudo requires a terminal for the account (the exception refused
by a visudo that rejects it, then added where visudo accepts it, the account working without a
terminal and on the fresh connection). Against the old script, the sudo-rs case fails with the
reported message.

---

## 37. Shepherd endpoint behind a published port (after M17)

A dogfooding fix after M17 (§35 #10), not a milestone. On a Synology NAS the Compose file published
the Shepherd endpoint as `2043:9443`. DROOL kept listening on 9443 inside its container, as it should,
but also told Shepherds to connect to `<NAS address>:9443`:
`Installing and enrolling as root (agent endpoint 192.168.1.83:9443)...`, then
`dial tcp 192.168.1.83:9443: connect: connection refused`. The installation rolled back correctly:
the Shepherd service was stopped and the host stayed managed over SSH. The cause: the resolver for
the advertised endpoint appended `agent.port`, the listen port, to the web UI's host name. Inside a
container that is the container's port, which other machines can't reach when the published port
differs. Implementation: `agent.public_port` and `server.public_port` (`internal/config`), the
resolver in `internal/agentsvc/identity.go`, `compose.yaml`'s `x-drool-ports`, migration 0022.

### 37.1 Invariants

1. **The listen port and the advertised Shepherd port are separate.** `agent.port` is only where the
   endpoint listens (and binds); `agent.public_port` is only what Shepherds are told. Neither changes
   the other, and a published port never changes `agent.port`.
2. **Container publishing doesn't leak internal topology into enrollment.** No token, installation,
   enrollment answer or displayed endpoint carries the container's listen port unless it is also the
   public one.
3. **An explicit advertised endpoint overrides.** `agent.advertise` (`DROOL_AGENT_ADVERTISE`) wins
   over the web UI's host name; an entry with a port is used as written.
4. **In Compose, one public-port setting drives both publishing and advertisement.** The
   `shepherds:` value in `x-drool-ports` (or `DROOL_AGENT_PUBLIC_PORT`) is the published port and the
   advertised port; the web port works the same way for the startup message.
5. **Installation shows and uses the externally reachable endpoint.** Both install dialogs say
   *Shepherds will connect to: …* before anything changes, from the same resolver the installation
   uses; so does the installer's output (`agent endpoint …`).
6. **A failed installation still rolls back to SSH.** The rollback is unchanged: the service is
   stopped and disabled, an identity that never connected and its token are revoked, and the host
   stays managed over SSH.

### 37.2 Resolution

For each address: `agent.advertise` entries if configured, else the host name the web UI was opened
with (`localhost` without one). An address without a port gets `agent.public_port` when it is set,
else `agent.port`. In order:

| Configured | Shepherds are told |
|---|---|
| `agent.advertise: [drool.example.net:7000]` | `drool.example.net:7000` |
| `agent.advertise: [nas.example.net]`, `agent.public_port: 2043` | `nas.example.net:2043` |
| nothing, `agent.public_port: 2043`, UI opened at `192.168.1.83:8080` | `192.168.1.83:2043` |
| nothing (the default) | `<web UI host>:9443` |

Every path takes it from `Endpoints(webHost)`, or the same rule: `CreateToken` (`POST /agents/tokens`,
the one-line installer, **Manual install…**), `InstallViaSSH` (one host, and bulk conversion, which
keeps the web UI's host name in `agent_rollouts.web_host` because it runs after the request), the
enrollment answer (`advertised()`: the configured entries with their ports completed), `GET
/agents/pki` (`advertised_endpoints`, `advertised_from`, `advertised_port`, and `endpoint_port`, the
listen port), the install dialogs and Trust (PKI), the startup log (`shepherds_connect_to` next to
`listen`), and a proposed controller address (`POST /agents/endpoints`: a host without a port gets
`advertised_port`). Endpoint migration otherwise works as before: explicit addresses, tested by each
Shepherd before commit. Recovery (an expired certificate within the grace period) uses the endpoints
the Shepherd already has; re-enrollment uses a new token.

### 37.3 Tests

`internal/config` (`public_port` validation; the listener stays `agent.port`), `internal/agentsvc`
(the resolver table, IPv6, the enrollment answer, completing a controller address), `cmd/drool` (the
startup message's web port), `internal/api` (published port: the PKI answer, the token, installation
over SSH through a forwarder standing in for Docker, and the Shepherd connecting through it; the same
for a bulk conversion; precedence; a refused endpoint rolling back as before; controller addresses),
integration `TestComposeFilePorts` (the shipped `compose.yaml` rendered by Docker Compose: defaults,
`DROOL_AGENT_PUBLIC_PORT`, `.env`, and the one line edited the way a Synology user edits it) and
`TestShepherdThroughPublishedPort` (this tree's image from the shipped `compose.yaml` with the
Shepherd port published as 2043, unprivileged containers on a test network: the startup log, the PKI
answer, the browser test `web/e2e/shepherd-endpoint.test.mjs`, a Shepherd installed over SSH that
connects through 2043, and the endpoint stored on the host).

---

## 38. Final 1.0 dogfooding batch (after M17)

The dogfooding pass meant to be the last before the 1.0 functionality freeze (2026-10-14), not a
milestone. It closed the §35 backlog and what further use found, and 1.0 functionality was frozen.
Real use of that build reopened the freeze once, the next day, for a correction batch (§40): parts of
§38.1 (the uninstall needed SSH) and §38.2 (an automatic failure alerted) are superseded there. 1.0 is
frozen again after §40; what follows is release work (a licence, the offline release-signing key,
`v1.0.0-rc.N`, qualification against the RC artifacts, release blockers only, `v1.0.0`). The product
boundary is unchanged: DROOL operates existing infrastructure; it doesn't provision it. Migrations 0023
(service expectations), 0024 (network classifications) and 0025 (Telegram destinations).

### 38.1 Shepherd uninstall

*Since §40.5 a connected Shepherd removes itself (no SSH needed); the SSH path below remains the way
when it can't be asked, and SSH verifies a self-removal when it works.*

`internal/agentsvc/uninstall.go`; `POST /hosts/{id}/agent/uninstall/plan` (agents.manage) and
`POST /hosts/{id}/agent/uninstall` (agents.manage and hosts.write, re-authentication
`shepherd_management`, the host's exclusive lock `agent-uninstall`); **Uninstall Shepherd…** on the
Management card whenever DROOL knows an identity for the host (**Remove Shepherd leftovers…** when it
isn't live).

1. **SSH is the path, before and after.** The removal runs over SSH as root (one script on stdin,
   `MODE=check` for the plan, `MODE=remove` for the removal), because SSH is what manages the host
   afterwards. A host DROOL can't manage over SSH is refused: `last_management_path` (no SSH access
   configured: the Shepherd is the only way in) or `ssh_unavailable` (SSH configured but not working
   as root now); `nothing_to_remove` when no file, process, service or trusted identity is left. A
   refusal changes nothing and is audited as denied.
2. **The plan is read from the host.** Each item (the unit file, `/usr/libexec/drool-agent`,
   `/etc/drool/agent.yaml`, `/etc/drool/release-keys.d`, `/var/lib/drool-agent` with the identity's
   private key, `/var/log/drool-agent`) with whether it's there, whether systemd runs, how many
   Shepherd processes run (found by `/proc/*/exe` and cmdline, never by name alone), and the
   controller-side steps, in words.
3. **Host first, controller in one transaction.** The script stops and disables the service (or the
   supervisor on a host without systemd), kills what's left, removes the items and reports what
   remains. Unless the outcome is unknown (the script didn't answer), one transaction then revokes
   the identity, revokes unused enrollment tokens for the host, sets management to SSH, clears the
   requested capability narrowing and records `agent.uninstall` (with what was removed and what
   remains, the revoked identity, `unused_enrollments_revoked`; never a secret). DROOL then drops the
   session and waits (bounded, 15 s) for no session to remain.
4. **Honest outcomes.** `uninstalled` only when the host reported nothing left and no connection
   remains; `partial` (what remains is listed; identity revoked, SSH management; **Try again**
   repeats it, idempotently); `unknown` (no answer from the host: nothing changed in DROOL); `nothing`.
   The wiki's manual removal stays for hosts DROOL can't reach ([Removing a Host](docs/wiki/Removing-a-Host.md)).

Tests: `internal/api` TestShepherdUninstall (a simulated host: plan, removal, revocation, SSH
afterwards, each refusal, partial and unknown outcomes, retry, roles, no enrollment token in the audit log),
integration TestShepherdUninstallOnHost (a real Shepherd under its supervisor in an unprivileged
container, removed from the browser by `web/e2e/shepherd-uninstall.test.mjs`; nothing left on the
host; SSH runs afterwards; a second removal finds nothing).

### 38.2 Acknowledge, snooze, expected state

*Superseded in part by §40.1: without an expected state a service is inventory only (a failed unit no
longer alerts), and the Services tab has a Monitor box per row instead of an expected-state dropdown.
§40.2 puts all three answers on the Alerts list.*

Three answers to an alert, now distinct in the product and the guide:

| | Means | Mechanism |
|---|---|---|
| Acknowledge | I've seen it | M12 acknowledgement: reminders stop, the alert stays open |
| Snooze | Not now | Maintenance (host or Pack, until a time): notifications held, health still recorded, what remains is notified when it ends. No separate snooze mechanism |
| Expected / Ignored | This is on purpose | The service's expected state, or a health override: the condition isn't a problem, so nothing alerts |

The alert page's **Quieter, for a while or for good** card offers all three.

**Expected service state** is a field of the M12.1 override model (§29), not a new inheritance
system: `service_expected` with a choice field `expected` (1 running, 2 stopped, 3 ignored;
serialized by name) at host, Pack and global scope with a `unit:` subject (migration 0023 relaxes the
subject check for service subjects at Pack and global scope, and converts M11's `watched_services`
into host-scope `service_expected: running`). Resolution: the unit's subject on the host, the host,
the subject in its Packs (Pack priority and conflicts as before), the Pack, the global subject,
global, recommended. Semantics, evaluated in `monsvc/health.go`:

| Expected | stopped | running | failed |
|---|---|---|---|
| (automatic) | ok | ok | `service_failed` (a failed unit is always in the default view) |
| running | `service_stopped` (2 min) | ok | `service_failed` |
| stopped | ok (*expected*) | `service_unexpected` (new check, recommended on, 2 min) | ok |
| ignored | never a problem | never a problem | never a problem |

State is collected whatever the expectation. Rows carry `expected`, `expected_source` (kind, label:
*set on this host*, *from pack X*, *set for every host*, override id), `health` and `health_note`,
so the Services tab says why something does or doesn't alert. Running and Stopped need
monitoring.manage; Ignored (switching checks off) needs monitoring.settings, and becoming ignored
counts as weakening. Pack pages and Monitoring → Settings have an **Expected services** card.

Tests: `internal/api` TestExpectedServiceState (each state, inheritance from a Pack and global,
alerts opening and clearing as *cleared by configuration*, state still collected), TestHealthLegacyMigration.

### 38.3 Network traffic classification

`internal/netmon/netsvc/classify.go`. Every flow carries `classification` {kind, label, source,
rule_id, detail}; kinds `drool`, `approved`, `user`, `service`, `unknown`; sources `drool`, `rule`,
`manual`, `port`, `none`. It is computed when flows are read (`newView` builds one classifier per
request), so a classification applies to traffic already observed and observed later, and nothing is
stored per flow. The `class` filter (`drool|approved|user|service|unknown|classified`) runs in Go
after the database filters, like the policy filter.

**DROOL's own traffic, from evidence only** (never a port alone; ambiguous stays unknown):

| Evidence (already owned by DROOL) | Flow classified `drool` |
|---|---|
| `fw_mgmt_observations` `ssh_in`: the address a host sees DROOL's SSH come from (SSH_CONNECTION, measured over SSH) and its sshd port | inbound TCP on that host to that port from that address; on the host whose address that is (where DROOL runs, if managed): outbound TCP to the managed host's addresses on that port |
| `fw_mgmt_observations` `agent_out`: the controller endpoint a Shepherd reported at hello | outbound TCP from that host to that address and port, by `drool-agent` or an unknown process; on the controller's host: inbound from the managed host's addresses |
| `host_hops`: hosts DROOL reaches through a jump host | outbound TCP from the jump host to the target's sshd port and addresses |

**User classifications** (`net_flow_rules`, migration 0024): label (1–120), note, expected, origin
`rule` or `flow` (made from one observed connection: *manually labeled*), and a match on host or Pack,
direction, protocol, service port and one remote side (a DROOL host, a Pack's hosts, or an address or
CIDR); empty fields match anything; a rule names at least a port or a remote side; at most 1000.
DROOL's evidence comes first and can't be overridden; then the most specific rule (flow origin +100;
host 8, Pack 4; remote host or single address 8, CIDR or Pack 4; port 4; protocol 1; direction 1);
then the port's service name; then unknown. Rules referencing a removed host go with it (cascade); a
Pack named in a rule blocks the Pack's deletion (§38.4). API: `GET /network/classifications`
(network.read), `POST`, `PUT /{id}`, `DELETE /{id}` (new permission `network.classify`:
administrators and operators; no re-authentication: labels change nothing on a host), audited
`network.classification.create|update|delete` with what the rule matches in words. No firewall rule
is ever created from a classification; nothing inspects packets.

UI: a **What it is · policy** column (the classification above the policy status) and a **What it
is** filter, the quick filters **DROOL's own traffic** and **Unknown traffic**, the flow dialog's evidence with **Classify…** (prefilled with exactly that connection;
widening it makes a `rule`), and a **Classifications** tab. The firewall policy status labels became
**Management path** and **Not in policy** (API values unchanged) so they don't read as
classifications.

Tests: `internal/api` TestNetworkClassification (each evidence path; port-22 and 9443 traffic from
other peers, other processes and over UDP stays unclassified; rule matching by host, Pack, remote
host, CIDR, port, direction and protocol; specificity; a rule never overriding DROOL's evidence;
filters; new observations classified at once; update and delete; validation; roles; audit; the Pack
deletion block; host removal; the firewall policy untouched), `web/e2e/network.test.mjs` (a label
for one connection over the details dialog, widened on the Classifications tab, filtered, deleted).

### 38.4 Pack deletion

`internal/packdeps`: one authoritative query, in the deletion's own transaction, of everything that
references a Pack: firewall policies (from/to JSON), schedules' targets (not archived), notification
routes' `group_ids_json`, active maintenance, Desired State assignments (non-archived baselines),
Pack-scope health overrides (expected services included), live enrollment tokens and network
classifications. Any of them **blocks** deletion: `DELETE /groups/{id}` answers `409 pack_in_use` with
`dependencies` (kind, id, name, a path to fix it, what to do), and `GET /groups/{id}/dependencies`
shows the same before asking. With nothing blocking, the dialog lists exactly what deletion removes
(memberships: the hosts stay; the Pack's own monitoring and Desired State priorities, its firewall
rollback timeout, standing helper approvals; the Pack id on used/revoked/expired tokens, released to
NULL). Removing a host from a Pack never deletes the host. Tests: `internal/api`
TestPackDeletionDependencies (each reference kind blocks, the list, a clean delete, no dangling
references), `web/e2e/packs.test.mjs`.

### 38.5 Administrator password reset and CLI wording

**Set password…** on Settings → Users (users.manage): `PATCH /users/{id}` with `password`, which now
asks for re-authentication with reason `user_password` (role and disable: `user_access`); the user's
sessions are revoked; audited `user.update` with `changes: ["password"]` and `sessions_revoked`, never
the password. On the controller, `drool user set-password --username NAME [--password-stdin]` does the
same for a locked-out installation (audited with `via: cli`). The CLI's human-facing text says
Shepherd(s), Pack(s) and Run(s) (`drool backup`, `restore`, `agent-ca`, `version`, update messages);
`drool shepherd-ca` is an alias of `agent-ca`; commands, flags, JSON and API names are unchanged.
Tests: `cmd/drool` TestUserSetPassword, `internal/api` user tests.

### 38.6 Compose data volume

Decision: `compose.yaml` names the volume, `name: ${DROOL_INSTANCE:-drool}_drool-data` (through the
`x-drool-data` anchor), so it no longer depends on the folder or Synology project. The default is
the name every documented installation already has (folder `~/drool`, project `drool`), so they keep
their data with no migration; a global `drool-data` was rejected because it would have given every
existing installation an empty database and made two instances share one by default.

- **Multiple instances**: each sets `DROOL_INSTANCE` (in `.env`) and gets `<name>_drool-data`. Two
  given the same name can't run on one database at once: the data directory lock (`drool.lock`,
  flock on the shared volume) stops the second (*in use by a running DROOL controller*).
- **No silent empty database**: `DROOL_DATA_VOLUME` (config `data_volume`, messages only) tells DROOL
  the volume's name; a DROOL that starts without data says *This DROOL has no data yet: it starts new
  in the volume …* in its startup banner and on the setup page (`setup/status` `data_volume`), with a
  pointer to the guide. The Podman unit sets `drool-data`.
- **Migration** (explicit, documented in [Compose Data Volume](docs/wiki/Compose-Data-Volume.md),
  backup first): check the current volume's name; `drool_drool-data` needs nothing; another name sets
  `DROOL_INSTANCE` to its prefix (Synology: edit the default in the YAML line). The guide covers
  finding the old volume after a mistaken start and removing only the empty one.

Tests: integration TestComposeDataVolume (rendered for several project names, `DROOL_INSTANCE`,
`.env`; this tree's image: an installation from before in a folder not called drool keeps its data
through the documented migration in a moved folder; a wrong name starts empty and says so in the log
and the setup status; two instances side by side; the same name refused while the first runs), the
existing compose tests now give every test project its own `DROOL_INSTANCE`.

### 38.7 Setup-only credentials

Credentials typed into Add host for DROOL's own account (`setup-<host>-login|key|sudo`, `temporary`)
live exactly as long as a host signs in with them. They stay while a setup is unfinished (resumable,
no TTL); removing the host, or editing it to sign in another way, deletes the ones it no longer uses
(`secrets.ForgetTemporaryTx`, in the host transaction; `setup_credentials_deleted` on `host.delete`
and `host.update`); one another host uses (a jump host included) is kept; stored credentials are
never deleted. DROOL's start sweeps temporary credentials no host references (`ForgetUnusedTemporaryTx`),
for ones left by older versions. Every deletion is a `credential.delete` with the name and the reason,
never a value. Tests: `internal/api` TestSetupCredentialLifecycle.

### 38.8 Stale README pointers

The three UI hints that said *see the README* (the remote audit copy's format, *Configuration* on the
security checklist, the restore commands on Settings → System) link to the user guide (`GuideLink`).

### 38.9 Dialog stacking (the hidden re-authentication prompt)

Cause, confirmed in the browser: every dialog used the same `z-50`, so stacking followed DOM order,
and `ReauthPrompt` is mounted before the router's content; a re-authentication prompt raised from
the SSH-account dialog was painted underneath it while focus moved into it, so the action looked
hung. Fix (`web/src/components/ui/modal.ts`): one modal layer stack. Every dialog (and the command
palette) renders in a portal on `document.body`, registers a layer, and gets `z-index 100 + its
position`; the newest is on top whatever the DOM order. Only the top layer reacts to Escape, the
backdrop and Tab (focus trapped inside it); `#root` and every lower layer are `inert`; focus moves into
the new top layer and returns, when it closes, to what had it (or the next layer's dialog). Tests:
`web/e2e/reauth-dialog.test.mjs` (desktop and mobile: the prompt above the dialog that raised it,
focus, Escape and backdrop affecting only the top, the underlying dialog inert, focus returning,
cancel, retry once), `web/src/api/client.test.ts`.

### 38.10 Flaky tests

- **TestHostDecisions** (`internal/recovery`): the cloned host's view was registered before `Start`,
  so the service could decide on it before the test's `MachineChanged` marked the clone; under load
  it sometimes did. The test now adds the clone after `Start`, calls `MachineChanged` first, then
  registers the view and the sessions. Deterministic: `-race -count=50` passes.
- **TestPoolCertificateJumpPaths** (`internal/sshx`): certificates' `ValidBefore` is whole seconds
  and the test clock was real time plus an offset, so whether the jump certificate had expired
  depended on the fraction of a second at issue plus the elapsed time (about 3 failures in 40 runs
  reproduced). The test clock is now frozen and moved explicitly, and the test SSH servers check
  validity against the same clock (`SetClockSkew`). Deterministic: `-race -count=50` passes.
- **TestBoundedReconnect** (`internal/recovery`, found in this batch's full-suite run): the test read
  the restore's status right after the last host was reconciled, while the service marks the restore
  finished in its next step. It now waits for both conditions (every host reconciled and the restore
  finished), keeping the assertion. Deterministic: `-race -count=20` passes.

### 38.11 Re-authentication policy review

Every route and handler that asks was reviewed against one rule: trust, access, privilege or
irreversible destruction asks; everyday administration doesn't. Reasons are typed
(`internal/api/reauth.go`): `account_security`, `user_access`, `user_password`, `host_access`,
`host_key`, `credential_in_use`, `ssh_ca`, `shepherd_management`, `shepherd_trust`,
`firewall_override`, `audit`, `recovery`; the server sends a fixed explanation the dialog shows.

- **Added**: installing a Shepherd (one host, and bulk conversions) and uninstalling one
  (`shepherd_management`); switching an established host's sign-in (a host whose key DROOL pins, to
  another credential or account, or dropping its credential in certificate mode: `host_access`, in the
  update handler); setting another user's password got its own reason (`user_password`).
- **Removed**: none. Ordinary firewall applies still don't ask (management-path guard plus automatic
  rollback); only overrides do. Expected service states, network classifications, Packs and
  metadata never ask.
- **Window**: 5 → 15 minutes (`auth.reauth_window`, `auth.DefaultReauthWindow`). Five minutes made a
  normal sequence of high-impact steps (switching several hosts to DROOL's own account, an install
  then a key approval) ask repeatedly; fifteen keeps one confirmation per task while a stolen session
  still needs the password for anything high-impact soon after. The server decides; the browser keeps
  no timer, and a refused request is repeated once after confirming.
- Switching an established host to DROOL's dedicated account still asks (route
  `POST /hosts/{id}/ssh-account`, `host_access`); with the stacking fix the prompt is visible, and
  within the window the following hosts don't ask again.

Tests: `internal/api` TestReauthRouteTable (the reviewed route set and reasons pinned), TestReauthWindowPolicy
(the harness clock moved, never waited for: inside, at the edge, expired; signing in counts; every
listed action refused with its reason and not performed; metadata never asks; confirming once covers
the rest; two-factor users confirm with a code), TestReauthForHighImpactActions.

### 38.12 systemd detection and service management

**Cause** (DietPi over SSH): DietPi runs systemd without D-Bus, so `systemctl` run by a non-root
account fails with *Failed to connect to bus*, which the scripts read as "no systemd"; and the old
check `[ -d /run/systemd/system ]` also fails for an account that can't traverse `/run/systemd`.
The Shepherd (root, which talks to systemd over its private socket) saw everything.

**Detection** (`discovery.SystemdProbe`, shared by `DiscoverScript`, `StatusScript`, the monitor's
service collector, the connection probe and the uninstall script): PID 1's name (`/proc/1/comm`)
decides (`systemd` → running, anything else → none); only when it can't be read, `/run/systemd/system`
(present → running; `/run/systemd` closed → unknown; else none). `systemctl` existing proves nothing.
A failing `systemctl` with systemd running is *permission-limited* (`@@sd-error permission …`) or a
failure, never "no systemd". SSH and Shepherd run the same script and reach the same facts.
Diagnostics distinguish *PID 1 is X, not systemd*, *systemd runs, but systemctl can't reach it as the
account DROOL signs in with*, and *systemctl failed*. On such hosts the hourly SSH pass reads the
services as root through the host's sudo (routine polls never use sudo), and the tab says so.

**Service actions** (Services tab **Actions…**): Start, Stop, Restart, Enable, Disable, Enable &
Start, Disable & Stop, as the typed `service` operation (over SSH or the Shepherd, the same
`hostops.Host.Service`; RBAC `jobs.execute`, Shepherd capability ceilings, audited as runs). Runtime
state and unit-file state are separate facts; `services.Actions` offers only what applies (no
Enable/Disable for static, indirect, generated, alias or unknown unit-file states, with the reason;
nothing that starts or enables a masked unit; only Stop for transient ones). After each operation the
unit is read back from the host and the result is what the host says (a start must still run after
two seconds; a stop or disable that didn't take is an error). Actions never change the expected state:
the confirmation warns when an action goes against it, and the result offers **Mark it expected
stopped/running on this host** as a separate step.

Tests: `internal/discovery` TestSystemdDetection (12 cases with the real scripts: PID 1 systemd with
and without a readable `/run/systemd`, systemctl without systemd, no systemd, D-Bus refused),
TestSystemdSameOverSSHAndShepherd, TestSystemdOldScriptOutput; integration TestSystemdOverUnprivilegedSSH
(the DietPi condition reproduced: an unprivileged container with PID 1 named systemd, `/run/systemd`
closed, no D-Bus, a real `systemctl` failing for the login account, a root-only fake systemd behind
sudo: detection, services read as root, Disable & Stop, Enable & Start, static and masked refusals);
`internal/monitor/services` (which actions each unit state gets); `internal/hostops`
TestServiceActionMatrix (every action, static, indirect, masked, not found, a failing command,
systemctl claiming success the host contradicts); `internal/api` TestServiceActionsLeaveExpectedState
(roles, audit, expected state unchanged by actions, the explicit follow-up); `internal/agent`
TestAdmissionCapabilities (a monitor-only Shepherd refuses service operations);
`web/e2e/monitoring.test.mjs` (expected states, health notes, the offered actions, the warning).

### 38.13 Telegram notifications

`notify.KindTelegram` (`internal/notify/telegram.go`): a bot posts with the Bot API's `sendMessage`.
Configuration: `chat_id` (a number such as -1001234567890, or `@channel`) and an optional
`thread_id` (a topic), the existing timeout; the bot token is a sealed, write-only secret
(`secrets_set.bot_token`). Messages reuse `Message`, the outbox, retries and rate limiting (20 a
minute per destination): HTML with only a bold headline, every other character escaped (clipped before
escaping), mentions neutralized, no link previews. 429 waits `retry_after` (or `Retry-After`, at most
an hour); 401/404 are the token, 403 the bot's membership, 400 the chat, the topic or a supergroup
migration (with the new id), all permanent; 5xx, 408 and network errors transient.

The request URL contains the token, so it exists only inside `sendTelegram`: never configuration (no
`url_display`), never followed through a redirect, never in an error (`url.Error` is reduced to its
cause, and every result is scrubbed of the token as is, `bot`+token, URL-escaped, and its secret half
alone), never logged; the API base URL is fixed (`Sender.TelegramAPI` exists for tests only).
Migration 0025 rebuilds `notify_destinations` for the new kind, keeping route memberships and delivery
links (a rebuild inside the migration's transaction would otherwise cascade). Tests: `internal/notify`
TestTelegram (validation, the request, chat ids, topics, conservative text, each answer, and the
token absent from every result after refused connections, untrusted certificates, plain HTTP, a
timeout, an echoing server, a redirect and a quoted URL), `internal/api` TestTelegramDestination (a
fake Bot API every API test's sender uses: write-only token, test notifications saved and unsaved,
deliveries, 429, a permanent failure, the token absent from responses, history, events, audit log,
the worker's log and the database except the sealed blob), the upgrade test (route and delivery links
survive 0025), `web/e2e/alerts.test.mjs` (the picker, setup steps, a masked token, the summary).

### 38.14 Scope

Nothing of 1.1 was started: no Docker/Podman, Synology or Proxmox monitoring, no Proxmox firewall, no
provisioning of any kind, no M18 DRIPS, DROPS or SPLASH, no Windows, no new navigation areas.

---

## 39. Direction after 1.0 (design note, not implemented)

Recorded so 1.1 starts from the agreed scope; nothing here exists in 1.0.

**Theme: observing and operating workloads and virtualization DROOL's hosts already run.** Likely:

- Docker and Podman: containers, their state, resources and logs on managed hosts; basic operational
  controls (start, stop, restart) as typed, audited operations like services.
- Synology: container and storage visibility on a NAS DROOL already manages.
- Proxmox: nodes, VMs and LXC containers, their state and resources.
- Proxmox's own firewall: awareness, and management where it can be done as safely as DROOL's host
  firewall (preview, management-path protection, automatic rollback).

**The boundary stays: DROOL operates existing infrastructure; it doesn't provision it.** No core
"Create VM", "Deploy LXC", "Create Docker stack" or "Provision cluster". Provisioning workflows, if
ever, belong to plugins or integrations outside the core. Monitoring and controls for workloads
follow the 1.0 rules: data collected over SSH or by the Shepherd within its capability ceiling,
honest about what each path can see, typed operations, RBAC, audit, no rules language.

### 39.1 M19 (proposed): a firewall baseline from observed traffic

Recorded during the 1.0 readiness pass (§43); not in 1.0, and no rule is ever generated automatically in 1.0.

**Why.** Writing the first policy for a host that already runs services is the hardest step in adopting DROOL's
firewall. DROOL already observes the traffic (DESIGN §19: continuously through a Shepherd, a snapshot over SSH) and
can already turn one observed connection into a draft rule (*Create policy from observed connection*) or a Guided-mode
suggestion (`fwsvc` `Suggestions`, `SuggestDraft`). M19 turns everything a host was seen doing into one baseline that a
person edits, reviews and approves.

**How it would work:**

1. **Learn.** A person picks hosts or a Pack and a learning window (for example a week). A baseline needs continuous
   observation (a Shepherd); hosts observed only by SSH snapshots are marked incomplete. For containers (Docker and
   Podman hosts, after the 1.1 container visibility of §39), published ports and container-to-container traffic are
   mapped onto the host's rules the way the compiler already coexists with Docker's chains (DESIGN §16).
2. **Propose.** DROOL groups the observed flows into named services and peers (hosts, Packs, named networks), proposes an
   allow rule for each, always keeps the management path, and leaves each default (inbound and outbound) to the person.
   Countries and networks (ASN) are context, never rules. Whatever is ambiguous — ephemeral ports, unknown peers,
   one-off connections — is listed for a decision, never allowed silently.
3. **Review** in the existing Firewall Designer as a draft (topology, rules and YAML), with the evidence behind each
   rule (connections, first and last seen, processes). The existing simulation is run against the observed traffic:
   every observed connection the draft would deny is listed before anything can be applied, and so is what the window
   can't have seen (monthly jobs, backups, failover paths).
4. **Approve and deploy explicitly.** Someone with `firewall.write` saves the policy and applies it through the existing
   path: preview, management-path protection, Monitor or Guided first if wanted, and the dead-man transaction with its
   automatic rollback.

**Never automatic.** No rule is created, saved or applied without a person approving it; the learning window enforces
nothing; observed traffic never becomes "allowed" by having been seen.

**Reuse:** the connection classifier and suggestions, the compiler's evaluation (Explain, simulation), the Designer,
the transaction protocol and its rollback. **New:** the baseline generator, its review step with per-rule evidence, and
the container mapping. **Risks:** a window misses rare traffic (so the review names what it couldn't see and
recommends Monitor or Guided first); rules overfitted to ephemeral ports; scale on large fleets; privacy stays as in
DESIGN §19.4 (no payloads, `network.read`).

---

## 40. Dogfooding correction batch (after the freeze)

Real use of the frozen build (2026-10-15) found five things worth reopening the freeze for, once:
simpler day-to-day behaviour, not a redesign, and no 1.1 work. 1.0 functionality is frozen again after
it. No migration (the expected-state model, its storage and the agent protocol version stay); one
additive protocol message pair and the Shepherd feature `uninstall-1`.

### 40.1 Service monitoring is opt-in

**Cause.** `automatic` (no expected state) still judged a failed unit a problem, so every unit systemd
reports failed (`rtl433.service`, `smartmontools.service`, `varnish.service`…) raised `service_failed`
and an alert, and the Services tab put an expected-state dropdown on every row.

**Semantics** (`monsvc/health.go` `services`). Inventory, state, history, enablement and actions are
collected and shown for every unit, as before. What DROOL alerts on is what someone asked it to
monitor:

| Expected (effective, after host/Pack/global inheritance) | stopped or missing | running | failed |
|---|---|---|---|
| automatic (no setting) | inventory only | inventory only | inventory only (shown as failed) |
| running (**Monitor** ticked) | `service_stopped` (critical; not installed: warning) | ok | `service_failed` (critical) |
| stopped (Customize…) | ok (*expected*) | `service_unexpected` (warning) | ok (*expected*) |
| ignored (Customize…) | never an issue | never an issue | never an issue |

Monitored means expected running or stopped. A unit whose state isn't known (its host stopped
reporting) and every unit while the host reports no inventory keep their issues: an evaluation without
data was a fake recovery before (an issue resolved as *recovered* when a stale host's policy changed).

**The Monitor box** (`PUT /hosts/{id}/monitoring/services/{unit}/monitor` `{monitor}`,
monitoring.manage; `monsvc.MonitorPlan`). It changes the host's own `service_expected` for the unit:
ticking sets running, or removes a host exception when a Pack or every-host setting already monitors
the unit; unticking removes the host's own setting, or, when a Pack or every-host setting monitors the
unit, makes a host exception (`ignored`), never revealing the inherited setting again. The exception
weakens detection, so it needs monitoring.settings (the existing `Weakens` rule); ticking and
unticking the host's own setting are an operator's, as watching was. Audited like any host override.
Rows carry `monitored`, `inherited` and `inherited_source` (what the host gets without its own
setting) and `customized` (overrides of the unit's checks: durations, turned off). The UI shows a small
line only when it isn't the plain case (*from pack X*, *expected stopped*, *not monitored here (pack X
monitors it)*, *ignored on this host*) and **⚙ Customized** only for check overrides or an expected
state set here that is more than ticking Monitor. **Customize…** (a row action) has the advanced
states (running, stopped, ignored), **Reset to inherited**, and the unit's check timing. The Pack
page's card is **Monitored services**. `watched_services` (host settings) stays the host's own
expected-running list (`SetWatchedTx`), so its clients and the fixture are unchanged.

**Settling the old issues.** At start (and after an upgrade) the evaluator judges every host's active
service issues against the configuration, reporting or not (`hasServiceIssue` in `reconfigure`):
an issue the expected state no longer allows ends as `reconfigured` (*isn't monitored: DROOL shows
its state, and alerts on a service only when it's monitored. This is a configuration change, not a
recovery.*), its alert as *cleared*; explicit Running/Stopped/Ignored settings are untouched.

**Higher-level UI.** A failed unit nobody monitors is never a health warning, alert, recent-events
warning or "Needs attention" item: the host's Services tile counts monitored services and their
problems (*None monitored*, *2 failed, not monitored* as a neutral note), the fleet row has
`service_issues` beside the factual `failed_services`, `attentionReasons` no longer lists failed units
(a monitored one is an open alert there), the Overview's *failed units* line and the inventory cards
are neutral facts, and the Services tab shows an unmonitored failure as a grey *Failed* with *Failed,
but not monitored, so it doesn't alert*. `service_unexpected` belongs to the services category.

Tests: `internal/api` TestServiceMonitoringOptIn (automatic failed and stopped units produce no issues
on SSH and Shepherd hosts alike; monitored running units failing and stopping; expected stopped
running; ignored; Pack and every-host expectations; host overrides and exceptions; checkbox
transitions, no-ops, operator refusals, validation; tile and fleet; `watched_services`; issue parity
between transports), TestServiceIssuesSettleAfterUpgrade (old automatic issues cleared at start, a
reporting and a stale host, explicit expectations kept, no fake recovery), TestExpectedServiceState,
TestHealthOverridesThresholdChanges (updated); web `monitor.test.ts`; `web/e2e/monitoring.test.mjs`
(the Monitor box, an unmonitored failure, Customize…, the Monitored filter, the actions' warnings).

### 40.2 Alert quick actions

**Actions…** on every open alert row (Active and Acknowledged; none on history), one button, a dialog:
**Acknowledge…** (active only; the note), **Snooze…** (the maintenance dialog preselected to the
alert's host, reason *Snoozed: <title>*), **Ignore…**, and a link to the alert. The alert page's
*Quieter* card opens the same Ignore dialog. The list holds the dialog, not the row, so it stays open
with its result when what it did takes the alert out of the list.

**Ignore** never closes an alert by hand. `GET /alerts/{id}/ignore-options` (alerts.read;
`monsvc.QuietOptions`) lists health settings that make the condition stop being a problem, each with
its effect, where it's undone, the override it writes and whether the caller may (the rules of `PUT
/monitoring/health/overrides`, which applies it with its own audit):

- a service: *Ignore <unit> on <host>* (host `ignored`, monitoring.settings); *Stop monitoring
  <unit> on <host>* only when the host's own setting is all that monitors it; the intentional
  opposite, *Expect <unit> to be stopped* (failed or stopped while expected running) or *to run*
  (running while expected stopped), never guessed;
- any other check: turn that check off for the subject, by its stable identity (`subjectOf`), keeping
  the subject's other fields; with no subject identity, for the host, said so.

The issue then ends as `reconfigured` or `disabled` and the alert as *cleared*, not recovered.
Permissions are the existing ones: alerts.acknowledge, maintenance.manage, monitoring.manage (and
monitoring.settings where detection weakens); a viewer sees no Actions…. Audit records now write an
expected state by name and *enabled* as a boolean (`monsvc.Diff`).

Tests: `internal/api` TestAlertIgnoreOptions (service and non-service options, operator and viewer,
acknowledged alerts, a Pack-monitored service, applying each kind, cleared not recovered, audit,
a closed alert, 404); `web/e2e/alerts.test.mjs` (acknowledge, snooze and ignore from the list, the list
refreshing, operator and viewer, a phone, the keyboard).

### 40.3 GeoIP/ASN: not configured vs unknown

**Cause.** A public address showed *country unknown / network unknown* whenever its `geo` was empty,
which was also the case for every address when no database was installed (or lookups were off); the
page's notice keyed on a single boolean that couldn't tell either side apart.

Every network view (summary, flow pages, a flow's details, a host's network) carries
`enrichment_state` `{country, asn}`, each `off` (lookups turned off), `unavailable` (on, but no usable
database of that kind: nothing was looked up; a broken replacement keeps the copy loaded before, which
stays `on`) or `on` (a database is loaded: a public address it has no answer for is genuinely
unknown). Rows show **—** (with the reason for screen readers and on hover) unless the state is `on`;
*unknown* is said only then. Network Activity shows one note at the top (*Country and network-owner
information is unavailable because GeoIP/ASN databases are not configured. Configure enrichment →*,
or the side that is missing, or *lookups are turned off*) linking to **Collection and enrichment**,
whose empty state no longer promises "Unknown". The flow dialog says *Not looked up: no ASN database is
configured*, or *Unknown (the GeoIP country database has no answer for this address)*. Enrichment stays
local; nothing is looked up online.

Tests: `internal/api` TestNetworkEnrichmentStates (off, no database, country only, ASN only, both, a
genuine miss, private and documentation addresses, a broken replacement, only an unreadable file, every
view agreeing); web `enrichment.test.ts`; `web/e2e/network.test.mjs` (a genuine miss with both
databases, no note); `web/e2e/network-enrichment.test.mjs`, run by TestBrowser after it removes the ASN
database and then the country one (the note, dashes, the details, the settings link, lookups off).

### 40.4 Cleaning up DROOL's SSH account when a host is removed

The host's **Remove host…** dialog (`POST /hosts/{id}/removal/plan`, hosts.write) offers **Clean up
DROOL access and remove the host** (recommended when there's something to clean up), **Remove from
DROOL only** (`DELETE /hosts/{id}`, unchanged: nothing on the host changes) and Cancel, and says what
stays either way (deployed files, packages, DROOL's firewall rules and tooling).

**Ownership** is proven on the host exactly as the setup proves it (§36): the host signs in with an
account DROOL created (`ssh_account_at`), and the marker `/var/lib/drool/ssh-accounts/<user>` exists
with DROOL's key fingerprint and the account's user ID. A name proves nothing. The setup now also
records the group useradd created with the account (`gid=`; for older markers the helper records the
account's own group before removing the account).

**The cleanup** (`sshaccount.Cleanup`, `POST /hosts/{id}/removal`, hosts.write, re-authentication
`host_access`, the host's exclusive lock). The account's last session (as root through its sudo) checks
ownership, stages a root-only helper in `/run/drool-account-cleanup.*` (or `/var/tmp`) and starts it
detached: a transient systemd unit when PID 1 is systemd (it outlives the session even with logind's
KillUserProcesses), else a process in its own session. DROOL then drops its pooled connections. The
helper waits for the account's processes to end (then ends what's left: DROOL's own), checks
ownership again, removes the sudo rule (only DROOL's file for this account, by its header) and the
policy's own leftovers, the home and mail spool when the account owns them, the account, the group only
when the marker proves it and nobody else uses it, the marker last (until then a rerun can prove what
is DROOL's), the empty marker directories, and itself. It leaves a marker written by another DROOL
alone. Interrupted, a rerun finishes; with nothing left, it changes nothing.

**Outcomes.** DROOL removed its own way in and says so: *Cleanup initiated; DROOL intentionally removed
its own management access and can't verify the final state through that account.* Every part is
checked before anything changes; a part that can't start (not provably DROOL's, the host unreachable,
`userdel` missing) refuses the whole cleanup with the reason, and the host stays (the user can still
choose Remove from DROOL only). If a part fails after another ran, the host stays and the result lists
what was done and what wasn't. Audited as `host.remove_cleanup` (refused or stopped) and `host.delete`
with the steps; never a key or password.

Tests: `internal/sshaccount` TestCleanupScripts (parsed only: they remove accounts); `internal/api`
TestHostRemovalCleanup (a fake host answering the scripts: forged, replaced and missing markers
refused with nothing changed, roles, re-authentication, the initiated result, Remove from DROOL only,
nothing to clean up, a Shepherd and the account with the account's cleanup failing after the Shepherd
went, then finishing; no secret in the audit log); integration TestSSHAccountCleanupOnRemoval (a real
account in an unprivileged container: the marker's group, forged markers refused, the cleanup through
the account's own session, everything gone on the container and unrelated users, groups, sudo rules
and files kept; the helper by hand: a foreign marker left alone, an interrupted run finished, a rerun
harmless), TestHostRemovalCleansUpBoth, and the browser test in `web/e2e/addhost.test.mjs` (TestBrowser
then checks the container).

### 40.5 Shepherd self-uninstall

**Protocol** (additive; protocol stays 1): `ControllerMessage.self_uninstall` `SelfUninstall`
{request_id, identity_id, controller_epoch, issued_at, expires_at (2 min), reason} and
`AgentMessage.self_uninstall` `SelfUninstallResult` {request_id, accepted, error, code, helper,
removes, already}. Shepherds announce `uninstall-1`; the controller never sends the request to one
that doesn't, and its compatibility status says *works, without removing itself when DROOL asks*.
`SelfUninstall.proceed` (added after CI found the race below) is the controller's confirmation that
it has an accepting answer.
No capability and no shell are needed: it removes the Shepherd and nothing else.

**The Shepherd** (`internal/agent/uninstall.go`) checks that the request is meant for it (well formed,
not expired or issued in the future, its identity, its controller epoch), starts the helper —
`agentrm.Script` with `MODE=self`, the same script the SSH path runs — from a root-only directory in
`/run` (or `/var/tmp`), detached (a transient systemd unit, or its own session), answers, and creates
the helper's GO file once the controller confirms it has the answer (`proceed`), sent for every
accepting answer, a late one included. Without it (the session ended, or an older controller) it
creates GO after 15 s. Creating GO right after sending, as first written, could lose the answer: a
gRPC `Send` returns once the message is queued, and a Shepherd stopped at once can drop it, which CI's
race-detector run showed as *the Shepherd disconnected before answering*. The helper waits for GO (at
most 20 s: an accepted removal goes ahead without it), stops and disables the service or kills the supervisor and agent (and update helpers),
removes the unit, programs, ceiling, release keys, identity, state and logs, and its own directory. A
repeated request is answered with the removal already started; while it goes, operations are refused.
Only a Shepherd running as root from the standard paths starts the real helper; anything else refuses
before writing anything (see the incident below).

**The controller** (`agentsvc.Uninstall`, `POST /hosts/{id}/agent/uninstall` `{method}`, the existing
permissions, re-authentication `shepherd_management` and lock). The plan says which ways work:
`shepherd` (connected, `uninstall-1`), `ssh` (SSH configured and working as root), and takes the
Shepherd's way when it can. Accepted: one transaction revokes the identity and unused enrollment
tokens, forgets the capability narrowing and sets management to SSH (with no SSH access, nothing is
invented: the host shows *DROOL has no way to manage this host…*), audited `agent.uninstall` with
`method`, `helper` and `outcome: initiated`; DROOL waits for the Shepherd to disconnect (or closes the
revoked identity's connection). Outcomes: `initiated` (*Self-removal initiated*, disconnected as
expected; DROOL can't look), `uninstalled` (*Verified removed*: SSH, used afterwards only to look,
shows nothing left), `partial` (what SSH still finds), `unknown` (no answer: nothing changed in
DROOL), `refused`. Offline but SSH works: the §38.1 removal. Neither: refused with the manual steps,
and **Forget the Shepherd** (`method: forget`) revokes and forgets it without touching the host. An
older Shepherd: *update it, or remove it over SSH or by hand*; SSH is offered only when it works.
**Remove host…** (§40.4) removes the Shepherd first this way, then DROOL's account.

**Incident while building it.** An in-process test Shepherd (running as the developer, not root)
received the new request and started the real helper on the workstation; its `systemctl` calls raised
two polkit password prompts (cancelled; nothing changed: the paths don't exist there and nothing
matched). Fixed by the root-and-standard-paths guard in the agent and a stand-in starter for every
in-process test agent (`StartRemoval`, `RemovalDir`); the real helper runs only in the container tests.

Tests: `internal/agent` TestSelfUninstallRequest (each refusal, the helper written root-only and
started once, repeats, operations refused meanwhile, a failed start leaving nothing, the guard),
TestRemovalScript; `internal/api` TestShepherdSelfUninstall (with SSH: verified; without: initiated, no
management path; a lost answer: unknown, nothing changed, then finished over SSH; a Shepherd without
`uninstall-1`: refused, SSH instead; partial; refused), TestShepherdUninstall (the SSH path, no path,
forget); `internal/agentsvc` TestJudge; integration TestShepherdSelfUninstallWithoutSSH (a real
Shepherd with no SSH path: accepted, disconnected, revoked, everything gone on the container),
TestShepherdUninstallOlderShepherd (the previous build: refused, removed over SSH),
TestShepherdUninstallOnHost (self-removal from the browser, verified over SSH), TestHostRemovalCleansUpBoth,
TestMixedVersionShepherds (the M16 Shepherd now *reduced*).

### 40.6 Smaller corrections found on the way

- `internal/engine` TestRestartMarksInterrupted raced under full-suite load (the first engine could
  write a host's state after the simulated restart recorded it); it now waits until both hosts are
  blocked in their operation.
- A service subject must be a systemd unit name (no spaces or slashes, with its type suffix).
- Service evaluation of one host is serialized and always uses the newest loaded configuration
  (`monsvc` `services`, a per-host lock). A tick loads the configuration once and walks every host;
  a change made meanwhile re-judges the host at once, and the tick, still holding the rules it started
  with, then undid that: monitoring a failed service opened its issue and the tick closed it as *isn't
  monitored*, so it re-opened as a new alert (an acknowledgement in between was refused). The old
  "every failure alerts" default had hidden this. Test: `internal/api` TestServicePassAfterAChange
  (a pass with the earlier configuration, after monitoring and after ignoring).
- Reads about a host that was removed meanwhile answer 404, never 500: the management router's
  file-manager, dry-run and package-version paths and the removal plan now map a missing host to
  *not found*. The host page stays behind the removal dialog while it shows the result, and kept
  asking. Test: `internal/api` TestRemovedHostReads (every host-scoped GET route, the host-filtered
  lists and both plans, for a removed host).
- Firewall status reports that arrive out of order, or were collected while a transaction was open on
  the host, no longer read as drift (`fwsvc.recordStatus`, `staleStatus`). The hello of the session a
  transaction verifies over carries a status from before the commit; handled after the commit's own
  report, it recorded *drift*. Handled before it, it did so too, with a `firewall.drift` audit record,
  on every first enforce. A report collected before the recorded one (by the host's clock within two
  minutes, then its committed revision and open journal) is ignored, and one with the host's journal
  `pending` keeps the drift as it was; a clock set back further, or a host restored to an older
  revision, still reports. Found by CI's race-detector run of TestFirewallAgentTransaction (*explain
  (drift)*); regression test TestFirewallStatusOrder.
- Versions compare pre-releases part by part, numbers as numbers (`release.CompareVersions`). Comparing
  the whole pre-release as text put `v0.1.0-alpha-102-g87a8202` before `alpha-22`, `alpha-33` and
  `alpha-97` once `main` passed 100 commits since the `v0.1.0-alpha` tag: Shepherds refused the update
  as a downgrade, and CI's release qualification (TestMixedVersionShepherds, TestReleaseUpgrade:
  Shepherds of M15 and M16 updating to the build) failed. It would also have put `rc.10` before `rc.9`.
  Shepherds already installed compare with their own code, so they keep refusing development builds
  from `alpha-100` on until a tag newer than `v0.1.0-alpha` exists (the next release candidate's, or a
  development tag): the owner's decision.
- The browser suite's health tests removed every health override between tests, the fixture's
  monitored services (host overrides since the previous batch) included; they now remove only their
  own.
- Integration TestDedicatedSSHAccount had failed since the previous batch (§38): it gave a host
  back its setup-only sudo password after switching it to another one, but the setup-credential
  lifecycle forgets that password at the switch, as designed. The test now checks it was forgotten
  and gives the host a stored sudo password instead.

### 40.7 A removed host leaves schedules and firewall policies

**Found in use, after the batch was pushed:** a host removed (to test the cleanup) and added back
couldn't be put back into its update schedule: *host host_… not found*, naming the removed host's id.
Foreign keys clean up what belongs to a host, but two kinds of configuration name hosts inside JSON
and kept the removed id: a schedule's targets (every later save of the schedule failed, and so did
every run: *The last run could not start: host … not found*), and a firewall policy's sources and
destinations (every later save of the policy failed with *unknown host*; the YAML view, which names
hosts, couldn't show it).

**Now** (`internal/hostrefs`), removing a host, either way (`DELETE /hosts/{id}` and the cleanup
removal, through `forgetHostTx`), takes it out of both in the removing transaction:

- **schedules** (not archived): out of the targets, the exclusions and the canary hosts, in a new
  revision by the remover. A schedule left without targets is disabled with *Disabled: its only
  target, X, was removed from DROOL. Choose its targets, then enable it.*; enabling a schedule without
  targets is refused. A schedule whose only canary was the host keeps a canary (a count of 1), as a
  run already did when its chosen canaries weren't among its targets.
- **firewall policies**: out of the sources and destinations. A policy left without a source or a
  destination is deleted (it could never match again, and a policy needs both). Compiling already
  ignored a removed host, so nothing allowed or blocked changes; the firewall's converger is kicked.

Nothing widens: a host taken out of a list only narrows it. Notification routes are deliberately not
changed: they keep a removed host (M12), because dropping it could widen a route to every host. The
removal plan lists each change (`changes`: *Either way, in DROOL: …* in the dialog); each change is
audited on its own (`schedule.update`, `firewall.policy.update` / `.delete`, with the reason) and listed
in `host.delete` (`taken_out_of`). At start, `hostrefs.RepairTx` does the same for hosts removed
before this (named from their `host.delete` audit record), as DROOL's own audited changes, and clears
a schedule's *The last run could not start: host … not found*.

Tests: `internal/api` TestRemovedHostLeavesSchedulesAndPolicies (five schedules, four policies and a
route; the plan; the removal; revisions, attention, enabling refused, audit; then the host added back
into the schedule, saved, and run on both hosts), TestRemovedHostRepairAtStart (the reported failure
reproduced, repaired, idempotent); `web/e2e/addhost.test.mjs` (the dialog lists the schedule, which is
then disabled).

### 40.8 Scope

Nothing of 1.1 was started: no dashboards, Docker/Podman, Synology or Proxmox work, no systemd logs,
no scheduled-service rules, no provisioning, no M18 DRIPS, DROPS or SPLASH, no Windows. 1.0
functionality is frozen again, pending the next RC's qualification.

## 41. UI/UX correction (before the release candidate)

An external review of the 1.0 build's screens (`ui-review-1.0`, 111 screens) produced the *DROOL 1.0
UI/UX Redesign and Usability Correction Specification*. This batch implements it in six phases, as
separate commits, on the existing components, routes, APIs and tokens — not a rewrite. Behaviour,
safety semantics and the object model are unchanged except where listed. Not done (out of bounds): a
new monitoring or alerting engine, a second firewall policy path, new execution capabilities or
workflow semantics, Shepherd trust or update-signing changes, DRIPS/plugins, M18.

### 41.1 Release qualification defects (separate commits)

- **Shepherd session data race** (`TestAgentCertificateLifecycle` under `-race`): `agent/session.go`
  passed the `grpc.Peer(&pr)` call option, which gRPC writes from its stream-finishing goroutine when
  the context is cancelled — concurrently with the session reading it. A real race, rare in tests
  (8 plain runs didn't show it; a deterministic reproduction with an unsynchronized canceller did).
  The peer now comes only from `peer.FromContext(stream.Context())`, read once (69d6cec).
- **`TestInspectRealAgentBuild` at a tag:** it assumed a build is never exactly at a tag. It has two
  subtests: without VCS information (always no version) and from the checkout (the release tag at
  `HEAD` when clean; v2+ tags ignored because Go ignores them for module `drool`) (c1051c0).
- **`TestAlertDestinationTestResults`'s hanging receiver** answered 204 when the client gave up, and
  Go's transport can use a response racing the cancelled connection's close, so "delivered" at exactly
  the timeout. The receiver now records the payload and never answers (`http.ErrAbortHandler`). Not a
  product defect: a real 2xx at the deadline is honestly "delivered" (d8e15d4).
- **A firewall report from before a restore's move** (`TestRestoreReconciliationFirewall` under
  `-race`: branch CI of `59baa30`, and 3 of 20 local runs). A Shepherd's firewall status collected
  before reconciliation moved its host to the restored controller's epoch could be handled after the
  move was recorded. The hello's status is recorded by a goroutine while reconciliation runs, and a
  status collected just before the move can arrive after it. `recordStatus` then marked the moved host's
  firewall as belonging to another epoch, and every later transaction on it was refused ("reconciliation
  required"). Nothing cleared it: Retry and Accept refuse a reconciled host, and adopting the host's
  state refuses an epoch mismatch. Latent since M16. A report in the epoch the controller was restored
  from (`controller_restores`), showing exactly the revision and hash whose move is recorded, now marks
  nothing. A policy committed since in that epoch, or an epoch the controller wasn't restored from, is
  still a mismatch, and an apply checks the host's fresh status first. `TestFirewallStatusFromBeforeTheMove`
  covers the cases. With the hello forced to arrive after the move, the restore test failed every run
  before and passes after; unforced, it passed 40 of 40 runs under `-race` (c9e1d29).

### 41.2 Navigation and search

The destinations are unchanged; spacing, the active state and icons were refined and the duplicate
Settings heading removed. `/hosts/{id}/…` keeps **Hosts** marked. Ctrl+K searches records as well as
pages: `GET /search?q=&limit=` (authenticated; at most 100 characters and 5 words, every word must
match; `limit` 1–10, 5 by default, one more fetched to say "more") returns groups — hosts (name,
display name, address and interface addresses, tags; `hosts.read`), Packs (`hosts.read`), services
with their host (`monitoring.read`; each word must match the unit or the host, at least one the
unit), active alerts (`alerts.read`) and recent runs (`jobs.read`) — each only if the principal holds
its permission. `LIKE` arguments are escaped (`%`, `_`, `\`). The palette debounces, cancels stale
requests, keeps focus and Escape, shows grouped options, and offers destinations for an empty query.
Order: pages first when the words name one (score ≥ 90) or nothing is typed; otherwise hosts, then
pages whose title or alias has a word starting with the query (≥ 70), then alerts, Packs, services
and runs, and pages that matched only a description last.

### 41.3 The host workspace

`/hosts/{id}` is a nested route: a header (breadcrumbs, name, OS, address, how DROOL manages it and
its fallback, the last check, open alerts, maintenance, a run in progress; Run command, Install
updates — in the menu on a phone — and an Actions menu: Reboot…, Refresh inventory, Test connection,
Start maintenance…, Edit host details, Remove host…; security problems — an unapproved or changed host
key, a pending restore reconciliation — as one-line notices under it on every section) over eight
sections at their own addresses: Overview (index), `monitoring` (its tabs keep
`?tab=`), `files` (+ `files/edit`), `network`, `firewall`, `desired`, `management`, `activity`.
Sections a role can't open are absent; on a phone they are a compact selector. Earlier links keep
working (`#management` → `management`). The Overview is ordered: identity and notices, health now and
the things to act on, then summaries linking to each section and recent events. Management holds
Connection, SSH access, Shepherd, Membership and the Danger zone. Activity lists the host's runs, the
schedules that target it — saying whether directly or through a Pack or tag, and never claiming what
future runs will target — and its audit records (`GET /audit?target=`).

### 41.4 Interaction patterns

- **Inspector** (`ui/Inspector.tsx`, Pattern A): a modal layer from the right (the whole screen on a
  phone) for one record — network connections, alerts. Steps (classify, acknowledge, snooze, ignore)
  replace its content with Back; Escape goes back, then closes; focus is trapped and restored; the
  re-authentication prompt can still come on top. No dialog on a dialog.
- **Focused confirmation** (`ui/ConfirmDialog.tsx`, Pattern B): title = action + target, body =
  consequences, Cancel (or a named alternative such as *Keep it running*) and the action always in a
  visible footer, errors shown where asked. `confirmAction()` replaced all 25 `confirm()` calls and the
  firewall's `prompt()`; the e2e session fails a test on any native alert, confirm or prompt (leaving a
  page with unsaved changes is the exception). It is never the only check: the server enforces
  permissions, re-authentication, locks, capabilities and each operation's rules.
- **Pages and wizards** (Pattern C) for multi-step work, unchanged: Add host, editors, the designer.
- Dialogs got a `footer` (Install/Uninstall Shepherd, Remove host): actions stay in sight while the
  body scrolls.
- Menus (`ui/Menu.tsx`: the host's Actions, a service or file row's ⋯) draw their open list above
  the page (a portal, fixed position): under the button, above it when there's no room below, kept on
  screen; a table's scroll box can't cut it off. Scrolling moves it with its button (also a scroll
  still settling as it opens); it closes on a click outside or once the button is off the screen.
- No hidden rightmost column at 1440 px: the file manager keeps Download in sight and puts Rename,
  Permissions, Owner and Delete in each row's ⋯ menu; the audit table wraps targets and shortens the
  details (the full JSON on hover); the host's System card lists addresses and interfaces as lines.
  A browser test checks the widest pages for sideways scrolling.

### 41.5 Monitoring, services, alerts

Charts measure their container (ResizeObserver) and draw at pixel width, two per row on wide screens,
instead of a fixed viewBox scaled into a quarter-width grid cell. The fleet filters show the common
ones with the rest under an advanced area, the active ones visible, and a reset. Services: a find box
(`?unit=` from search), Actions… plus a ⋯ menu per row, bounded lists on a phone; semantics unchanged
(§40.1). Alerts open in the inspector with their actions; `/alerts/{id}` stays the linkable page. A
service alert's page and inspector lead to the unit among the host's services
(`?tab=services&unit=`, found and highlighted as from the search); storage and hardware alerts to
those tabs.

### 41.6 Network Activity and the firewall topology

A summary, one search, the common filters (host, direction, external, unknown, policy status) with
the number applied, the rest under More filters; the table fits 1440 px; a phone shows 25 compact
entries a page (server cursors unchanged). A connection opens in the inspector; classifying is a step
in it. Fix: classification rules read "()" in the classifier's own view because it loaded rules
without `describe()` — the descriptions now name hosts and packs. The topology fits the configured
graph (configured keys from `toGraph(... "configured")`), groups observed nodes, offers Fit, zoom,
reset and Focus on the selection, tells configured from observed, and moves display options and the
legend over the canvas. While nothing is selected and nothing is unsaved, the Details/Preview side
panel gives way to the canvas, so the map gets the full width (about 1.5× larger at 1440 px);
selecting something or changing the draft brings it back, and a toggle opens or hides it by hand.
The policy model and compiler are untouched.

### 41.7 Runs, schedules, workflows, desired state

New run puts the essentials first and the timeout, OS conditions and read-only under Advanced options
(their values listed in its header when closed). **Rollout default:** for a step that changes hosts
and two or more targets, the effective rollout is the recommended one (1 canary, batches of 5, 5 at a
time, pause after 2 failures) *until the user changes it*; from then on theirs is used as targets
change, with the recommendation offered beside it — never applied for them. A schedule or workflow
made from the run starts with the chosen or recommended rollout; a new workflow that changes hosts
starts with the recommendation. Starting a run is reviewed in `RunConfirmDialog` (operation and
parameters, host count and names folded past 8, rollout and what all-at-once risks, skipped and flagged
hosts, preview and desired-state warnings, a package dry run's or a deployment preview's findings, or
"not previewed"); the review uses the preview of exactly the current targets (never placeholder data).
A workflow's Run now reviews in the same dialog. A Pack's page has Run on this pack and Install updates
in its header, like a host's (New run with the Pack, the second with the System update preset).
Schedules say whether they run their own steps or a pinned workflow revision. Desired State: Unknown is explained as *not verified* and never counted
compliant; host rows link their baselines; a host's resource rows show that they open (a chevron
and a one-line hint) for what differs, the value's source, history and the actions; the four
automation pages say how they relate. A Pack's name on the Packs page opens its page.

### 41.8 Settings directory, host busy

`/settings` (index) is a directory of every setting, grouped, each linking to the page that owns it
(`SettingsDirectoryPage.tsx`; permission-filtered; no forms). `GET /hosts/{id}/busy`
(`hosts.read`) lists who holds the host's locks in words — a run (by name, linked), an inventory
refresh, a status check, a preview or dry run, file operations, a Shepherd install or uninstall, host
removal — for the header and the dialogs that need the host (first in the dialog, above its options:
*available again when it finishes*, re-checked). Advisory: the locks taken when work starts are
unchanged.

### 41.9 GeoIP/ASN databases without setup

`netmon/geoip.Updater` keeps DB-IP Lite Country and ASN (CC BY 4.0;
`https://download.db-ip.com/free/dbip-{country,asn}-lite-YYYY-MM.mmdb.gz`, published on the first of
each month, no account) in `<data_dir>/geoip/downloaded` with `state.json` (release, build epoch,
SHA-256, ETag, Last-Modified, last check/success/error).

- **When:** a background loop; the first check 5 s after start only if a needed database is missing
  or the last success is a day old; then every 24 h (1 h while nothing could be downloaded yet); Check
  now (`POST /network/enrichment/check`, `network.settings`, audited `network.enrichment.check`); a
  settings change that allows downloads with a database missing.
- **What:** per kind not provided by the operator's files: this month's release, conditional on the
  stored ETag/Last-Modified (304: nothing), else on 404 last month's (kept while it's the newest).
- **Safety:** HTTPS (redirects may not leave the scheme), dial/TLS/header timeouts and 5 min overall,
  64 MB compressed and 256 MB unpacked limits (`Content-Length` and streamed), 3 attempts for
  transient errors, gzip CRC, `Open` + a probe that the database answers well-known addresses with the
  expected kind, no older build than the one in use, a temporary file `fsync`ed and renamed in the
  same directory, the directory synced, then `Enricher.Reload`. Failures keep the databases in use;
  one check at a time (`TryLock`; `ErrChecking`); a crash's temporary files are removed at start.
  Logs name the kind, release and provider — never addresses.
- **Enricher:** scans the operator's directory and the download directory; for each kind the
  operator's files win (whatever their build date), then the newest. `OperatorKinds`, `DownloadedInUse`.
- **Switches:** `netmon.Settings.GeoIPDownloads` (`geoip_downloads`, default true; optional on save),
  lookups off (`enrichment: false`), and `network.geoip_download: false` (no updater requests at all).
  `DROOL_TEST_GEOIP_URL` (TEST MODE, logged) points at a stand-in; every test controller runs with
  downloads off unless a test turns them on.
- **States** (`EnrichmentStatus.state`): off, available, updating, update_failed, preparing,
  unavailable, not_configured; per kind `enrichment_state` adds `preparing`, plus `downloads`,
  `download_error`, and the `attribution`/`attribution_url` the licence asks for, shown with the data
  (tables, inspector, host network card, topology). Network Activity shows a quiet note while
  preparing, never a configuration warning.

### 41.10 Routes and APIs

New: `GET /search`, `GET /hosts/{id}/busy`, `POST /network/enrichment/check`; web routes
`/hosts/{id}/{monitoring,files,files/edit,network,firewall,desired,management,activity}` and
`/settings` (directory). Changed (additive): `NetSettings.geoip_downloads`; `NetEnrichment.state`,
`download_directory`, `files[].downloaded`, `downloads`; `NetEnrichState` `preparing`, `downloads`,
`download_error`, `attribution`, `attribution_url`; `NetTopology.attribution`, `attribution_url`.
Configuration: `network.geoip_download`. Removed: nothing; old host-page links redirect.

### 41.11 Tests

Go: `internal/api` TestSearch (permissions, escaping, limits), TestHostBusy, TestNetworkClassification
(described rules), TestGeoIPDownloads, TestGeoIPWithoutDownloads; `internal/netmon/geoip` updater tests
(offline, retries, corrupt, wrong data, oversized with and without a length, custom files kept and
preferred, failures keep the database in use, restart without a new download, month fallback, no
downgrade, one check at a time, turned off, a damaged file replaced, stale temporary files). Web unit:
filters, navsearch, the search's group order. Browser (TestBrowser, about 18 min, 83 tests): the
workspace, sidebar state and deep links, search, read-only roles, the settings directory, the run review
and rollout default, every former `confirm()` path through DROOL's dialogs, inspectors from the
keyboard, network paging and filters, topology, a busy host's explanation (Install comes back by
itself), a Pack's Install updates, a service alert's link to the service, desired-state rows that open,
a file row's menu, and no hidden column at 1440 px on the widest pages; TestRestoreReconciliation
(restore e2e); integration TestGeoIPDownloads with a stand-in server (preparing without a warning,
available with the attribution, Check now, a restart without a new download, a broken update keeping
the databases, the operator's files untouched).

Qualification (final tree `a1c8145`): `gofmt` and `go vet` (also with the integration tag) clean,
generated code current, `go test ./...` and `make test-race` (CI's checks job) pass
(51 packages, no data race); the container integration suite
passes (36 tests, 365 with their subtests, none fail; 15 skip without their flags: the release-artifact
tests, the VM suites and the soak), TestBrowser 83 of 83 in 17 min 39 s. In the docs lab
(`ui-review-1.0-after`): every screen of the first review captured again in both themes and compared; the specification's acceptance scenarios A–H walked through by hand; the first
review's six task paths performed again and counted (restarting a service 8 → 7 interactions, the
others equal, adding a host with a Shepherd 19 → 20); every captured page checked for content behind a
sideways scroll at 1440 and 1280 px. The review found eleven problems in the UI, fixed in five commits
(`0e5eba9`, `9844286`, `751a332`, `85e108a`, `a1c8145`), and two test assumptions: a capture or test
waiting for a loading text to disappear before the loading began, and the search test assuming the
fixture's services were already reported when it runs first.


### 41.12 Final usability and accessibility pass

The second external review (Claude, ChatGPT, Gemini) accepted the redesign provisionally and asked for one narrowly
scoped pass before the release candidate: host status clarity, an accessibility audit, the firewall simulation and
topology, the restore and change previews, and a written design standard. No feature was added, and the navigation,
the host workspace, routes, APIs, policy semantics and safety checks are unchanged. Gemini's suggested enhancements
(multi-column sorting, chart controls, editor syntax highlighting, richer rule evaluation) were not taken up.

**Host states.** `hosts.status` is DROOL's connection and management readiness, from connection tests and the
Shepherd's session: *Ready* means DROOL can sign in and manage the host. Two things made it read as health:

- it sat in green next to the host's name, the first thing one reads, while a critical alert was open;
- the Hosts list's "Health" column showed the last status poll's reachability (online, offline).

Now:

- **The header.** The name stands alone, and a second line names each state: **Connection** (`HostStatusBadge`),
  **Last check** (`ReachabilityBadge`), **Health** and the open alerts. Health is `HealthBadge`, computed from the
  host's monitoring overview the same way as a fleet row: Healthy, Healthy · N notices, Degraded · N issues,
  Unhealthy · N issues, Stale or No data.
- **The Hosts list.** Health is monitoring's verdict, from the fleet summary as on the Monitoring page. Without
  `monitoring.read` the column is "Last check".
- **Severity in words.** On the Monitoring page it was only in an `aria-hidden` icon and its colour.

**The audit.** In the docs lab (the `ui-review-1.0-after` data), every main page (69 routes) was checked at 1440, 1280,
1024 and 390 px (390 px with touch emulation), in both themes. The checks were:

- axe-core 4.14: the WCAG 2.0, 2.1 and 2.2 A and AA rules, and best practices;
- the browser suite's own contrast check;
- content beyond the window, cut off or overlapping, and tables behind a sideways scroll;
- touch target sizes (390 px);
- focus visibility on every Tab stop (1440 px);
- in the last run, Tab stops nobody can see (inside a visually hidden box) at every width.

Eleven dialogs, inspectors and the search were also opened at every width and theme, and 47 scripted keyboard and
screen-reader checks ran per theme: menus, dialogs, inspectors and their steps, host navigation, titles, popovers, the
phone menu and a confirmed removal.

**Found and fixed:**

- **Titles and announcements.** Every page was titled "DROOL", and moving inside DROOL wasn't announced. Titles now come
  from the page's heading, the host's section and the selected view, and a polite live region names the new page
  (`PageTitle`).
- **Contrast.** In the light theme, accent text on the page background was 4.40:1 and accent badges 3.86:1. A badge
  inside a selected card was 3.44:1 (4.25:1 in the dark theme). The accent token changed from `#2f6fde` to `#2459bd`
  (light) and from `#6b9ff2` to `#7dabf4` (dark).
- **Links in text** were told apart by colour only (WCAG 1.4.1). They are now underlined.
- **Accessible names** didn't start with the visible text (WCAG 2.5.3), on monitoring tiles, the fleet's alert counts,
  Pack and health buttons, topology line labels, compact connection entries and the alert inspector's choices. Also
  fixed:
  - an `aria-label` on a `<p>` meant screen readers heard nothing of the inheritance legend;
  - selects and a textarea without names;
  - empty table headers and a table without a caption.
- **Keyboard.**
  - Three tab rows (Firewall, Shepherds, Network) and two smaller ones were ARIA tabs without arrow keys. They now use a
    shared `Tabs` component.
  - Menus had no name; they are now named by their button. Menu items showed focus only as a faint tint; they now show
    the outline.
  - Focus was lost when an inspector step opened or closed (an alert's Acknowledge…, a connection's Classify…). It now
    moves into the step and returns to the step's button.
  - Escape in the security and account popovers lost focus; it now returns to the button.
  - After a confirmed removal, focus fell back to the document; it now goes to the main content.
  - Monitoring's "Select all shown hosts" sat in the table header, which cards hide. On a phone, and at 1024 px once
    this pass made that list cards there, it was a Tab stop nobody could see. A header cell with a control now stays
    above the cards, labelled "Select all shown".
  - Topology lines were Tab stops with no visible focus, beside their labelled buttons; only the labels are Tab stops
    now.
  - The YAML editor showed no focus.
- **Layout.**
  - At 1024 px, nine list pages hid columns behind a sideways scroll: Hosts, Monitoring, resolved Alerts, Network
    Activity, Schedules, Audit, a host's Services, Files and Network. On Files that included each row's actions.
  - At 1280 px the audit log was 52 px too wide.
  - At 390 px the Files table was 627 px too wide; a schedule's history, the audit log and resolved alerts also
    overflowed.
  - The top bar wrapped at 1024 px.

  Now `lib/tableFit.ts` turns every `.rtable` into cards on a phone and wherever it doesn't fit. Connections and services
  switch to their compact lists instead (`useTableFits`). The Files and schedule-history tables became `rtable`s, long
  badges wrap, and card content can't widen its card. The audit log's columns are tighter, so it fits at 1280 px. Below
  1280 px the top bar's search is narrower and the role gives way.
- **Touch.** At 390 px, about 1,750 controls per theme were under 44 px (1,753 light, 1,760 dark): buttons 28–36,
  fields 36, tabs 30–38, navigation entries 32, menu items 36 and dialog close buttons 32. The designer's move arrows
  were 26 × 28 px, and the Firewall page's host checkbox was a bare 13 × 13 px box. On touch screens
  (`pointer: coarse`), buttons, fields, selects, tabs, menu items, navigation, checkbox labels, card titles and
  inspector links are now 44 px tall, and icon-only buttons are 44 × 44 px, in cards too. The Firewall checkbox has a
  label like the other lists. Text links in sentences and lists, and link-styled buttons inside text, keep their size
  (WCAG 2.5.8 inline and spacing exceptions).

**Firewall simulation and topology.**

- **"Can A reach B?"** said "Simulation" only in a footnote after asking, and its green YES could read as a live test.
  One sentence above the form now says it is a simulation from the policy DROOL wants that sends nothing, tries no
  connection and changes no policy. The answer line says "the policy allows it (simulated)", and the designer's box
  says "simulated on this draft: nothing is applied".
- **The topology.** With the side panel open the canvas narrows (about 770 px at 1440), and fitting the whole policy put
  it at about 0.4 (labels about 5 px). When a selection opens the panel and the whole policy would fit only below 0.75,
  the view now frames the selection and its neighbours. Fit, or closing the panel, frames the policy again.
- **Unchanged:** the canvas engine, the layout and the policy semantics.

**Previews.** Verified:

- the run review: operation, hosts, rollout, warnings, "Start run on N hosts";
- the restore plan: each correction Now → Will be per host and resource, steps, rollout, warnings, what isn't included
  and why, "Restore N resources";
- the baseline editor's impact: nothing on the hosts changes until a restore.

One ambiguity and one defect, in the restore dialog:

- **Ambiguity.** "Restore managed state on every host" over "Checking the hosts and planning the correction…" could
  read as a restore in progress. It now says "(nothing changes yet)" and "This is the plan: nothing changes until you
  choose Restore".
- **Defect.** The plan arrived, then vanished again for a second preview, because filling in the rollout changed the
  request. It is now one preview: the rollout is sent once someone changes it, and the plan stays in sight while a new
  one is worked out.

No confirmation dialogs were added.

**Examined and unchanged** (they met the requirements):

- dialogs' focus trap and focus return;
- inspectors' Escape and focus return;
- the skip link;
- the search (a combobox with an active descendant);
- the phone navigation drawer;
- chart patterns;
- tab rows that scroll sideways on a phone (an established pattern).

**The standard.** [DESIGN-STANDARD.md](DESIGN-STANDARD.md) writes the validated conventions down as rules:

- the principle;
- navigation and the host workspace;
- hierarchy;
- the building blocks and status words;
- the three interaction patterns and reviews before execution;
- responsive and accessibility requirements;
- terminology;
- honest state;
- a checklist for additions.

`CLAUDE.md` requires it for UI work.

**Tests.**

- **Web unit:** health words and worst severity, and the cards-or-table decision.
- **Browser:** `accessibility.test.mjs` covers host states in the header and the list, titles and the announcement, tab
  rows and menus from the keyboard, inspector step focus, popover focus, no table behind a sideways scroll and no Tab
  stop out of sight at 1024 px, 44 px touch targets (in the header, dialogs, cards, filter rows and through checkbox
  labels) and the simulation's wording. `health.test.mjs` follows the Pack button's new name. `monitoring.test.mjs`
  checks that an SD card is never called healthy in the storage section, not anywhere on the page: the header now
  names the host's Health, monitoring's verdict on the whole host.

Qualification (final tree `433ca4e`): typecheck clean, the web unit tests pass (131), `gofmt` and `go vet` (also with
the integration tag) clean, generated code current, `go test ./...` passes (51 packages; the only Go change is
TestBrowser's file list), and the browser suite passes: TestBrowser 91 of 91 in 19 min (its first full run failed once
on a test assumption the new header broke, fixed in `433ca4e`) and TestRestoreReconciliation. The accessibility audit's
last run, on the final code, found no text below AA contrast, no content cut off, overlapping or behind a sideways
scroll, no untitled page and no Tab stop out of sight at any width; what remains is listed in the pass's report: canvas
labels on the topology and text-sized links within WCAG 2.5.8's exceptions. The audit, its raw data and the screenshots
caught five regressions the pass itself had introduced, each fixed before qualification: Alerts tab labels overlapping
on a phone, Pack card titles squeezed, the audit log's Details column growing to its JSON width, the move arrows losing
their touch width in cards, and Monitoring's Select all hidden in the header at 1024 px. Branch CI and the tag
qualification need a push and a tag, which wait for authorization.

## 42. Before the release candidate: Shepherd updates and release prerequisites

### 42.1 License

The owner added `LICENSE` on GitHub (2026-10-17, `dec927a`): the FSF's GPLv3 text, byte for byte. That commit
broke `main`'s release job on purpose: `build.sh` refuses a source with `LICENSE` but no SPDX id, so a license
can't be shipped without being declared. `packaging/release/license` now says `GPL-3.0-only`: version 3, as chosen,
without the "or any later version" grant, which only the owner can add (`GPL-3.0-or-later`). The id goes into the
native packages (`license:`), the image's label and its index annotation (`org.opencontainers.image.licenses`), and
DROOL's own packages in the SBOMs (its Go module in each binary, the web UI's npm package and the binaries' directory;
`relkit sbom-normalize -license`; not the image's root, which holds the base image's packages too). `LICENSE` is in
the archives, the packages (`/usr/share/drool/LICENSE`), the image (`/usr/share/doc/drool/LICENSE`) and the release
directory. The README, the release notes template, the archives' and the package's notes say which license applies.
Tests: `relkit` (DROOL's own packages only, nothing without a license), `TestReleaseArtifacts` (DROOL's packages in
every SBOM declare the release's license), `TestContainerImage` (the label).

### 42.2 Shepherd updates: what was wrong

Checked against the code before anything changed (`agentsvc/updates.go`, `agent/update.go`, the API, the Updates tab
and the tests):

- **"Automatic" re-rolled uploaded releases; nothing discovered or downloaded anything.** The policy (default *manual*)
  picked the newest *uploaded* release in a free-text channel. A release existed only after an operator created a key
  offline (`drool release keygen`), registered it, made every host trust it (an install option or
  `sudo drool-agent trust-release-key` per host), unpacked each architecture's binary from the release, signed it and
  uploaded binary and manifest. Shepherds trusted only built-in keys (`release_keys.txt`, empty) and the host's own;
  M17 had decided "no project signature on Shepherd update manifests". So official releases could never be installed
  without that work, and updating the controller left every Shepherd on its old version.
- **The automatic policy had never run in a test.** No test set it.
- **Rollout logic that unattended updates would have exposed:**
  - an offline canary didn't block the batches (waves skip pending offline hosts), so a batch could start with no
    canary result; the canary was the host with the smallest random ID, possibly one already current (marked healthy
    "already running" at once), with the same effect;
  - a host-local refusal (`updates.policy: deny`, an untrusted key, a version that failed there before) was a failed
    update: with the default canary and failure limit of one, a single such host paused every rollout;
  - a version that failed on a host was refused there forever, even a person's retry, though a controller restart
    during the health window is enough to roll an update back;
  - after an automatic rollout to a version finished, hosts enrolled later or behind were never updated automatically;
  - the wave advance ran from the ticker and from report goroutines without a lock (a double offer was possible).
- **The page didn't say any of this.** It offered "Automatic: new releases are rolled out by themselves", a free-text
  channel, a release-key card and a binary upload as the main path, and nothing about whether anything was ever
  checked.

### 42.3 Official releases: trust and discovery

**Three trust domains**, none on a controller:

| Key | Signs | Trusted by |
|---|---|---|
| DROOL release key (OpenSSH) | a release's `SHA256SUMS` | people who download DROOL (`allowed_signers`) |
| DROOL Shepherd release key (new; `drool release keygen` format) | each Shepherd binary's update description | DROOL and every Shepherd: public half built in, `internal/release/official_keys.txt` |
| An operator's own release keys | their own Shepherd builds (custom releases) | the hosts that trust them locally (`/etc/drool/release-keys.d`) |

- `internal/release/official.go` embeds `official_keys.txt` (empty until the owner adds the key) and exposes it; the
  Shepherd's `TrustedReleaseKeys(official)` is the built-in keys unless agent.yaml says `updates.official: deny`
  (a new, strictly parsed setting), plus the host's own. Nothing the controller sends can change either. Test builds
  can add a key only with the `drooltestofficial` build tag and an `-X` the integration tests pass; without the tag
  the variable doesn't exist and the linker ignores the `-X`. `drool version --json`, `drool-agent version --json`
  and `release.json` list the built-in key ids; `drool-agent status` says whether official releases are trusted.
- **Shepherds follow the controller.** The official update is the Shepherd binary the controller ships with, when the
  controller is a stable or prerelease build. `agentsvc/official.go` checks the release source
  (`agent.update_source`, default `https://github.com/irnutsmurt/drool/releases`, `off` to disable): 30 s after start
  (1 s with `DROOL_TEST_UPDATE_TIMING=fast`), every 6 h, hourly while its own release isn't published or the source is
  unreachable, and on **Check now**. For each architecture it ships it fetches
  `download/<tag>/drool-agent_<v>_linux_<arch>.release.json` (64 KiB at most) and stores it only if the signature
  verifies against the built-in keys and it names exactly this version, channel and architecture, a protocol range
  this controller speaks, and the sha256 and size of the binary it shipped; the binary goes from disk into the
  artifact store, and the release is an ordinary one. Nothing executable is downloaded: the source decides only
  whether an update is available. Without internet access the same description is uploaded by hand (manifest only,
  `POST /agents/releases` with an empty body: accepted only for a binary the controller ships).
- **A newer DROOL** is read from `<source>/latest` without following the redirect (GitHub answers with the newest
  release's tag; a mirror may answer the tag as text), and reported only after its amd64 description verifies. It
  is never installed: the operator updates DROOL, and the Shepherds follow.
- **States** (`official.state`): `verified`, `not_published`, `rejected` (a description was found and failed: audit
  `agent.release.official_rejected`, a problem on the page, nothing stored), `unreachable`, `development` (a
  development build has no official release; nothing is fetched), `no_key` (no built-in key; nothing is fetched),
  `off`, `no_binaries`, `pending`. The result is kept in the setting `agent_release_check`.
- **Shepherds before 1.0** have no built-in key and don't report their trust: they're offered official releases, and
  when they refuse (an untrusted key) they're skipped with the reason. Updating them once with a custom release, or
  reinstalling them, brings them onto the official key.

### 42.4 Rollouts

- **Who is in a rollout**: active Shepherds behind its version (none already current), on a platform with a release,
  that can take it. `Blocker()` says why one can't, before anything is offered: no release for its platform; updates
  turned off on the host; it doesn't trust the key (the host refuses official releases, it was built without the
  project key, or a custom key it doesn't have); `min_upgrade_from`; a protocol it doesn't speak; the version failed
  there before (unless retried). A person's rollout lists such hosts as *skipped* with the reason; an automatic one
  leaves them out and the status page names them. A rollout that a person starts for hosts in another running or
  paused update is refused with which and why.
- **Order and the canary**: connected Shepherds that report their trust first, then other connected ones, then offline
  ones, by name. Wave 0 must hold `canary` hosts that take the update (healthy, in progress, or connected and pending);
  short of that, the next connected pending host of a later wave joins wave 0, so batches never start without a canary
  result.
- **Refusals are skips.** 1.0 Shepherds send `UpdateReport.refusal` (`untrusted_key`, `updates_denied`,
  `failed_before`, `downgrade`, `upgrade_path`, `protocol`, `platform`, `busy`); older ones are known by the words they
  have always used, and only before they trust the manifest. A refusal marks the host *skipped* (never a failure, never
  a pause) and its identity *refused*. A bad signature or a corrupted download is still a failure. 1.0 Shepherds report
  their release trust in the hello (`ReleaseTrust`: key ids, official, official denied, updates denied), kept in
  `agent_identities.release_trust_json` (migration 0026).
- **Try again**: `POST /agents/rollouts` with `retry` sets `agent_rollouts.retry`; its offers carry `UpdateOffer.retry`,
  and only then does a Shepherd install a version that failed on it before.
- **The automatic policy** keeps Shepherds on the newest release in its channel (prerelease includes stable; any other
  name is exact) that the controller speaks, or the exact version. It never fights a person: a paused or cancelled
  rollout to the target stops it for that version (a cancel's reason now says who), and hosts in someone's running
  rollout are left alone. Hosts behind the target that no rollout covers, and that an automatic rollout didn't already
  try, get a rollout of their own (new hosts, returning ones). An older automatic rollout still running or paused is
  cancelled as *superseded by the update to …* when a newer target appears.
- Waves and the policy run under one lock (`rollMu`); a verified official release triggers a pass at once.

### 42.5 Policy, defaults, migration 0026

- **Default**: automatic, stable, canary 1, batches of 5, pause after 1 failure, 120 s health window. Channels offered:
  `stable` and `prerelease` (what the release pipeline stamps); the API still takes another lower-case name for custom
  releases, and validates the exact version.
- **Kept preferences**: a saved policy row is never changed. Migration 0026 writes `manual` for an installation that
  has agent releases or update rollouts but never saved a policy: it was updating by hand. It also adds
  `agent_identities.release_trust_json` and `agent_rollouts.retry`. The schema upgrade test counts the extra setting
  row; `TestShepherdUpdatePolicyMigration` covers never used, uploaded, rolled out, saved automatic and install
  rollouts only.

### 42.6 API and the Updates page

- `GET /agents/updates/status` (agents.read): policy and whether it was saved, channels, state (`none`, `current`,
  `available`, `updating`, `attention`, `blocked`) with a one-line summary, the target, the controller's version,
  the official state, the check (enabled, source, checking, last success, last error, next), a newer DROOL, counts
  (current, to update, updating, waiting for an offline host, can't update, offline), the hosts that can't update and
  why (with a retry version where the update failed), problems (paused, failed, rolled back, verification), running
  rollouts and the last eight. `POST /agents/updates/check` (agents.manage) starts a check. Releases say `official`
  and `shepherds_behind`; rollouts `retry`; identities `release_trust`.
- **Shepherds → Updates** (`components/agents/Updates.tsx`), ordered by DESIGN-STANDARD §3: status in a word and a
  sentence; a newer DROOL; a notice when official updates can't work here (no key, development build, not published,
  unreachable, off, rejected), next to the settings; **Update Shepherds automatically**; **Update channel** (Stable
  (recommended), Release candidates); **Last checked** with **Check now**; counts; **Update N Shepherds to …** when
  automatic updates are off. **Needs attention** lists paused updates (Resume, Cancel…), failed and rolled-back hosts
  (Try again…) and verification failures, then the hosts that can't update and why. **Updating now** and **Recent
  updates** count each rollout and list its hosts. **Advanced settings** (`?advanced=1`): the rollout numbers and an
  exact version, custom releases with their keys and upload (manifest-only accepted), and the official release source
  with the built-in key ids. Saving a setting asks for no confirmation; starting, cancelling and retrying an update do.
  A setting keeps the keyboard focus while it saves: it stays enabled and shows the choice being saved.
  A viewer gets the same page read-only. Bulk installs moved to **Shepherds → Installing Shepherds**; the conversion
  dialog's stale "Agents → Rollouts" link went with them.

### 42.7 Release engineering

- `sign.sh RELEASE_DIR RELEASE_KEY SHEPHERD_KEY` signs both Shepherd binaries (from the release's own archives, with its
  version and channel, through the release's own `drool release sign`) into
  `drool-agent_<v>_linux_<arch>.release.json`, after checking that the Shepherd key is among `release.json`'s
  `official_release_keys` (a warning for development builds; `OFFICIAL_RELEASE_KEYS` replaces the list in tests), and
  only then writes `SHA256SUMS.sig`. `TestReleaseSigning` covers the refusal and the descriptions.
- `build.sh` refuses a stable build without a Shepherd release key in `official_keys.txt` (Shepherds built without it
  could never take an official update) and with the placeholder maintainer `maintainers@drool.invalid`. Since the
  readiness pass it refuses a release candidate the same way, and without `LICENSE`: the stable tag goes on the final
  candidate's commit, so a candidate without them could never become the release (and `sign.sh` already refused to
  sign its Shepherds). Development builds only warn. Shown by building tags `v1.0.0-rc.3` and `v1.0.0` in a
  throwaway clone. `TestReleaseArtifacts` checks that the controller, the Shepherds and `release.json` name the
  same built-in keys, and at least one for a candidate or stable release.
- The workflows name their runner image (`ubuntu-24.04`): GitHub moves `ubuntu-latest` to Ubuntu 26 from 2026-10-19,
  and a qualification must not change runner image halfway (§42.1's license fix restored `main`'s release job).

### 42.8 Workflow audit

The primary workflows, asked: what is the user trying to do, what should DROOL do for them, which decisions are
theirs, are internals exposed as routine controls, does the default path work without the user guide, are dangers and
unknowns honest, are settings where people look for them.

| Workflow | Finding | Done |
|---|---|---|
| Shepherd updates and trust | Updates didn't work by default; the page was built around keys, manifests and rollout numbers (§42.2) | §42.3–42.6 |
| Shepherd installation | The install dialog asked, as a routine field, for a release-signing key to trust: with official updates, only custom builds need one | Under *Advanced: also trust your own release-signing key*, with a line saying updates need nothing else |
| Shepherd installation (bulk) | Convert to Shepherd asked for canary and batch sizes up front | The rollout in words (*One host first, then 5 at a time; it stops at the first failure*), the numbers behind **Change the rollout…** |
| Firewall | A host's mode form put the rollback timer's seconds beside the mode, and outbound choices as raw values (`allow`, `restrict`) | Outbound in words (*Allow (recommended)*, *Restrict to what policies allow*); the timer under *Advanced: rollback timeout (120 s, default)* |
| First run, adding a host | The recommended path (DROOL's own account) comes first and its name is prefilled; *Connect through* (a jump host) and *DROOL's account name* could move under Advanced | After 1.0 |
| Monitoring and alerts | Service monitoring is opt-in, routing and delivery history are under Advanced (§40, §41) | — |
| Runs, files, schedules | The run review summarises the rollout; its numbers are under Advanced options; a choice is kept (§41) | — |
| Desired State | The restore plan reviews before anything changes; automatic restore is under Advanced (§41) | — |
| Shepherds list | Shows the protocol number and certificate dates beside each version: detail most people don't need | After 1.0 |
| Keyboard focus, every page | About 80 buttons and fields are disabled while their own request runs (`disabled={….isPending}`). Chromium then moves the focus to the top of the document when the request takes longer than a frame. The browser test found it on the new Updates page | Fixed there: the two settings and **Check now** stay enabled and show the choice being saved (DESIGN-STANDARD §6). The other places after 1.0, for example with `watchFocusLoss` also returning the focus from a control disabled under it |

### 42.9 Tests

- **Unit:** `release` (official keys, test-key injection only under the build tag); `agent` (refusal codes, retry of a
  failed version, official trust allowed and denied, the hello's trust report, no claim without a built-in key);
  `agentcaps` (`updates.official`); `agentsvc` (refusals by code and by older Shepherds' words, real failures never
  taken for one; channels; `Blocker` for every reason, retry); `store` (migration 0026 preserving, the upgrade test's
  extra row); `relkit` (DROOL's license on its own SBOM packages only).
- **In process** (`internal/api/shepherd_updates_test.go`, real gRPC Shepherds that verify, download and stage what
  they're offered; only the helper that would switch this machine's Shepherd is replaced): defaults and validation;
  the official check against a release source the test controls (not published, verified and stored, a newer
  release reported, a forged newer release ignored and said); every rejection (another key, a tampered description,
  another version, another channel, another binary, an incompatible protocol, not a description) storing nothing and
  showing a problem; no request at all for a development build, without a key, or turned off; unreachable; the whole
  automatic path (canary first, batch only after it is healthy, an offline host catching up, a host refusing official
  releases left out and explained, no repeat); a canary rollback pausing the rest, Try again; a saved manual policy
  kept, Update now, nothing to do once current; a release candidate on Stable and on Release candidates; a manifest
  uploaded without its binary; RBAC for viewers and operators.
- **Integration, real binaries in containers:** `TestOfficialShepherdUpdates` (test builds that trust a key made for
  the run, a local stand-in for the release page): install DROOL v1.4.0 and three Shepherds, publish v1.5.0, update
  DROOL — the two Shepherds that trust official releases update by themselves, canary then batch, the version links
  say so, the third is left alone with its reason, nothing was uploaded or registered (audit). `TestAgentUpdates`
  now expects an untrusted key and `updates.policy: deny` to be refused up front with the reason. Every test that
  uploads releases of its own turns automatic updates off first, and the upload helper refuses otherwise.
- **Release:** `TestReleaseSigning` (the Shepherd key refused when it isn't built into a release, the descriptions
  verified against it and naming the release's channel), `TestReleaseArtifacts` (the same built-in keys in the
  controller, the Shepherds and `release.json`), `TestContainerImage` (the license label).
- **Browser:** `shepherd-updates` in TestBrowser covers:
  - the first screen, without the expert words;
  - honesty without a key;
  - the two settings saved without a confirmation;
  - with the save held back, the checkbox keeping the focus and showing the choice;
  - Advanced settings from the keyboard;
  - a viewer's read-only page;
  - the install dialog without a key field.

  The contrast test covers the page with Advanced open, and `monitoring` follows the conversion dialog. The screenshot
  lab (not committed) walked a fleet through every state at 1440, 1280, 1024 and 390 px in both themes, with no
  sideways scroll and no page errors.
- **Results** (2026-10-17):
  - `go test ./...`: 51 packages pass.
  - `make test-race` on a quiet machine: 51 packages pass. An earlier run beside the screenshot lab timed out once in
    `agentmon` (`TestEngineProducesPointsStateAndServiceEvents`, unchanged since M14; it passes alone).
  - Web: typecheck, 131 unit tests.
  - The release built from `9e6cd55` (a development build: it warns about the placeholder maintainer and the missing
    Shepherd key). Against it, all pass (441 s): `TestReleaseArtifacts`, `TestReleaseSigning`, `TestContainerImage`,
    `TestReleasePackages` (10 distributions), `TestReleaseUpgrade` (alpha, M16), `TestMixedVersionShepherds`,
    `TestAgentUpdates` and `TestOfficialShepherdUpdates`.
  - TestBrowser: 94 of 94, plus both enrichment phases, in 19.6 min. The first full run failed two `shepherd-updates`
    checks: a real focus loss (fixed in `d7ee57a`, §42.8) and a visibility check in the test.
  - CI's release job now also runs `TestOfficialShepherdUpdates`.

### 42.10 What the owner still decides

- **The DROOL Shepherd release key**: created offline (`drool release keygen`), its public line committed to
  `internal/release/official_keys.txt` before the release candidate. Until then every build says official releases
  can't be verified, and a candidate or stable build refuses. Its private half signs each release's Shepherd
  descriptions (`sign.sh`).
- **The DROOL release key** (OpenSSH), its public half in `packaging/keys/allowed_signers` (unchanged).
- **The packages' maintainer**: a real contact, committed as the default in `build.sh` (the release workflow doesn't
  set `DROOL_MAINTAINER`); a candidate or stable build refuses the placeholder.
- **GPL-3.0-only or -or-later**: `LICENSE` is the FSF's GPLv3 text; "only" was taken as the literal reading.
- Pre-1.0 Shepherds (development builds, the dogfooding fleet) can't take official updates: one custom release or a
  reinstall each, once.
