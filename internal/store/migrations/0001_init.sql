-- M0: identity, sessions, API tokens, audit, settings.
-- Hosts, secrets and jobs arrive in later migrations (M1, M2).

CREATE TABLE users (
  id TEXT PRIMARY KEY,
  username TEXT NOT NULL UNIQUE COLLATE NOCASE,
  password_hash TEXT NOT NULL,              -- argon2id PHC string
  role TEXT NOT NULL CHECK (role IN ('admin','operator','auditor','viewer')),
  disabled INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  password_changed_at INTEGER NOT NULL,
  last_login_at INTEGER
);

CREATE TABLE sessions (
  token_hash BLOB PRIMARY KEY,              -- sha256 of the cookie value
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  csrf_token TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,              -- absolute expiry
  source_ip TEXT NOT NULL DEFAULT '',
  user_agent TEXT NOT NULL DEFAULT ''
);
CREATE INDEX sessions_by_user ON sessions(user_id);

CREATE TABLE api_tokens (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  token_hash BLOB NOT NULL UNIQUE,          -- sha256 of the secret part
  created_at INTEGER NOT NULL,
  last_used_at INTEGER,
  expires_at INTEGER
);
CREATE INDEX api_tokens_by_user ON api_tokens(user_id);

CREATE TABLE audit_events (
  id TEXT PRIMARY KEY,                      -- ULID: sortable by time
  ts INTEGER NOT NULL,
  actor_type TEXT NOT NULL CHECK (actor_type IN ('user','token','system','anonymous')),
  actor_id TEXT NOT NULL DEFAULT '',
  actor_name TEXT NOT NULL DEFAULT '',
  action TEXT NOT NULL,
  target_type TEXT NOT NULL DEFAULT '',
  target_id TEXT NOT NULL DEFAULT '',
  result TEXT NOT NULL CHECK (result IN ('success','failure','denied')),
  source_ip TEXT NOT NULL DEFAULT '',
  request_id TEXT NOT NULL DEFAULT '',
  details_json TEXT NOT NULL DEFAULT '{}',
  prev_hash BLOB                            -- reserved for the 1.0 hash chain
);
CREATE INDEX audit_by_ts ON audit_events(ts);
CREATE INDEX audit_by_action ON audit_events(action, ts);
CREATE INDEX audit_by_actor ON audit_events(actor_id, ts);
CREATE INDEX audit_by_target ON audit_events(target_id, ts);

CREATE TRIGGER audit_no_update BEFORE UPDATE ON audit_events
  BEGIN SELECT RAISE(ABORT, 'audit_events is append-only'); END;
CREATE TRIGGER audit_no_delete BEFORE DELETE ON audit_events
  BEGIN SELECT RAISE(ABORT, 'audit_events is append-only'); END;

CREATE TABLE settings (
  key TEXT PRIMARY KEY,
  value_json TEXT NOT NULL,
  updated_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  updated_at INTEGER NOT NULL
);
