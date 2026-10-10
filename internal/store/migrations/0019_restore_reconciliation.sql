-- M16: full restore reconciliation (DESIGN §10.3, ARCHITECTURE-ALPHA §33.1).
--
-- `drool restore` gives the restored controller a new epoch and records the
-- restore here. Until a host has been compared and moved to the new epoch,
-- nothing the old epoch said can change it: agents refuse other epochs'
-- commands, the firewall tool refuses other epochs' transactions.

CREATE TABLE controller_restores (
  id TEXT PRIMARY KEY,
  restored_at INTEGER NOT NULL,
  backup_created_at INTEGER NOT NULL,
  backup_version TEXT NOT NULL DEFAULT '',
  backup_host TEXT NOT NULL DEFAULT '',
  previous_epoch TEXT NOT NULL DEFAULT '',  -- the epoch the backup carried
  epoch TEXT NOT NULL,                      -- the restored controller's new one
  restored_by TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL CHECK (status IN ('reconciling','finished')),
  finished_at INTEGER,
  finished_by TEXT NOT NULL DEFAULT '',
  summary_json TEXT NOT NULL DEFAULT '{}'   -- what the restore set aside (jobs, transactions, deliveries, schedules, ...)
);

-- Each host's reconciliation after a restore.
--   pending      not compared yet (its Shepherd hasn't connected, or SSH not checked)
--   recovered    nothing versioned to reconcile: the host is simply observed again
--   reconciled   compared, identical (or adopted), moved to the new epoch
--   ahead        the host has changes made after the backup: an administrator decides
--   attention    the host and the backup disagree otherwise: an administrator decides
--   incompatible its Shepherd can't move to the new epoch (too old, or protocol)
--   blocked      not reconciled for safety (revoked, quarantined, identity changed)
--   unreachable  couldn't be checked (tried again later)
CREATE TABLE host_reconciliation (
  restore_id TEXT NOT NULL REFERENCES controller_restores(id) ON DELETE CASCADE,
  host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  state TEXT NOT NULL CHECK (state IN ('pending','recovered','reconciled','ahead','attention','incompatible','blocked','unreachable')),
  reason TEXT NOT NULL DEFAULT '',
  domains_json TEXT NOT NULL DEFAULT '[]',  -- per domain: what DROOL had, what the host has, the verdict
  checked_at INTEGER,
  reconciled_at INTEGER,
  decided_by TEXT NOT NULL DEFAULT '',      -- who adopted the host's state, when it came to that
  attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (restore_id, host_id)
);
CREATE INDEX host_reconciliation_by_state ON host_reconciliation(restore_id, state);

-- Shepherds that present a certificate from this controller's root but
-- an identity the database has no record of (enrolled after the backup
-- was taken). They are refused, and listed so an administrator can enroll
-- them again. (A known identity whose certificate comes from an
-- intermediate rotated after the backup renews it through certificate
-- recovery instead.)
CREATE TABLE unknown_agents (
  identity_id TEXT PRIMARY KEY,
  hostname TEXT NOT NULL DEFAULT '',
  machine_id TEXT NOT NULL DEFAULT '',
  agent_version TEXT NOT NULL DEFAULT '',
  address TEXT NOT NULL DEFAULT '',
  key_fingerprint TEXT NOT NULL DEFAULT '',
  cert_not_after INTEGER,
  first_seen_at INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 1,
  dismissed_at INTEGER
);

-- A firewall transaction that was open when the backup was taken is closed
-- by the restore without claiming an outcome: its state stays as recorded
-- (usually unknown) and closed_reason says why it is no longer pursued.
ALTER TABLE fw_transactions ADD COLUMN closed_reason TEXT NOT NULL DEFAULT '';
