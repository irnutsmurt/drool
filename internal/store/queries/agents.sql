-- M7: agents (ARCHITECTURE-ALPHA section 23).

-- name: InsertAgentCA :exec
INSERT INTO agent_ca (id, kind, cert_der, fingerprint, key_ciphertext, key_nonce, key_version, status, not_after, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListAgentCA :many
SELECT * FROM agent_ca ORDER BY created_at;

-- name: GetAgentRoot :one
SELECT * FROM agent_ca WHERE kind = 'root';

-- name: GetActiveIntermediate :one
SELECT * FROM agent_ca WHERE kind = 'intermediate' AND status = 'active';

-- name: SetAgentCAStatus :exec
UPDATE agent_ca SET status = ? WHERE id = ?;

-- name: RemoveAgentCAKey :exec
UPDATE agent_ca SET key_ciphertext = NULL, key_nonce = NULL, key_version = NULL, key_removed_at = ? WHERE id = ?;

-- name: RestoreAgentCAKey :exec
UPDATE agent_ca SET key_ciphertext = ?, key_nonce = ?, key_version = ?, key_removed_at = NULL WHERE id = ?;

-- name: InsertTrustManifest :exec
INSERT INTO agent_trust_manifests (version, payload, signature, created_at) VALUES (?, ?, ?, ?);

-- name: LatestTrustManifest :one
SELECT * FROM agent_trust_manifests ORDER BY version DESC LIMIT 1;

-- name: ListTrustManifests :many
SELECT * FROM agent_trust_manifests ORDER BY version DESC;

-- name: InsertEnrollmentToken :exec
INSERT INTO agent_enrollment_tokens (id, token_hash, description, host_id, group_id, preset, created_by, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetEnrollmentTokenByHash :one
SELECT * FROM agent_enrollment_tokens WHERE token_hash = ?;

-- name: GetEnrollmentToken :one
SELECT * FROM agent_enrollment_tokens WHERE id = ?;

-- name: ListEnrollmentTokens :many
SELECT t.*, COALESCE(h.name, '') AS host_name, COALESCE(g.name, '') AS group_name
FROM agent_enrollment_tokens t
LEFT JOIN hosts h ON h.id = t.host_id
LEFT JOIN groups g ON g.id = t.group_id
ORDER BY t.created_at DESC LIMIT 200;

-- name: UseEnrollmentToken :execrows
-- Atomic single use: only an unused, unrevoked, unexpired token matches.
UPDATE agent_enrollment_tokens SET used_at = ?, used_by_identity = ?
WHERE id = ? AND used_at IS NULL AND revoked_at IS NULL AND expires_at > ?;

-- name: RevokeEnrollmentToken :execrows
UPDATE agent_enrollment_tokens SET revoked_at = ? WHERE id = ? AND used_at IS NULL AND revoked_at IS NULL;

-- name: InsertAgentIdentity :exec
INSERT INTO agent_identities (id, host_id, public_key, fingerprint, status, enrolled_at, token_id,
  cert_serial, cert_not_after, cert_issued_at, agent_version, protocol_min, protocol_max, hostname, machine_id, os, arch, ceiling_json)
VALUES (?, ?, ?, ?, 'active', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetAgentIdentity :one
SELECT * FROM agent_identities WHERE id = ?;

-- name: GetLiveIdentityForHost :one
SELECT * FROM agent_identities WHERE host_id = ? AND status != 'revoked';

-- name: ListIdentitiesForHost :many
SELECT * FROM agent_identities WHERE host_id = ? ORDER BY enrolled_at DESC;

-- name: ListLiveIdentities :many
SELECT * FROM agent_identities WHERE status != 'revoked' ORDER BY host_id;

-- name: UpdateIdentityCert :exec
UPDATE agent_identities SET cert_serial = ?, cert_not_after = ?, cert_issued_at = ? WHERE id = ?;

-- name: MarkIdentityRecovered :exec
UPDATE agent_identities SET recovered_at = ? WHERE id = ?;

-- name: UpdateIdentityHello :exec
UPDATE agent_identities SET agent_version = ?, protocol = ?, protocol_min = ?, protocol_max = ?, compatible = ?, compat_reason = ?,
  hostname = ?, machine_id = ?, os = ?, arch = ?, ceiling_json = ?, effective_json = ?, manifest_version = ?,
  epoch_reported = ?, endpoints_json = ?, boot_id = ?, last_seen_at = ?, last_address = ?, connected = 1
WHERE id = ?;

-- name: TouchIdentity :exec
UPDATE agent_identities SET last_seen_at = ?, boot_id = ? WHERE id = ?;

-- name: SetIdentityConnected :exec
UPDATE agent_identities SET connected = ?, last_seen_at = ? WHERE id = ?;

-- name: ResetIdentitiesConnected :exec
UPDATE agent_identities SET connected = 0;

-- name: SetIdentityEffective :exec
UPDATE agent_identities SET effective_json = ? WHERE id = ?;

-- name: SetIdentityManifestVersion :exec
UPDATE agent_identities SET manifest_version = ? WHERE id = ?;

-- name: SetIdentityEndpoints :exec
UPDATE agent_identities SET endpoints_json = ? WHERE id = ?;

-- name: RevokeIdentity :execrows
UPDATE agent_identities SET status = 'revoked', revoked_at = ?, revoked_by = ?, revoke_reason = ?
WHERE id = ? AND status != 'revoked';

-- name: QuarantineIdentity :exec
UPDATE agent_identities SET status = 'quarantined', quarantined_at = ?, conflict_json = ? WHERE id = ? AND status = 'active';

-- name: ReleaseQuarantine :execrows
UPDATE agent_identities SET status = 'active', quarantined_at = NULL, conflict_json = '' WHERE id = ? AND status = 'quarantined';

-- name: SetIdentityUpdate :exec
UPDATE agent_identities SET update_state = ?, update_version = ?, update_detail = ?, update_at = ? WHERE id = ?;

-- name: SetHostManagement :exec
UPDATE hosts SET management_method = ?, management_fallback = ?, updated_at = ? WHERE id = ?;

-- name: SetHostRequestedCaps :exec
UPDATE hosts SET requested_caps_json = ?, updated_at = ? WHERE id = ?;

-- name: InsertAgentOnlyHost :exec
INSERT INTO hosts (id, name, description, connect_address, management_method, management_fallback, ssh_port, ssh_user,
  become_method, status, status_detail, ssh_configured, created_at, updated_at)
VALUES (?, ?, '', ?, 'agent', 'none', 22, '', 'none', 'ready', 'managed by the DROOL agent', 0, ?, ?);

-- name: SetHostStatusAgent :exec
UPDATE hosts SET status = ?, status_detail = ?, updated_at = ? WHERE id = ?;

-- name: GetStateVersion :one
SELECT * FROM state_versions WHERE host_id = ? AND domain = ?;

-- name: ListStateVersions :many
SELECT * FROM state_versions WHERE host_id = ? ORDER BY domain;

-- name: UpsertStateVersion :exec
INSERT INTO state_versions (host_id, domain, epoch, revision, content_hash, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (host_id, domain) DO UPDATE SET epoch = excluded.epoch, revision = excluded.revision,
  content_hash = excluded.content_hash, updated_at = excluded.updated_at;

-- name: ReportStateVersion :exec
UPDATE state_versions SET reported_epoch = ?, reported_revision = ?, reported_hash = ?, reported_at = ?, status = ?
WHERE host_id = ? AND domain = ?;

-- name: InsertReportedOnlyState :exec
-- The host reports a domain the controller has no record of (e.g. after
-- restoring an old backup): keep the report, flag it.
INSERT INTO state_versions (host_id, domain, epoch, revision, content_hash, updated_at,
  reported_epoch, reported_revision, reported_hash, reported_at, status)
VALUES (?, ?, ?, 0, '', ?, ?, ?, ?, ?, ?)
ON CONFLICT (host_id, domain) DO NOTHING;

-- name: UpsertObservedAddress :exec
-- Never changes the source of an existing address of another kind.
INSERT INTO host_addresses (host_id, address, family, interface, source, first_seen_at, last_seen_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (host_id, address) DO UPDATE SET last_seen_at = excluded.last_seen_at, interface = excluded.interface,
  source = CASE WHEN host_addresses.source = 'configured' THEN 'configured' ELSE excluded.source END;

-- name: DeleteStaleObservedAddresses :exec
DELETE FROM host_addresses WHERE host_id = ? AND source IN ('discovered','agent') AND last_seen_at < ?;

-- name: InsertReleaseKey :exec
INSERT INTO agent_release_keys (id, public_key, name, added_by, added_at) VALUES (?, ?, ?, ?, ?);

-- name: ListReleaseKeys :many
SELECT * FROM agent_release_keys ORDER BY added_at;

-- name: DeleteReleaseKey :execrows
DELETE FROM agent_release_keys WHERE id = ?;

-- name: InsertRelease :exec
INSERT INTO agent_releases (id, version, channel, os, arch, protocol_min, protocol_max, sha256, size, key_id,
  manifest_payload, signature, uploaded_by, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetRelease :one
SELECT * FROM agent_releases WHERE id = ?;

-- name: GetReleaseFor :one
SELECT * FROM agent_releases WHERE version = ? AND os = ? AND arch = ?;

-- name: ListReleases :many
SELECT * FROM agent_releases ORDER BY created_at DESC;

-- name: DeleteRelease :execrows
DELETE FROM agent_releases WHERE id = ?;

-- name: ListReleaseHashes :many
SELECT sha256 FROM agent_releases;

-- name: InsertRollout :exec
INSERT INTO agent_rollouts (id, version, status, canary, batch_size, max_failures, health_window_s, automatic, created_by, created_at, updated_at)
VALUES (?, ?, 'running', ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetRollout :one
SELECT * FROM agent_rollouts WHERE id = ?;

-- name: ListRollouts :many
SELECT * FROM agent_rollouts ORDER BY created_at DESC LIMIT 50;

-- name: ActiveRollouts :many
SELECT * FROM agent_rollouts WHERE status = 'running' ORDER BY created_at;

-- name: SetRolloutStatus :exec
UPDATE agent_rollouts SET status = ?, paused_reason = ?, updated_at = ? WHERE id = ?;

-- name: SetRolloutFailures :exec
UPDATE agent_rollouts SET failures = ?, updated_at = ? WHERE id = ?;

-- name: InsertRolloutHost :exec
INSERT INTO agent_rollout_hosts (rollout_id, host_id, wave, state, from_version, updated_at) VALUES (?, ?, ?, 'pending', ?, ?);

-- name: ListRolloutHosts :many
SELECT rh.*, h.name AS host_name FROM agent_rollout_hosts rh JOIN hosts h ON h.id = rh.host_id
WHERE rh.rollout_id = ? ORDER BY rh.wave, h.name;

-- name: SetRolloutHostState :exec
UPDATE agent_rollout_hosts SET state = ?, detail = ?, updated_at = ? WHERE rollout_id = ? AND host_id = ?;

-- name: InsertEndpointMigration :exec
INSERT INTO agent_endpoint_migrations (id, endpoint, status, created_by, created_at) VALUES (?, ?, 'testing', ?, ?);

-- name: GetEndpointMigration :one
SELECT * FROM agent_endpoint_migrations WHERE id = ?;

-- name: ListEndpointMigrations :many
SELECT * FROM agent_endpoint_migrations ORDER BY created_at DESC LIMIT 20;

-- name: SetEndpointMigrationStatus :exec
UPDATE agent_endpoint_migrations SET status = ?, committed_at = ? WHERE id = ?;

-- name: UpsertEndpointTest :exec
INSERT INTO agent_endpoint_tests (migration_id, identity_id, ok, error, tested_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (migration_id, identity_id) DO UPDATE SET ok = excluded.ok, error = excluded.error, tested_at = excluded.tested_at;

-- name: MarkEndpointTestCommitted :exec
UPDATE agent_endpoint_tests SET committed = 1 WHERE migration_id = ? AND identity_id = ?;

-- name: ListEndpointTests :many
SELECT * FROM agent_endpoint_tests WHERE migration_id = ?;

-- name: SetJobHostTransport :exec
UPDATE job_hosts SET transport = ?, transport_note = ? WHERE job_id = ? AND host_id = ?;

-- name: GetHostByName :one
SELECT * FROM hosts WHERE name = ?;

-- name: SetRolloutKind :exec
UPDATE agent_rollouts SET kind = ?, preset = ? WHERE id = ?;
