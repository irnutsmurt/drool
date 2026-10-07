-- M8: firewall policy and safe enforcement (DESIGN sections 15-19,
-- ARCHITECTURE-ALPHA section 24).

-- Custom services; built-in ones live in code.
CREATE TABLE fw_services (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  description TEXT NOT NULL DEFAULT '',
  entries_json TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

-- Network intent. Evaluated in position order; denies win over allows.
CREATE TABLE fw_policies (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE COLLATE NOCASE,
  description TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1,
  action TEXT NOT NULL CHECK (action IN ('allow','deny')),
  from_json TEXT NOT NULL,
  to_json TEXT NOT NULL,
  services_json TEXT NOT NULL,
  position INTEGER NOT NULL,
  created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

-- Per-host firewall settings. Mode: off, monitor (observe), guided
-- (observe and suggest), enforce (apply and detect drift).
CREATE TABLE fw_host_config (
  host_id TEXT PRIMARY KEY REFERENCES hosts(id) ON DELETE CASCADE,
  mode TEXT NOT NULL DEFAULT 'off' CHECK (mode IN ('off','monitor','guided','enforce')),
  outbound TEXT NOT NULL DEFAULT 'allow' CHECK (outbound IN ('allow','restrict')),
  rollback_timeout_s INTEGER,
  updated_at INTEGER NOT NULL
);

CREATE TABLE fw_group_config (
  group_id TEXT PRIMARY KEY REFERENCES groups(id) ON DELETE CASCADE,
  rollback_timeout_s INTEGER,
  updated_at INTEGER NOT NULL
);

-- What DROOL knows about each host's firewall: the detected environment,
-- the last status report of the host tooling, and what is committed there.
CREATE TABLE fw_host_state (
  host_id TEXT PRIMARY KEY REFERENCES hosts(id) ON DELETE CASCADE,
  env_json TEXT NOT NULL DEFAULT '',
  eligibility_json TEXT NOT NULL DEFAULT '',
  env_checked_at INTEGER,
  status_json TEXT NOT NULL DEFAULT '',
  status_at INTEGER,
  enforced INTEGER NOT NULL DEFAULT 0,
  applied_revision INTEGER NOT NULL DEFAULT 0,
  applied_rules_hash TEXT NOT NULL DEFAULT '',
  applied_full_hash TEXT NOT NULL DEFAULT '',
  applied_sets_hash TEXT NOT NULL DEFAULT '',
  applied_policy_json TEXT NOT NULL DEFAULT '',
  override_mgmt INTEGER NOT NULL DEFAULT 0,
  sets_generation INTEGER NOT NULL DEFAULT 0,
  sets_generation_applied INTEGER NOT NULL DEFAULT 0,
  sets_error TEXT NOT NULL DEFAULT '',
  sets_at INTEGER,
  drift TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL
);

-- How management connections reach each host, as the host saw them
-- (SSH_CONNECTION over a fresh connection; the agent's resolved endpoints).
CREATE TABLE fw_mgmt_observations (
  host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  kind TEXT NOT NULL CHECK (kind IN ('ssh_in','agent_out')),
  address TEXT NOT NULL,
  port INTEGER NOT NULL,
  observed_at INTEGER NOT NULL,
  PRIMARY KEY (host_id, kind, address, port)
);

CREATE TABLE fw_transactions (
  id TEXT PRIMARY KEY,
  host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  job_id TEXT,
  kind TEXT NOT NULL DEFAULT 'apply' CHECK (kind IN ('apply','remove')),
  state TEXT NOT NULL CHECK (state IN ('preparing','pending','committed','rolled_back','failed','unknown')),
  transport TEXT NOT NULL DEFAULT '',
  epoch TEXT NOT NULL,
  revision INTEGER NOT NULL,
  previous_hash TEXT NOT NULL DEFAULT '',
  candidate_hash TEXT NOT NULL,
  rules_hash TEXT NOT NULL DEFAULT '',
  sets_hash TEXT NOT NULL DEFAULT '',
  timeout_s INTEGER NOT NULL,
  deadline INTEGER,
  measured_ms INTEGER NOT NULL DEFAULT 0,
  override_mgmt INTEGER NOT NULL DEFAULT 0,
  override_timeout INTEGER NOT NULL DEFAULT 0,
  allow_drift INTEGER NOT NULL DEFAULT 0,
  detail TEXT NOT NULL DEFAULT '',
  error_class TEXT NOT NULL DEFAULT '',
  candidate TEXT NOT NULL,
  policy_json TEXT NOT NULL DEFAULT '',
  steps_json TEXT NOT NULL DEFAULT '[]',
  created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  finished_at INTEGER
);
CREATE INDEX fw_transactions_by_host ON fw_transactions(host_id, created_at);
CREATE INDEX fw_transactions_open ON fw_transactions(state) WHERE state IN ('preparing','pending','unknown');

-- Aggregated connection observations (DESIGN 19.1).
CREATE TABLE fw_observations (
  host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  direction TEXT NOT NULL CHECK (direction IN ('in','out')),
  protocol TEXT NOT NULL,
  port INTEGER NOT NULL,
  peer TEXT NOT NULL,
  source TEXT NOT NULL CHECK (source IN ('agent','snapshot')),
  count INTEGER NOT NULL,
  first_seen INTEGER NOT NULL,
  last_seen INTEGER NOT NULL,
  PRIMARY KEY (host_id, direction, protocol, port, peer)
);
