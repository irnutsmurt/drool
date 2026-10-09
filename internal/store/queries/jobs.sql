-- name: InsertJob :exec
INSERT INTO jobs (id, name, spec_json, spec_version, idempotency_key, status, rerun_of, created_by, created_at)
VALUES (?, ?, ?, ?, ?, 'pending', ?, ?, ?);

-- name: GetJob :one
SELECT * FROM jobs WHERE id = ?;

-- name: GetJobByIdempotencyKey :one
SELECT * FROM jobs WHERE created_by = ? AND idempotency_key = ?;

-- name: ListJobs :many
SELECT * FROM jobs
WHERE (CAST(sqlc.arg(before_id) AS TEXT) = '' OR id < CAST(sqlc.arg(before_id) AS TEXT))
  AND (CAST(sqlc.arg(status) AS TEXT) = '' OR status = CAST(sqlc.arg(status) AS TEXT))
  AND (CAST(sqlc.arg(host_id) AS TEXT) = '' OR id IN (SELECT job_id FROM job_hosts WHERE host_id = CAST(sqlc.arg(host_id) AS TEXT)))
ORDER BY id DESC
LIMIT sqlc.arg(lim);

-- name: SetJobRunning :exec
UPDATE jobs SET status = 'running', pause_reason = '', started_at = COALESCE(started_at, ?) WHERE id = ?;

-- name: SetJobPaused :exec
UPDATE jobs SET status = 'paused', pause_reason = ? WHERE id = ?;

-- name: FinishJob :exec
UPDATE jobs SET status = ?, finished_at = ? WHERE id = ?;

-- name: InterruptActiveJobs :many
UPDATE jobs SET status = 'interrupted', finished_at = ?
WHERE status IN ('pending','running','paused') RETURNING id;

-- name: InsertJobHost :exec
INSERT INTO job_hosts (job_id, host_id, host_name, position, state) VALUES (?, ?, ?, ?, 'queued');

-- name: ListJobHosts :many
SELECT * FROM job_hosts WHERE job_id = ? ORDER BY position;

-- name: GetJobHost :one
SELECT * FROM job_hosts WHERE job_id = ? AND host_id = ?;

-- name: SetJobHostState :exec
-- Positional parameters only: mixing ? with sqlc.narg() misnumbers them in SQLite.
UPDATE job_hosts SET state = ?, current_step = ?, attempt = ?,
  started_at = COALESCE(started_at, ?) WHERE job_id = ? AND host_id = ?;

-- name: FinishJobHost :exec
UPDATE job_hosts SET state = ?, exit_code = ?, error_class = ?, error_message = ?, error_hint = ?,
  retryable = ?, finished_at = ? WHERE job_id = ? AND host_id = ?;

-- name: CountJobHostStates :many
SELECT state, count(*) AS n FROM job_hosts WHERE job_id = ? GROUP BY state;

-- name: InterruptActiveJobHosts :exec
UPDATE job_hosts SET state = 'interrupted', finished_at = ?,
  error_class = 'InterruptedError', error_message = 'the controller restarted while this host was in progress; outcome unknown'
WHERE state NOT IN ('succeeded','failed','timed_out','cancelled','skipped','interrupted');

-- name: InsertJobStep :exec
INSERT INTO job_steps (job_id, host_id, step_index, op_type, state, attempt, started_at)
VALUES (?, ?, ?, ?, 'running', ?, ?)
ON CONFLICT (job_id, host_id, step_index) DO UPDATE SET state = 'running', attempt = excluded.attempt,
  started_at = excluded.started_at, finished_at = NULL, exit_code = NULL;

-- name: FinishJobStep :exec
UPDATE job_steps SET state = ?, exit_code = ?, changed = ?, result_json = ?, stdout_bytes = ?, stderr_bytes = ?,
  truncated = ?, finished_at = ? WHERE job_id = ? AND host_id = ? AND step_index = ?;

-- name: ListJobSteps :many
SELECT * FROM job_steps WHERE job_id = ? AND host_id = ? ORDER BY step_index;

-- name: InterruptActiveJobSteps :exec
UPDATE job_steps SET state = 'interrupted', finished_at = ? WHERE state = 'running';

-- name: InsertJobOutput :exec
INSERT INTO job_output (job_id, host_id, step_index, seq, stream, ts, chunk) VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: ListJobOutput :many
SELECT * FROM job_output WHERE job_id = ? AND host_id = ? AND step_index = ? AND seq > ?
ORDER BY seq LIMIT ?;

-- name: MaxJobOutputSeq :one
SELECT CAST(COALESCE(MAX(seq), 0) AS INTEGER) FROM job_output WHERE job_id = ? AND host_id = ? AND step_index = ?;

-- name: DeleteFinishedJobsBefore :execrows
DELETE FROM jobs WHERE finished_at IS NOT NULL AND finished_at < ?;

-- name: SetJobOrigin :exec
UPDATE jobs SET schedule_id = ?, occurrence_id = ?, workflow_id = ?, workflow_revision = ? WHERE id = ?;

-- name: InsertJobHostInWave :exec
INSERT INTO job_hosts (job_id, host_id, host_name, position, state, wave) VALUES (?, ?, ?, ?, 'queued', ?);

-- name: SkipJobHost :exec
UPDATE job_hosts SET state = 'skipped', error_class = ?, error_message = ?, finished_at = ?, wave = -1
WHERE job_id = ? AND host_id = ?;

-- name: SetJobReconcile :exec
UPDATE jobs SET reconcile_id = ? WHERE id = ?;

-- name: SetJobHostSteps :exec
-- The steps this host runs when they differ per host (a desired-state
-- correction): a JSON list of step indexes.
UPDATE job_hosts SET steps = ? WHERE job_id = ? AND host_id = ?;
