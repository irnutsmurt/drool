package api

import (
	"net/http"
	"time"

	"drool/internal/auth"
)

// Re-authentication policy (ARCHITECTURE-ALPHA §33.3, reviewed in the final
// After-M17 batch, §38). Ordinary administration never asks for the password
// again; high-impact trust, privilege and destructive actions need a session
// that confirmed who it is within the re-authentication window (signing in
// counts), and one confirmation covers every such action until the window
// ends. Whether an action is high-impact is decided per action, never by HTTP
// verb: the route table names a reason for whole routes, and handlers whose
// requests are only sometimes high-impact (replacing a credential hosts use,
// switching an established host's sign-in, a firewall override, a changed
// host key) ask with the reason themselves.

// reauthReason is a typed reason for a confirmation. The browser shows its
// fixed explanation (reauthExplanations), never text from a request.
type reauthReason string

const (
	reauthAccount      reauthReason = "account_security"    // your own sign-in: two-factor, recovery codes, API tokens
	reauthUsers        reauthReason = "user_access"         // who may sign in and what they may do
	reauthHostAccess   reauthReason = "host_access"         // how DROOL gets administrative access to a host
	reauthHostKey      reauthReason = "host_key"            // the SSH host key DROOL trusts for a host
	reauthCredential   reauthReason = "credential_in_use"   // a credential hosts use to let DROOL in
	reauthSSHCA        reauthReason = "ssh_ca"              // which SSH certificates hosts trust
	reauthShepherd     reauthReason = "shepherd_management" // installing or removing a host's Shepherd, revoking its identity
	reauthShepherdPKI  reauthReason = "shepherd_trust"      // what Shepherds trust: PKI, manifests, release keys, endpoints
	reauthFirewall     reauthReason = "firewall_override"   // a firewall change without DROOL's safeguards
	reauthAudit        reauthReason = "audit"               // the audit trail and its remote copy
	reauthRecovery     reauthReason = "recovery"            // decisions after a restore
	reauthUserPassword reauthReason = "user_password"       // setting another user's password
)

var reauthExplanations = map[reauthReason]string{
	reauthAccount:      "You're changing how your own account signs in or acts. Confirm your identity to continue.",
	reauthUsers:        "You're changing who can sign in to DROOL or what they may do. Confirm your identity to continue.",
	reauthHostAccess:   "You're changing how DROOL gets administrative access to this host. Confirm your identity to continue.",
	reauthHostKey:      "You're replacing the SSH host key DROOL trusts for this host. Confirm your identity to continue.",
	reauthCredential:   "You're replacing a credential that hosts use to let DROOL in. Confirm your identity to continue.",
	reauthSSHCA:        "You're changing which SSH certificates your hosts trust. Confirm your identity to continue.",
	reauthShepherd:     "You're changing how DROOL manages this host (its Shepherd). Confirm your identity to continue.",
	reauthShepherdPKI:  "You're changing what DROOL's Shepherds trust. Confirm your identity to continue.",
	reauthFirewall:     "This firewall change turns off a safeguard that keeps DROOL connected to the host. Confirm your identity to continue.",
	reauthAudit:        "You're changing DROOL's audit trail. Confirm your identity to continue.",
	reauthRecovery:     "You're deciding how this restored controller treats a host. Confirm your identity to continue.",
	reauthUserPassword: "You're setting another user's password; they will be signed out everywhere. Confirm your identity to continue.",
}

type reauthInfo struct {
	MFA     bool `json:"mfa"`      // ask for the authenticator code too
	WindowS int  `json:"window_s"` // how long a confirmation lasts
	// Reason is a typed code; Explanation its fixed wording.
	Reason      reauthReason `json:"reason,omitempty"`
	Explanation string       `json:"explanation,omitempty"`
}

// requireReauth writes reauth_required and returns false unless the
// request's session confirmed who it is within the window.
func (s *Server) requireReauth(w http.ResponseWriter, r *http.Request, why reauthReason) bool {
	p := principalFrom(r.Context())
	if p == nil || p.Kind != auth.KindSession {
		writeProblem(w, r, errForbidden("this action requires an interactive session, not an API token"))
		return false
	}
	if s.d.Auth.RecentlyAuthenticated(p) {
		return true
	}
	st, _ := s.d.Auth.MFAStatus(r.Context(), nil, p.UserID)
	pr := problem(http.StatusForbidden, "reauth_required",
		"Confirm it's you: this action needs your password again"+map[bool]string{true: " and a code from your authenticator", false: ""}[st.Enabled]+".")
	pr.Title = "Confirm it's you"
	pr.Reauth = &reauthInfo{MFA: st.Enabled, WindowS: int(s.d.Auth.ReauthWindow() / time.Second), Reason: why, Explanation: reauthExplanations[why]}
	writeProblem(w, r, pr)
	return false
}
