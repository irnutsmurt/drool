-- name: AuditChainHead :one
-- The newest link of the audit hash chain (ARCHITECTURE-ALPHA 33.4).
SELECT seq, hash FROM audit_events WHERE seq IS NOT NULL ORDER BY seq DESC LIMIT 1;

-- name: InsertChainedAuditEvent :exec
-- Every audit record is written through audit.Writer, which links it into
-- the chain; there is deliberately no query that inserts an unchained record.
INSERT INTO audit_events (id, ts, actor_type, actor_id, actor_name, action, target_type, target_id,
                          result, source_ip, request_id, details_json, seq, prev_hash, hash)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListAuditEvents :many
-- Cursor pagination: pass the last seen id as before_id ('' for the first page).
SELECT * FROM audit_events
WHERE (CAST(sqlc.arg(before_id) AS TEXT) = '' OR id < CAST(sqlc.arg(before_id) AS TEXT))
  AND (CAST(sqlc.arg(actor_id) AS TEXT) = '' OR actor_id = CAST(sqlc.arg(actor_id) AS TEXT))
  AND (CAST(sqlc.arg(action_prefix) AS TEXT) = '' OR action LIKE CAST(sqlc.arg(action_prefix) AS TEXT) || '%')
  AND (CAST(sqlc.arg(target_id) AS TEXT) = '' OR target_id = CAST(sqlc.arg(target_id) AS TEXT))
  AND ts >= sqlc.arg(since)
ORDER BY id DESC
LIMIT sqlc.arg(lim);

-- name: ListUnchainedAuditEvents :many
-- Records written before the chain existed (the first link's digest covers them).
SELECT * FROM audit_events WHERE seq IS NULL ORDER BY id;

-- name: GetAuditEventBySeq :one
SELECT * FROM audit_events WHERE seq = ?;
