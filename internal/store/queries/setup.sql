-- name: CountAdmins :one
-- Any admin, enabled or not: once one exists, setup is closed.
SELECT count(*) FROM users WHERE role = 'admin';

-- name: InsertSetupCode :exec
INSERT INTO setup_codes (id, kind, code_hash, created_at, expires_at) VALUES (?, ?, ?, ?, ?);

-- name: DeleteSetupCodesByKind :exec
DELETE FROM setup_codes WHERE kind = ?;

-- name: DeleteAllSetupCodes :exec
DELETE FROM setup_codes;

-- name: ListValidSetupCodes :many
SELECT * FROM setup_codes WHERE expires_at IS NULL OR expires_at > ?;

-- name: CountSetupCodes :one
SELECT count(*) FROM setup_codes;
