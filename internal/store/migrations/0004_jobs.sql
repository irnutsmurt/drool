-- M2: jobs, per-host execution state, steps and output.

CREATE TABLE jobs (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  spec_json TEXT NOT NULL,                  -- canonical job spec (steps, targets, strategy)
  spec_version INTEGER NOT NULL,
  idempotency_key TEXT,
  status TEXT NOT NULL CHECK (status IN
    ('pending','running','paused','completed','cancelled','interrupted')),
  pause_reason TEXT NOT NULL DEFAULT '',
  rerun_of TEXT REFERENCES jobs(id) ON DELETE SET NULL,
  created_by TEXT NOT NULL REFERENCES users(id),
  created_at INTEGER NOT NULL,
  started_at INTEGER,
  finished_at INTEGER
);
CREATE UNIQUE INDEX jobs_idempotency ON jobs(created_by, idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX jobs_by_created ON jobs(created_at);
CREATE INDEX jobs_by_status ON jobs(status);

CREATE TABLE job_hosts (
  job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  host_id TEXT NOT NULL,                    -- no FK: history survives host deletion
  host_name TEXT NOT NULL,                  -- snapshot
  position INTEGER NOT NULL,                -- execution order
  state TEXT NOT NULL CHECK (state IN
    ('queued','waiting_lock','connecting','running','rebooting','waiting_host','verifying',
     'succeeded','failed','timed_out','cancelled','skipped','interrupted')),
  current_step INTEGER NOT NULL DEFAULT 0,
  attempt INTEGER NOT NULL DEFAULT 0,
  exit_code INTEGER,
  error_class TEXT NOT NULL DEFAULT '',
  error_message TEXT NOT NULL DEFAULT '',
  error_hint TEXT NOT NULL DEFAULT '',
  retryable INTEGER NOT NULL DEFAULT 0,
  started_at INTEGER,
  finished_at INTEGER,
  PRIMARY KEY (job_id, host_id)
);
CREATE INDEX job_hosts_by_host ON job_hosts(host_id, job_id);

CREATE TABLE job_steps (
  job_id TEXT NOT NULL,
  host_id TEXT NOT NULL,
  step_index INTEGER NOT NULL,
  op_type TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('running','succeeded','failed','timed_out','cancelled','interrupted')),
  attempt INTEGER NOT NULL DEFAULT 1,
  exit_code INTEGER,
  changed INTEGER,
  result_json TEXT NOT NULL DEFAULT '{}',
  stdout_bytes INTEGER NOT NULL DEFAULT 0,
  stderr_bytes INTEGER NOT NULL DEFAULT 0,
  truncated INTEGER NOT NULL DEFAULT 0,
  started_at INTEGER NOT NULL,
  finished_at INTEGER,
  PRIMARY KEY (job_id, host_id, step_index),
  FOREIGN KEY (job_id, host_id) REFERENCES job_hosts(job_id, host_id) ON DELETE CASCADE
);

CREATE TABLE job_output (
  job_id TEXT NOT NULL,
  host_id TEXT NOT NULL,
  step_index INTEGER NOT NULL,
  seq INTEGER NOT NULL,
  stream INTEGER NOT NULL CHECK (stream IN (1,2)),   -- 1 stdout, 2 stderr
  ts INTEGER NOT NULL,
  chunk BLOB NOT NULL,
  PRIMARY KEY (job_id, host_id, step_index, seq),
  FOREIGN KEY (job_id, host_id, step_index) REFERENCES job_steps(job_id, host_id, step_index) ON DELETE CASCADE
);
