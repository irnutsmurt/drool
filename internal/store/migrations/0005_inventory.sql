-- M3: discovered facts and polled status.

CREATE TABLE host_facts (
  host_id TEXT PRIMARY KEY REFERENCES hosts(id) ON DELETE CASCADE,
  collected_at INTEGER NOT NULL,
  os_family TEXT NOT NULL DEFAULT 'unknown',   -- debian | redhat | suse | arch | unknown
  distro TEXT NOT NULL DEFAULT '',             -- os-release ID, e.g. ubuntu, rocky
  distro_version TEXT NOT NULL DEFAULT '',     -- os-release VERSION_ID
  distro_name TEXT NOT NULL DEFAULT '',        -- PRETTY_NAME
  kernel TEXT NOT NULL DEFAULT '',
  arch TEXT NOT NULL DEFAULT '',
  hostname TEXT NOT NULL DEFAULT '',
  cpu_count INTEGER,
  mem_bytes INTEGER,
  pkg_manager TEXT NOT NULL DEFAULT '',
  init_system TEXT NOT NULL DEFAULT '',
  container_runtimes TEXT NOT NULL DEFAULT '',  -- comma-separated: docker,podman
  facts_json TEXT NOT NULL DEFAULT '{}'         -- full parsed facts incl. disks, interfaces, services, ports, warnings
);

CREATE TABLE host_status (
  host_id TEXT PRIMARY KEY REFERENCES hosts(id) ON DELETE CASCADE,
  checked_at INTEGER NOT NULL,
  reachable INTEGER NOT NULL,
  boot_id TEXT NOT NULL DEFAULT '',
  uptime_s INTEGER,
  load1 REAL,
  mem_used_pct REAL,
  max_fs_used_pct REAL,
  max_fs_mount TEXT NOT NULL DEFAULT '',
  failed_units INTEGER,
  failed_unit_names TEXT NOT NULL DEFAULT '',  -- comma-separated, first 20
  reboot_required INTEGER,                     -- NULL = unknown
  reboot_reason TEXT NOT NULL DEFAULT '',
  updates_available INTEGER,                   -- NULL = unknown
  security_updates INTEGER,                    -- NULL = unknown / not detectable
  updates_checked_at INTEGER,
  metadata_refreshed_at INTEGER,
  last_error_class TEXT NOT NULL DEFAULT '',
  last_error TEXT NOT NULL DEFAULT ''
);
