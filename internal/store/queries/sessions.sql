-- name: CreateSession :exec
INSERT INTO sessions (token_hash, user_id, csrf_token, created_at, last_seen_at, expires_at, source_ip, user_agent, reauth_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: SetSessionReauth :exec
UPDATE sessions SET reauth_at = ? WHERE token_hash = ?;

-- name: GetSessionWithUser :one
SELECT sqlc.embed(sessions), sqlc.embed(users)
FROM sessions JOIN users ON users.id = sessions.user_id
WHERE sessions.token_hash = ?;

-- name: TouchSession :exec
UPDATE sessions SET last_seen_at = ? WHERE token_hash = ?;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE token_hash = ?;

-- name: DeleteUserSessions :exec
DELETE FROM sessions WHERE user_id = ?;

-- name: DeleteUserSessionsExcept :exec
DELETE FROM sessions WHERE user_id = ? AND token_hash != ?;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE expires_at < ? OR last_seen_at < ?;
