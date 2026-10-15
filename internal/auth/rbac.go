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

	// Agents (M7). Enrolling an agent creates a managed host and changing
	// the PKI or release trust changes who the fleet trusts: admin only.
	PermAgentsRead   Permission = "agents.read"   // agent status, PKI status, releases, rollouts
	PermAgentsEnroll Permission = "agents.enroll" // enrollment tokens, install the agent over SSH
	PermAgentsManage Permission = "agents.manage" // revoke, resolve conflicts, narrow capabilities, updates, endpoints
	PermAgentsPKI    Permission = "agents.pki"    // export/remove the root key, rotate the intermediate, import manifests

	// Firewall (M8). Changing network policy can cut hosts off, so writing
	// is admin only; the emergency override (applying a policy that removes
	// a management path, short rollback timeouts, replacing drifted rules)
	// is a separate permission.
	PermFirewallRead     Permission = "firewall.read"     // policies, explain, simulation, transactions
	PermFirewallWrite    Permission = "firewall.write"    // policies, services, modes, detection, snapshots, apply
	PermFirewallOverride Permission = "firewall.override" // emergency override, timeout override, replace drift

	// Network monitoring (M10). Observed connections are sensitive
	// metadata (who talks to whom, from which process): viewers don't see
	// them. Snapshots run ss as root over SSH; settings and enrichment
	// changes are admin only.
	PermNetworkRead     Permission = "network.read"     // observed connections, topology, enrichment status
	PermNetworkSnapshot Permission = "network.snapshot" // take a connection snapshot of an SSH host
	PermNetworkSettings Permission = "network.settings" // collection, retention, enrichment
	// Classifying observed traffic (what a flow is, whether it's expected)
	// is operator work, like acknowledging alerts: labels, never enforcement.
	PermNetworkClassify Permission = "network.classify"

	// System monitoring (M11). Reading metrics, services and hardware
	// health is for everyone; watching services, the flash probe and
	// refreshes for operators; thresholds, retention, standing helper
	// approvals and agent resource budgets for administrators. Installing
	// helpers is a package job (jobs.execute).
	PermMonitoringRead     Permission = "monitoring.read"
	PermMonitoringManage   Permission = "monitoring.manage"
	PermMonitoringSettings Permission = "monitoring.settings"

	// Alerts and notifications (M12). Everyone who sees monitoring sees
	// alerts. Acknowledging and maintenance are operator work. Destination
	// configuration (where alerts go, SMTP accounts, webhook URLs) is
	// admin only; operators and auditors see it without secrets (secrets
	// are never returned to anyone) and operators may send tests.
	PermAlertsRead          Permission = "alerts.read"
	PermAlertsAcknowledge   Permission = "alerts.acknowledge"
	PermMaintenanceManage   Permission = "maintenance.manage"
	PermNotificationsRead   Permission = "notifications.read"
	PermNotificationsManage Permission = "notifications.manage"
	PermNotificationsTest   Permission = "notifications.test"

	// Scheduling and workflows (M13). A schedule runs with its owner's
	// permissions, checked again before every run: managing schedules
	// never grants anything the owner couldn't run by hand. Editing or
	// enabling someone else's schedule, and reassigning owners, is
	// schedules.admin.
	PermSchedulesRead   Permission = "schedules.read"
	PermSchedulesManage Permission = "schedules.manage"
	PermSchedulesAdmin  Permission = "schedules.admin"
	PermWorkflowsRead   Permission = "workflows.read"
	PermWorkflowsManage Permission = "workflows.manage"

	// Desired state (M14). Everyone who sees hosts sees compliance and
	// drift (file contents need files.read, as in the file manager).
	// Restoring managed state is operator work and additionally needs the
	// permissions of the corrective operations (jobs.execute; files.deploy
	// and files.elevate for files). Baselines, assignments, priorities and
	// Accept current state change policy: administrators. Automatic
	// restore persists authority, so turning it on is administrator-only
	// and the owner's permissions are checked before every automatic job.
	PermDesiredRead    Permission = "desired.read"
	PermDesiredRestore Permission = "desired.restore"
	PermDesiredManage  Permission = "desired.manage"
	PermDesiredAuto    Permission = "desired.auto"
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
		PermAgentsRead, PermAgentsEnroll, PermAgentsManage, PermAgentsPKI,
		PermFirewallRead, PermFirewallWrite, PermFirewallOverride,
		PermNetworkRead, PermNetworkSnapshot, PermNetworkSettings, PermNetworkClassify,
		PermMonitoringRead, PermMonitoringManage, PermMonitoringSettings,
		PermAlertsRead, PermAlertsAcknowledge, PermMaintenanceManage, PermNotificationsRead, PermNotificationsManage, PermNotificationsTest,
		PermSchedulesRead, PermSchedulesManage, PermSchedulesAdmin, PermWorkflowsRead, PermWorkflowsManage,
		PermDesiredRead, PermDesiredRestore, PermDesiredManage, PermDesiredAuto,
	},
	// Operators can already run commands as root (jobs.execute), so file
	// elevation adds no power they don't have.
	RoleOperator: {
		PermHostsRead, PermJobsRead, PermJobsExecute, PermCredentialsUse,
		PermFilesRead, PermFilesWrite, PermFilesDeploy, PermFilesElevate,
		PermAgentsRead, PermFirewallRead, PermNetworkRead, PermNetworkSnapshot, PermNetworkClassify, PermMonitoringRead, PermMonitoringManage,
		PermAlertsRead, PermAlertsAcknowledge, PermMaintenanceManage, PermNotificationsRead, PermNotificationsTest,
		PermSchedulesRead, PermSchedulesManage, PermWorkflowsRead, PermWorkflowsManage,
		PermDesiredRead, PermDesiredRestore,
	},
	RoleAuditor: {
		PermHostsRead, PermJobsRead, PermAuditRead, PermSettingsRead, PermAgentsRead, PermFirewallRead, PermNetworkRead, PermMonitoringRead,
		PermAlertsRead, PermNotificationsRead, PermSchedulesRead, PermWorkflowsRead, PermDesiredRead,
	},
	RoleViewer: {
		PermHostsRead, PermJobsRead, PermAgentsRead, PermFirewallRead, PermMonitoringRead, PermAlertsRead, PermSchedulesRead, PermWorkflowsRead,
		PermDesiredRead,
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
