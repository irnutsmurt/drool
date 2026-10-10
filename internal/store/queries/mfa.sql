-- name: GetUserMFA :one
SELECT * FROM user_mfa WHERE user_id = ?;

-- name: UpsertPendingMFA :exec
-- A new, unconfirmed authenticator (replaces an unconfirmed one).
INSERT INTO user_mfa (user_id, secret_ciphertext, secret_nonce, secret_key_version, enabled_at, last_step, created_at, updated_at)
VALUES (?, ?, ?, ?, NULL, 0, ?, ?)
ON CONFLICT (user_id) DO UPDATE SET secret_ciphertext = excluded.secret_ciphertext, secret_nonce = excluded.secret_nonce,
  secret_key_version = excluded.secret_key_version, enabled_at = NULL, last_step = 0, updated_at = excluded.updated_at
WHERE user_mfa.enabled_at IS NULL;

-- name: EnableMFA :execrows
UPDATE user_mfa SET enabled_at = ?, last_step = ?, updated_at = ? WHERE user_id = ? AND enabled_at IS NULL;

-- name: UseTOTPStep :execrows
-- Accept a code's time step only once, and only moving forward.
UPDATE user_mfa SET last_step = ?, updated_at = ? WHERE user_id = ? AND last_step < ?;

-- name: DeleteUserMFA :execrows
DELETE FROM user_mfa WHERE user_id = ?;

-- name: DeleteRecoveryCodes :exec
DELETE FROM user_recovery_codes WHERE user_id = ?;

-- name: InsertRecoveryCode :exec
INSERT INTO user_recovery_codes (user_id, code_hash, created_at) VALUES (?, ?, ?);

-- name: UseRecoveryCode :execrows
UPDATE user_recovery_codes SET used_at = ? WHERE user_id = ? AND code_hash = ? AND used_at IS NULL;

-- name: CountUnusedRecoveryCodes :one
SELECT count(*) FROM user_recovery_codes WHERE user_id = ? AND used_at IS NULL;

-- name: ListMFAEnabledUsers :many
SELECT user_id FROM user_mfa WHERE enabled_at IS NOT NULL;
