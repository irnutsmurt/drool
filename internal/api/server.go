// Package api implements the public REST API (/api/v1), health endpoints and
// static serving of the embedded web UI.
package api

import (
	"context"
	"database/sql"
	"drool/internal/agentsvc"
	"drool/internal/alerts"
	"drool/internal/artifacts"
	"drool/internal/files"
	"drool/internal/firewall/fwsvc"
	"drool/internal/hostops"
	"drool/internal/mgmt"
	"drool/internal/model"
	"drool/internal/monitor/monsvc"
	"drool/internal/netmon/netsvc"
	"drool/internal/recovery"
	"drool/internal/sched"
	"drool/internal/sshaccount"
	"drool/internal/sshca"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"drool/internal/audit"
	"drool/internal/auth"
	"drool/internal/config"
	"drool/internal/desired"
	"drool/internal/engine"
	"drool/internal/inventory"
	"drool/internal/logging"
	"drool/internal/poller"
	"drool/internal/secrets"
	"drool/internal/sshx"
	"drool/internal/store"
	"drool/internal/store/db"
)

// Deps are the server's dependencies.
type Deps struct {
	Config   *config.Loaded
	Store    *store.Store
	Auth     *auth.Service
	Audit    *audit.Writer
	Logs     *logging.Loggers
	UI       fs.FS // contents of web/dist; may lack index.html
	Registry *prometheus.Registry
	TLSMode  string

	Secrets   *secrets.Service
	Inventory *inventory.Service
	Pool      *sshx.Pool
	Engine    *engine.Engine
	Poller    *poller.Poller // may be nil (tests)
	Simulator Simulator
	Files     files.Opener      // file manager and deployment previews; nil disables them
	Agents    *agentsvc.Service // agent endpoint; nil disables agent routes
	Router    *mgmt.Router      // management paths; nil: SSH only
	WebPin    func() string     // curl --pinnedpubkey value of the web TLS key
	Firewall  *fwsvc.Service    // firewall policy (M8)
	Network   *netsvc.Service   // network monitoring (M10); nil disables its routes
	Monitor   *monsvc.Service   // system monitoring (M11); nil disables its routes
	Alerts    *alerts.Service   // alerts, maintenance, notifications (M12); nil disables their routes
	Sched     *sched.Service    // schedules and workflows (M13); nil disables their routes
	Desired   *desired.Service  // desired state (M14); nil disables its routes
	Artifacts *artifacts.Store  // deployment artifacts
	Integrity *audit.Integrity  // audit chain checks (M16); nil disables their routes
	SSHCA     *sshca.Service    // SSH user certificate authority (M16); nil disables its routes
	AuditSink *audit.Sink       // remote audit copy (M16); nil disables its routes
	Recovery  *recovery.Service // restore reconciliation (M16); nil disables its routes
	// SSHAccount sets up DROOL's own SSH account on hosts (After M17); nil
	// disables its routes.
	SSHAccount *sshaccount.Service
	// StaleAfter is how old a status check may be before it is shown as stale.
	StaleAfter time.Duration
}

// Simulator runs package/system-update dry runs on a host's management path.
type Simulator interface {
	Simulate(ctx context.Context, hostID string, op model.Operation) (*hostops.SimResult, error)
}

// Server is the HTTP API server.
type Server struct {
	d              Deps
	log            *slog.Logger
	secLog         *slog.Logger
	trustedProxies []netip.Prefix
	secureCookie   bool
	ready          atomic.Bool
	metrics        *httpMetrics
	handler        http.Handler
}

// authMode controls authentication for a route.
type authMode int

const (
	public authMode = iota // no authentication
	authed                 // any authenticated principal
)

// route is one API endpoint. The route table is also used by the OpenAPI
// conformance test, so every endpoint must be declared here.
type route struct {
	method      string
	path        string // ServeMux pattern path, e.g. /api/v1/users/{id}
	auth        authMode
	perm        auth.Permission // required permission ("" = none beyond auth mode)
	sessionOnly bool            // reject bearer-token principals
	// reauth: a high-impact action that a stolen session alone must not be
	// enough for, and why (reauth.go). Needs a browser session whose user
	// confirmed who they are within the re-authentication window (implies
	// sessionOnly).
	reauth reauthReason
	h      http.HandlerFunc
}

// APIPrefix is the versioned API base path.
const APIPrefix = "/api/v1"

func (s *Server) routes() []route {
	return []route{
		{method: "GET", path: APIPrefix + "/setup/status", auth: public, h: s.handleSetupStatus},
		{method: "POST", path: APIPrefix + "/setup/claim", auth: public, h: s.handleSetupClaim},

		{method: "POST", path: APIPrefix + "/auth/login", auth: public, h: s.handleLogin},
		{method: "POST", path: APIPrefix + "/auth/login/mfa", auth: public, h: s.handleLoginMFA},
		{method: "POST", path: APIPrefix + "/auth/logout", auth: authed, h: s.handleLogout},
		{method: "GET", path: APIPrefix + "/auth/me", auth: authed, h: s.handleMe},
		{method: "POST", path: APIPrefix + "/auth/password", auth: authed, sessionOnly: true, h: s.handleChangeOwnPassword},
		{method: "POST", path: APIPrefix + "/auth/reauth", auth: authed, sessionOnly: true, h: s.handleReauth},
		{method: "GET", path: APIPrefix + "/auth/mfa", auth: authed, sessionOnly: true, h: s.handleGetMFA},
		{method: "POST", path: APIPrefix + "/auth/mfa/enroll", auth: authed, reauth: reauthAccount, h: s.handleEnrollMFA},
		{method: "POST", path: APIPrefix + "/auth/mfa/confirm", auth: authed, sessionOnly: true, h: s.handleConfirmMFA},
		{method: "POST", path: APIPrefix + "/auth/mfa/recovery-codes", auth: authed, reauth: reauthAccount, h: s.handleRegenerateRecoveryCodes},
		{method: "DELETE", path: APIPrefix + "/auth/mfa", auth: authed, reauth: reauthAccount, h: s.handleDisableMFA},

		{method: "GET", path: APIPrefix + "/users", auth: authed, perm: auth.PermUsersManage, h: s.handleListUsers},
		{method: "POST", path: APIPrefix + "/users", auth: authed, perm: auth.PermUsersManage, reauth: reauthUsers, h: s.handleCreateUser},
		{method: "GET", path: APIPrefix + "/users/{id}", auth: authed, perm: auth.PermUsersManage, h: s.handleGetUser},
		// Always high-impact; the handler confirms with the reason its fields give (a password, or access).
		{method: "PATCH", path: APIPrefix + "/users/{id}", auth: authed, perm: auth.PermUsersManage, sessionOnly: true, h: s.handleUpdateUser},
		{method: "DELETE", path: APIPrefix + "/users/{id}", auth: authed, perm: auth.PermUsersManage, reauth: reauthUsers, h: s.handleDeleteUser},
		{method: "POST", path: APIPrefix + "/users/{id}/mfa/reset", auth: authed, perm: auth.PermUsersManage, reauth: reauthUsers, h: s.handleResetUserMFA},

		{method: "GET", path: APIPrefix + "/tokens", auth: authed, h: s.handleListTokens},
		{method: "POST", path: APIPrefix + "/tokens", auth: authed, reauth: reauthAccount, h: s.handleCreateToken},
		{method: "DELETE", path: APIPrefix + "/tokens/{id}", auth: authed, h: s.handleDeleteToken},

		{method: "GET", path: APIPrefix + "/audit", auth: authed, perm: auth.PermAuditRead, h: s.handleListAudit},
		{method: "GET", path: APIPrefix + "/audit/integrity", auth: authed, perm: auth.PermAuditRead, h: s.handleAuditIntegrity},
		{method: "POST", path: APIPrefix + "/audit/integrity/verify", auth: authed, perm: auth.PermAuditRead, h: s.handleVerifyAudit},
		{method: "POST", path: APIPrefix + "/audit/integrity/acknowledge", auth: authed, perm: auth.PermSettingsWrite, reauth: reauthAudit, h: s.handleAcknowledgeAuditBreak},
		{method: "GET", path: APIPrefix + "/audit/sink", auth: authed, perm: auth.PermSettingsRead, h: s.handleGetAuditSink},
		{method: "PUT", path: APIPrefix + "/audit/sink", auth: authed, perm: auth.PermSettingsWrite, reauth: reauthAudit, h: s.handlePutAuditSink},
		{method: "POST", path: APIPrefix + "/audit/sink/test", auth: authed, perm: auth.PermSettingsWrite, h: s.handleTestAuditSink},

		{method: "GET", path: APIPrefix + "/system/info", auth: authed, h: s.handleSystemInfo},
		{method: "GET", path: APIPrefix + "/system/security", auth: authed, perm: auth.PermSettingsRead, h: s.handleSecurityFacts},
		{method: "GET", path: APIPrefix + "/system/restore", auth: authed, perm: auth.PermSettingsRead, h: s.handleRestoreStatus},
		{method: "POST", path: APIPrefix + "/system/restore/finish", auth: authed, perm: auth.PermSettingsWrite, reauth: reauthRecovery, h: s.handleFinishRestore},
		{method: "GET", path: APIPrefix + "/hosts/{id}/reconciliation", auth: authed, perm: auth.PermHostsRead, h: s.handleHostReconciliation},
		{method: "POST", path: APIPrefix + "/hosts/{id}/reconciliation/accept", auth: authed, perm: auth.PermSettingsWrite, reauth: reauthRecovery, h: s.handleAcceptHostState},
		{method: "POST", path: APIPrefix + "/hosts/{id}/reconciliation/retry", auth: authed, perm: auth.PermSettingsWrite, h: s.handleRetryHostReconciliation},

		{method: "GET", path: APIPrefix + "/secrets", auth: authed, perm: auth.PermCredentialsUse, h: s.handleListSecrets},
		{method: "POST", path: APIPrefix + "/secrets", auth: authed, perm: auth.PermCredentialsManage, h: s.handleCreateSecret},
		{method: "GET", path: APIPrefix + "/secrets/controller-ssh-key", auth: authed, perm: auth.PermHostsRead, h: s.handleControllerKey},
		{method: "GET", path: APIPrefix + "/ssh-ca", auth: authed, perm: auth.PermCredentialsUse, h: s.handleGetSSHCA},
		{method: "POST", path: APIPrefix + "/ssh-ca", auth: authed, perm: auth.PermCredentialsManage, reauth: reauthSSHCA, h: s.handleCreateSSHCA},
		{method: "POST", path: APIPrefix + "/ssh-ca/rotate", auth: authed, perm: auth.PermCredentialsManage, reauth: reauthSSHCA, h: s.handleRotateSSHCA},
		{method: "POST", path: APIPrefix + "/ssh-ca/{id}/activate", auth: authed, perm: auth.PermCredentialsManage, reauth: reauthSSHCA, h: s.handleActivateSSHCA},
		{method: "POST", path: APIPrefix + "/ssh-ca/{id}/retire", auth: authed, perm: auth.PermCredentialsManage, reauth: reauthSSHCA, h: s.handleRetireSSHCA},
		{method: "GET", path: APIPrefix + "/hosts/{id}/ssh-auth", auth: authed, perm: auth.PermHostsRead, h: s.handleGetHostSSHAuth},
		{method: "PUT", path: APIPrefix + "/hosts/{id}/ssh-auth", auth: authed, perm: auth.PermCredentialsManage, reauth: reauthHostAccess, h: s.handleSetHostSSHAuth},
		{method: "POST", path: APIPrefix + "/hosts/{id}/ssh-account/plan", auth: authed, perm: auth.PermCredentialsManage, h: s.handlePlanSSHAccount},
		{method: "POST", path: APIPrefix + "/hosts/{id}/ssh-account", auth: authed, perm: auth.PermCredentialsManage, reauth: reauthHostAccess, h: s.handleSetUpSSHAccount},
		{method: "GET", path: APIPrefix + "/secrets/{id}", auth: authed, perm: auth.PermCredentialsUse, h: s.handleGetSecret},
		{method: "PATCH", path: APIPrefix + "/secrets/{id}", auth: authed, perm: auth.PermCredentialsManage, h: s.handleUpdateSecret},
		{method: "DELETE", path: APIPrefix + "/secrets/{id}", auth: authed, perm: auth.PermCredentialsManage, h: s.handleDeleteSecret},

		// The navigation search: each kind of record only with its own read permission.
		{method: "GET", path: APIPrefix + "/search", auth: authed, h: s.handleSearch},
		{method: "GET", path: APIPrefix + "/hosts", auth: authed, perm: auth.PermHostsRead, h: s.handleListHosts},
		{method: "POST", path: APIPrefix + "/hosts", auth: authed, perm: auth.PermHostsWrite, h: s.handleCreateHost},
		{method: "GET", path: APIPrefix + "/hosts/{id}", auth: authed, perm: auth.PermHostsRead, h: s.handleGetHost},
		{method: "PATCH", path: APIPrefix + "/hosts/{id}", auth: authed, perm: auth.PermHostsWrite, h: s.handleUpdateHost},
		{method: "DELETE", path: APIPrefix + "/hosts/{id}", auth: authed, perm: auth.PermHostsWrite, h: s.handleDeleteHost},
		{method: "POST", path: APIPrefix + "/hosts/{id}/removal/plan", auth: authed, perm: auth.PermHostsWrite, h: s.handlePlanRemoval},
		{method: "POST", path: APIPrefix + "/hosts/{id}/removal", auth: authed, perm: auth.PermHostsWrite, reauth: reauthHostAccess, h: s.handleRemoveWithCleanup},
		{method: "GET", path: APIPrefix + "/hosts/{id}/hostkeys", auth: authed, perm: auth.PermHostsRead, h: s.handleListHostKeys},
		{method: "POST", path: APIPrefix + "/hosts/{id}/hostkeys/scan", auth: authed, perm: auth.PermHostsWrite, h: s.handleScanHostKeys},
		{method: "POST", path: APIPrefix + "/hosts/{id}/hostkeys/approve", auth: authed, perm: auth.PermHostKeysApprove, h: s.handleApproveHostKey},
		{method: "POST", path: APIPrefix + "/hosts/{id}/probe", auth: authed, perm: auth.PermJobsExecute, h: s.handleProbeHost},
		{method: "POST", path: APIPrefix + "/hosts/{id}/refresh", auth: authed, perm: auth.PermJobsExecute, h: s.handleRefreshHost},
		{method: "GET", path: APIPrefix + "/hosts/{id}/facts", auth: authed, perm: auth.PermHostsRead, h: s.handleHostFacts},

		{method: "GET", path: APIPrefix + "/hosts/{id}/files", auth: authed, perm: auth.PermFilesRead, h: s.handleFilesList},
		{method: "GET", path: APIPrefix + "/hosts/{id}/files/stat", auth: authed, perm: auth.PermFilesRead, h: s.handleFileStat},
		{method: "GET", path: APIPrefix + "/hosts/{id}/files/content", auth: authed, perm: auth.PermFilesRead, h: s.handleFileDownload},
		{method: "PUT", path: APIPrefix + "/hosts/{id}/files/content", auth: authed, perm: auth.PermFilesWrite, h: s.handleFileUpload},
		{method: "GET", path: APIPrefix + "/hosts/{id}/files/text", auth: authed, perm: auth.PermFilesRead, h: s.handleFileText},
		{method: "PUT", path: APIPrefix + "/hosts/{id}/files/text", auth: authed, perm: auth.PermFilesWrite, h: s.handleFileSaveText},
		{method: "POST", path: APIPrefix + "/hosts/{id}/files/mkdir", auth: authed, perm: auth.PermFilesWrite, h: s.handleFileChange("mkdir")},
		{method: "POST", path: APIPrefix + "/hosts/{id}/files/rename", auth: authed, perm: auth.PermFilesWrite, h: s.handleFileChange("rename")},
		{method: "POST", path: APIPrefix + "/hosts/{id}/files/delete", auth: authed, perm: auth.PermFilesWrite, h: s.handleFileChange("delete")},
		{method: "POST", path: APIPrefix + "/hosts/{id}/files/chmod", auth: authed, perm: auth.PermFilesWrite, h: s.handleFileChange("chmod")},
		{method: "POST", path: APIPrefix + "/hosts/{id}/files/chown", auth: authed, perm: auth.PermFilesWrite, h: s.handleFileChange("chown")},
		{method: "GET", path: APIPrefix + "/hosts/{id}/managed-files", auth: authed, perm: auth.PermFilesRead, h: s.handleManagedFiles},

		{method: "POST", path: APIPrefix + "/artifacts", auth: authed, perm: auth.PermFilesDeploy, h: s.handleArtifactUpload},
		{method: "GET", path: APIPrefix + "/artifacts/{sha}", auth: authed, perm: auth.PermFilesDeploy, h: s.handleArtifactGet},
		{method: "GET", path: APIPrefix + "/artifacts/{sha}/content", auth: authed, perm: auth.PermFilesRead, h: s.handleArtifactContent},
		{method: "POST", path: APIPrefix + "/file-deployments/preview", auth: authed, perm: auth.PermFilesDeploy, h: s.handleDeployPreview},
		{method: "GET", path: APIPrefix + "/file-deployments", auth: authed, perm: auth.PermFilesRead, h: s.handleListFileDeployments},
		{method: "GET", path: APIPrefix + "/file-deployments/{id}", auth: authed, perm: auth.PermFilesRead, h: s.handleGetFileDeployment},
		{method: "POST", path: APIPrefix + "/file-deployments/{id}/rollback", auth: authed, perm: auth.PermJobsExecute, h: s.handleRollbackFileDeployment},

		{method: "GET", path: APIPrefix + "/groups", auth: authed, perm: auth.PermHostsRead, h: s.handleListGroups},
		{method: "POST", path: APIPrefix + "/groups", auth: authed, perm: auth.PermHostsWrite, h: s.handleCreateGroup},
		{method: "GET", path: APIPrefix + "/groups/{id}", auth: authed, perm: auth.PermHostsRead, h: s.handleGetGroup},
		{method: "PATCH", path: APIPrefix + "/groups/{id}", auth: authed, perm: auth.PermHostsWrite, h: s.handleUpdateGroup},
		{method: "DELETE", path: APIPrefix + "/groups/{id}", auth: authed, perm: auth.PermHostsWrite, h: s.handleDeleteGroup},
		{method: "GET", path: APIPrefix + "/groups/{id}/dependencies", auth: authed, perm: auth.PermHostsRead, h: s.handleGroupDependencies},
		{method: "POST", path: APIPrefix + "/groups/{id}/members", auth: authed, perm: auth.PermHostsWrite, h: s.handleAddGroupMembers},
		{method: "DELETE", path: APIPrefix + "/groups/{id}/members/{host_id}", auth: authed, perm: auth.PermHostsWrite, h: s.handleRemoveGroupMember},

		{method: "POST", path: APIPrefix + "/jobs/preview", auth: authed, perm: auth.PermJobsRead, h: s.handlePreviewJob},
		{method: "POST", path: APIPrefix + "/jobs", auth: authed, perm: auth.PermJobsExecute, h: s.handleCreateJob},
		{method: "POST", path: APIPrefix + "/jobs/simulate", auth: authed, perm: auth.PermJobsExecute, h: s.handleSimulateJob},
		{method: "GET", path: APIPrefix + "/jobs", auth: authed, perm: auth.PermJobsRead, h: s.handleListJobs},
		{method: "GET", path: APIPrefix + "/jobs/{id}", auth: authed, perm: auth.PermJobsRead, h: s.handleGetJob},
		{method: "GET", path: APIPrefix + "/jobs/{id}/hosts", auth: authed, perm: auth.PermJobsRead, h: s.handleListJobHosts},
		{method: "GET", path: APIPrefix + "/jobs/{id}/hosts/{hostId}", auth: authed, perm: auth.PermJobsRead, h: s.handleGetJobHost},
		{method: "GET", path: APIPrefix + "/jobs/{id}/hosts/{hostId}/output", auth: authed, perm: auth.PermJobsRead, h: s.handleJobOutput},
		{method: "GET", path: APIPrefix + "/jobs/{id}/events", auth: authed, perm: auth.PermJobsRead, h: s.handleJobEvents},
		{method: "POST", path: APIPrefix + "/jobs/{id}/cancel", auth: authed, perm: auth.PermJobsExecute, h: s.handleCancelJob},
		{method: "POST", path: APIPrefix + "/jobs/{id}/resume", auth: authed, perm: auth.PermJobsExecute, h: s.handleResumeJob},
		{method: "POST", path: APIPrefix + "/jobs/{id}/rerun", auth: authed, perm: auth.PermJobsExecute, h: s.handleRerunJob},
		// Workflows and schedules (M13).
		{method: "GET", path: APIPrefix + "/workflows", auth: authed, perm: auth.PermWorkflowsRead, h: s.handleListWorkflows},
		{method: "POST", path: APIPrefix + "/workflows", auth: authed, perm: auth.PermWorkflowsManage, h: s.handleCreateWorkflow},
		{method: "GET", path: APIPrefix + "/workflows/templates", auth: authed, perm: auth.PermWorkflowsRead, h: s.handleWorkflowTemplates},
		{method: "GET", path: APIPrefix + "/workflows/{id}", auth: authed, perm: auth.PermWorkflowsRead, h: s.handleGetWorkflow},
		{method: "PUT", path: APIPrefix + "/workflows/{id}", auth: authed, perm: auth.PermWorkflowsManage, h: s.handleUpdateWorkflow},
		{method: "DELETE", path: APIPrefix + "/workflows/{id}", auth: authed, perm: auth.PermWorkflowsManage, h: s.handleArchiveWorkflow},
		{method: "GET", path: APIPrefix + "/workflows/{id}/revisions/{rev}", auth: authed, perm: auth.PermWorkflowsRead, h: s.handleWorkflowRevision},
		{method: "POST", path: APIPrefix + "/workflows/{id}/run", auth: authed, perm: auth.PermJobsExecute, h: s.handleRunWorkflow},
		{method: "GET", path: APIPrefix + "/schedules", auth: authed, perm: auth.PermSchedulesRead, h: s.handleListSchedules},
		{method: "POST", path: APIPrefix + "/schedules", auth: authed, perm: auth.PermSchedulesManage, h: s.handleCreateSchedule},
		{method: "POST", path: APIPrefix + "/schedules/preview", auth: authed, perm: auth.PermSchedulesRead, h: s.handlePreviewSchedule},
		{method: "POST", path: APIPrefix + "/schedules/next-runs", auth: authed, perm: auth.PermSchedulesRead, h: s.handleNextRuns},
		{method: "GET", path: APIPrefix + "/schedules/{id}", auth: authed, perm: auth.PermSchedulesRead, h: s.handleGetSchedule},
		{method: "PUT", path: APIPrefix + "/schedules/{id}", auth: authed, perm: auth.PermSchedulesManage, h: s.handleUpdateSchedule},
		{method: "DELETE", path: APIPrefix + "/schedules/{id}", auth: authed, perm: auth.PermSchedulesManage, h: s.handleArchiveSchedule},
		{method: "POST", path: APIPrefix + "/schedules/{id}/enable", auth: authed, perm: auth.PermSchedulesManage, h: s.handleEnableSchedule},
		{method: "POST", path: APIPrefix + "/schedules/{id}/disable", auth: authed, perm: auth.PermSchedulesManage, h: s.handleDisableSchedule},
		{method: "POST", path: APIPrefix + "/schedules/{id}/run-now", auth: authed, perm: auth.PermSchedulesManage, h: s.handleRunScheduleNow},
		{method: "POST", path: APIPrefix + "/schedules/{id}/owner", auth: authed, perm: auth.PermSchedulesAdmin, h: s.handleReassignSchedule},
		{method: "GET", path: APIPrefix + "/schedules/{id}/occurrences", auth: authed, perm: auth.PermSchedulesRead, h: s.handleScheduleOccurrences},
		// Desired state (M14).
		{method: "GET", path: APIPrefix + "/desired/summary", auth: authed, perm: auth.PermDesiredRead, h: s.handleDesiredSummary},
		{method: "GET", path: APIPrefix + "/desired/hosts", auth: authed, perm: auth.PermDesiredRead, h: s.handleDesiredHosts},
		{method: "GET", path: APIPrefix + "/desired/hosts/{id}", auth: authed, perm: auth.PermDesiredRead, h: s.handleDesiredHost},
		{method: "GET", path: APIPrefix + "/desired/hosts/{id}/resource", auth: authed, perm: auth.PermDesiredRead, h: s.handleDesiredResource},
		{method: "GET", path: APIPrefix + "/desired/hosts/{id}/review", auth: authed, perm: auth.PermDesiredRead, h: s.handleDesiredReview},
		{method: "POST", path: APIPrefix + "/desired/hosts/{id}/refresh", auth: authed, perm: auth.PermDesiredRestore, h: s.handleDesiredRefresh},
		{method: "POST", path: APIPrefix + "/desired/hosts/{id}/accept", auth: authed, perm: auth.PermDesiredManage, h: s.handleDesiredAccept},
		{method: "PUT", path: APIPrefix + "/desired/hosts/{id}/overrides", auth: authed, perm: auth.PermDesiredManage, h: s.handleDesiredSetOverride},
		{method: "DELETE", path: APIPrefix + "/desired/hosts/{id}/overrides", auth: authed, perm: auth.PermDesiredManage, h: s.handleDesiredResetOverride},
		{method: "POST", path: APIPrefix + "/desired/refresh", auth: authed, perm: auth.PermDesiredRestore, h: s.handleDesiredRefresh},
		{method: "POST", path: APIPrefix + "/desired/restore/preview", auth: authed, perm: auth.PermDesiredRestore, h: s.handleDesiredRestorePreview},
		{method: "POST", path: APIPrefix + "/desired/restore", auth: authed, perm: auth.PermDesiredRestore, h: s.handleDesiredRestore},
		{method: "GET", path: APIPrefix + "/desired/history", auth: authed, perm: auth.PermDesiredRead, h: s.handleDesiredHistory},
		{method: "GET", path: APIPrefix + "/desired/baselines", auth: authed, perm: auth.PermDesiredRead, h: s.handleListBaselines},
		{method: "POST", path: APIPrefix + "/desired/baselines", auth: authed, perm: auth.PermDesiredManage, h: s.handleCreateBaseline},
		{method: "POST", path: APIPrefix + "/desired/baselines/preview", auth: authed, perm: auth.PermDesiredManage, h: s.handlePreviewBaseline},
		{method: "GET", path: APIPrefix + "/desired/baselines/{id}", auth: authed, perm: auth.PermDesiredRead, h: s.handleGetBaseline},
		{method: "PUT", path: APIPrefix + "/desired/baselines/{id}", auth: authed, perm: auth.PermDesiredManage, h: s.handleUpdateBaseline},
		{method: "DELETE", path: APIPrefix + "/desired/baselines/{id}", auth: authed, perm: auth.PermDesiredManage, h: s.handleArchiveBaseline},
		{method: "GET", path: APIPrefix + "/desired/baselines/{id}/revisions/{rev}", auth: authed, perm: auth.PermDesiredRead, h: s.handleBaselineRevision},
		{method: "PUT", path: APIPrefix + "/desired/baselines/{id}/auto", auth: authed, perm: auth.PermDesiredAuto, h: s.handleBaselineAuto},
		{method: "GET", path: APIPrefix + "/desired/groups", auth: authed, perm: auth.PermDesiredRead, h: s.handleDesiredGroups},
		{method: "PUT", path: APIPrefix + "/desired/groups/{id}/priority", auth: authed, perm: auth.PermDesiredManage, h: s.handleSetDesiredPriority},
		{method: "POST", path: APIPrefix + "/desired/groups/{id}/priority/preview", auth: authed, perm: auth.PermDesiredManage, h: s.handlePreviewDesiredPriority},

		// Agents (M7).
		{method: "GET", path: APIPrefix + "/agents", auth: authed, perm: auth.PermAgentsRead, h: s.handleListAgents},
		{method: "GET", path: APIPrefix + "/agents/pki", auth: authed, perm: auth.PermAgentsRead, h: s.handleAgentPKI},
		{method: "GET", path: APIPrefix + "/agents/pki/root.crt", auth: authed, perm: auth.PermAgentsRead, h: s.handleAgentRootCert},
		{method: "POST", path: APIPrefix + "/agents/pki/export-root", auth: authed, perm: auth.PermAgentsPKI, sessionOnly: true, reauth: reauthShepherdPKI, h: s.handleAgentExportRoot},
		{method: "POST", path: APIPrefix + "/agents/pki/remove-root", auth: authed, perm: auth.PermAgentsPKI, sessionOnly: true, reauth: reauthShepherdPKI, h: s.handleAgentRemoveRoot},
		{method: "POST", path: APIPrefix + "/agents/pki/rotate", auth: authed, perm: auth.PermAgentsPKI, sessionOnly: true, reauth: reauthShepherdPKI, h: s.handleAgentRotate},
		{method: "POST", path: APIPrefix + "/agents/pki/manifests", auth: authed, perm: auth.PermAgentsPKI, reauth: reauthShepherdPKI, h: s.handleAgentImportManifest},
		{method: "GET", path: APIPrefix + "/agents/presets", auth: authed, perm: auth.PermAgentsRead, h: s.handleAgentPresets},
		{method: "GET", path: APIPrefix + "/agents/unknown", auth: authed, perm: auth.PermAgentsRead, h: s.handleListUnknownAgents},
		{method: "POST", path: APIPrefix + "/agents/unknown/{id}/dismiss", auth: authed, perm: auth.PermAgentsManage, h: s.handleDismissUnknownAgent},
		{method: "GET", path: APIPrefix + "/agents/tokens", auth: authed, perm: auth.PermAgentsEnroll, h: s.handleListAgentTokens},
		{method: "POST", path: APIPrefix + "/agents/tokens", auth: authed, perm: auth.PermAgentsEnroll, h: s.handleCreateAgentToken},
		{method: "POST", path: APIPrefix + "/agents/tokens/{id}/revoke", auth: authed, perm: auth.PermAgentsEnroll, h: s.handleRevokeAgentToken},
		{method: "POST", path: APIPrefix + "/agents/{id}/revoke", auth: authed, perm: auth.PermAgentsManage, reauth: reauthShepherd, h: s.handleRevokeAgent},
		{method: "POST", path: APIPrefix + "/agents/{id}/resolve-conflict", auth: authed, perm: auth.PermAgentsManage, reauth: reauthShepherd, h: s.handleResolveConflict},
		{method: "GET", path: APIPrefix + "/agents/updates/policy", auth: authed, perm: auth.PermAgentsRead, h: s.handleGetUpdatePolicy},
		{method: "PUT", path: APIPrefix + "/agents/updates/policy", auth: authed, perm: auth.PermAgentsManage, h: s.handleSetUpdatePolicy},
		{method: "GET", path: APIPrefix + "/agents/release-keys", auth: authed, perm: auth.PermAgentsRead, h: s.handleListReleaseKeys},
		{method: "POST", path: APIPrefix + "/agents/release-keys", auth: authed, perm: auth.PermAgentsManage, reauth: reauthShepherdPKI, h: s.handleAddReleaseKey},
		{method: "DELETE", path: APIPrefix + "/agents/release-keys/{id}", auth: authed, perm: auth.PermAgentsManage, reauth: reauthShepherdPKI, h: s.handleDeleteReleaseKey},
		{method: "GET", path: APIPrefix + "/agents/releases", auth: authed, perm: auth.PermAgentsRead, h: s.handleListReleases},
		{method: "POST", path: APIPrefix + "/agents/releases", auth: authed, perm: auth.PermAgentsManage, h: s.handleUploadRelease},
		{method: "DELETE", path: APIPrefix + "/agents/releases/{id}", auth: authed, perm: auth.PermAgentsManage, h: s.handleDeleteRelease},
		{method: "GET", path: APIPrefix + "/agents/rollouts", auth: authed, perm: auth.PermAgentsRead, h: s.handleListRollouts},
		{method: "POST", path: APIPrefix + "/agents/rollouts", auth: authed, perm: auth.PermAgentsManage, h: s.handleStartRollout},
		{method: "GET", path: APIPrefix + "/agents/rollouts/{id}", auth: authed, perm: auth.PermAgentsRead, h: s.handleGetRollout},
		{method: "POST", path: APIPrefix + "/agents/rollouts/{id}/pause", auth: authed, perm: auth.PermAgentsManage, h: s.handleRolloutAction("pause")},
		{method: "POST", path: APIPrefix + "/agents/rollouts/{id}/resume", auth: authed, perm: auth.PermAgentsManage, h: s.handleRolloutAction("resume")},
		{method: "POST", path: APIPrefix + "/agents/rollouts/{id}/cancel", auth: authed, perm: auth.PermAgentsManage, h: s.handleRolloutAction("cancel")},
		{method: "GET", path: APIPrefix + "/agents/endpoints", auth: authed, perm: auth.PermAgentsRead, h: s.handleListEndpointMigrations},
		{method: "POST", path: APIPrefix + "/agents/endpoints", auth: authed, perm: auth.PermAgentsManage, h: s.handleProposeEndpoint},
		{method: "POST", path: APIPrefix + "/agents/endpoints/{id}/commit", auth: authed, perm: auth.PermAgentsManage, reauth: reauthShepherdPKI, h: s.handleCommitEndpoint},
		{method: "GET", path: APIPrefix + "/hosts/{id}/agent", auth: authed, perm: auth.PermHostsRead, h: s.handleHostAgent},
		{method: "PUT", path: APIPrefix + "/hosts/{id}/management", auth: authed, perm: auth.PermHostsWrite, h: s.handleSetManagement},
		{method: "PUT", path: APIPrefix + "/hosts/{id}/agent/caps", auth: authed, perm: auth.PermAgentsManage, h: s.handleSetAgentCaps},
		{method: "POST", path: APIPrefix + "/hosts/{id}/agent/install", auth: authed, perm: auth.PermAgentsEnroll, reauth: reauthShepherd, h: s.handleInstallAgent},
		{method: "POST", path: APIPrefix + "/hosts/{id}/agent/uninstall/plan", auth: authed, perm: auth.PermAgentsManage, h: s.handlePlanUninstallAgent},
		{method: "POST", path: APIPrefix + "/hosts/{id}/agent/uninstall", auth: authed, perm: auth.PermAgentsManage, reauth: reauthShepherd, h: s.handleUninstallAgent},
		{method: "POST", path: APIPrefix + "/hosts/{id}/state/{domain}/adopt", auth: authed, perm: auth.PermAgentsManage, h: s.handleAdoptState},

		// Firewall (M8).
		{method: "GET", path: APIPrefix + "/firewall/settings", auth: authed, perm: auth.PermFirewallRead, h: s.handleFirewallSettings},
		{method: "PUT", path: APIPrefix + "/firewall/settings", auth: authed, perm: auth.PermFirewallWrite, h: s.handleSetFirewallSettings},
		{method: "GET", path: APIPrefix + "/firewall/services", auth: authed, perm: auth.PermFirewallRead, h: s.handleListFirewallServices},
		{method: "POST", path: APIPrefix + "/firewall/services", auth: authed, perm: auth.PermFirewallWrite, h: s.handleCreateFirewallService},
		{method: "DELETE", path: APIPrefix + "/firewall/services/{id}", auth: authed, perm: auth.PermFirewallWrite, h: s.handleDeleteFirewallService},
		{method: "GET", path: APIPrefix + "/firewall/groups", auth: authed, perm: auth.PermFirewallRead, h: s.handleListFirewallGroups},
		{method: "PUT", path: APIPrefix + "/firewall/groups/{id}", auth: authed, perm: auth.PermFirewallWrite, h: s.handleSetFirewallGroup},
		{method: "GET", path: APIPrefix + "/firewall/policies", auth: authed, perm: auth.PermFirewallRead, h: s.handleListFirewallPolicies},
		{method: "POST", path: APIPrefix + "/firewall/policies", auth: authed, perm: auth.PermFirewallWrite, h: s.handleCreateFirewallPolicy},
		{method: "POST", path: APIPrefix + "/firewall/policies/preview", auth: authed, perm: auth.PermFirewallRead, h: s.handleFirewallPolicyPreview},
		{method: "GET", path: APIPrefix + "/firewall/policies/{id}", auth: authed, perm: auth.PermFirewallRead, h: s.handleGetFirewallPolicy},
		{method: "PUT", path: APIPrefix + "/firewall/policies/{id}", auth: authed, perm: auth.PermFirewallWrite, h: s.handleUpdateFirewallPolicy},
		{method: "DELETE", path: APIPrefix + "/firewall/policies/{id}", auth: authed, perm: auth.PermFirewallWrite, h: s.handleDeleteFirewallPolicy},
		{method: "GET", path: APIPrefix + "/firewall/hosts", auth: authed, perm: auth.PermFirewallRead, h: s.handleListFirewallHosts},
		{method: "GET", path: APIPrefix + "/firewall/hosts/{id}", auth: authed, perm: auth.PermFirewallRead, h: s.handleGetFirewallHost},
		{method: "PUT", path: APIPrefix + "/firewall/hosts/{id}", auth: authed, perm: auth.PermFirewallWrite, h: s.handleSetFirewallHost},
		{method: "POST", path: APIPrefix + "/firewall/hosts/{id}/detect", auth: authed, perm: auth.PermFirewallWrite, h: s.handleFirewallDetect},
		{method: "POST", path: APIPrefix + "/firewall/hosts/{id}/snapshot", auth: authed, perm: auth.PermFirewallWrite, h: s.handleFirewallSnapshot},
		{method: "GET", path: APIPrefix + "/firewall/hosts/{id}/observations", auth: authed, perm: auth.PermNetworkRead, h: s.handleFirewallObservations},
		{method: "GET", path: APIPrefix + "/firewall/hosts/{id}/suggestions", auth: authed, perm: auth.PermNetworkRead, h: s.handleFirewallSuggestions},
		{method: "POST", path: APIPrefix + "/firewall/hosts/{id}/adopt", auth: authed, perm: auth.PermFirewallWrite, h: s.handleFirewallAdopt},
		{method: "POST", path: APIPrefix + "/firewall/apply", auth: authed, perm: auth.PermFirewallWrite, h: s.handleFirewallApply},
		{method: "GET", path: APIPrefix + "/firewall/transactions", auth: authed, perm: auth.PermFirewallRead, h: s.handleListFirewallTransactions},
		{method: "GET", path: APIPrefix + "/firewall/transactions/{id}", auth: authed, perm: auth.PermFirewallRead, h: s.handleGetFirewallTransaction},
		{method: "GET", path: APIPrefix + "/firewall/document", auth: authed, perm: auth.PermFirewallRead, h: s.handleGetFirewallDocument},
		{method: "PUT", path: APIPrefix + "/firewall/document", auth: authed, perm: auth.PermFirewallWrite, h: s.handleSaveFirewallDocument},
		{method: "POST", path: APIPrefix + "/firewall/document/check", auth: authed, perm: auth.PermFirewallRead, h: s.handleCheckFirewallDocument},
		{method: "POST", path: APIPrefix + "/firewall/document/explain", auth: authed, perm: auth.PermFirewallRead, h: s.handleFirewallDraftExplain},
		{method: "GET", path: APIPrefix + "/firewall/layout", auth: authed, perm: auth.PermFirewallRead, h: s.handleGetFirewallLayout},
		{method: "PUT", path: APIPrefix + "/firewall/layout", auth: authed, perm: auth.PermFirewallWrite, h: s.handleSetFirewallLayout},
		{method: "POST", path: APIPrefix + "/firewall/simulate", auth: authed, perm: auth.PermFirewallRead, h: s.handleFirewallSimulate},
		{method: "GET", path: APIPrefix + "/network/summary", auth: authed, perm: auth.PermNetworkRead, h: s.handleNetworkSummary},
		{method: "GET", path: APIPrefix + "/network/flows", auth: authed, perm: auth.PermNetworkRead, h: s.handleNetworkFlows},
		{method: "GET", path: APIPrefix + "/network/flows/{id}", auth: authed, perm: auth.PermNetworkRead, h: s.handleNetworkFlow},
		{method: "GET", path: APIPrefix + "/network/filters", auth: authed, perm: auth.PermNetworkRead, h: s.handleNetworkFacets},
		{method: "GET", path: APIPrefix + "/network/topology", auth: authed, perm: auth.PermNetworkRead, h: s.handleNetworkTopology},
		{method: "GET", path: APIPrefix + "/network/settings", auth: authed, perm: auth.PermNetworkRead, h: s.handleNetworkSettings},
		{method: "PUT", path: APIPrefix + "/network/settings", auth: authed, perm: auth.PermNetworkSettings, h: s.handleSetNetworkSettings},
		{method: "GET", path: APIPrefix + "/network/enrichment", auth: authed, perm: auth.PermNetworkRead, h: s.handleNetworkEnrichment},
		{method: "GET", path: APIPrefix + "/network/classifications", auth: authed, perm: auth.PermNetworkRead, h: s.handleListClassifications},
		{method: "POST", path: APIPrefix + "/network/classifications", auth: authed, perm: auth.PermNetworkClassify, h: s.handlePutClassification},
		{method: "PUT", path: APIPrefix + "/network/classifications/{id}", auth: authed, perm: auth.PermNetworkClassify, h: s.handlePutClassification},
		{method: "DELETE", path: APIPrefix + "/network/classifications/{id}", auth: authed, perm: auth.PermNetworkClassify, h: s.handleDeleteClassification},
		{method: "POST", path: APIPrefix + "/network/enrichment/reload", auth: authed, perm: auth.PermNetworkSettings, h: s.handleReloadNetworkEnrichment},
		{method: "GET", path: APIPrefix + "/hosts/{id}/network", auth: authed, perm: auth.PermNetworkRead, h: s.handleHostNetwork},
		{method: "GET", path: APIPrefix + "/monitoring/summary", auth: authed, perm: auth.PermMonitoringRead, h: s.handleMonitoringSummary},
		{method: "GET", path: APIPrefix + "/monitoring/issues", auth: authed, perm: auth.PermMonitoringRead, h: s.handleMonitoringIssues},
		{method: "GET", path: APIPrefix + "/monitoring/settings", auth: authed, perm: auth.PermMonitoringRead, h: s.handleMonitoringSettings},
		{method: "PUT", path: APIPrefix + "/monitoring/settings", auth: authed, perm: auth.PermMonitoringSettings, h: s.handleSetMonitoringSettings},
		{method: "GET", path: APIPrefix + "/monitoring/health/checks", auth: authed, perm: auth.PermMonitoringRead, h: s.handleHealthChecks},
		{method: "GET", path: APIPrefix + "/monitoring/health/global", auth: authed, perm: auth.PermMonitoringRead, h: s.handleHealthGlobal},
		{method: "GET", path: APIPrefix + "/monitoring/health/groups", auth: authed, perm: auth.PermMonitoringRead, h: s.handleHealthGroups},
		{method: "GET", path: APIPrefix + "/monitoring/health/groups/{id}", auth: authed, perm: auth.PermMonitoringRead, h: s.handleHealthGroup},
		{method: "PUT", path: APIPrefix + "/monitoring/health/groups/{id}/priority", auth: authed, perm: auth.PermMonitoringSettings, h: s.handleSetGroupPriority},
		{method: "GET", path: APIPrefix + "/monitoring/health/overrides", auth: authed, perm: auth.PermMonitoringRead, h: s.handleListOverrides},
		{method: "PUT", path: APIPrefix + "/monitoring/health/overrides", auth: authed, perm: auth.PermMonitoringManage, h: s.handlePutOverride},
		{method: "DELETE", path: APIPrefix + "/monitoring/health/overrides/{id}", auth: authed, perm: auth.PermMonitoringManage, h: s.handleDeleteOverride},
		{method: "POST", path: APIPrefix + "/monitoring/health/preview", auth: authed, perm: auth.PermMonitoringRead, h: s.handleHealthPreview},
		{method: "GET", path: APIPrefix + "/hosts/{id}/monitoring/health", auth: authed, perm: auth.PermMonitoringRead, h: s.handleHostHealth},
		{method: "GET", path: APIPrefix + "/monitoring/approvals", auth: authed, perm: auth.PermMonitoringRead, h: s.handleListApprovals},
		{method: "POST", path: APIPrefix + "/monitoring/approvals", auth: authed, perm: auth.PermMonitoringSettings, h: s.handleAddApproval},
		{method: "DELETE", path: APIPrefix + "/monitoring/approvals/{id}", auth: authed, perm: auth.PermMonitoringSettings, h: s.handleDeleteApproval},
		{method: "POST", path: APIPrefix + "/monitoring/helpers/preview", auth: authed, perm: auth.PermMonitoringRead, h: s.handlePreviewHelpers},
		{method: "POST", path: APIPrefix + "/monitoring/helpers/install", auth: authed, perm: auth.PermJobsExecute, h: s.handleInstallHelpers},
		{method: "GET", path: APIPrefix + "/hosts/{id}/monitoring", auth: authed, perm: auth.PermMonitoringRead, h: s.handleHostMonitoring},
		{method: "GET", path: APIPrefix + "/hosts/{id}/monitoring/history", auth: authed, perm: auth.PermMonitoringRead, h: s.handleHostMonitoringHistory},
		{method: "GET", path: APIPrefix + "/hosts/{id}/monitoring/services", auth: authed, perm: auth.PermMonitoringRead, h: s.handleHostServices},
		{method: "GET", path: APIPrefix + "/hosts/{id}/monitoring/services/{unit}/history", auth: authed, perm: auth.PermMonitoringRead, h: s.handleHostServiceHistory},
		{method: "PUT", path: APIPrefix + "/hosts/{id}/monitoring/services/{unit}/monitor", auth: authed, perm: auth.PermMonitoringManage, h: s.handleMonitorService},
		{method: "GET", path: APIPrefix + "/hosts/{id}/monitoring/service-events", auth: authed, perm: auth.PermMonitoringRead, h: s.handleHostServiceEvents},
		{method: "GET", path: APIPrefix + "/hosts/{id}/monitoring/storage", auth: authed, perm: auth.PermMonitoringRead, h: s.handleHostStorage},
		{method: "GET", path: APIPrefix + "/hosts/{id}/monitoring/hardware", auth: authed, perm: auth.PermMonitoringRead, h: s.handleHostHardware},
		{method: "GET", path: APIPrefix + "/hosts/{id}/monitoring/events", auth: authed, perm: auth.PermMonitoringRead, h: s.handleHostMonitoringEvents},
		{method: "GET", path: APIPrefix + "/hosts/{id}/monitoring/helpers", auth: authed, perm: auth.PermMonitoringRead, h: s.handleHostHelpers},
		{method: "PUT", path: APIPrefix + "/hosts/{id}/monitoring/settings", auth: authed, perm: auth.PermMonitoringManage, h: s.handleSetHostMonitoringSettings},
		{method: "POST", path: APIPrefix + "/hosts/{id}/monitoring/refresh", auth: authed, perm: auth.PermMonitoringManage, h: s.handleRefreshHostMonitoring},
		{method: "GET", path: APIPrefix + "/hosts/{id}/monitoring/diagnostics", auth: authed, perm: auth.PermMonitoringManage, h: s.handleHostDiagnostics},
		{method: "POST", path: APIPrefix + "/hosts/{id}/monitoring/diagnostics", auth: authed, perm: auth.PermMonitoringManage, h: s.handleHostDiagnostics},
		{method: "POST", path: APIPrefix + "/agents/conversions/preview", auth: authed, perm: auth.PermAgentsEnroll, h: s.handlePreviewConversion},
		{method: "POST", path: APIPrefix + "/agents/conversions", auth: authed, perm: auth.PermAgentsEnroll, reauth: reauthShepherd, h: s.handleStartConversion},
		{method: "POST", path: APIPrefix + "/hosts/{id}/network/snapshot", auth: authed, perm: auth.PermNetworkSnapshot, h: s.handleHostNetworkSnapshot},
		{method: "GET", path: APIPrefix + "/firewall/propagation", auth: authed, perm: auth.PermFirewallRead, h: s.handleFirewallPropagation},

		{method: "GET", path: APIPrefix + "/alerts", auth: authed, perm: auth.PermAlertsRead, h: s.handleListAlerts},
		{method: "GET", path: APIPrefix + "/alerts/summary", auth: authed, perm: auth.PermAlertsRead, h: s.handleAlertSummary},
		{method: "GET", path: APIPrefix + "/alerts/{id}", auth: authed, perm: auth.PermAlertsRead, h: s.handleGetAlert},
		{method: "POST", path: APIPrefix + "/alerts/{id}/acknowledge", auth: authed, perm: auth.PermAlertsAcknowledge, h: s.handleAcknowledgeAlert},
		{method: "GET", path: APIPrefix + "/alerts/{id}/ignore-options", auth: authed, perm: auth.PermAlertsRead, h: s.handleAlertIgnoreOptions},
		{method: "GET", path: APIPrefix + "/events", auth: authed, perm: auth.PermAlertsRead, h: s.handleListEvents},
		{method: "GET", path: APIPrefix + "/maintenance", auth: authed, perm: auth.PermAlertsRead, h: s.handleListMaintenance},
		{method: "POST", path: APIPrefix + "/maintenance", auth: authed, perm: auth.PermMaintenanceManage, h: s.handleStartMaintenance},
		{method: "GET", path: APIPrefix + "/maintenance/{id}", auth: authed, perm: auth.PermAlertsRead, h: s.handleGetMaintenance},
		{method: "POST", path: APIPrefix + "/maintenance/{id}/end", auth: authed, perm: auth.PermMaintenanceManage, h: s.handleEndMaintenance},
		{method: "GET", path: APIPrefix + "/notifications/destinations", auth: authed, perm: auth.PermNotificationsRead, h: s.handleListDestinations},
		{method: "POST", path: APIPrefix + "/notifications/destinations", auth: authed, perm: auth.PermNotificationsManage, h: s.handleCreateDestination},
		{method: "GET", path: APIPrefix + "/notifications/destinations/{id}", auth: authed, perm: auth.PermNotificationsRead, h: s.handleGetDestination},
		{method: "PATCH", path: APIPrefix + "/notifications/destinations/{id}", auth: authed, perm: auth.PermNotificationsManage, h: s.handleUpdateDestination},
		{method: "DELETE", path: APIPrefix + "/notifications/destinations/{id}", auth: authed, perm: auth.PermNotificationsManage, h: s.handleDeleteDestination},
		{method: "POST", path: APIPrefix + "/notifications/destinations/{id}/test", auth: authed, perm: auth.PermNotificationsTest, h: s.handleTestDestination},
		{method: "POST", path: APIPrefix + "/notifications/test", auth: authed, perm: auth.PermNotificationsManage, h: s.handleTestUnsavedDestination},
		{method: "GET", path: APIPrefix + "/notifications/routes", auth: authed, perm: auth.PermNotificationsRead, h: s.handleListRoutes},
		{method: "POST", path: APIPrefix + "/notifications/routes", auth: authed, perm: auth.PermNotificationsManage, h: s.handleCreateRoute},
		{method: "POST", path: APIPrefix + "/notifications/routes/preview", auth: authed, perm: auth.PermNotificationsRead, h: s.handlePreviewRoute},
		{method: "PATCH", path: APIPrefix + "/notifications/routes/{id}", auth: authed, perm: auth.PermNotificationsManage, h: s.handleUpdateRoute},
		{method: "DELETE", path: APIPrefix + "/notifications/routes/{id}", auth: authed, perm: auth.PermNotificationsManage, h: s.handleDeleteRoute},
		{method: "GET", path: APIPrefix + "/notifications/deliveries", auth: authed, perm: auth.PermNotificationsRead, h: s.handleListDeliveries},
		{method: "GET", path: APIPrefix + "/notifications/settings", auth: authed, perm: auth.PermNotificationsRead, h: s.handleNotificationSettings},
		{method: "PUT", path: APIPrefix + "/notifications/settings", auth: authed, perm: auth.PermNotificationsManage, h: s.handleSetNotificationSettings},
	}
}

// New builds the server.
func New(d Deps) (*Server, error) {
	if d.StaleAfter <= 0 {
		d.StaleAfter = 15 * time.Minute
	}
	s := &Server{
		d:            d,
		log:          d.Logs.App,
		secLog:       d.Logs.Security,
		secureCookie: !d.Config.IsInsecurePlaintext(),
	}
	for _, p := range d.Config.Server.TrustedProxies {
		pfx, err := netip.ParsePrefix(p)
		if err != nil {
			addr, err2 := netip.ParseAddr(p)
			if err2 != nil {
				return nil, err
			}
			pfx = netip.PrefixFrom(addr, addr.BitLen())
		}
		s.trustedProxies = append(s.trustedProxies, pfx)
	}
	s.metrics = newHTTPMetrics(d.Registry)

	mux := http.NewServeMux()
	for _, rt := range s.routes() {
		mux.Handle(rt.method+" "+rt.path, s.wrapRoute(rt))
	}
	mux.Handle(APIPrefix+"/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, r, errNotFound("endpoint"))
	}))
	// Agent installer (public: no secrets; see handleAgentInstallScript).
	mux.HandleFunc("GET /agent/install.sh", s.handleAgentInstallScript)
	mux.HandleFunc("GET /agent/download/{platform}", s.handleAgentDownload)
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /ready", s.handleReady)
	mux.Handle("GET /metrics", metricsHandler(d.Registry))
	mux.Handle("/", s.uiHandler())

	s.handler = s.withRequestContext(s.withRecover(s.withSecurityHeaders(s.withAccessLog(mux))))
	return s, nil
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler { return s.handler }

// SetReady marks the server ready to receive traffic.
func (s *Server) SetReady(v bool) { s.ready.Store(v) }

// wrapRoute applies authentication, CSRF and permission checks.
func (s *Server) wrapRoute(rt route) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rt.auth == public {
			rt.h(w, r)
			return
		}
		p, prob := s.authenticate(r)
		if prob != nil {
			writeProblem(w, r, prob)
			return
		}
		if isStateChanging(r.Method) && !auth.CheckCSRF(p, r.Header.Get(csrfHeader)) {
			s.secLog.Warn("csrf check failed", "user_id", p.UserID, "path", r.URL.Path, "request_id", requestIDFrom(r.Context()))
			writeProblem(w, r, errCSRF)
			return
		}
		if (rt.sessionOnly || rt.reauth != "") && p.Kind != auth.KindSession {
			writeProblem(w, r, errForbidden("this endpoint requires an interactive session, not an API token"))
			return
		}
		if rt.perm != "" && !p.Can(rt.perm) {
			ctx := withPrincipal(r.Context(), p)
			_ = s.d.Audit.Record(ctx, audit.Event{
				Action: "authz.denied", Result: audit.Denied,
				Details: map[string]any{"permission": string(rt.perm), "method": r.Method, "path": r.URL.Path},
			})
			writeProblem(w, r, errForbidden("missing permission "+string(rt.perm)))
			return
		}
		r = r.WithContext(withPrincipal(r.Context(), p))
		if rt.reauth != "" && !s.requireReauth(w, r, rt.reauth) {
			return
		}
		rt.h(w, r)
	})
}

const (
	sessionCookie = "drool_session"
	csrfHeader    = "X-DROOL-CSRF"
)

func isStateChanging(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

func (s *Server) authenticate(r *http.Request) (*auth.Principal, *Problem) {
	ctx := r.Context()
	if h := r.Header.Get("Authorization"); h != "" {
		tok, ok := cutPrefixFold(h, "Bearer ")
		if !ok {
			return nil, errUnauthenticated
		}
		p, err := s.d.Auth.AuthenticateToken(ctx, tok)
		if err != nil {
			if err != auth.ErrUnauthenticated {
				s.log.Error("token authentication", "err", err)
				return nil, errInternal
			}
			s.secLog.Warn("invalid API token", "source_ip", clientIPFrom(ctx), "request_id", requestIDFrom(ctx))
			return nil, errUnauthenticated
		}
		return p, nil
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil, errUnauthenticated
	}
	p, err := s.d.Auth.AuthenticateSession(ctx, c.Value)
	if err != nil {
		if err != auth.ErrUnauthenticated {
			s.log.Error("session authentication", "err", err)
			return nil, errInternal
		}
		return nil, errUnauthenticated
	}
	return p, nil
}

func (s *Server) setSessionCookie(w http.ResponseWriter, value string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: value, Path: "/",
		Expires: expires, HttpOnly: true, Secure: s.secureCookie, SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.secureCookie, SameSite: http.SameSiteStrictMode,
	})
}

// inTxAudited runs fn in a write transaction together with the audit record
// it returns, so a change and its audit trail commit or roll back together.
// On failure nothing is recorded; callers audit failed attempts separately
// where useful.
func (s *Server) inTxAudited(ctx context.Context, fn func(q *db.Queries) (audit.Event, error)) error {
	var emit func()
	err := s.d.Store.InTx(ctx, func(q *db.Queries) error {
		ev, err := fn(q)
		if err != nil {
			return err
		}
		emit, err = s.d.Audit.RecordTx(ctx, q, ev)
		return err
	})
	if err == nil {
		emit()
	}
	return err
}

// inRawTxAudited is inTxAudited for changes that use raw SQL: fn gets the
// transaction itself and the query set over it.
func (s *Server) inRawTxAudited(ctx context.Context, fn func(tx *sql.Tx, q *db.Queries) (audit.Event, error)) error {
	var emit func()
	err := s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		ev, err := fn(tx, q)
		if err != nil {
			return err
		}
		emit, err = s.d.Audit.RecordTx(ctx, q, ev)
		return err
	})
	if err == nil {
		emit()
	}
	return err
}

// RunSessionCleanup periodically deletes expired sessions until ctx ends.
func (s *Server) RunSessionCleanup(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := s.d.Auth.CleanupExpired(ctx); err != nil {
				s.log.Error("session cleanup", "err", err)
			} else if n > 0 {
				s.log.Debug("session cleanup", "deleted", n)
			}
		}
	}
}
