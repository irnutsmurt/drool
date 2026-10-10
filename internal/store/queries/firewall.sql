-- Firewall (M8). ASCII only: sqlc mangles star expansion otherwise.

-- name: ListFwServices :many
SELECT * FROM fw_services ORDER BY name;

-- name: InsertFwService :exec
INSERT INTO fw_services (id, name, description, entries_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?);

-- name: UpdateFwService :exec
UPDATE fw_services SET description = ?, entries_json = ?, updated_at = ? WHERE id = ?;

-- name: GetFwService :one
SELECT * FROM fw_services WHERE id = ?;

-- name: DeleteFwService :exec
DELETE FROM fw_services WHERE id = ?;

-- name: ListFwPolicies :many
SELECT * FROM fw_policies ORDER BY position, created_at;

-- name: GetFwPolicy :one
SELECT * FROM fw_policies WHERE id = ?;

-- name: InsertFwPolicy :exec
INSERT INTO fw_policies (id, name, description, enabled, action, from_json, to_json, services_json, position, created_by, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: UpdateFwPolicy :exec
UPDATE fw_policies SET name = ?, description = ?, enabled = ?, action = ?, from_json = ?, to_json = ?, services_json = ?,
  position = ?, updated_at = ? WHERE id = ?;

-- name: DeleteFwPolicy :exec
DELETE FROM fw_policies WHERE id = ?;

-- name: MaxFwPolicyPosition :one
SELECT CAST(COALESCE(MAX(position), 0) AS INTEGER) FROM fw_policies;

-- name: GetFwHostConfig :one
SELECT * FROM fw_host_config WHERE host_id = ?;

-- name: ListFwHostConfigs :many
SELECT * FROM fw_host_config;

-- name: UpsertFwHostConfig :exec
INSERT INTO fw_host_config (host_id, mode, outbound, rollback_timeout_s, updated_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (host_id) DO UPDATE SET mode = excluded.mode, outbound = excluded.outbound,
  rollback_timeout_s = excluded.rollback_timeout_s, updated_at = excluded.updated_at;

-- name: ListFwGroupConfigs :many
SELECT * FROM fw_group_config;

-- name: UpsertFwGroupConfig :exec
INSERT INTO fw_group_config (group_id, rollback_timeout_s, updated_at) VALUES (?, ?, ?)
ON CONFLICT (group_id) DO UPDATE SET rollback_timeout_s = excluded.rollback_timeout_s, updated_at = excluded.updated_at;

-- name: EnsureFwHostState :exec
INSERT INTO fw_host_state (host_id, updated_at) VALUES (?, ?) ON CONFLICT (host_id) DO NOTHING;

-- name: GetFwHostState :one
SELECT * FROM fw_host_state WHERE host_id = ?;

-- name: ListFwHostStates :many
SELECT * FROM fw_host_state;

-- name: SetFwHostEnv :exec
UPDATE fw_host_state SET env_json = ?, eligibility_json = ?, env_checked_at = ?, updated_at = ? WHERE host_id = ?;

-- name: SetFwHostStatus :exec
UPDATE fw_host_state SET status_json = ?, status_at = ?, drift = ?, updated_at = ? WHERE host_id = ?;

-- name: SetFwHostApplied :exec
UPDATE fw_host_state SET enforced = ?, applied_revision = ?, applied_rules_hash = ?, applied_full_hash = ?,
  applied_sets_hash = ?, applied_policy_json = ?, override_mgmt = ?, sets_generation = ?, sets_generation_applied = ?,
  sets_error = '', drift = '', updated_at = ? WHERE host_id = ?;

-- name: SetFwHostSetsDesired :exec
UPDATE fw_host_state SET sets_generation = ?, updated_at = ? WHERE host_id = ?;

-- name: SetFwHostSetsApplied :exec
UPDATE fw_host_state SET sets_generation_applied = ?, applied_sets_hash = ?, applied_full_hash = ?, applied_policy_json = ?,
  sets_error = '', sets_at = ?, updated_at = ? WHERE host_id = ?;

-- name: SetFwHostSetsError :exec
UPDATE fw_host_state SET sets_error = ?, sets_at = ?, updated_at = ? WHERE host_id = ?;

-- name: UpsertFwMgmtObservation :exec
INSERT INTO fw_mgmt_observations (host_id, kind, address, port, observed_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (host_id, kind, address, port) DO UPDATE SET observed_at = excluded.observed_at;

-- name: DeleteFwMgmtObservations :exec
DELETE FROM fw_mgmt_observations WHERE host_id = ? AND kind = ?;

-- name: ListFwMgmtObservations :many
SELECT * FROM fw_mgmt_observations WHERE host_id = ? ORDER BY kind, address, port;

-- name: InsertFwTransaction :exec
INSERT INTO fw_transactions (id, host_id, job_id, kind, state, transport, epoch, revision, previous_hash, candidate_hash,
  rules_hash, sets_hash, timeout_s, deadline, measured_ms, override_mgmt, override_timeout, allow_drift, detail,
  error_class, candidate, policy_json, steps_json, created_by, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: UpdateFwTransaction :exec
UPDATE fw_transactions SET state = ?, transport = ?, previous_hash = ?, deadline = ?, detail = ?, error_class = ?,
  steps_json = ?, updated_at = ?, finished_at = ? WHERE id = ?;

-- name: GetFwTransaction :one
SELECT * FROM fw_transactions WHERE id = ?;

-- name: ListFwTransactions :many
SELECT id, host_id, job_id, kind, state, transport, epoch, revision, previous_hash, candidate_hash, rules_hash, sets_hash,
  timeout_s, deadline, measured_ms, override_mgmt, override_timeout, allow_drift, detail, error_class, steps_json,
  created_by, created_at, updated_at, finished_at
FROM fw_transactions WHERE (sqlc.narg('host_id') IS NULL OR host_id = sqlc.narg('host_id'))
ORDER BY created_at DESC LIMIT sqlc.arg('lim');

-- name: ListOpenFwTransactions :many
-- closed_reason: set aside by a controller restore (M16); never pursued again.
SELECT * FROM fw_transactions WHERE state IN ('preparing','pending','unknown') AND closed_reason = '' ORDER BY created_at;

-- name: ListAllHostAddresses :many
SELECT * FROM host_addresses ORDER BY host_id, family, address;

-- name: ListFwNetworks :many
SELECT * FROM fw_networks ORDER BY name;

-- name: InsertFwNetwork :exec
INSERT INTO fw_networks (id, name, description, cidrs_json, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?);

-- name: UpdateFwNetwork :exec
UPDATE fw_networks SET name = ?, description = ?, cidrs_json = ?, updated_at = ? WHERE id = ?;

-- name: DeleteFwNetwork :exec
DELETE FROM fw_networks WHERE id = ?;
