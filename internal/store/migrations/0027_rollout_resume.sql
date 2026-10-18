-- Resuming a paused Shepherd update accepts the failures so far
-- (ARCHITECTURE-ALPHA s43): only hosts that fail after it count toward the
-- rollout's failure limit again. 0 when it was never resumed.
ALTER TABLE agent_rollouts ADD COLUMN resumed_at INTEGER NOT NULL DEFAULT 0;
