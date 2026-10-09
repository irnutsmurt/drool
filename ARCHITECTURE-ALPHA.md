# DROOL Technical Alpha: Internal Architecture

**v0.13 · 2026-10-09 (v0.1.0-alpha + M6 + M7 + M8 + M9 + M10 + M11 + M12 + M12.1 + M13 + M14)** · Implements the *Technical Alpha* slice of `DESIGN.md` (§33), M6 file management and single-file deployment (DESIGN §13), M7, the DROOL agent and its lifecycle (DESIGN §4, §6, §8, §9, §28; §23 here), M8, firewall policy and safe enforcement on plain-nftables hosts (DESIGN §15–19; §24 here), M9, the Firewall Designer and an application-wide Light/Dark/System theme (§25 here), M10, network connectivity monitoring and enrichment (DESIGN §19.1, §19.4; §26 here), M11, system monitoring, service inventory and hardware health (DESIGN §11.3; §27 here), M12, alerts, notifications and maintenance (DESIGN §11.4, §12.13, §22.8; §28 here), M12.1, monitoring health overrides (DESIGN §11.3; §29 here), M13, scheduling, fleet automation and workflows (DESIGN §12.4, §12.6, §12.13, §26; §30 here), and M14, desired state, drift and reconciliation (DESIGN §14; §31 here).

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
- **M12.1:** sparse, inherited health overrides for M11's evaluator at global, group, host and subject scope with explicit group priorities and visible conflicts, stable subject identities, server-side provenance for every effective value, an impact preview, optional high-GPU-utilization health (off by default), truthful issue resolution reasons that M12 reports as "cleared", never "recovered" (§29)

### Out of scope (but the design must not block them)
Docker/container firewalling, WireGuard identity networking, templated files and directory sync, MFA, plugins, multi-hop jumps. (YAML editing of the firewall policy was added in M9, §25.) For network monitoring (M10): automatic or geographic blocking, threat scores, threat-intelligence feeds and IP reputation, packet capture and payload inspection, IDS signatures, SIEM features, alerting beyond new-destination visibility, eBPF collection. For system monitoring (M11): dashboard designers and query languages, external time-series databases, SMART self-tests, GPU driver installation, log collection, Windows hosts (a deferred future agent; DESIGN §2.6). For alerting (M12): on-call rotations, escalation trees and paging, incident grouping and correlation, anomaly or baseline thresholds, rule expressions, ticketing, SLA/MTTR reporting, automatic remediation, log alerting, notification plugins or executable hooks (recurring maintenance windows arrived with M13). For health overrides (M12.1): rule expressions or a monitoring language, anomaly or ML baselines, per-group subject selectors, GPU driver/Xid error collection, scheduled overrides. For scheduling (M13): DAGs, branching or a workflow language, approval pipelines, job-result notification routing (PagerDuty and the like), dynamic groups, scheduled firewall changes (firewall policy is applied from the Firewall page), cron syntax. For desired state (M14): compliance expressions and benchmarks (CIS, STIG), a DSL, remediation scripts, DAGs, package version policy, a package-registry abstraction, firewall desired state outside the Firewall (and Docker firewalling), Proxmox/Synology resources, SIEM, dynamic groups, Windows, plugins.

### Supported managed hosts
Debian 12+, Ubuntu 22.04+, RHEL/Rocky/Alma 9+, current Fedora (dnf5). The integration matrix (§18) runs Debian 12 and 13, Ubuntu 24.04 and 26.04, Rocky 9 and 10, and Fedora 44.
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

- **One process, one binary.** Subcommands: `serve`, `migrate`, `user create`, `backup`, `restore`, `version`.
- Runs as an unprivileged `drool` user. It never needs local root.
- Data directory: `/var/lib/drool/` (`drool.db`, `master.key`, `tls/`, `logs/`, `backups/`, `drool.lock`). `serve` holds `drool.lock` (flock) for its lifetime so a restore can't replace files under a running controller. Database files are created owner-only (0600) whatever the umask.
- Packaging: static binary with a systemd unit, plus a container image (distroless; data on a volume).
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
test/integration/             distro matrix, fault injection, VM tests (Docker CLI; see §18)
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
- **Master key:** 32 random bytes in `/var/lib/drool/master.key` (0600, created on first start), or from `DROOL_MASTER_KEY_FILE`. `key_version` allows rotation (`drool secrets rotate-key` re-encrypts all rows).
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

| Page | Contents |
|---|---|
| Setup | One-time first-admin claim: setup code, username, password, confirm (§12.5) |
| Login | — |
| Dashboard | Host counts by status; updates pending / security; reboot required; failed units; running and recent jobs; event feed |
| Hosts | Filterable table (name, status, OS, address, updates, reboot, last seen, tags) |
| Add host (wizard) | 1) address, port, user, jump host · 2) credential (shows controller public key + copy snippet) · 3) **host key scan → compare fingerprint → approve** · 4) probe with step-by-step results · 5) discover |
| Host detail | Facts, addresses, status, pinned key (+ rotate flow), tags and groups, job history, actions |
| Groups | CRUD, membership |
| Files (M6) | Per host: current path and breadcrumbs, go-to-path, **Use administrator privileges** toggle with a warning banner, table (name/type, size, owner:group, `rw-r--r--` + `0644` with a plain-language tooltip, modified), upload with progress and cancel, download, new folder, rename/move, delete (one confirmation), permissions editor (checkboxes + octal + description), owner/group; text editor with conflict handling |
| Deploy file (M6) | New-job kind: upload artifact, destination, overwrite policy, owner/group/mode, elevation, validation, service action; per-host preview with diffs; results with before/after, validation, separate service result and Roll back; host page lists files deployed by DROOL |
| New job | Command / Script / Packages / System update / Service / Reboot forms, "Preview changes" for package operations, targets with a live preview (skips shown), rollout settings, **one confirmation stating the target count and the consequence** (removals, reboots). Host pages link here pre-filled (Install updates, Reboot, Restart a service) |
| Job detail | Summary bar (succeeded / failed / skipped / ...), per-host table updating live, host drawer with step results, stdout/stderr tabs, truncation notice, error class explanation and retry hint; Cancel / Resume / Re-run failed |
| Credentials | List (metadata), add SSH key / password / sudo password, controller public key |
| Audit | Filterable event list |
| Settings | Users, API tokens, system info including config value sources, retention |

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
| Integration (`make test-integration`) | The real `drool` binary driven over its REST API against unprivileged containers (sshd only): Debian 12/13, Ubuntu 24.04/26.04, Rocky 9/10, Fedora 44. Connection test, discovery, commands and scripts, passwordless/password/wrong-password sudo, package operations and their errors, dpkg lock, interrupted dpkg, system updates, jump host (key approval, operations, cancellation, changed jump key) |
| Fault injection | Host down before the operation, connection lost during it, cancellation of root processes, output beyond the cap, controller crash mid-job, authentication failure, changed host key |
| Files (M6) | Unit: path/mode/diff helpers, artifact store (integrity, GC), filedeploy sequence with a fake FS, SFTP and elevated scripts against the in-process SSH server (atomic writes, zero bytes, 24 MiB, cancel and broken uploads, conflicts, symlinks, unsafe directories, validation). Integration on Debian and Rocky containers: browse, upload/download incl. zero-byte and 256 MiB with a controller memory check, cancelled upload and killed host mid-upload (destination unchanged), mkdir/rename/move/delete/chmod, symlink refusal, permission denied, NOPASSWD/password/wrong-password sudo, unsafe directory, text-edit conflict, jump host, changed host key, audit without content; deployment by tag and group with mixed success, no-op, preview states, validation pass/fail, rollback (content and metadata, refused after a later change, removal of a created file), service failure after install. VM: deployment + sshd reload and unit restart on real systemd |
| Scheduling (M13) | Injected clock against the real engine and real maintenance (DST, durability with crash hooks, missed/late/overlap, ownership, windows); real-process `TestSchedulerRestart` (SIGKILL mid-burst, an outage across due times); opt-in `TestSchedulerScale` (§30.14) |
| Desired state (M14) | An in-memory fleet (units, packages, files, failures) behind the real engine, filedeploy and maintenance with an injected clock (§31.13); API tests over real SSH; container `TestDesiredState` (APT and DNF); `TestVMDesired` on a Debian TCG VM (real systemd, packages and files over SSH and the agent); opt-in `TestDesiredScale` (§31.14) |
| systemd and reboots (`-vm`) | Cloud-image VMs under qemu with software emulation (TCG) inside an unprivileged container (no `--privileged`, no `/dev/kvm`): real service operations and their errors, reboots (direct, through a jump host, onto a changed host key), reboot-required after a kernel install, needrestart, cancelling a live transaction, unattended-upgrades contention |

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

States per host: current, update available, staged, updating, healthy, rolled back, failed, offline. Policy: manual or automatic (channel or exact version), canary size, batch size, max failures, health window. Release upload requires a registered release key; `min_upgrade_from` and version comparison refuse unsupported downgrades.

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

`GET /monitoring/health/checks`; `GET /monitoring/health/global`, `/groups`, `/groups/{id}`, `/hosts/{id}/monitoring/health[?check&subject]` return a `HealthView`: per check, every field with its effective value and source, this scope's own value (`own`) and what it inherits (`inherited`, what "Reset to inherited" restores), conflicts, and for hosts a summary ("Using recommended defaults", "3 inherited group overrides, 2 host overrides"), the host's groups and its subjects. `PUT /monitoring/health/overrides` sets one key's complete sparse field set (empty removes it); `DELETE /monitoring/health/overrides/{id}`; `PUT /monitoring/health/groups/{id}/priority`; `POST /monitoring/health/preview` computes affected hosts, how many are above the previous and the new warning level now, which active issues would close or stay — for a field change or a priority change — without changing anything. Lists are bounded (overrides ≤1000 in responses, preview hosts ≤50).

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

`Resource{kind, name | path, state, enabled, artifact, owner, group, mode, elevated, validate, service}`: services (`.service` units; `state` running/stopped/unmanaged and/or `enabled`), packages (installed/absent), files (artifact SHA-256 required; owner/group/mode optional; validation, reload/restart). `Normalize` validates and canonicalizes; `Same` compares canonical forms; `Want`/`Describe` give the words; `Corrective(allowRemovals)` returns the typed operation that restores it. `Snapshot.Resolve(host)` walks the levels (host declaration → assigned → groups by priority → global) and returns `HostState{Resources: key → Effective{Resource | Conflict, Source{level, label, baselines, groups, competing, shadowed, auto}}, Hash}`; the labels ("Required by Global baseline …", "Inherited from group: … (baseline …)", "Baseline …, assigned to this host", "Custom for this host", "Not managed on this host", "Conflict between groups a and b (same priority 0)") are produced here and only here.

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

`/desired/summary` (counts by status for hosts and resources, automatic restore paused, firewall and agent state-version counts when readable), `/desired/hosts` (paged list with status filter), `/desired/hosts/{id}` (effective state with provenance, observations, diffs, groups, baselines, firewall line), `…/resource?key=` (one resource with its history), `…/review?key=` (files: state, metadata, diff when allowed), `…/refresh`, `…/accept`, `…/overrides` (PUT, DELETE); `/desired/refresh`; `/desired/restore/preview` (plan + `missing_permissions`) and `/desired/restore` (with `plan_hash`; 409 when the plan changed); `/desired/history` (filters host, key, baseline, kind; paged); `/desired/baselines` (list, create), `/desired/baselines/preview`, `/desired/baselines/{id}` (get with revisions and counts, update, archive), `…/revisions/{rev}`, `…/auto`; `/desired/groups`, `/desired/groups/{id}/priority` and its preview. Jobs gain `origin.reconcile_id`/`reconcile_kind`/`baseline_id`/`baseline_name`, job hosts `steps`, and `/jobs/preview` `desired_warnings`. Permissions in §12.2; route-level checks in `TestAuthzMatrix`. Audited: `desired.baseline.create`/`update`/`archive`, `desired.group_priority`, `desired.accept`, `desired.override.set`/`remove`, `desired.refresh`, `desired.review`, `desired.restore` (+ `job.create`), `desired.auto`, `desired.auto_paused`.

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
