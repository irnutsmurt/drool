-- Final After-M17 batch: the expected state of a service (running,
-- stopped, ignored) is a health setting like the others: per host, and as
-- a default for a pack or for every host, with the same inheritance and
-- provenance (ARCHITECTURE-ALPHA §38). A unit name means the same thing on
-- every host, so service subjects may now be set at pack and global scope
-- too; other subjects (a filesystem's UUID, a disk) stay per host.
CREATE TABLE mon_health_overrides_v2 (
  id TEXT PRIMARY KEY,
  scope TEXT NOT NULL CHECK (scope IN ('global','group','host')),
  group_id TEXT REFERENCES groups(id) ON DELETE CASCADE,
  host_id TEXT REFERENCES hosts(id) ON DELETE CASCADE,
  check_id TEXT NOT NULL,
  -- '' = the whole check at this scope; otherwise a stable subject
  -- identity ("fsuuid:...", "mount:/srv", "disk:wwn-...", "gpu:uuid:GPU-...",
  -- "sensor:...", "iface:eth0", "unit:nginx.service"). Pack and global
  -- scope: services ("unit:...") only.
  subject TEXT NOT NULL DEFAULT '',
  subject_label TEXT NOT NULL DEFAULT '',
  fields_json TEXT NOT NULL,
  created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_at INTEGER NOT NULL,
  updated_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  updated_at INTEGER NOT NULL,
  CHECK ((scope = 'global' AND group_id IS NULL AND host_id IS NULL)
      OR (scope = 'group' AND group_id IS NOT NULL AND host_id IS NULL)
      OR (scope = 'host' AND host_id IS NOT NULL AND group_id IS NULL)),
  CHECK (subject = '' OR scope = 'host' OR subject LIKE 'unit:%')
);
INSERT INTO mon_health_overrides_v2 (id, scope, group_id, host_id, check_id, subject, subject_label, fields_json, created_by, created_at, updated_by, updated_at)
SELECT id, scope, group_id, host_id, check_id, subject, subject_label, fields_json, created_by, created_at, updated_by, updated_at FROM mon_health_overrides;
DROP TABLE mon_health_overrides;
ALTER TABLE mon_health_overrides_v2 RENAME TO mon_health_overrides;
CREATE UNIQUE INDEX mon_health_overrides_key ON mon_health_overrides(scope, ifnull(group_id, ''), ifnull(host_id, ''), check_id, subject);
CREATE INDEX mon_health_overrides_host ON mon_health_overrides(host_id) WHERE host_id IS NOT NULL;
CREATE INDEX mon_health_overrides_group ON mon_health_overrides(group_id) WHERE group_id IS NOT NULL;
CREATE TRIGGER mon_health_gen_ovr_ins AFTER INSERT ON mon_health_overrides BEGIN UPDATE mon_health_gen SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER mon_health_gen_ovr_upd AFTER UPDATE ON mon_health_overrides BEGIN UPDATE mon_health_gen SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER mon_health_gen_ovr_del AFTER DELETE ON mon_health_overrides BEGIN UPDATE mon_health_gen SET gen = gen + 1 WHERE id = 1; END;

-- A watched service was one expected to run: each becomes that setting on
-- its host, and the watch list goes.
INSERT OR IGNORE INTO mon_health_overrides (id, scope, host_id, check_id, subject, subject_label, fields_json, created_at, updated_at)
SELECT 'hov_' || lower(hex(randomblob(13))), 'host', s.host_id, 'service_expected', 'unit:' || j.value, j.value, '{"expected":"running"}',
  CAST(strftime('%s', 'now') AS INTEGER) * 1000, CAST(strftime('%s', 'now') AS INTEGER) * 1000
FROM mon_host_settings s, json_each(s.settings_json, '$.watched_services') j
WHERE json_valid(s.settings_json) AND j.type = 'text' AND j.value != '';
UPDATE mon_host_settings SET settings_json = json_remove(settings_json, '$.watched_services')
WHERE json_valid(settings_json) AND json_type(settings_json, '$.watched_services') IS NOT NULL;
