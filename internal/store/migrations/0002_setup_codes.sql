-- First-admin bootstrap ("claim") codes. Only hashes are stored.
-- All rows are deleted when the first administrator exists.
CREATE TABLE setup_codes (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL CHECK (kind IN ('generated','provided')),
  code_hash BLOB NOT NULL,                  -- sha256 of the normalized code
  created_at INTEGER NOT NULL,
  expires_at INTEGER                        -- NULL = no expiry (provided secrets)
);
