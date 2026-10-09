-- M12.1: monitoring health overrides.
--
-- M11's evaluator judges every host against the Recommended health
-- profile. Overrides change that judgement at four scopes, most specific
-- first: a subject (one filesystem, disk, GPU, sensor, interface, service
-- or probe target of one host), the host, its groups, the fleet (global).
-- Each row stores only the fields it overrides (fields_json); a field that
-- is absent is inherited from the next less specific scope. This is health
-- evaluation, not notification routing (M12 routes are separate).

CREATE TABLE mon_health_overrides (
  id TEXT PRIMARY KEY,
  scope TEXT NOT NULL CHECK (scope IN ('global','group','host')),
  group_id TEXT REFERENCES groups(id) ON DELETE CASCADE,
  host_id TEXT REFERENCES hosts(id) ON DELETE CASCADE,
  check_id TEXT NOT NULL,
  -- '' = the whole check at this scope; otherwise a stable subject
  -- identity ("fsuuid:...", "mount:/srv", "disk:wwn-...", "gpu:uuid:GPU-...",
  -- "sensor:...", "iface:eth0", "unit:nginx.service"); host scope only.
  subject TEXT NOT NULL DEFAULT '',
  subject_label TEXT NOT NULL DEFAULT '',  -- what the subject was called when last seen
  fields_json TEXT NOT NULL,               -- only the overridden fields
  created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_at INTEGER NOT NULL,
  updated_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  updated_at INTEGER NOT NULL,
  CHECK ((scope = 'global' AND group_id IS NULL AND host_id IS NULL AND subject = '')
      OR (scope = 'group' AND group_id IS NOT NULL AND host_id IS NULL AND subject = '')
      OR (scope = 'host' AND host_id IS NOT NULL AND group_id IS NULL))
);
CREATE UNIQUE INDEX mon_health_overrides_key ON mon_health_overrides(scope, ifnull(group_id, ''), ifnull(host_id, ''), check_id, subject);
CREATE INDEX mon_health_overrides_host ON mon_health_overrides(host_id) WHERE host_id IS NOT NULL;
CREATE INDEX mon_health_overrides_group ON mon_health_overrides(group_id) WHERE group_id IS NOT NULL;

-- A group's precedence when a host is in several groups that override the
-- same field: higher wins; equal priorities with different values are a
-- visible conflict, never resolved by name, id or order.
CREATE TABLE mon_group_health (
  group_id TEXT PRIMARY KEY REFERENCES groups(id) ON DELETE CASCADE,
  priority INTEGER NOT NULL DEFAULT 0 CHECK (priority BETWEEN -1000 AND 1000),
  updated_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  updated_at INTEGER NOT NULL
);

-- A generation counter the evaluator compares to notice configuration
-- changes made anywhere (override edits, group membership from the groups
-- page, agent enrollment or the firewall designer, group deletion).
CREATE TABLE mon_health_gen (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  gen INTEGER NOT NULL
);
INSERT INTO mon_health_gen (id, gen) VALUES (1, 0);

CREATE TRIGGER mon_health_gen_ovr_ins AFTER INSERT ON mon_health_overrides BEGIN UPDATE mon_health_gen SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER mon_health_gen_ovr_upd AFTER UPDATE ON mon_health_overrides BEGIN UPDATE mon_health_gen SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER mon_health_gen_ovr_del AFTER DELETE ON mon_health_overrides BEGIN UPDATE mon_health_gen SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER mon_health_gen_prio_ins AFTER INSERT ON mon_group_health BEGIN UPDATE mon_health_gen SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER mon_health_gen_prio_upd AFTER UPDATE ON mon_group_health BEGIN UPDATE mon_health_gen SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER mon_health_gen_prio_del AFTER DELETE ON mon_group_health BEGIN UPDATE mon_health_gen SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER mon_health_gen_mem_ins AFTER INSERT ON group_members BEGIN UPDATE mon_health_gen SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER mon_health_gen_mem_del AFTER DELETE ON group_members BEGIN UPDATE mon_health_gen SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER mon_health_gen_grp_del AFTER DELETE ON groups BEGIN UPDATE mon_health_gen SET gen = gen + 1 WHERE id = 1; END;

-- Why an issue ended: the condition recovered, its check was disabled,
-- the configuration changed so the current state no longer qualifies, or
-- the subject (a filesystem, a disk) disappeared. M12 keeps the reason.
ALTER TABLE mon_issues ADD COLUMN resolution TEXT;
ALTER TABLE mon_issues ADD COLUMN resolution_note TEXT NOT NULL DEFAULT '';
ALTER TABLE alerts ADD COLUMN resolution TEXT;
ALTER TABLE alerts ADD COLUMN resolution_note TEXT NOT NULL DEFAULT '';
