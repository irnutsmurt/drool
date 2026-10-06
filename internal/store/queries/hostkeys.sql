-- name: ListHostKeys :many
SELECT * FROM ssh_host_keys WHERE host_id = ? ORDER BY status = 'pinned' DESC, last_seen_at DESC;

-- name: GetHostKey :one
SELECT * FROM ssh_host_keys WHERE id = ?;

-- name: GetPinnedHostKey :one
SELECT * FROM ssh_host_keys WHERE host_id = ? AND status = 'pinned';

-- name: ListPinnedHostKeys :many
SELECT * FROM ssh_host_keys WHERE status = 'pinned';

-- name: FindOfferedHostKey :one
SELECT * FROM ssh_host_keys WHERE host_id = ? AND fingerprint_sha256 = ? AND status = 'offered';

-- name: InsertHostKey :exec
INSERT INTO ssh_host_keys (id, host_id, key_type, public_key, fingerprint_sha256, status, first_seen_at, last_seen_at)
VALUES (?, ?, ?, ?, ?, 'offered', ?, ?);

-- name: TouchHostKey :exec
UPDATE ssh_host_keys SET last_seen_at = ? WHERE id = ?;

-- name: RetirePinnedHostKey :exec
UPDATE ssh_host_keys SET status = 'retired', decided_at = ? WHERE host_id = ? AND status = 'pinned';

-- name: DeleteRetiredHostKeyDuplicate :exec
DELETE FROM ssh_host_keys WHERE host_id = ? AND fingerprint_sha256 = ? AND status = 'retired';

-- name: PinHostKey :exec
UPDATE ssh_host_keys SET status = 'pinned', decided_by = ?, decided_at = ? WHERE id = ?;

-- name: DeleteOfferedHostKeys :exec
DELETE FROM ssh_host_keys WHERE host_id = ? AND status = 'offered';
