# DROOL Technical Alpha: Internal Architecture

**v0.7 · 2026-10-07 (v0.1.0-alpha + M6 + M7 + M8 + M9)** · Implements the *Technical Alpha* slice of `DESIGN.md` (§33), M6 file management and single-file deployment (DESIGN §13), M7, the DROOL agent and its lifecycle (DESIGN §4, §6, §8, §9, §28; §23 here), M8, firewall policy and safe enforcement on plain-nftables hosts (DESIGN §15–19; §24 here), and M9, the Firewall Designer and an application-wide Light/Dark/System theme (§25 here).

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

### Out of scope (but the design must not block them)
Docker/container firewalling, WireGuard identity networking, batch/canary rollouts, scheduling, templated files and directory sync, desired state, YAML editing, MFA, plugins, multi-hop jumps.

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
│                       (Agent Driver: Alpha 2)                                 │
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
- **Agent driver (Alpha 2):** forwards the same typed operation over gRPC. The agent implements it natively and enforces its capability ceiling.

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
  "concurrency": 10,
  "failure_threshold": 3,      // stop starting new hosts after N failures (null = never)
  "batch_size": null,          // reserved (1.0)
  "canary": null               // reserved (1.0)
}
```

Target semantics: union of `hosts ∪ groups ∪ tag matches`, minus `exclude_hosts`. Each tag entry matches hosts having that tag (OR across entries). AND queries come with dynamic groups later. **Targets are resolved once at job start, and the resolved host list is stored with the job.** A job never retargets mid-run.

### 5.3 Job

```jsonc
{
  "name": "Update web",
  "steps": [ { "type": "system_update", "scope": "all", "reboot": "if_required" } ],
  "targets": { ... },
  "strategy": { ... }
}
```

`steps` run sequentially per host, and a failed step stops that host. The UI builds 1–2 steps in the alpha. The model allows more.

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
  circuit breaker / failure_threshold may pause the job: no new hosts start,
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

### 11.5 Circuit breaker
Per job: **5 consecutive** host failures with the same class among {Authentication, HostKey, Authorization} → the job becomes `paused` with a reason ("5 consecutive authentication failures; likely a shared credential problem"). `POST /jobs/{id}/resume` or `/cancel`.

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
| YAML | Canonical Go types + generated JSON Schema |
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
