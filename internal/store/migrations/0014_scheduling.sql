-- M13: scheduling, fleet automation and workflows.
--
-- Schedules decide *when* and under which rollout strategy; every run is
-- an ordinary job created through the job engine, which decides *how* it
-- executes (locks, typed operations, retries, capabilities, management
-- path, output, cancellation, audit).

-- Where a job came from (a schedule's occurrence, a workflow revision).
ALTER TABLE jobs ADD COLUMN schedule_id TEXT;
ALTER TABLE jobs ADD COLUMN occurrence_id TEXT;
ALTER TABLE jobs ADD COLUMN workflow_id TEXT;
ALTER TABLE jobs ADD COLUMN workflow_revision INTEGER;
CREATE INDEX jobs_by_schedule ON jobs(schedule_id, created_at) WHERE schedule_id IS NOT NULL;

-- A host's rollout wave, fixed when the job is created (canaries are wave
-- 0 of a canary rollout). -1: not part of this run (skipped up front, e.g.
-- the host was in maintenance when a scheduled run started).
ALTER TABLE job_hosts ADD COLUMN wave INTEGER NOT NULL DEFAULT 0;

-- Reusable workflows: ordered typed steps plus a default rollout. Every
-- edit is a new revision; nothing that pinned a revision changes with it.
CREATE TABLE workflows (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  latest_revision INTEGER NOT NULL,
  created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  archived_at INTEGER
);
CREATE UNIQUE INDEX workflows_name ON workflows(name COLLATE NOCASE) WHERE archived_at IS NULL;

CREATE TABLE workflow_revisions (
  workflow_id TEXT NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
  revision INTEGER NOT NULL,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  steps_json TEXT NOT NULL,
  strategy_json TEXT NOT NULL,
  created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_by_name TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  PRIMARY KEY (workflow_id, revision)
);

-- Schedules: a job (an inline job or a pinned workflow revision) or a
-- recurring maintenance window. config_json is the current revision's
-- complete configuration (recurrence and time zone, action, targets,
-- rollout, missed-run and overlap policies, maintenance duration).
CREATE TABLE schedules (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL CHECK (kind IN ('job','maintenance')),
  name TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  archived_at INTEGER,
  -- Runs execute with the owner's permissions, checked again before
  -- every occurrence; NULL: the owner's account was deleted.
  owner_id TEXT REFERENCES users(id) ON DELETE SET NULL,
  revision INTEGER NOT NULL DEFAULT 1,
  config_json TEXT NOT NULL,
  workflow_id TEXT REFERENCES workflows(id) ON DELETE SET NULL,
  workflow_revision INTEGER,
  window_id TEXT REFERENCES schedules(id) ON DELETE SET NULL, -- associated recurring maintenance window
  next_run_at INTEGER,               -- next nominal occurrence (unix ms); NULL: none
  attention TEXT NOT NULL DEFAULT '', -- why it needs attention ('' when fine)
  attention_at INTEGER,
  created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_at INTEGER NOT NULL,
  updated_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX schedules_due ON schedules(next_run_at) WHERE enabled = 1 AND archived_at IS NULL;
CREATE INDEX schedules_workflow ON schedules(workflow_id) WHERE workflow_id IS NOT NULL;

-- Every revision of a schedule's configuration, so history can say which
-- configuration produced an occurrence.
CREATE TABLE schedule_revisions (
  schedule_id TEXT NOT NULL REFERENCES schedules(id) ON DELETE CASCADE,
  revision INTEGER NOT NULL,
  config_json TEXT NOT NULL,
  owner_id TEXT,
  changed_by_name TEXT NOT NULL DEFAULT '',
  changed_at INTEGER NOT NULL,
  PRIMARY KEY (schedule_id, revision)
);

-- Occurrences, kept independently of jobs: what was due when, and what
-- happened (dispatched to a job or a maintenance window, skipped, missed
-- while DROOL was not running, expired, blocked). One row per nominal
-- time per schedule: a restart can never create a second job for it.
CREATE TABLE schedule_occurrences (
  id TEXT PRIMARY KEY,
  schedule_id TEXT NOT NULL REFERENCES schedules(id) ON DELETE CASCADE,
  schedule_revision INTEGER NOT NULL,
  scheduled_for INTEGER NOT NULL,    -- nominal time (unix ms); manual runs: when requested
  manual INTEGER NOT NULL DEFAULT 0, -- "Run now"
  status TEXT NOT NULL CHECK (status IN ('queued','dispatched','skipped','missed','expired','blocked','failed')),
  reason TEXT NOT NULL DEFAULT '',
  missed_count INTEGER NOT NULL DEFAULT 0,  -- missed: occurrences this row stands for
  missed_until INTEGER,                     -- missed: the last of them
  dispatched_at INTEGER,
  job_id TEXT REFERENCES jobs(id) ON DELETE SET NULL,
  maintenance_id TEXT REFERENCES maintenance(id) ON DELETE SET NULL,
  triggered_by TEXT NOT NULL DEFAULT '',    -- manual runs: who
  skipped_hosts INTEGER NOT NULL DEFAULT 0, -- hosts left out (maintenance)
  created_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX schedule_occurrences_once ON schedule_occurrences(schedule_id, scheduled_for) WHERE manual = 0;
CREATE INDEX schedule_occurrences_by_schedule ON schedule_occurrences(schedule_id, scheduled_for);
CREATE INDEX schedule_occurrences_queued ON schedule_occurrences(schedule_id) WHERE status = 'queued';
CREATE INDEX schedule_occurrences_job ON schedule_occurrences(job_id) WHERE job_id IS NOT NULL;

-- A maintenance started by a recurring window.
ALTER TABLE maintenance ADD COLUMN schedule_id TEXT;
ALTER TABLE maintenance ADD COLUMN occurrence_id TEXT;
CREATE INDEX maintenance_by_schedule ON maintenance(schedule_id) WHERE schedule_id IS NOT NULL;
