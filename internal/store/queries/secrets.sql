-- name: InsertSecret :exec
INSERT INTO secrets (id, name, kind, ciphertext, nonce, key_version, public_part, description, created_by, created_at, updated_at, temporary)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetSecret :one
SELECT * FROM secrets WHERE id = ?;

-- name: GetSecretByName :one
SELECT * FROM secrets WHERE name = ?;

-- name: ListSecrets :many
SELECT * FROM secrets ORDER BY name;

-- name: UpdateSecretMeta :exec
UPDATE secrets SET name = ?, description = ?, updated_at = ? WHERE id = ?;

-- name: UpdateSecretValue :exec
UPDATE secrets SET ciphertext = ?, nonce = ?, key_version = ?, public_part = ?, updated_at = ? WHERE id = ?;

-- name: DeleteSecret :exec
DELETE FROM secrets WHERE id = ?;

-- name: ListHostsUsingSecret :many
SELECT id, name FROM hosts
WHERE ssh_credential_id = CAST(sqlc.arg(id) AS TEXT) OR become_credential_id = CAST(sqlc.arg(id) AS TEXT)
ORDER BY name;
