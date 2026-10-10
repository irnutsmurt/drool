-- M16: tamper-evident audit history (DESIGN §22.6, ARCHITECTURE-ALPHA §33.4).
--
-- Every record written from now on is a link of a hash chain: seq is its
-- position (1, 2, 3, ... without gaps), prev_hash the previous record's
-- hash, hash = SHA-256 over the canonical encoding of the record and
-- prev_hash. Records written before the chain existed keep seq NULL; the
-- chain's first record (audit.chain.start) commits to all of them with a
-- digest. The table stays append-only (the triggers of 0001 still refuse
-- UPDATE and DELETE).
ALTER TABLE audit_events ADD COLUMN seq INTEGER;
ALTER TABLE audit_events ADD COLUMN hash BLOB;
CREATE UNIQUE INDEX audit_by_seq ON audit_events(seq) WHERE seq IS NOT NULL;

-- Results of verifying the chain. Deliberately outside the chain: a broken
-- chain must not stop DROOL from recording that it is broken.
CREATE TABLE audit_verifications (
  id TEXT PRIMARY KEY,
  cause TEXT NOT NULL,                       -- startup, scheduled, manual, restore
  requested_by TEXT NOT NULL DEFAULT '',
  started_at INTEGER NOT NULL,
  finished_at INTEGER,
  result TEXT NOT NULL CHECK (result IN ('running','ok','failed','error')),
  records INTEGER NOT NULL DEFAULT 0,        -- chained records checked
  legacy_records INTEGER NOT NULL DEFAULT 0, -- records from before the chain (covered by its digest)
  head_seq INTEGER NOT NULL DEFAULT 0,
  sealed_through INTEGER NOT NULL DEFAULT 0, -- newest seq covered by a valid checkpoint
  checkpoints INTEGER NOT NULL DEFAULT 0,
  checkpoints_unverifiable INTEGER NOT NULL DEFAULT 0,
  first_bad_seq INTEGER,
  problem TEXT NOT NULL DEFAULT '',          -- the first problem, plain language
  detail TEXT NOT NULL DEFAULT '',           -- its technical detail
  problems_json TEXT NOT NULL DEFAULT '[]',  -- every problem found (bounded)
  problem_count INTEGER NOT NULL DEFAULT 0,
  evidence_file TEXT NOT NULL DEFAULT ''
);
CREATE INDEX audit_verifications_by_start ON audit_verifications(started_at);

-- The optional remote copy of the audit history (one destination). The
-- queue is the audit table itself: everything after cursor_seq is still to
-- be sent.
CREATE TABLE audit_sink (
  id TEXT PRIMARY KEY CHECK (id = 'default'),
  enabled INTEGER NOT NULL DEFAULT 0,
  url TEXT NOT NULL,
  ca_pem TEXT NOT NULL DEFAULT '',           -- extra CA for the receiver's certificate; '' = system roots
  header_name TEXT NOT NULL DEFAULT '',      -- e.g. Authorization
  secret_ciphertext BLOB,                    -- the header value, sealed with the master key
  secret_nonce BLOB,
  secret_key_version INTEGER,
  cursor_seq INTEGER NOT NULL DEFAULT 0,     -- delivered through this seq
  last_attempt_at INTEGER,
  last_success_at INTEGER,
  last_error TEXT NOT NULL DEFAULT '',
  failures INTEGER NOT NULL DEFAULT 0,       -- consecutive
  next_attempt_at INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  updated_by TEXT REFERENCES users(id) ON DELETE SET NULL
);
