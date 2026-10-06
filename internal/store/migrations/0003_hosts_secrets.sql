-- M1: credentials (encrypted), hosts, management hops, pinned SSH host keys,
-- addresses, tags and static groups.

CREATE TABLE secrets (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE COLLATE NOCASE,
  kind TEXT NOT NULL CHECK (kind IN ('ssh_private_key','password','sudo_password')),
  ciphertext BLOB NOT NULL,                 -- AES-256-GCM; AAD = id + kind
  nonce BLOB NOT NULL,
  key_version INTEGER NOT NULL,
  public_part TEXT NOT NULL DEFAULT '',     -- e.g. SSH public key; safe to display
  description TEXT NOT NULL DEFAULT '',
  created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE hosts (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE COLLATE NOCASE,
  description TEXT NOT NULL DEFAULT '',
  connect_address TEXT NOT NULL,            -- hostname or IP used to dial
  management_method TEXT NOT NULL DEFAULT 'ssh' CHECK (management_method IN ('ssh','agent')),
  ssh_port INTEGER NOT NULL DEFAULT 22 CHECK (ssh_port BETWEEN 1 AND 65535),
  ssh_user TEXT NOT NULL,
  ssh_credential_id TEXT REFERENCES secrets(id),
  become_method TEXT NOT NULL DEFAULT 'sudo' CHECK (become_method IN ('none','sudo')),
  become_credential_id TEXT REFERENCES secrets(id),   -- NULL = passwordless sudo
  ssh_needs_pty INTEGER NOT NULL DEFAULT 0,
  jump_only INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'pending_hostkey'
    CHECK (status IN ('pending_hostkey','untested','ready','unreachable','hostkey_mismatch',
                      'auth_failed','unsupported','disabled')),
  status_detail TEXT NOT NULL DEFAULT '',
  last_probe_at INTEGER,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

-- Multi-hop in the model; the alpha allows at most one hop (ARCHITECTURE-ALPHA §8.1).
CREATE TABLE host_hops (
  host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  hop_index INTEGER NOT NULL,
  via_host_id TEXT NOT NULL REFERENCES hosts(id),
  PRIMARY KEY (host_id, hop_index)
);
CREATE INDEX host_hops_by_via ON host_hops(via_host_id);

CREATE TABLE ssh_host_keys (
  id TEXT PRIMARY KEY,
  host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  key_type TEXT NOT NULL,                   -- ssh-ed25519, ecdsa-sha2-nistp256, ssh-rsa, ...
  public_key BLOB NOT NULL,                 -- SSH wire format
  fingerprint_sha256 TEXT NOT NULL,         -- "SHA256:..."
  status TEXT NOT NULL CHECK (status IN ('offered','pinned','retired')),
  first_seen_at INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  decided_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  decided_at INTEGER
);
CREATE UNIQUE INDEX one_pinned_key ON ssh_host_keys(host_id) WHERE status = 'pinned';
CREATE UNIQUE INDEX host_key_unique ON ssh_host_keys(host_id, fingerprint_sha256, status);

CREATE TABLE host_addresses (
  host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  address TEXT NOT NULL,
  family INTEGER NOT NULL CHECK (family IN (4,6)),
  interface TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL CHECK (source IN ('configured','discovered','dns')),
  first_seen_at INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  PRIMARY KEY (host_id, address)
);

CREATE TABLE host_tags (
  host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  tag TEXT NOT NULL,                        -- "key:value" or bare "key"
  PRIMARY KEY (host_id, tag)
);
CREATE INDEX host_tags_by_tag ON host_tags(tag);

CREATE TABLE groups (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE COLLATE NOCASE,
  description TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE group_members (
  group_id TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
  host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  PRIMARY KEY (group_id, host_id)
);
CREATE INDEX group_members_by_host ON group_members(host_id);
