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

	// Files (M6). Reading as the login user can expose secrets (keys,
	// tokens in home directories), so auditors and viewers don't get it.
	// Elevation is separate and never implied.
	PermFilesRead    Permission = "files.read"    // browse, download, view text, deployment history and diffs
	PermFilesWrite   Permission = "files.write"   // upload, edit, mkdir, rename/move, delete, chmod, chown
	PermFilesDeploy  Permission = "files.deploy"  // upload deployment artifacts, preview, deploy, roll back
	PermFilesElevate Permission = "files.elevate" // any of the above as root (sudo)
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
		PermFilesRead, PermFilesWrite, PermFilesDeploy, PermFilesElevate,
	},
	// Operators can already run commands as root (jobs.execute), so file
	// elevation adds no power they don't have.
	RoleOperator: {
		PermHostsRead, PermJobsRead, PermJobsExecute, PermCredentialsUse,
		PermFilesRead, PermFilesWrite, PermFilesDeploy, PermFilesElevate,
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
