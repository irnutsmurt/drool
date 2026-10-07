-- M9: named network objects for the firewall designer (one or more CIDRs
-- under a name, e.g. admin-lan). Policies refer to them by id.
CREATE TABLE fw_networks (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE COLLATE NOCASE,
  description TEXT NOT NULL DEFAULT '',
  cidrs_json TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
