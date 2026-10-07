-- M7: DROOL agent, agent PKI, enrollment, identities, state versions,
-- signed agent releases and update rollouts (ARCHITECTURE-ALPHA §23).

-- Agent PKI material. Private keys are sealed with the master key
-- (AES-256-GCM, AAD = id + kind). The root key is NULL once exported and
-- removed from the controller: it then never reappears in backups.
CREATE TABLE agent_ca (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL CHECK (kind IN ('root','intermediate')),
  cert_der BLOB NOT NULL,
  fingerprint TEXT NOT NULL UNIQUE,
  key_ciphertext BLOB,
  key_nonce BLOB,
  key_version INTEGER,
  status TEXT NOT NULL CHECK (status IN ('active','retired','revoked')),
  not_after INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  key_removed_at INTEGER
);
CREATE UNIQUE INDEX agent_ca_one_root ON agent_ca(kind) WHERE kind = 'root';
CREATE UNIQUE INDEX agent_ca_one_active_intermediate ON agent_ca(kind) WHERE kind = 'intermediate' AND status = 'active';

-- Root-signed trust manifests (DESIGN §4.3.1). Append-only; the newest is
-- distributed to agents.
CREATE TABLE agent_trust_manifests (
  version INTEGER PRIMARY KEY,
  payload BLOB NOT NULL,
  signature BLOB NOT NULL,
  created_at INTEGER NOT NULL
);

-- Single-use enrollment tokens. Only a hash is stored.
CREATE TABLE agent_enrollment_tokens (
  id TEXT PRIMARY KEY,
  token_hash BLOB NOT NULL UNIQUE,
  description TEXT NOT NULL DEFAULT '',
  host_id TEXT REFERENCES hosts(id) ON DELETE CASCADE,     -- bound to an existing host (Install agent)
  group_id TEXT REFERENCES groups(id) ON DELETE CASCADE,   -- new hosts are created in this group
  preset TEXT NOT NULL DEFAULT '',                         -- the suggested ceiling, informational
  created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  used_at INTEGER,
  used_by_identity TEXT,
  revoked_at INTEGER
);

-- Agent identities: one long-lived Ed25519 key per enrolled agent.
CREATE TABLE agent_identities (
  id TEXT PRIMARY KEY,
  host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  public_key BLOB NOT NULL UNIQUE,
  fingerprint TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('active','quarantined','revoked')),
  enrolled_at INTEGER NOT NULL,
  token_id TEXT,
  cert_serial TEXT NOT NULL DEFAULT '',
  cert_not_after INTEGER NOT NULL DEFAULT 0,
  cert_issued_at INTEGER NOT NULL DEFAULT 0,
  recovered_at INTEGER,
  revoked_at INTEGER,
  revoked_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  revoke_reason TEXT NOT NULL DEFAULT '',
  quarantined_at INTEGER,
  conflict_json TEXT NOT NULL DEFAULT '',                  -- the sessions seen at the same time
  agent_version TEXT NOT NULL DEFAULT '',
  protocol INTEGER NOT NULL DEFAULT 0,                     -- negotiated
  protocol_min INTEGER NOT NULL DEFAULT 0,
  protocol_max INTEGER NOT NULL DEFAULT 0,
  compatible INTEGER NOT NULL DEFAULT 1,
  compat_reason TEXT NOT NULL DEFAULT '',
  hostname TEXT NOT NULL DEFAULT '',
  machine_id TEXT NOT NULL DEFAULT '',
  os TEXT NOT NULL DEFAULT '',
  arch TEXT NOT NULL DEFAULT '',
  ceiling_json TEXT NOT NULL DEFAULT '{}',                 -- as reported (authoritative on the host)
  effective_json TEXT NOT NULL DEFAULT '{}',
  manifest_version INTEGER NOT NULL DEFAULT 0,
  epoch_reported TEXT NOT NULL DEFAULT '',
  endpoints_json TEXT NOT NULL DEFAULT '[]',
  boot_id TEXT NOT NULL DEFAULT '',
  last_seen_at INTEGER,
  last_address TEXT NOT NULL DEFAULT '',
  connected INTEGER NOT NULL DEFAULT 0,                    -- reset at controller start
  update_state TEXT NOT NULL DEFAULT '',
  update_version TEXT NOT NULL DEFAULT '',
  update_detail TEXT NOT NULL DEFAULT '',
  update_at INTEGER
);
CREATE UNIQUE INDEX agent_identities_one_live_per_host ON agent_identities(host_id) WHERE status != 'revoked';

-- Management paths (DESIGN §8): management_method is the preferred path.
ALTER TABLE hosts ADD COLUMN management_fallback TEXT NOT NULL DEFAULT 'none' CHECK (management_fallback IN ('none','ssh'));
-- Agent-only hosts have no SSH configuration.
ALTER TABLE hosts ADD COLUMN ssh_configured INTEGER NOT NULL DEFAULT 1;
-- The controller's narrowing of the agent's ceiling ('' = no narrowing).
ALTER TABLE hosts ADD COLUMN requested_caps_json TEXT NOT NULL DEFAULT '';

-- Which management path ran each job host.
ALTER TABLE job_hosts ADD COLUMN transport TEXT NOT NULL DEFAULT '';
ALTER TABLE job_hosts ADD COLUMN transport_note TEXT NOT NULL DEFAULT '';

-- State versions per host and domain (DESIGN §9). Revisions are
-- controller-owned; reported_* is what the host last said it applied.
CREATE TABLE state_versions (
  host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  domain TEXT NOT NULL,
  epoch TEXT NOT NULL,
  revision INTEGER NOT NULL,
  content_hash TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL,
  reported_epoch TEXT NOT NULL DEFAULT '',
  reported_revision INTEGER NOT NULL DEFAULT 0,
  reported_hash TEXT NOT NULL DEFAULT '',
  reported_at INTEGER,
  status TEXT NOT NULL DEFAULT 'ok' CHECK (status IN ('ok','host_ahead','epoch_mismatch')),
  PRIMARY KEY (host_id, domain)
);

-- Agent observations are an address source of their own (DESIGN §5.2).
CREATE TABLE host_addresses_new (
  host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  address TEXT NOT NULL,
  family INTEGER NOT NULL CHECK (family IN (4,6)),
  interface TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL CHECK (source IN ('configured','discovered','dns','agent')),
  first_seen_at INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  PRIMARY KEY (host_id, address)
);
INSERT INTO host_addresses_new SELECT * FROM host_addresses;
DROP TABLE host_addresses;
ALTER TABLE host_addresses_new RENAME TO host_addresses;

-- Release-signing public keys the controller knows (for display and early
-- checks only: agents trust their own local key list).
CREATE TABLE agent_release_keys (
  id TEXT PRIMARY KEY,                                     -- key id
  public_key BLOB NOT NULL,
  name TEXT NOT NULL DEFAULT '',
  added_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  added_at INTEGER NOT NULL
);

-- Signed agent releases; binaries live in the artifact store.
CREATE TABLE agent_releases (
  id TEXT PRIMARY KEY,
  version TEXT NOT NULL,
  channel TEXT NOT NULL,
  os TEXT NOT NULL,
  arch TEXT NOT NULL,
  protocol_min INTEGER NOT NULL,
  protocol_max INTEGER NOT NULL,
  sha256 TEXT NOT NULL REFERENCES artifacts(sha256),
  size INTEGER NOT NULL,
  key_id TEXT NOT NULL,
  manifest_payload BLOB NOT NULL,
  signature BLOB NOT NULL,
  uploaded_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_at INTEGER NOT NULL,
  UNIQUE (version, os, arch)
);

-- Agent update rollouts: canary, then bounded batches, pausing on failures.
CREATE TABLE agent_rollouts (
  id TEXT PRIMARY KEY,
  version TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('running','paused','completed','cancelled')),
  canary INTEGER NOT NULL,
  batch_size INTEGER NOT NULL,
  max_failures INTEGER NOT NULL,
  health_window_s INTEGER NOT NULL,
  failures INTEGER NOT NULL DEFAULT 0,
  paused_reason TEXT NOT NULL DEFAULT '',
  automatic INTEGER NOT NULL DEFAULT 0,
  created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE agent_rollout_hosts (
  rollout_id TEXT NOT NULL REFERENCES agent_rollouts(id) ON DELETE CASCADE,
  host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  wave INTEGER NOT NULL,                                   -- 0 = canary
  state TEXT NOT NULL CHECK (state IN ('pending','offered','staged','updating','healthy','rolled_back','failed','skipped')),
  from_version TEXT NOT NULL DEFAULT '',
  detail TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (rollout_id, host_id)
);

-- Controller endpoint migrations (DESIGN §6.5): candidate → tested by each
-- agent → committed only for agents that confirmed it.
CREATE TABLE agent_endpoint_migrations (
  id TEXT PRIMARY KEY,
  endpoint TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('testing','committed','cancelled')),
  created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_at INTEGER NOT NULL,
  committed_at INTEGER
);
CREATE TABLE agent_endpoint_tests (
  migration_id TEXT NOT NULL REFERENCES agent_endpoint_migrations(id) ON DELETE CASCADE,
  identity_id TEXT NOT NULL REFERENCES agent_identities(id) ON DELETE CASCADE,
  ok INTEGER NOT NULL,
  error TEXT NOT NULL DEFAULT '',
  tested_at INTEGER NOT NULL,
  committed INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (migration_id, identity_id)
);
