-- name: GetSetting :one
SELECT * FROM settings WHERE key = ?;

-- name: ListSettings :many
SELECT * FROM settings ORDER BY key;

-- name: UpsertSetting :exec
INSERT INTO settings (key, value_json, updated_by, updated_at) VALUES (?, ?, ?, ?)
ON CONFLICT (key) DO UPDATE SET value_json = excluded.value_json,
  updated_by = excluded.updated_by, updated_at = excluded.updated_at;
