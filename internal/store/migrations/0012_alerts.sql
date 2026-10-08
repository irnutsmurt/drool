-- M12: alerts, notifications and maintenance.
--
-- M11's health evaluator owns detection: it opens, changes and resolves
-- mon_issues. M12 consumes those transitions in the same transaction:
-- an issue that becomes active with Warning or Critical severity opens an
-- alert (one per activation, so a service that fails, recovers and fails
-- again has two alerts), and the notifications it needs are queued in the
-- durable outbox (notify_deliveries) before anything is sent.

-- When M11 knows a condition started before its duration was satisfied
-- (CPU, memory, interfaces, temperatures, services), it records that time.
ALTER TABLE mon_issues ADD COLUMN observed_at INTEGER;

CREATE TABLE alerts (
  id TEXT PRIMARY KEY,
  host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  host_name TEXT NOT NULL,
  issue_id TEXT NOT NULL,          -- mon_issues.id (issues are pruned earlier than alerts)
  check_id TEXT NOT NULL,
  subject TEXT NOT NULL DEFAULT '',
  category TEXT NOT NULL,
  severity TEXT NOT NULL CHECK (severity IN ('warning','critical')),
  peak_severity TEXT NOT NULL CHECK (peak_severity IN ('warning','critical')),
  state TEXT NOT NULL CHECK (state IN ('active','acknowledged','resolved')),
  title TEXT NOT NULL,
  detail TEXT NOT NULL DEFAULT '',
  value REAL,
  observed_at INTEGER,             -- condition first observed (only when M11 knows it)
  issue_started_at INTEGER NOT NULL,
  opened_at INTEGER NOT NULL,
  acknowledged_at INTEGER,
  ack_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  ack_by_name TEXT NOT NULL DEFAULT '',
  ack_note TEXT NOT NULL DEFAULT '',
  resolved_at INTEGER,
  suppressed INTEGER NOT NULL DEFAULT 0,        -- currently suppressed by maintenance
  suppressed_by TEXT,                           -- maintenance id
  ever_suppressed INTEGER NOT NULL DEFAULT 0,
  notify_after INTEGER,            -- opening notification due (after maintenance ends)
  next_reminder_at INTEGER,
  reminders_sent INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX alerts_one_open_per_issue ON alerts(issue_id) WHERE state != 'resolved';
CREATE INDEX alerts_state ON alerts(state, opened_at);
CREATE INDEX alerts_host ON alerts(host_id, opened_at);
CREATE INDEX alerts_resolved ON alerts(resolved_at) WHERE state = 'resolved';
CREATE INDEX alerts_opened ON alerts(opened_at, id);
CREATE INDEX alerts_subject ON alerts(host_id, check_id, subject, opened_at);
CREATE INDEX alerts_due ON alerts(notify_after) WHERE notify_after IS NOT NULL;
CREATE INDEX alerts_reminder ON alerts(next_reminder_at) WHERE next_reminder_at IS NOT NULL;

-- An alert's compact timeline (bounded per alert by the service).
CREATE TABLE alert_events (
  id INTEGER PRIMARY KEY,
  alert_id TEXT NOT NULL REFERENCES alerts(id) ON DELETE CASCADE,
  ts INTEGER NOT NULL,
  kind TEXT NOT NULL,
  text TEXT NOT NULL,
  actor TEXT NOT NULL DEFAULT ''
);
CREATE INDEX alert_events_by_alert ON alert_events(alert_id, ts, id);

-- Immediate maintenance with an optional expiry (recurring windows are
-- M13). It suppresses notifications only.
CREATE TABLE maintenance (
  id TEXT PRIMARY KEY,
  reason TEXT NOT NULL DEFAULT '',
  created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_by_name TEXT NOT NULL DEFAULT '',
  started_at INTEGER NOT NULL,
  ends_at INTEGER,                 -- NULL: until ended manually
  ended_at INTEGER,
  ended_by_name TEXT NOT NULL DEFAULT ''
);
CREATE INDEX maintenance_open ON maintenance(ended_at, started_at);

CREATE TABLE maintenance_targets (
  maintenance_id TEXT NOT NULL REFERENCES maintenance(id) ON DELETE CASCADE,
  kind TEXT NOT NULL CHECK (kind IN ('host','group')),
  target_id TEXT NOT NULL,
  name TEXT NOT NULL,
  PRIMARY KEY (maintenance_id, kind, target_id)
);
CREATE INDEX maintenance_targets_by_target ON maintenance_targets(kind, target_id);

-- Notification destinations. Secrets (SMTP password, webhook URL for
-- Discord/Slack, webhook authorization header) are sealed with the master
-- key like every other credential and never returned by the API.
CREATE TABLE notify_destinations (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE COLLATE NOCASE,
  kind TEXT NOT NULL CHECK (kind IN ('email','webhook','discord','slack')),
  enabled INTEGER NOT NULL DEFAULT 1,
  config_json TEXT NOT NULL,
  secret_ciphertext BLOB,
  secret_nonce BLOB,
  secret_key_version INTEGER,
  created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  last_success_at INTEGER,
  last_failure_at INTEGER,
  last_error TEXT NOT NULL DEFAULT '',
  consecutive_failures INTEGER NOT NULL DEFAULT 0,
  failing_since INTEGER,
  last_test_at INTEGER,
  last_test_ok INTEGER,
  last_test_result TEXT NOT NULL DEFAULT ''
);

-- Routes select destinations for alerts. The default route exists from the
-- start (Warning and Critical, recoveries on); filters within a route are
-- combined with AND, values within a filter with OR, and every matching
-- route contributes its destinations (each destination is notified once).
CREATE TABLE notify_routes (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  is_default INTEGER NOT NULL DEFAULT 0,
  enabled INTEGER NOT NULL DEFAULT 1,
  severities_json TEXT NOT NULL DEFAULT '[]',
  host_ids_json TEXT NOT NULL DEFAULT '[]',
  group_ids_json TEXT NOT NULL DEFAULT '[]',
  categories_json TEXT NOT NULL DEFAULT '[]',
  checks_json TEXT NOT NULL DEFAULT '[]',
  recovery INTEGER NOT NULL DEFAULT 1,
  reminder_minutes INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX notify_routes_one_default ON notify_routes(is_default) WHERE is_default = 1;

CREATE TABLE notify_route_destinations (
  route_id TEXT NOT NULL REFERENCES notify_routes(id) ON DELETE CASCADE,
  destination_id TEXT NOT NULL REFERENCES notify_destinations(id) ON DELETE CASCADE,
  PRIMARY KEY (route_id, destination_id)
);
CREATE INDEX notify_route_destinations_by_dest ON notify_route_destinations(destination_id);

INSERT INTO notify_routes (id, name, is_default, enabled, severities_json, recovery, reminder_minutes, created_at, updated_at)
VALUES ('nrt_default', 'Recommended', 1, 1, '["warning","critical"]', 1, 0, 0, 0);

-- The durable outbox: one row per (alert, destination, notification). It
-- is written in the transaction that changes the alert; workers claim rows
-- with a lease, so a controller crash at any point leaves the row to be
-- delivered (or re-delivered) after restart. dedupe_key makes queueing
-- idempotent.
CREATE TABLE notify_deliveries (
  id TEXT PRIMARY KEY,           -- rowid orders the queue (per alert and destination, sent in order)
  dedupe_key TEXT NOT NULL UNIQUE,
  alert_id TEXT REFERENCES alerts(id) ON DELETE CASCADE,
  destination_id TEXT REFERENCES notify_destinations(id) ON DELETE SET NULL,
  destination_name TEXT NOT NULL,
  destination_kind TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('opening','escalation','reminder','recovery')),
  recovery_wanted INTEGER NOT NULL DEFAULT 0,  -- the routes that chose this destination want recoveries
  message_json TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('queued','sending','delivered','failed','cancelled')),
  attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_at INTEGER NOT NULL,
  lease_until INTEGER,
  created_at INTEGER NOT NULL,
  finished_at INTEGER,
  last_error TEXT NOT NULL DEFAULT '',
  last_category TEXT NOT NULL DEFAULT '',
  last_status_code INTEGER,
  ambiguous INTEGER NOT NULL DEFAULT 0       -- an attempt may have reached the receiver
);
CREATE INDEX notify_deliveries_due ON notify_deliveries(status, next_attempt_at);
CREATE INDEX notify_deliveries_alert ON notify_deliveries(alert_id, destination_id);
CREATE INDEX notify_deliveries_dest ON notify_deliveries(destination_id, created_at);
CREATE INDEX notify_deliveries_created ON notify_deliveries(created_at, id);

CREATE TABLE notify_attempts (
  delivery_id TEXT NOT NULL REFERENCES notify_deliveries(id) ON DELETE CASCADE,
  n INTEGER NOT NULL,
  started_at INTEGER NOT NULL,
  finished_at INTEGER,
  outcome TEXT NOT NULL,           -- delivered | transient | permanent | interrupted
  category TEXT NOT NULL DEFAULT '',
  status_code INTEGER,
  error TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (delivery_id, n)
) WITHOUT ROWID;

-- The controller's event timeline (DESIGN 22.8): important lifecycle
-- events only, never each delivery retry.
CREATE TABLE events (
  id INTEGER PRIMARY KEY,
  ts INTEGER NOT NULL,
  kind TEXT NOT NULL,
  severity TEXT NOT NULL DEFAULT 'info' CHECK (severity IN ('info','warning','critical')),
  host_id TEXT,
  object_type TEXT NOT NULL DEFAULT '',
  object_id TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL
);
CREATE INDEX events_ts ON events(ts);
CREATE INDEX events_host ON events(host_id, ts);
