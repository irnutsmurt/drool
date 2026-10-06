-- name: UpsertHostFacts :exec
INSERT INTO host_facts (host_id, collected_at, os_family, distro, distro_version, distro_name, kernel, arch, hostname,
  cpu_count, mem_bytes, pkg_manager, init_system, container_runtimes, facts_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (host_id) DO UPDATE SET collected_at = excluded.collected_at, os_family = excluded.os_family,
  distro = excluded.distro, distro_version = excluded.distro_version, distro_name = excluded.distro_name,
  kernel = excluded.kernel, arch = excluded.arch, hostname = excluded.hostname, cpu_count = excluded.cpu_count,
  mem_bytes = excluded.mem_bytes, pkg_manager = excluded.pkg_manager, init_system = excluded.init_system,
  container_runtimes = excluded.container_runtimes, facts_json = excluded.facts_json;

-- name: GetHostFacts :one
SELECT * FROM host_facts WHERE host_id = ?;

-- name: ListHostFacts :many
SELECT * FROM host_facts;

-- name: GetHostStatus :one
SELECT * FROM host_status WHERE host_id = ?;

-- name: ListHostStatus :many
SELECT * FROM host_status;

-- name: UpsertStatusCheck :exec
-- A status poll: everything except update/metadata fields.
INSERT INTO host_status (host_id, checked_at, reachable, boot_id, uptime_s, load1, mem_used_pct, max_fs_used_pct,
  max_fs_mount, failed_units, failed_unit_names, reboot_required, reboot_reason, last_error_class, last_error)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (host_id) DO UPDATE SET checked_at = excluded.checked_at, reachable = excluded.reachable,
  boot_id = CASE WHEN excluded.reachable = 1 THEN excluded.boot_id ELSE host_status.boot_id END,
  uptime_s = excluded.uptime_s, load1 = excluded.load1, mem_used_pct = excluded.mem_used_pct,
  max_fs_used_pct = excluded.max_fs_used_pct, max_fs_mount = excluded.max_fs_mount,
  failed_units = excluded.failed_units, failed_unit_names = excluded.failed_unit_names,
  reboot_required = COALESCE(excluded.reboot_required, host_status.reboot_required),
  reboot_reason = CASE WHEN excluded.reboot_required IS NULL THEN host_status.reboot_reason ELSE excluded.reboot_reason END,
  last_error_class = excluded.last_error_class, last_error = excluded.last_error;

-- name: UpdateStatusUpdates :exec
-- The caller merges reboot fields with the current row (positional parameters only).
UPDATE host_status SET updates_available = ?, security_updates = ?, updates_checked_at = ?,
  reboot_required = ?, reboot_reason = ? WHERE host_id = ?;

-- name: UpdateStatusRefreshed :exec
UPDATE host_status SET metadata_refreshed_at = ? WHERE host_id = ?;

-- name: UpsertDiscoveredAddress :exec
-- Never changes the source of an existing (e.g. configured) address.
INSERT INTO host_addresses (host_id, address, family, interface, source, first_seen_at, last_seen_at)
VALUES (?, ?, ?, ?, 'discovered', ?, ?)
ON CONFLICT (host_id, address) DO UPDATE SET last_seen_at = excluded.last_seen_at, interface = excluded.interface;

-- name: DeleteStaleDiscoveredAddresses :exec
DELETE FROM host_addresses WHERE host_id = ? AND source = 'discovered' AND last_seen_at < ?;
