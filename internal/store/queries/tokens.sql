-- name: CreateAPIToken :exec
INSERT INTO api_tokens (id, user_id, name, token_hash, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?);

-- name: GetAPITokenWithUser :one
SELECT sqlc.embed(api_tokens), sqlc.embed(users)
FROM api_tokens JOIN users ON users.id = api_tokens.user_id
WHERE api_tokens.id = ?;

-- name: ListAPITokens :many
SELECT * FROM api_tokens WHERE user_id = ? ORDER BY created_at DESC;

-- name: GetAPIToken :one
SELECT * FROM api_tokens WHERE id = ?;

-- name: TouchAPIToken :exec
UPDATE api_tokens SET last_used_at = ? WHERE id = ?;

-- name: DeleteAPIToken :exec
DELETE FROM api_tokens WHERE id = ?;

-- name: DeleteUserAPITokens :exec
DELETE FROM api_tokens WHERE user_id = ?;
