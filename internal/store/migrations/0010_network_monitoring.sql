-- M10: network connectivity monitoring. The M8 observation table is
-- rebuilt rather than joined by a second one: one flow per host, direction,
-- protocol, local address, service port and remote address, now with the
-- last ports seen, the owning process, the remote address's scope
-- (internal/public/special, from internal_ranges) and how it was
-- collected. Ephemeral client ports are details, not identity, so the
-- table grows with distinct peers, not with connections; retention and a
-- per-host cap bound it (network settings).
CREATE TABLE fw_observations_m10 (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  direction TEXT NOT NULL CHECK (direction IN ('in','out')),
  protocol TEXT NOT NULL CHECK (protocol IN ('tcp','udp')),
  local_ip TEXT NOT NULL DEFAULT '',
  port INTEGER NOT NULL CHECK (port BETWEEN 1 AND 65535),
  peer TEXT NOT NULL,
  local_port INTEGER NOT NULL DEFAULT 0 CHECK (local_port BETWEEN 0 AND 65535),
  remote_port INTEGER NOT NULL DEFAULT 0 CHECK (remote_port BETWEEN 0 AND 65535),
  scope TEXT NOT NULL DEFAULT 'public' CHECK (scope IN ('internal','public','special')),
  source TEXT NOT NULL CHECK (source IN ('conntrack','sockets','snapshot')),
  process TEXT NOT NULL DEFAULT '',
  pid INTEGER NOT NULL DEFAULT 0,
  count INTEGER NOT NULL CHECK (count >= 0),
  first_seen INTEGER NOT NULL,
  last_seen INTEGER NOT NULL,
  UNIQUE (host_id, direction, protocol, local_ip, port, peer)
);
INSERT INTO fw_observations_m10 (host_id, direction, protocol, port, peer, source, count, first_seen, last_seen)
SELECT host_id, direction, protocol, port, peer, CASE source WHEN 'agent' THEN 'sockets' ELSE 'snapshot' END, count, first_seen, last_seen
FROM fw_observations WHERE protocol IN ('tcp','udp') AND port BETWEEN 1 AND 65535;
DROP TABLE fw_observations;
ALTER TABLE fw_observations_m10 RENAME TO fw_observations;
CREATE INDEX fw_observations_by_host ON fw_observations(host_id, last_seen);
CREATE INDEX fw_observations_by_last ON fw_observations(last_seen);
CREATE INDEX fw_observations_by_first ON fw_observations(first_seen);
CREATE INDEX fw_observations_by_peer ON fw_observations(peer);
CREATE INDEX fw_observations_by_port ON fw_observations(port);

-- How each host is observed: the agent's continuous source (conntrack or
-- socket sampling, with its explanation) and the last SSH snapshot.
CREATE TABLE net_collectors (
  host_id TEXT PRIMARY KEY REFERENCES hosts(id) ON DELETE CASCADE,
  source TEXT NOT NULL DEFAULT '',
  detail TEXT NOT NULL DEFAULT '',
  sample_seconds INTEGER NOT NULL DEFAULT 0,
  events_lost INTEGER NOT NULL DEFAULT 0,
  dropped INTEGER NOT NULL DEFAULT 0,
  forwarded INTEGER NOT NULL DEFAULT 0,
  reported_at INTEGER,
  snapshot_at INTEGER
);
