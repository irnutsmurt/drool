-- Final After-M17 batch: classifications of observed network traffic
-- (ARCHITECTURE-ALPHA §38). A rule says what recurring traffic is ("NFS
-- media storage") and whether it is expected, matching flows by what DROOL
-- observes: the host (or a pack), the direction, the protocol, the service
-- port and the remote side (a DROOL host, a pack's hosts, or an address or
-- CIDR); an empty field matches anything. "flow" rules were made from one
-- observed connection ("manually labeled"). Classification is computed when
-- flows are read, so a rule applies to traffic already seen and seen later.
-- It enforces nothing: no firewall rule comes from it.
CREATE TABLE net_flow_rules (
  id TEXT PRIMARY KEY,
  label TEXT NOT NULL CHECK (length(label) BETWEEN 1 AND 120),
  note TEXT NOT NULL DEFAULT '',
  expected INTEGER NOT NULL DEFAULT 0 CHECK (expected IN (0, 1)),
  origin TEXT NOT NULL DEFAULT 'rule' CHECK (origin IN ('rule', 'flow')),
  host_id TEXT REFERENCES hosts(id) ON DELETE CASCADE,
  -- No cascade: a pack named here can't be deleted (Pack deletion lists it).
  host_group_id TEXT REFERENCES groups(id),
  direction TEXT NOT NULL DEFAULT '' CHECK (direction IN ('', 'in', 'out')),
  protocol TEXT NOT NULL DEFAULT '' CHECK (protocol IN ('', 'tcp', 'udp')),
  port INTEGER NOT NULL DEFAULT 0 CHECK (port BETWEEN 0 AND 65535),
  remote_host_id TEXT REFERENCES hosts(id) ON DELETE CASCADE,
  remote_group_id TEXT REFERENCES groups(id),
  remote_cidr TEXT NOT NULL DEFAULT '',
  created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_at INTEGER NOT NULL,
  updated_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  updated_at INTEGER NOT NULL,
  CHECK (host_id IS NULL OR host_group_id IS NULL),
  CHECK ((remote_host_id IS NOT NULL) + (remote_group_id IS NOT NULL) + (remote_cidr != '') <= 1)
);
CREATE INDEX net_flow_rules_group ON net_flow_rules(host_group_id) WHERE host_group_id IS NOT NULL;
CREATE INDEX net_flow_rules_remote_group ON net_flow_rules(remote_group_id) WHERE remote_group_id IS NOT NULL;
