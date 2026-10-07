-- M6: controller artifact store and managed file deployments
-- (ARCHITECTURE-ALPHA §10.6, §10.7).

-- Content-addressed blobs under <data_dir>/artifacts/sha256/<ab>/<sha256>.
-- Rows are the index; the file's name is its checksum, so integrity can be
-- re-verified at any time.
CREATE TABLE artifacts (
  sha256 TEXT PRIMARY KEY CHECK (length(sha256) = 64),
  size INTEGER NOT NULL CHECK (size >= 0),
  name TEXT NOT NULL DEFAULT '',            -- original file name, informational
  is_text INTEGER NOT NULL DEFAULT 0,       -- UTF-8 text small enough to diff and show
  created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_at INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL
);

-- One row per host per deployment (or rollback) of a file. Kept when the
-- job is pruned: it is the managed file's history and holds the references
-- that keep previous versions available for rollback.
CREATE TABLE file_deployments (
  id TEXT PRIMARY KEY,
  job_id TEXT REFERENCES jobs(id) ON DELETE SET NULL,
  host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  path TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('deploy','rollback')),
  rollback_of TEXT REFERENCES file_deployments(id) ON DELETE SET NULL,
  outcome TEXT NOT NULL CHECK (outcome IN ('installed','unchanged','removed','validation_failed','failed')),
  before_exists INTEGER NOT NULL,
  before_sha256 TEXT NOT NULL DEFAULT '',   -- '' when the file didn't exist
  before_artifact TEXT REFERENCES artifacts(sha256), -- NULL when not captured (absent, or too large)
  before_meta TEXT NOT NULL DEFAULT '{}',   -- {"owner","group","mode","size"}
  after_sha256 TEXT NOT NULL DEFAULT '',
  after_artifact TEXT REFERENCES artifacts(sha256),
  after_meta TEXT NOT NULL DEFAULT '{}',
  validation TEXT NOT NULL DEFAULT '{}',    -- {"command","exit_code","ok"}
  service TEXT NOT NULL DEFAULT '{}',       -- {"name","action","ok","error"}
  error TEXT NOT NULL DEFAULT '',
  op_json TEXT NOT NULL DEFAULT '{}',       -- the deployment's settings (no content): reused by rollbacks
  created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX file_deployments_by_host_path ON file_deployments(host_id, path, created_at);
CREATE INDEX file_deployments_by_job ON file_deployments(job_id);
CREATE INDEX file_deployments_before ON file_deployments(before_artifact);
CREATE INDEX file_deployments_after ON file_deployments(after_artifact);
