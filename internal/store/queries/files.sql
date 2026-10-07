-- name: UpsertArtifact :exec
INSERT INTO artifacts (sha256, size, name, is_text, created_by, created_at, last_used_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (sha256) DO UPDATE SET last_used_at = excluded.last_used_at,
  name = CASE WHEN artifacts.name = '' THEN excluded.name ELSE artifacts.name END;

-- name: GetArtifact :one
SELECT * FROM artifacts WHERE sha256 = ?;

-- name: TouchArtifact :exec
UPDATE artifacts SET last_used_at = ? WHERE sha256 = ?;

-- name: ListUnreferencedArtifacts :many
-- Candidates for garbage collection: not part of any deployment history,
-- not named by any job spec still kept, and not used recently.
SELECT a.* FROM artifacts a
WHERE a.last_used_at < ?
  AND NOT EXISTS (SELECT 1 FROM file_deployments d WHERE d.before_artifact = a.sha256 OR d.after_artifact = a.sha256)
  AND NOT EXISTS (SELECT 1 FROM jobs j WHERE instr(j.spec_json, a.sha256) > 0);

-- name: DeleteArtifact :exec
DELETE FROM artifacts WHERE sha256 = ?;

-- name: ListArtifactHashes :many
SELECT sha256 FROM artifacts;

-- name: InsertFileDeployment :exec
INSERT INTO file_deployments (id, job_id, host_id, path, kind, rollback_of, outcome, before_exists, before_sha256,
  before_artifact, before_meta, after_sha256, after_artifact, after_meta, validation, service, error, op_json, created_by, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetFileDeployment :one
SELECT d.*, h.name AS host_name FROM file_deployments d JOIN hosts h ON h.id = d.host_id WHERE d.id = ?;

-- name: ListFileDeployments :many
SELECT d.*, h.name AS host_name FROM file_deployments d JOIN hosts h ON h.id = d.host_id
WHERE (CAST(sqlc.arg(host_id) AS TEXT) = '' OR d.host_id = CAST(sqlc.arg(host_id) AS TEXT))
  AND (CAST(sqlc.arg(path) AS TEXT) = '' OR d.path = CAST(sqlc.arg(path) AS TEXT))
  AND (CAST(sqlc.arg(job_id) AS TEXT) = '' OR d.job_id = CAST(sqlc.arg(job_id) AS TEXT))
ORDER BY d.created_at DESC, d.id DESC
LIMIT sqlc.arg(lim);

-- name: ListManagedFiles :many
-- The latest deployment of every managed path on a host.
SELECT d.*, h.name AS host_name FROM file_deployments d JOIN hosts h ON h.id = d.host_id
WHERE d.host_id = ? AND d.created_at = (
  SELECT max(d2.created_at) FROM file_deployments d2 WHERE d2.host_id = d.host_id AND d2.path = d.path)
ORDER BY d.path;
