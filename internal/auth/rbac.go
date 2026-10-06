package auth

import "slices"

// Permission is a capability checked by API handlers. Checks are always made
// against permissions, never role names, so custom roles later are a data
// change only (ARCHITECTURE-ALPHA §12.2).
type Permission string

const (
	PermHostsRead         Permission = "hosts.read"
	PermHostsWrite        Permission = "hosts.write"
	PermHostKeysApprove   Permission = "hostkeys.approve"
	PermJobsRead          Permission = "jobs.read"
	PermJobsExecute       Permission = "jobs.execute"
	PermCredentialsUse    Permission = "credentials.use"
	PermCredentialsManage Permission = "credentials.manage"
	PermAuditRead         Permission = "audit.read"
	PermUsersManage       Permission = "users.manage"
	PermSettingsRead      Permission = "settings.read"
	PermSettingsWrite     Permission = "settings.write"
)

// Role names stored in users.role.
const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleAuditor  = "auditor"
	RoleViewer   = "viewer"
)

var Roles = []string{RoleAdmin, RoleOperator, RoleAuditor, RoleViewer}

var rolePermissions = map[string][]Permission{
	RoleAdmin: {
		PermHostsRead, PermHostsWrite, PermHostKeysApprove,
		PermJobsRead, PermJobsExecute,
		PermCredentialsUse, PermCredentialsManage,
		PermAuditRead, PermUsersManage, PermSettingsRead, PermSettingsWrite,
	},
	RoleOperator: {
		PermHostsRead, PermJobsRead, PermJobsExecute, PermCredentialsUse,
	},
	RoleAuditor: {
		PermHostsRead, PermJobsRead, PermAuditRead, PermSettingsRead,
	},
	RoleViewer: {
		PermHostsRead, PermJobsRead,
	},
}

// ValidRole reports whether role is a known role.
func ValidRole(role string) bool {
	_, ok := rolePermissions[role]
	return ok
}

// PermissionsFor returns the permissions granted to role.
func PermissionsFor(role string) []Permission {
	return slices.Clone(rolePermissions[role])
}

// RoleHas reports whether role grants p.
func RoleHas(role string, p Permission) bool {
	return slices.Contains(rolePermissions[role], p)
}
