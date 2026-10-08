-- M11: system monitoring, service inventory and hardware health.
--
-- Metric history is downsampled in tiers so high-frequency samples never
-- grow the database without bound: tier 0 holds raw points (one-minute
-- windows from agents, one per poll from SSH hosts) for a day, tier 1
-- five-minute windows for eight days, tier 2 hourly windows for the
-- retention period. A point stores average and maximum per series as
-- little-endian float32 pairs (NaN: no value) in the order of its layout:
-- a deduplicated list of series keys.
CREATE TABLE mon_layouts (
  id INTEGER PRIMARY KEY,
  hash TEXT NOT NULL UNIQUE,
  keys_json TEXT NOT NULL
);
CREATE TABLE mon_points (
  host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  tier INTEGER NOT NULL CHECK (tier IN (0, 1, 2)),
  ts INTEGER NOT NULL,        -- window start, unix seconds
  span INTEGER NOT NULL,      -- window length, seconds
  n INTEGER NOT NULL,         -- samples aggregated
  layout INTEGER NOT NULL REFERENCES mon_layouts(id),
  data BLOB NOT NULL,
  PRIMARY KEY (host_id, tier, ts)
) WITHOUT ROWID;

-- Periods without routine telemetry (an agent's offline buffer overflowed).
CREATE TABLE mon_gaps (
  id INTEGER PRIMARY KEY,
  host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  from_ts INTEGER NOT NULL,
  to_ts INTEGER NOT NULL,
  reason TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX mon_gaps_host ON mon_gaps(host_id, to_ts);

-- Each host's current picture (one row per host, replaced on every report).
CREATE TABLE mon_current (
  host_id TEXT PRIMARY KEY REFERENCES hosts(id) ON DELETE CASCADE,
  source TEXT NOT NULL CHECK (source IN ('agent','ssh')),
  updated_at INTEGER NOT NULL,          -- ms, when the state was collected
  state_json TEXT NOT NULL,
  raw_json TEXT NOT NULL DEFAULT '',    -- SSH: counters for the next poll's rates
  storage_json TEXT NOT NULL DEFAULT '',
  storage_at INTEGER NOT NULL DEFAULT 0,
  services_note TEXT NOT NULL DEFAULT '',
  services_supported INTEGER NOT NULL DEFAULT 0,
  services_at INTEGER NOT NULL DEFAULT 0,
  probes_json TEXT NOT NULL DEFAULT '', -- latest probe result per mount
  kernel_json TEXT NOT NULL DEFAULT ''  -- kernel message dedup: boot id and last timestamp
);

-- Services: one row per unit, written only when something changed.
CREATE TABLE mon_services (
  host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  unit TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  load_state TEXT NOT NULL DEFAULT '',
  active_state TEXT NOT NULL,
  sub_state TEXT NOT NULL,
  file_state TEXT NOT NULL DEFAULT '',
  since INTEGER NOT NULL DEFAULT 0,     -- unix seconds, from systemd
  restarts INTEGER,                     -- NULL: unknown
  result TEXT NOT NULL DEFAULT '',
  transient INTEGER NOT NULL DEFAULT 0,
  known INTEGER NOT NULL DEFAULT 1,     -- 0 while the host's telemetry is lost
  changed_at INTEGER NOT NULL,          -- ms: the last state change DROOL saw
  PRIMARY KEY (host_id, unit)
) WITHOUT ROWID;

-- Service state changes (Running → Failed, Unknown → Running, ...).
CREATE TABLE mon_service_events (
  id INTEGER PRIMARY KEY,
  host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  unit TEXT NOT NULL,
  ts INTEGER NOT NULL,                  -- ms
  from_state TEXT NOT NULL,             -- active/sub, or "unknown"
  to_state TEXT NOT NULL,
  result TEXT NOT NULL DEFAULT ''
);
CREATE INDEX mon_service_events_unit ON mon_service_events(host_id, unit, ts);
CREATE INDEX mon_service_events_ts ON mon_service_events(ts);

-- Other monitoring events: kernel storage evidence, probe results,
-- fidelity changes, telemetry gaps, reboots.
CREATE TABLE mon_events (
  id INTEGER PRIMARY KEY,
  host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  ts INTEGER NOT NULL,                  -- ms
  kind TEXT NOT NULL,
  subject TEXT NOT NULL DEFAULT '',
  severity TEXT NOT NULL DEFAULT 'info',
  message TEXT NOT NULL,
  data_json TEXT NOT NULL DEFAULT ''
);
CREATE INDEX mon_events_host ON mon_events(host_id, ts);
CREATE INDEX mon_events_ts ON mon_events(ts);

-- Health issues found by the evaluator (what M12 alerts will consume).
CREATE TABLE mon_issues (
  id TEXT PRIMARY KEY,
  host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  check_id TEXT NOT NULL,
  subject TEXT NOT NULL DEFAULT '',
  severity TEXT NOT NULL CHECK (severity IN ('info','warning','critical')),
  state TEXT NOT NULL CHECK (state IN ('active','resolved')),
  title TEXT NOT NULL,
  detail TEXT NOT NULL DEFAULT '',
  value REAL,
  started_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  resolved_at INTEGER
);
CREATE UNIQUE INDEX mon_issues_one_active ON mon_issues(host_id, check_id, subject) WHERE state = 'active';
CREATE INDEX mon_issues_state ON mon_issues(state, host_id);
CREATE INDEX mon_issues_resolved ON mon_issues(resolved_at) WHERE state = 'resolved';

-- Per-host monitoring settings (watched services, flash probe, agent budget).
CREATE TABLE mon_host_settings (
  host_id TEXT PRIMARY KEY REFERENCES hosts(id) ON DELETE CASCADE,
  settings_json TEXT NOT NULL,
  updated_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  updated_at INTEGER NOT NULL
);

-- Monitoring helper installations DROOL requested (never uninstalled).
CREATE TABLE mon_helper_installs (
  host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  package TEXT NOT NULL,
  job_id TEXT,
  status TEXT NOT NULL CHECK (status IN ('requested','installed','failed')),
  scope TEXT NOT NULL CHECK (scope IN ('host','selection','group','global')),
  requested_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  requested_at INTEGER NOT NULL,
  detail TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (host_id, package)
);

-- Standing approvals: install an allowlisted helper wherever it would
-- help, for one group or every host.
CREATE TABLE mon_helper_approvals (
  id TEXT PRIMARY KEY,
  scope TEXT NOT NULL CHECK (scope IN ('group','global')),
  group_id TEXT REFERENCES groups(id) ON DELETE CASCADE,
  package TEXT NOT NULL,
  created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX mon_helper_approvals_one ON mon_helper_approvals(scope, COALESCE(group_id, ''), package);

-- Bulk SSH → agent conversion reuses the agent rollout machinery.
ALTER TABLE agent_rollouts ADD COLUMN kind TEXT NOT NULL DEFAULT 'update';
ALTER TABLE agent_rollouts ADD COLUMN preset TEXT NOT NULL DEFAULT '';
