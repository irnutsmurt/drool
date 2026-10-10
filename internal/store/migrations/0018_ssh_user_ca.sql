-- M16: just-in-time SSH with an OpenSSH user certificate authority
-- (ARCHITECTURE-ALPHA §33.2). DROOL's CA signs a fresh Ed25519 key for each
-- SSH connection it opens to a host in certificate mode; the certificate
-- names the login user and expires within minutes. Hosts trust the CA
-- through TrustedUserCAKeys.

CREATE TABLE ssh_user_cas (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  public_key TEXT NOT NULL,                 -- authorized_keys line of the CA key
  fingerprint TEXT NOT NULL,                -- SHA256:...
  key_ciphertext BLOB,                      -- private key sealed with the master key (AAD: id, 'ssh-user-ca'); NULL once retired
  key_nonce BLOB,
  key_version INTEGER,
  -- active: issues certificates. next: introduced for a rotation, trusted on
  -- hosts but not issuing yet. previous: replaced by a rotation, still
  -- trusted until retired. retired: its private key is destroyed.
  state TEXT NOT NULL CHECK (state IN ('active','next','previous','retired')),
  next_serial INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  activated_at INTEGER,
  retired_at INTEGER
);
CREATE UNIQUE INDEX ssh_user_cas_one_active ON ssh_user_cas(state) WHERE state = 'active';
CREATE UNIQUE INDEX ssh_user_cas_one_next ON ssh_user_cas(state) WHERE state = 'next';

-- How DROOL authenticates over SSH to the host: its credential (key or
-- password), or a certificate from the active CA. The certificate names
-- ssh_principal, or the SSH user when empty.
ALTER TABLE hosts ADD COLUMN ssh_auth TEXT NOT NULL DEFAULT 'credential' CHECK (ssh_auth IN ('credential','certificate'));
ALTER TABLE hosts ADD COLUMN ssh_principal TEXT NOT NULL DEFAULT '';

-- What DROOL knows about each host's trust in each CA.
CREATE TABLE ssh_ca_host_trust (
  host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  ca_id TEXT NOT NULL REFERENCES ssh_user_cas(id) ON DELETE CASCADE,
  installed_at INTEGER,                     -- DROOL installed its trust file listing this CA
  verified_at INTEGER,                      -- a fresh connection authenticated with a certificate from this CA
  failed_at INTEGER,                        -- the last verification failed
  error TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (host_id, ca_id)
);
CREATE INDEX ssh_ca_host_trust_by_ca ON ssh_ca_host_trust(ca_id);
