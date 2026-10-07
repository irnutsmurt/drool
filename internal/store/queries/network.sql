-- M10: network observations (fw_observations) and collection status.

-- name: UpsertFwObservation :exec
INSERT INTO fw_observations (host_id, direction, protocol, local_ip, port, peer, local_port, remote_port, scope, source, process, pid, count, first_seen, last_seen)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (host_id, direction, protocol, local_ip, port, peer) DO UPDATE SET
  -- A snapshot sees connections that are open right now: it can't add to
  -- a count, only raise it. Continuous sources report new connections.
  count = CASE WHEN excluded.source = 'snapshot' THEN max(fw_observations.count, excluded.count) ELSE fw_observations.count + excluded.count END,
  -- The best source that ever saw the flow: conntrack > sockets > snapshot.
  source = CASE
    WHEN fw_observations.source = 'conntrack' OR excluded.source = 'conntrack' THEN 'conntrack'
    WHEN fw_observations.source = 'sockets' OR excluded.source = 'sockets' THEN 'sockets'
    ELSE 'snapshot' END,
  first_seen = min(fw_observations.first_seen, excluded.first_seen),
  last_seen = max(fw_observations.last_seen, excluded.last_seen),
  local_port = CASE WHEN excluded.last_seen >= fw_observations.last_seen AND excluded.local_port > 0 THEN excluded.local_port ELSE fw_observations.local_port END,
  remote_port = CASE WHEN excluded.last_seen >= fw_observations.last_seen AND excluded.remote_port > 0 THEN excluded.remote_port ELSE fw_observations.remote_port END,
  process = CASE WHEN excluded.process != '' AND excluded.last_seen >= fw_observations.last_seen THEN excluded.process
    WHEN fw_observations.process = '' THEN excluded.process ELSE fw_observations.process END,
  pid = CASE WHEN excluded.process != '' AND excluded.last_seen >= fw_observations.last_seen THEN excluded.pid
    WHEN fw_observations.process = '' THEN excluded.pid ELSE fw_observations.pid END,
  scope = excluded.scope;

-- name: ListFwObservations :many
SELECT * FROM fw_observations WHERE host_id = ? ORDER BY count DESC, last_seen DESC LIMIT ?;

-- name: GetFwObservation :one
SELECT * FROM fw_observations WHERE id = ?;

-- name: DeleteFwObservations :exec
DELETE FROM fw_observations WHERE host_id = ?;

-- name: PruneFwObservations :execrows
DELETE FROM fw_observations WHERE id IN (SELECT o.id FROM fw_observations o WHERE o.last_seen < ? LIMIT ?);

-- name: CountObservationsByHost :many
SELECT host_id, count(*) AS n FROM fw_observations GROUP BY host_id;

-- name: TrimFwObservations :execrows
DELETE FROM fw_observations WHERE id IN (
  SELECT o.id FROM fw_observations o WHERE o.host_id = ? ORDER BY o.last_seen ASC, o.id ASC LIMIT ?);

-- name: ListObservationPeers :many
SELECT DISTINCT peer, scope FROM fw_observations;

-- name: SetObservationScope :exec
UPDATE fw_observations SET scope = ? WHERE peer = ? AND scope != ?;

-- name: CountFwObservations :one
SELECT count(*) FROM fw_observations;

-- name: HostFirstSeenPeer :one
-- When this host first talked to this remote address (any port, either
-- direction), within the retained history.
SELECT CAST(min(first_seen) AS INTEGER) FROM fw_observations WHERE host_id = ? AND peer = ?;

-- name: FleetFirstSeenPeer :one
SELECT CAST(min(first_seen) AS INTEGER) FROM fw_observations WHERE peer = ?;

-- name: UpsertNetCollector :exec
INSERT INTO net_collectors (host_id, source, detail, sample_seconds, events_lost, dropped, forwarded, reported_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (host_id) DO UPDATE SET source = excluded.source, detail = excluded.detail, sample_seconds = excluded.sample_seconds,
  events_lost = net_collectors.events_lost + excluded.events_lost, dropped = net_collectors.dropped + excluded.dropped,
  forwarded = excluded.forwarded, reported_at = excluded.reported_at;

-- name: SetNetSnapshotAt :exec
INSERT INTO net_collectors (host_id, snapshot_at) VALUES (?, ?)
ON CONFLICT (host_id) DO UPDATE SET snapshot_at = excluded.snapshot_at;

-- name: GetNetCollector :one
SELECT * FROM net_collectors WHERE host_id = ?;

-- name: ListNetCollectors :many
SELECT * FROM net_collectors;
