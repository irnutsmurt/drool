# DROOL Technical Alpha: Internal Architecture

**v0.2 · 2026-10-06** · Implements the *Technical Alpha* slice of `DESIGN.md` v0.3 (§33).

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

### Out of scope (but the design must not block them)
Agent, firewall, batch/canary rollouts, scheduling, file deployment, desired state, YAML editing, MFA, plugins, multi-hop jumps.

### Supported managed hosts
Debian 12+, Ubuntu 22.04+, RHEL/Rocky/Alma 9+, current Fedora (dnf5).
Requirements on the host: `sshd`, POSIX `sh`, coreutils, systemd, and `sudo` if the login user isn't root. **No Python required.**

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
- Data directory: `/var/lib/drool/` (`drool.db`, `master.key`, `ssh/`, `tls/`, `logs/`).
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
test/integration/             testcontainers distro matrix, fault injection
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
{ "type": "package", "action": "install" | "remove" | "upgrade", "names": ["htop"] }

// system_update
{ "type": "system_update",
  "mode": "upgrade" | "full_upgrade" | "security",
  "allow_removals": false,       // required true for unattended full_upgrade that removes packages
  "refresh": true,
  "reboot": "never" | "if_required" | "always" }

// service (idempotent states; restarted/reloaded are inherently imperative)
{ "type": "service", "name": "nginx",
  "state": "running" | "stopped" | "restarted" | "reloaded" | null,
  "enabled": true | false | null }

// reboot
{ "type": "reboot", "wait": true, "timeout_s": 900 }

// discover (inventory facts)
{ "type": "discover" }
```

Each operation type declares static metadata used by the engine:

| Op | Lock | Idempotent | Requires become | Default timeout |
|---|---|---|---|---|
| command / script | exclusive (shared if `read_only`) | no | per spec | 300 s / 600 s |
| package | exclusive | yes | yes | 1800 s |
| system_update | exclusive | yes | yes | 3600 s |
| service | exclusive | yes (except restarted/reloaded) | yes | 120 s |
| reboot | exclusive | no | yes | 900 s |
| discover | shared | yes | no (some facts better with) | 120 s |

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

```go
type PackageBackend interface {
    Refresh() Script
    Install(names []string) Script
    Remove(names []string) Script
    Upgrade(names []string) Script
    UpgradeAll(scope Scope) (Script, error)   // scope: all | security
    ListUpdates() Script                      // → parsed []Update{name, from, to, security}
    RebootRequired() Script
    ParseError(stderr string, exit int) *model.OpError
}
```

| | APT (Debian/Ubuntu) | DNF (RHEL 9+/Fedora, dnf4 & dnf5) |
|---|---|---|
| Refresh | `apt-get update` | `dnf makecache` |
| Install | `apt-get install -y --no-install-recommends?` (setting) | `dnf install -y` |
| Remove | `apt-get remove -y` | `dnf remove -y` |
| `mode: upgrade` | `apt-get upgrade -y` (never removes; may hold back) | `dnf upgrade -y` |
| `mode: full_upgrade` | `apt-get full-upgrade -y` (may remove/replace packages; see below) | same as `upgrade` (dnf has no distinction) |
| `mode: security` | **Not supported in alpha** → `ValidationError` (decision D4) | `dnf upgrade --security -y` where updateinfo exists; otherwise `ValidationError` with an explanation |
| List updates | `apt list --upgradable` (parsed; origin decides `security`) | `dnf check-update` / `dnf updateinfo list --security` |
| Reboot required | `/var/run/reboot-required` exists | `needs-restarting -r` (dnf4) / `dnf needs-restarting -r` (dnf5); fallback: compare running kernel with newest installed |
| Lock detection | `Could not get lock` / `dpkg frontend lock` | `Waiting for process with pid` / dnf lock messages |

- **APT full upgrade removals (decision D3):** before running, the backend simulates (`apt-get -s full-upgrade`) and parses `Remv` lines. If any packages would be removed and `allow_removals` is false, the host fails with `ValidationError{retryable: false}` listing them, and nothing is changed. `POST /jobs/preview` runs the same simulation so the UI can show removals and require explicit approval, which sets `allow_removals: true`.
- **Security updates on APT are detected and displayed only:** `ListUpdates` marks packages whose candidate comes from a `*-security` origin, and these feed the `security_updates` count. We don't execute origin-based security-only patching in the alpha.
- Lock contention → `PackageManagerError{retryable: true, detail: "locked by <process>"}`. The engine retries with backoff, up to 5 attempts over about 10 min.
- `Changed` is computed by comparing package versions before and after for the named packages (or from the upgraded-package list).
- dpkg interrupted state (`dpkg was interrupted`) → `PackageManagerError{retryable: false}`, with the explanation "run `dpkg --configure -a`". The alpha does **not** auto-repair.

### 10.3 service

Reads `systemctl show -p LoadState,ActiveState,SubState,UnitFileState <unit>`.

- `LoadState=not-found` → `ServiceError`.
- Applies only the transitions needed (start/stop/enable/disable). `restarted` and `reloaded` always act.
- Re-reads the state afterward and returns `Changed` plus the before and after states.

### 10.4 reboot

1. Record the current `boot_id`.
2. Run `systemd-run --on-active=2 systemctl reboot` (falling back to `nohup sh -c 'sleep 2; systemctl reboot'`), so the SSH command returns cleanly.
3. Evict the pooled connection. State becomes `rebooting`, then `waiting_host`.
4. Poll (exponential backoff, 5 s → 30 s) until SSH works **and the host key still matches** **and `boot_id` has changed**. Only a changed `boot_id` proves the reboot actually happened.
5. Timeout → `TimeoutError`, and the host stays in `waiting_host` until the poller sees it.

`system_update` with `reboot: if_required` runs RebootRequired after the upgrade and then does the above.

### 10.5 command / script

- `when` conditions are evaluated against stored facts **before** connecting. A mismatch → `skipped`, with the reason recorded.
- OS-specific bodies (`body: {debian: ..., redhat: ...}`) pick the body by `os_family`. No match → `skipped`.
- Never retried on `ExecutionError`. Retried only on `ConnectionError` raised **before** the command started.

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

| Error class | Retried? |
|---|---|
| ConnectionError (before op started) | yes |
| ConnectionError (mid-op) | only if op is idempotent |
| HostKeyError, AuthenticationError, AuthorizationError | **never** |
| PackageManagerError (locked) | yes, longer schedule |
| TimeoutError | only if op is idempotent |
| ExecutionError, ValidationError, ServiceError | never |

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
- Every **6 h** (and after any package job): update counts (ListUpdates **without** refreshing; a refresh happens once a day).
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
  GET               /hosts/{id}/facts
  GET               /hosts/{id}/status
  GET               /hosts/{id}/jobs

Groups
  GET|POST          /groups
  GET|PATCH|DELETE  /groups/{id}                       PATCH {host_ids} replaces members

Jobs
  POST   /jobs/preview               spec → resolved targets, skips (when), validation errors
  POST   /jobs                       spec → job
  GET    /jobs                       ?status= &host= &created_by=
  GET    /jobs/{id}                  job + summary counts per state
  GET    /jobs/{id}/hosts            per-host state
  GET    /jobs/{id}/hosts/{hid}      steps + results
  GET    /jobs/{id}/hosts/{hid}/output?step=&stream=     (text/plain, ranged)
  GET    /jobs/{id}/events           SSE
  POST   /jobs/{id}/cancel
  POST   /jobs/{id}/resume
  POST   /jobs/{id}/rerun            {hosts: "failed" | "interrupted" | [ids]}

Audit / system
  GET    /audit                      ?actor= &action= &target= &since=
  GET    /schema                     JSON Schema for canonical model
  GET    /system/info                version, schema version, config sources
GET /health  GET /ready  GET /metrics   (unversioned)
```

`api/openapi.yaml` is the contract. The UI client is generated from it (`openapi-typescript`), and CI fails if the handlers and the spec drift apart (route/schema conformance test).

---

## 14. Web UI

**Stack:** React + TypeScript + Vite, TanStack Query (server state), React Router, shadcn/ui + Tailwind, generated API types. Built into `web/dist` and embedded with `go:embed`.

| Page | Contents |
|---|---|
| Setup | One-time first-admin claim: setup code, username, password, confirm (§12.5) |
| Login | — |
| Dashboard | Host counts by status; updates pending / security; reboot required; failed units; running and recent jobs; event feed |
| Hosts | Filterable table (name, status, OS, address, updates, reboot, last seen, tags); multi-select → *Run command / Update / Service / Reboot* |
| Add host (wizard) | 1) address, port, user, jump host · 2) credential (shows controller public key + copy snippet) · 3) **host key scan → compare fingerprint → approve** · 4) probe with step-by-step results · 5) discover |
| Host detail | Facts, addresses, status, pinned key (+ rotate flow), tags and groups, job history, actions |
| Groups | CRUD, membership |
| New job | Operation form (per type) → targets (live preview count and list, skips shown) → strategy → **confirmation dialog listing the target count and operation** |
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

- `drool backup -o file.tar.zst`: `VACUUM INTO` a consistent snapshot, plus config and TLS material, plus a manifest (version, schema version, timestamp). The master key is included only with `--include-master-key`.
- `drool restore file.tar.zst`: stop the service → verify the manifest → run migrations forward if needed.
- In the alpha, restore has no reconciliation concerns (no managed state domains yet), but the manifest records schema and epoch fields for the Beta.

---

## 18. Testing

| Layer | Approach |
|---|---|
| Parsers (os-release, apt/dnf output, systemctl show, ss, df) | Table-driven unit tests with captured fixtures from each distro |
| Engine | Fake Driver with scripted results/latency/errors: retries, circuit breaker, locks, cancellation, failure_threshold, restart → interrupted |
| SSH | In-process `x/crypto/ssh` test server: host key mismatch, algorithm pinning, auth failures, jump dialing |
| Integration | testcontainers-go matrix with systemd + sshd images: Debian 12/13, Ubuntu 24.04/26.04, Rocky 9/10, Fedora current. Topology: controller → jump → target on separate Docker networks |
| Fault injection | Kill the connection mid-command; hold the dpkg lock; wrong sudo password; `requiretty`; change the host key; reboot timing; output flood beyond the cap |
| API | Conformance against `openapi.yaml`; authz matrix test (every route × every role) |

---

## 19. Forward Compatibility Hooks

| Future | Alpha preparation |
|---|---|
| Agent (Alpha 2) | `Driver` interface over typed ops; `management_method` column; capability check hook in engine (no-op for SSH) |
| Multi-hop | `host_hops` table; dialer recursion is written but limited to one hop |
| Firewall Beta | `sshx.DialFresh()` (no pool, no reuse); `host_addresses` with `source`; reserved `state_versions` |
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
