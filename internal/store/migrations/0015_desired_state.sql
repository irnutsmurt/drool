-- M14: desired state, drift and reconciliation.
--
-- Desired configuration (baselines, their revisions, where they apply,
-- group priorities and host-specific settings) is kept apart from what
-- DROOL observes on hosts (ds_host_state, ds_resource_state) and from the
-- history of changes and corrections (ds_history, ds_reconciles).
-- Nothing here expands the fleet's configuration per host: the controller
-- computes each host's effective desired state from these sparse rows.
-- Corrections are ordinary jobs (jobs.reconcile_id).

-- A baseline is a named collection of typed desired resources (services,
-- packages, managed files). Every change is an immutable revision.
CREATE TABLE ds_baselines (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  revision INTEGER NOT NULL,              -- the current revision
  -- Automatic restore is off by default. When on, corrective jobs run with
  -- the owner's permissions, checked again before every job.
  auto_enabled INTEGER NOT NULL DEFAULT 0,
  auto_strategy_json TEXT NOT NULL DEFAULT '{}',
  owner_id TEXT REFERENCES users(id) ON DELETE SET NULL,
  -- Why automatic restore is paused ('' when it isn't).
  attention TEXT NOT NULL DEFAULT '',
  attention_at INTEGER,
  created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_at INTEGER NOT NULL,
  updated_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  updated_at INTEGER NOT NULL,
  archived_at INTEGER
);
CREATE UNIQUE INDEX ds_baselines_name ON ds_baselines(name COLLATE NOCASE) WHERE archived_at IS NULL;

CREATE TABLE ds_baseline_revisions (
  baseline_id TEXT NOT NULL REFERENCES ds_baselines(id) ON DELETE CASCADE,
  revision INTEGER NOT NULL,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  resources_json TEXT NOT NULL,           -- typed resources in canonical order
  assignments_json TEXT NOT NULL,         -- where the baseline applied when saved
  content_hash TEXT NOT NULL,
  created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_by_name TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  PRIMARY KEY (baseline_id, revision)
);

-- Where a baseline applies now: everywhere, a group, or one host.
CREATE TABLE ds_assignments (
  baseline_id TEXT NOT NULL REFERENCES ds_baselines(id) ON DELETE CASCADE,
  scope TEXT NOT NULL CHECK (scope IN ('global','group','host')),
  group_id TEXT REFERENCES groups(id) ON DELETE CASCADE,
  host_id TEXT REFERENCES hosts(id) ON DELETE CASCADE,
  CHECK ((scope = 'global' AND group_id IS NULL AND host_id IS NULL)
      OR (scope = 'group' AND group_id IS NOT NULL AND host_id IS NULL)
      OR (scope = 'host' AND host_id IS NOT NULL AND group_id IS NULL))
);
CREATE UNIQUE INDEX ds_assignments_key ON ds_assignments(baseline_id, scope, ifnull(group_id, ''), ifnull(host_id, ''));
CREATE INDEX ds_assignments_group ON ds_assignments(group_id) WHERE group_id IS NOT NULL;
CREATE INDEX ds_assignments_host ON ds_assignments(host_id) WHERE host_id IS NOT NULL;

-- A group's precedence when a host is in several groups whose baselines
-- declare the same resource: higher wins; equal priorities that disagree
-- are a conflict, never resolved by name, id or order.
CREATE TABLE ds_group_priority (
  group_id TEXT PRIMARY KEY REFERENCES groups(id) ON DELETE CASCADE,
  priority INTEGER NOT NULL DEFAULT 0 CHECK (priority BETWEEN -1000 AND 1000),
  updated_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  updated_at INTEGER NOT NULL
);

-- Host-specific declarations ("Custom for this host"), the most specific
-- scope: a resource value (e.g. from Accept current state) or an
-- exclusion (the resource isn't managed on this host).
CREATE TABLE ds_host_overrides (
  host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  key TEXT NOT NULL,
  resource_json TEXT NOT NULL DEFAULT '',  -- '' when excluded
  excluded INTEGER NOT NULL DEFAULT 0,
  origin TEXT NOT NULL DEFAULT 'accept' CHECK (origin IN ('accept','manual')),
  note TEXT NOT NULL DEFAULT '',
  version INTEGER NOT NULL DEFAULT 1,
  created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_at INTEGER NOT NULL,
  updated_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (host_id, key)
);

-- A generation counter the evaluator compares to notice configuration
-- changes made anywhere (baselines, assignments, priorities, host
-- settings, group membership, hosts added or removed).
CREATE TABLE ds_gen (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  gen INTEGER NOT NULL
);
INSERT INTO ds_gen (id, gen) VALUES (1, 0);

CREATE TRIGGER ds_gen_bl_ins AFTER INSERT ON ds_baselines BEGIN UPDATE ds_gen SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER ds_gen_bl_upd AFTER UPDATE OF revision, archived_at, name, auto_enabled, attention ON ds_baselines BEGIN UPDATE ds_gen SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER ds_gen_bl_del AFTER DELETE ON ds_baselines BEGIN UPDATE ds_gen SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER ds_gen_as_ins AFTER INSERT ON ds_assignments BEGIN UPDATE ds_gen SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER ds_gen_as_del AFTER DELETE ON ds_assignments BEGIN UPDATE ds_gen SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER ds_gen_pr_ins AFTER INSERT ON ds_group_priority BEGIN UPDATE ds_gen SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER ds_gen_pr_upd AFTER UPDATE ON ds_group_priority BEGIN UPDATE ds_gen SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER ds_gen_pr_del AFTER DELETE ON ds_group_priority BEGIN UPDATE ds_gen SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER ds_gen_ov_ins AFTER INSERT ON ds_host_overrides BEGIN UPDATE ds_gen SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER ds_gen_ov_upd AFTER UPDATE ON ds_host_overrides BEGIN UPDATE ds_gen SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER ds_gen_ov_del AFTER DELETE ON ds_host_overrides BEGIN UPDATE ds_gen SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER ds_gen_mem_ins AFTER INSERT ON group_members BEGIN UPDATE ds_gen SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER ds_gen_mem_del AFTER DELETE ON group_members BEGIN UPDATE ds_gen SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER ds_gen_grp_del AFTER DELETE ON groups BEGIN UPDATE ds_gen SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER ds_gen_host_ins AFTER INSERT ON hosts BEGIN UPDATE ds_gen SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER ds_gen_host_del AFTER DELETE ON hosts BEGIN UPDATE ds_gen SET gen = gen + 1 WHERE id = 1; END;

-- What DROOL last observed per host: when packages and files were last
-- checked (controller clock), why a host's state is unknown, and the
-- per-status counts behind the fleet summary.
CREATE TABLE ds_host_state (
  host_id TEXT PRIMARY KEY REFERENCES hosts(id) ON DELETE CASCADE,
  eff_hash TEXT NOT NULL DEFAULT '',      -- the effective desired state's hash
  status TEXT NOT NULL DEFAULT 'none',    -- the host's overall status
  n_total INTEGER NOT NULL DEFAULT 0,
  n_compliant INTEGER NOT NULL DEFAULT 0,
  n_drift INTEGER NOT NULL DEFAULT 0,
  n_conflict INTEGER NOT NULL DEFAULT 0,
  n_unknown INTEGER NOT NULL DEFAULT 0,
  n_reconciling INTEGER NOT NULL DEFAULT 0,
  n_failed INTEGER NOT NULL DEFAULT 0,
  n_auto_paused INTEGER NOT NULL DEFAULT 0,
  packages_at INTEGER,                    -- last successful package check
  packages_note TEXT NOT NULL DEFAULT '', -- why the last check failed
  files_at INTEGER,
  files_note TEXT NOT NULL DEFAULT '',
  unknown_since INTEGER,                  -- host-wide: when its state became unknown
  changed_at INTEGER NOT NULL
);
CREATE INDEX ds_host_state_status ON ds_host_state(status);

-- The current compliance of each managed resource on each host. Rows are
-- written only when something meaningful changes (status, the
-- difference, the observed value), never per unchanged check.
CREATE TABLE ds_resource_state (
  host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  key TEXT NOT NULL,                      -- service:<unit> | package:<name> | file:<path>
  kind TEXT NOT NULL CHECK (kind IN ('service','package','file')),
  status TEXT NOT NULL CHECK (status IN ('compliant','drift','conflict','unknown','reconciling','failed')),
  desired_json TEXT NOT NULL DEFAULT '',  -- the effective declaration ('' for a conflict)
  source_json TEXT NOT NULL DEFAULT '{}', -- provenance: scope, baseline and revision, competing declarations
  observed_json TEXT NOT NULL DEFAULT '', -- the latest observation of this resource
  observed_at INTEGER,                    -- when that observation was made
  diffs_json TEXT NOT NULL DEFAULT '[]',  -- what differs, field by field
  sig TEXT NOT NULL DEFAULT '',           -- status and difference signature (history dedup)
  last_known TEXT NOT NULL DEFAULT '',    -- the status before an unknown period
  note TEXT NOT NULL DEFAULT '',          -- why unknown, why a restore failed
  since INTEGER NOT NULL,                 -- in this status since
  reconcile_id TEXT,                      -- the corrective job in progress
  job_id TEXT,
  last_job_id TEXT NOT NULL DEFAULT '',   -- the last corrective job (also after it finished)
  -- Automatic restore bookkeeping (bounded retries, re-drift detection).
  auto_attempts INTEGER NOT NULL DEFAULT 0,
  auto_next_at INTEGER,
  auto_paused TEXT NOT NULL DEFAULT '',   -- why automatic restore stopped for this resource
  restored_at INTEGER,
  redrifts INTEGER NOT NULL DEFAULT 0,
  redrift_since INTEGER,
  PRIMARY KEY (host_id, key)
);
CREATE INDEX ds_resource_status ON ds_resource_state(status, host_id) WHERE status <> 'compliant';
CREATE INDEX ds_resource_job ON ds_resource_state(job_id) WHERE job_id IS NOT NULL;

-- Meaningful transitions only: drift detected or changed, compliant
-- again (and why), conflicts, unknown, restores requested, succeeded and
-- failed, current state accepted, automatic restore paused. host_name and
-- the desired value are snapshots, so history stays true after edits.
CREATE TABLE ds_history (
  id INTEGER PRIMARY KEY,
  ts INTEGER NOT NULL,
  host_id TEXT NOT NULL,                  -- no FK: history outlives the host
  host_name TEXT NOT NULL,
  key TEXT NOT NULL DEFAULT '',           -- '' for host-level entries
  kind TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT '',
  prev_status TEXT NOT NULL DEFAULT '',
  -- How drift ended: restored (a corrective job changed the host and a
  -- fresh observation proved it), configuration (the desired state
  -- changed), observed (the host changed by other means).
  resolution TEXT NOT NULL DEFAULT '',
  detail TEXT NOT NULL DEFAULT '',
  desired_json TEXT NOT NULL DEFAULT '',
  observed_json TEXT NOT NULL DEFAULT '',
  diffs_json TEXT NOT NULL DEFAULT '[]',
  baseline_id TEXT NOT NULL DEFAULT '',
  baseline_revision INTEGER NOT NULL DEFAULT 0,
  source_label TEXT NOT NULL DEFAULT '',
  job_id TEXT NOT NULL DEFAULT '',
  actor_id TEXT NOT NULL DEFAULT '',
  actor_name TEXT NOT NULL DEFAULT ''
);
CREATE INDEX ds_history_host ON ds_history(host_id, key, id);
CREATE INDEX ds_history_ts ON ds_history(ts);
CREATE INDEX ds_history_baseline ON ds_history(baseline_id, id) WHERE baseline_id <> '';

-- Corrections: each is one ordinary job. items_json lists every host and
-- resource it corrects with the exact desired value and its source.
CREATE TABLE ds_reconciles (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL CHECK (kind IN ('manual','auto')),
  baseline_id TEXT NOT NULL DEFAULT '',   -- automatic restores: the baseline
  requested_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  job_id TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('active','done')),
  items_json TEXT NOT NULL,
  summary TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  finished_at INTEGER
);
CREATE INDEX ds_reconciles_active ON ds_reconciles(created_at) WHERE status = 'active';
CREATE INDEX ds_reconciles_baseline ON ds_reconciles(baseline_id, created_at) WHERE baseline_id <> '';

-- Where a job came from: a desired-state correction.
ALTER TABLE jobs ADD COLUMN reconcile_id TEXT;
-- The steps a host runs, when they differ per host (a correction runs
-- only what each host needs): a JSON list of step indexes; NULL = all.
ALTER TABLE job_hosts ADD COLUMN steps TEXT;
