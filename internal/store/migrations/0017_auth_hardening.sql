-- M16: re-authentication for high-impact actions and TOTP two-factor
-- authentication (ARCHITECTURE-ALPHA §33.3).

-- When the session's user last proved who they are (password, plus the
-- second factor when enrolled). Signing in counts. High-impact actions need
-- this to be recent; the check is on the server.
ALTER TABLE sessions ADD COLUMN reauth_at INTEGER;
UPDATE sessions SET reauth_at = created_at;

-- One TOTP authenticator per user (RFC 6238: SHA-1, 6 digits, 30 s).
CREATE TABLE user_mfa (
  user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  secret_ciphertext BLOB NOT NULL,          -- sealed with the master key (AAD: user id, 'mfa-totp')
  secret_nonce BLOB NOT NULL,
  secret_key_version INTEGER NOT NULL,
  enabled_at INTEGER,                       -- NULL until the user confirmed a code from the new authenticator
  last_step INTEGER NOT NULL DEFAULT 0,     -- newest accepted time step: each code works once
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

-- Single-use recovery codes, stored as SHA-256 of the code (each code has
-- 80 random bits, so a fast hash is enough; the codes are shown once).
CREATE TABLE user_recovery_codes (
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  code_hash BLOB NOT NULL,
  created_at INTEGER NOT NULL,
  used_at INTEGER,
  PRIMARY KEY (user_id, code_hash)
);
