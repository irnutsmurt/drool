-- name: InsertAuditEvent :exec
INSERT INTO audit_events (id, ts, actor_type, actor_id, actor_name, action, target_type, target_id,
                          result, source_ip, request_id, details_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

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
