-- name: CreateUser :exec
INSERT INTO users (id, username, password_hash, role, disabled, created_at, updated_at, password_changed_at)
VALUES (?, ?, ?, ?, 0, ?, ?, ?);

-- name: GetUser :one
SELECT * FROM users WHERE id = ?;

-- name: GetUserByUsername :one
SELECT * FROM users WHERE username = ?;

-- name: ListUsers :many
SELECT * FROM users ORDER BY username;

-- name: CountUsers :one
SELECT count(*) FROM users;

-- name: CountActiveAdmins :one
SELECT count(*) FROM users WHERE role = 'admin' AND disabled = 0;

-- name: UpdateUserRole :exec
UPDATE users SET role = ?, updated_at = ? WHERE id = ?;

-- name: UpdateUserDisabled :exec
UPDATE users SET disabled = ?, updated_at = ? WHERE id = ?;

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = ?, password_changed_at = ?, updated_at = ? WHERE id = ?;

-- name: TouchUserLogin :exec
UPDATE users SET last_login_at = ? WHERE id = ?;

-- name: DeleteUser :exec
DELETE FROM users WHERE id = ?;
