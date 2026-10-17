-- Shepherd updates for 1.0 (ARCHITECTURE-ALPHA s42).
--
-- Which signatures each Shepherd accepts for its own updates, as its hello
-- says (keys, official releases, updates allowed); '' when it doesn't say
-- (Shepherds built before 1.0).
ALTER TABLE agent_identities ADD COLUMN release_trust_json TEXT NOT NULL DEFAULT '';

-- A rollout a person started to try a version again on hosts where it
-- failed before: only then does a Shepherd retry it.
ALTER TABLE agent_rollouts ADD COLUMN retry INTEGER NOT NULL DEFAULT 0;

-- Automatic Shepherd updates are on by default from 1.0. An installation
-- that already rolled out releases of its own without ever saving an
-- update policy keeps updating by hand, as it did: its policy is written
-- down as manual. A saved policy is never changed.
INSERT INTO settings (key, value_json, updated_by, updated_at)
SELECT 'agent_updates',
  '{"policy":"manual","channel":"stable","version":"","canary":1,"batch_size":5,"max_failures":1,"health_window_s":120}',
  NULL, CAST(strftime('%s', 'now') AS INTEGER) * 1000
WHERE NOT EXISTS (SELECT 1 FROM settings WHERE key = 'agent_updates')
  AND (EXISTS (SELECT 1 FROM agent_releases) OR EXISTS (SELECT 1 FROM agent_rollouts WHERE kind = 'update'));
