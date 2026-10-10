-- name: InsertHost :exec
INSERT INTO hosts (id, name, description, connect_address, management_method, ssh_port, ssh_user,
                   ssh_credential_id, become_method, become_credential_id, ssh_needs_pty, jump_only,
                   status, created_at, updated_at)
VALUES (?, ?, ?, ?, 'ssh', ?, ?, ?, ?, ?, ?, ?, 'pending_hostkey', ?, ?);

-- name: GetHost :one
SELECT * FROM hosts WHERE id = ?;

-- name: ListHosts :many
SELECT * FROM hosts ORDER BY name;

-- name: UpdateHost :exec
UPDATE hosts SET name = ?, description = ?, connect_address = ?, ssh_port = ?, ssh_user = ?,
  ssh_credential_id = ?, become_method = ?, become_credential_id = ?, ssh_needs_pty = ?, jump_only = ?,
  updated_at = ?
WHERE id = ?;

-- name: UpdateHostStatus :exec
UPDATE hosts SET status = ?, status_detail = ?, last_probe_at = ?, updated_at = ? WHERE id = ?;

-- name: DeleteHost :exec
DELETE FROM hosts WHERE id = ?;

-- name: GetHostHop :one
SELECT via_host_id FROM host_hops WHERE host_id = ? AND hop_index = 0;

-- name: ListHostHops :many
SELECT * FROM host_hops ORDER BY host_id, hop_index;

-- name: DeleteHostHops :exec
DELETE FROM host_hops WHERE host_id = ?;

-- name: InsertHostHop :exec
INSERT INTO host_hops (host_id, hop_index, via_host_id) VALUES (?, ?, ?);

-- name: ListHostsViaHop :many
SELECT h.id, h.name FROM host_hops hh JOIN hosts h ON h.id = hh.host_id WHERE hh.via_host_id = ? ORDER BY h.name;

-- name: ListHostTags :many
SELECT * FROM host_tags ORDER BY host_id, tag;

-- name: ListTagsForHost :many
SELECT tag FROM host_tags WHERE host_id = ? ORDER BY tag;

-- name: DeleteHostTags :exec
DELETE FROM host_tags WHERE host_id = ?;

-- name: InsertHostTag :exec
INSERT INTO host_tags (host_id, tag) VALUES (?, ?);

-- name: UpsertHostAddress :exec
INSERT INTO host_addresses (host_id, address, family, interface, source, first_seen_at, last_seen_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (host_id, address) DO UPDATE SET last_seen_at = excluded.last_seen_at, source = excluded.source;

-- name: DeleteConfiguredAddresses :exec
DELETE FROM host_addresses WHERE host_id = ? AND source = 'configured';

-- name: ListHostAddresses :many
SELECT * FROM host_addresses WHERE host_id = ? ORDER BY family, address;

-- name: SetHostSSHAuth :exec
-- How DROOL authenticates over SSH: 'credential' or 'certificate' (M16).
UPDATE hosts SET ssh_auth = ?, ssh_principal = ?, updated_at = ? WHERE id = ?;

-- name: CountActiveSSHCA :one
SELECT count(*) FROM ssh_user_cas WHERE state = 'active' AND key_ciphertext IS NOT NULL;
