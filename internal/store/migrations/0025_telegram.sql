-- Final After-M17 batch: Telegram as a notification destination
-- (ARCHITECTURE-ALPHA §38). SQLite can't change a CHECK in place, so
-- notify_destinations is rebuilt. Migrations run in a transaction with
-- foreign keys on, where dropping the table would remove the routes that
-- use each destination (ON DELETE CASCADE) and unlink its deliveries (SET
-- NULL): both are kept aside first and put back after.
CREATE TEMP TABLE keep_route_destinations AS SELECT route_id, destination_id FROM notify_route_destinations;
CREATE TEMP TABLE keep_delivery_destinations AS SELECT id, destination_id FROM notify_deliveries WHERE destination_id IS NOT NULL;

CREATE TABLE notify_destinations_new (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE COLLATE NOCASE,
  kind TEXT NOT NULL CHECK (kind IN ('email','webhook','discord','slack','telegram')),
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
INSERT INTO notify_destinations_new SELECT * FROM notify_destinations;
DROP TABLE notify_destinations;
ALTER TABLE notify_destinations_new RENAME TO notify_destinations;

INSERT OR IGNORE INTO notify_route_destinations (route_id, destination_id) SELECT route_id, destination_id FROM keep_route_destinations;
UPDATE notify_deliveries SET destination_id = (SELECT k.destination_id FROM keep_delivery_destinations k WHERE k.id = notify_deliveries.id)
  WHERE id IN (SELECT id FROM keep_delivery_destinations);
DROP TABLE keep_route_destinations;
DROP TABLE keep_delivery_destinations;
