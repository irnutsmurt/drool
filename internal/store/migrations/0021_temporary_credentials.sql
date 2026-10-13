-- After M17: a credential entered only to set up DROOL's own SSH account on
-- a host (ARCHITECTURE-ALPHA §36). DROOL forgets it once the host signs in
-- with that account, unless another host still uses it.
ALTER TABLE secrets ADD COLUMN temporary INTEGER NOT NULL DEFAULT 0;

-- When the host switched to DROOL's own SSH account (NULL: it signs in with
-- an account someone else made, or its sign-in was edited since).
ALTER TABLE hosts ADD COLUMN ssh_account_at INTEGER;
