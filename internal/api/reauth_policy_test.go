package api

import (
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"drool/internal/auth"
)

// The re-authentication policy as reviewed in the final After-M17 batch
// (ARCHITECTURE-ALPHA §38). Every route that always asks, with its reason:
// adding one or taking one away is a decision, made here.
var reviewedReauthRoutes = map[string]reauthReason{
	"POST /api/v1/auth/mfa/enroll":                  reauthAccount,
	"POST /api/v1/auth/mfa/recovery-codes":          reauthAccount,
	"DELETE /api/v1/auth/mfa":                       reauthAccount,
	"POST /api/v1/tokens":                           reauthAccount,
	"POST /api/v1/users":                            reauthUsers,
	"DELETE /api/v1/users/{id}":                     reauthUsers,
	"POST /api/v1/users/{id}/mfa/reset":             reauthUsers,
	"POST /api/v1/audit/integrity/acknowledge":      reauthAudit,
	"PUT /api/v1/audit/sink":                        reauthAudit,
	"POST /api/v1/system/restore/finish":            reauthRecovery,
	"POST /api/v1/hosts/{id}/reconciliation/accept": reauthRecovery,
	"POST /api/v1/ssh-ca":                           reauthSSHCA,
	"POST /api/v1/ssh-ca/rotate":                    reauthSSHCA,
	"POST /api/v1/ssh-ca/{id}/activate":             reauthSSHCA,
	"POST /api/v1/ssh-ca/{id}/retire":               reauthSSHCA,
	"PUT /api/v1/hosts/{id}/ssh-auth":               reauthHostAccess,
	"POST /api/v1/hosts/{id}/ssh-account":           reauthHostAccess,
	"POST /api/v1/hosts/{id}/removal":               reauthHostAccess, // removing DROOL's account and Shepherd from the host (dogfooding correction batch)
	"POST /api/v1/agents/pki/export-root":           reauthShepherdPKI,
	"POST /api/v1/agents/pki/remove-root":           reauthShepherdPKI,
	"POST /api/v1/agents/pki/rotate":                reauthShepherdPKI,
	"POST /api/v1/agents/pki/manifests":             reauthShepherdPKI,
	"POST /api/v1/agents/release-keys":              reauthShepherdPKI,
	"DELETE /api/v1/agents/release-keys/{id}":       reauthShepherdPKI,
	"POST /api/v1/agents/endpoints/{id}/commit":     reauthShepherdPKI,
	"POST /api/v1/agents/{id}/revoke":               reauthShepherd,
	"POST /api/v1/agents/{id}/resolve-conflict":     reauthShepherd,
	"POST /api/v1/hosts/{id}/agent/install":         reauthShepherd,
	"POST /api/v1/hosts/{id}/agent/uninstall":       reauthShepherd,
	"POST /api/v1/agents/conversions":               reauthShepherd,
}

// Routes whose handler asks only for some requests (tested below): user
// changes (always, with user_password when a password is set), host updates
// that switch an established host's sign-in, credential replacements hosts
// use, firewall overrides, and replacing a pinned host key.
var askingHandlers = []string{
	"PATCH /api/v1/users/{id}",
	"PATCH /api/v1/hosts/{id}",
	"PATCH /api/v1/secrets/{id}",
	"POST /api/v1/firewall/apply",
	"POST /api/v1/hosts/{id}/hostkeys/approve",
}

func TestReauthRouteTable(t *testing.T) {
	h := newHarness(t)
	got := map[string]reauthReason{}
	all := map[string]bool{}
	for _, rt := range h.srv.routes() {
		k := rt.method + " " + rt.path
		all[k] = true
		if rt.reauth == "" {
			continue
		}
		got[k] = rt.reauth
		if rt.auth != authed {
			t.Errorf("%s asks for confirmation but isn't for signed-in users", k)
		}
		if reauthExplanations[rt.reauth] == "" {
			t.Errorf("%s: reason %q has no explanation", k, rt.reauth)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(reviewedReauthRoutes)) {
		if got[k] != reviewedReauthRoutes[k] {
			t.Errorf("%s: reason %q, reviewed %q", k, got[k], reviewedReauthRoutes[k])
		}
	}
	for _, k := range slices.Sorted(maps.Keys(got)) {
		if _, ok := reviewedReauthRoutes[k]; !ok {
			t.Errorf("%s asks for confirmation (%s) but isn't in the reviewed set", k, got[k])
		}
	}
	for _, k := range askingHandlers {
		if !all[k] {
			t.Errorf("no route %s", k)
		}
	}
	for r, text := range reauthExplanations {
		if !strings.HasSuffix(text, "Confirm your identity to continue.") || len(text) > 160 {
			t.Errorf("%s: %q", r, text)
		}
	}
}

// TestReauthWindowPolicy runs the policy against the harness's clock (moved,
// never waited for): one confirmation, signing in included, covers every
// high-impact action for 15 minutes; after that each asks again, with its
// reason and fixed wording; two-factor users confirm with a code; ordinary
// administration never asks.
func TestReauthWindowPolicy(t *testing.T) {
	h := newHarness(t)
	h.mkUser("admin", auth.RoleAdmin)
	c := h.login("admin")
	at := func(d time.Duration) { h.clock.Store(int64(d)) }
	type reauthProblem struct {
		Code   string `json:"code"`
		Reauth *struct {
			MFA         bool   `json:"mfa"`
			WindowS     int    `json:"window_s"`
			Reason      string `json:"reason"`
			Explanation string `json:"explanation"`
		} `json:"reauth"`
	}
	asks := func(what string, r resp, reason reauthReason) {
		t.Helper()
		var p reauthProblem
		r.json(t, &p)
		if r.status != 403 || p.Code != "reauth_required" || p.Reauth == nil || p.Reauth.Reason != string(reason) ||
			p.Reauth.Explanation != reauthExplanations[reason] || p.Reauth.WindowS != 900 {
			t.Fatalf("%s should ask (%s): %d %s", what, reason, r.status, r.body)
		}
	}
	passes := func(what string, r resp) {
		t.Helper()
		if r.status == 403 && strings.Contains(string(r.body), "reauth_required") {
			t.Fatalf("%s asked for confirmation: %s", what, r.body)
		}
		if r.status >= 500 {
			t.Fatalf("%s: %d %s", what, r.status, r.body)
		}
	}
	confirm := func() {
		t.Helper()
		if r := c.reauth(t, map[string]string{"password": pw}); r.status != 200 {
			t.Fatalf("confirm: %d %s", r.status, r.body)
		}
	}

	// Things to act on.
	var hst hostJSON
	c.do("POST", "/api/v1/hosts", map[string]any{"name": "web-01", "address": "192.0.2.10", "port": 22, "user": "alex"}).json(t, &hst)
	if _, err := h.st.Writer.Exec(`INSERT INTO ssh_host_keys (id, host_id, key_type, public_key, fingerprint_sha256, status, first_seen_at, last_seen_at)
		VALUES ('hk_rp', ?, 'ssh-ed25519', 'AAAA', 'SHA256:pinned', 'pinned', 1, 1)`, hst.ID); err != nil {
		t.Fatal(err)
	}
	var cred struct{ ID string }
	c.do("POST", "/api/v1/secrets", map[string]any{"name": "ops-password", "kind": "password", "value": "a stored password"}).json(t, &cred)
	h.mkUser("pat", auth.RoleViewer)
	pat := userID(t, h, "pat")

	// Signing in counts as confirming: high-impact actions go through
	// without asking, all of them, inside the window.
	at(0)
	passes("create a user right after signing in", c.do("POST", "/api/v1/users", map[string]string{"username": "u1", "password": pw, "role": "viewer"}))
	at(10 * time.Minute)
	passes("an API token inside the window", c.do("POST", "/api/v1/tokens", map[string]any{"name": "ci"}))
	at(15*time.Minute - 5*time.Second)
	passes("set a password just inside the window", c.do("PATCH", "/api/v1/users/"+pat, map[string]any{"password": "a-brand-new-password-1"}))

	// Expired: each high-impact action asks again, with its own reason.
	at(15*time.Minute + time.Second)
	asks("create a user", c.do("POST", "/api/v1/users", map[string]string{"username": "u2", "password": pw, "role": "viewer"}), reauthUsers)
	asks("set another user's password", c.do("PATCH", "/api/v1/users/"+pat, map[string]any{"password": "another-new-password-2"}), reauthUserPassword)
	asks("change a role", c.do("PATCH", "/api/v1/users/"+pat, map[string]any{"role": "operator"}), reauthUsers)
	asks("switch an established host to DROOL's own account", c.do("POST", "/api/v1/hosts/"+hst.ID+"/ssh-account", map[string]any{}), reauthHostAccess)
	asks("switch an established host's sign-in", c.do("PATCH", "/api/v1/hosts/"+hst.ID, map[string]any{"credential_id": cred.ID}), reauthHostAccess)
	asks("uninstall a Shepherd", c.do("POST", "/api/v1/hosts/"+hst.ID+"/agent/uninstall", nil), reauthShepherd)
	asks("install a Shepherd", c.do("POST", "/api/v1/hosts/"+hst.ID+"/agent/install", map[string]any{"preset": "managed"}), reauthShepherd)
	asks("create an SSH CA", c.do("POST", "/api/v1/ssh-ca", map[string]any{}), reauthSSHCA)
	asks("turn on two-factor", c.do("POST", "/api/v1/auth/mfa/enroll", nil), reauthAccount)
	// What was refused didn't happen.
	if _, err := h.st.R.GetUserByUsername(t.Context(), "u2"); err == nil {
		t.Fatal("a refused action happened")
	}
	var role string
	h.st.Reader.QueryRow(`SELECT role FROM users WHERE id = ?`, pat).Scan(&role)
	if role != auth.RoleViewer {
		t.Fatalf("a refused role change happened: %s", role)
	}

	// Ordinary administration never asks, inside or outside the window.
	passes("rename a host and tag it", c.do("PATCH", "/api/v1/hosts/"+hst.ID, map[string]any{"name": "web-01a", "tags": []string{"role:web"}}))
	passes("plan a Shepherd's removal", c.do("POST", "/api/v1/hosts/"+hst.ID+"/agent/uninstall/plan", nil))
	var pack struct{ ID string }
	r := c.do("POST", "/api/v1/groups", map[string]any{"name": "web", "host_ids": []string{hst.ID}})
	passes("create a Pack", r)
	r.json(t, &pack)
	if r := c.do("PUT", "/api/v1/monitoring/health/overrides", map[string]any{"scope": "host", "host_id": hst.ID, "check": "service_expected",
		"subject": "unit:cups.service", "subject_label": "cups.service", "fields": map[string]any{"expected": "stopped"}}); r.status != 200 {
		t.Fatalf("expect a service stopped: %d %s", r.status, r.body)
	}
	passes("classify traffic", c.do("POST", "/api/v1/network/classifications", map[string]any{"label": "Printer", "port": 9100}))
	passes("rename a credential", c.do("PATCH", "/api/v1/secrets/"+cred.ID, map[string]any{"name": "ops-password-2"}))
	passes("remove a Pack", c.do("DELETE", "/api/v1/groups/"+pack.ID, nil))

	// Confirming once covers them all again; the retried request goes
	// through.
	confirm()
	passes("create a user after confirming", c.do("POST", "/api/v1/users", map[string]string{"username": "u2", "password": pw, "role": "viewer"}))
	passes("change a role after confirming", c.do("PATCH", "/api/v1/users/"+pat, map[string]any{"role": "operator"}))
	var me meResponse
	c.do("GET", "/api/v1/auth/me", nil).json(t, &me)
	if me.ReauthUntil == nil || me.ReauthWindowS != 900 {
		t.Fatalf("me after confirming: %+v", me)
	}
	until, _ := time.Parse(time.RFC3339, *me.ReauthUntil)
	if d := until.Sub(h.now()); d < 14*time.Minute || d > 15*time.Minute+time.Second {
		t.Fatalf("confirmed until %s (%v from now)", *me.ReauthUntil, d)
	}
	at(31 * time.Minute)
	asks("an API token after the confirmation expired", c.do("POST", "/api/v1/tokens", map[string]any{"name": "ci-2"}), reauthAccount)

	// Two-factor users confirm with a code too, and are told so.
	h.mkUser("mfa", auth.RoleAdmin)
	at(32 * time.Minute)
	m := h.login("mfa")
	secret, _ := enrollMFA(t, h, m)
	at(48 * time.Minute)
	r = m.do("POST", "/api/v1/users", map[string]string{"username": "u3", "password": pw, "role": "viewer"})
	var p reauthProblem
	r.json(t, &p)
	if r.status != 403 || p.Reauth == nil || !p.Reauth.MFA || p.Reauth.Reason != string(reauthUsers) {
		t.Fatalf("two-factor user: %d %s", r.status, r.body)
	}
	if r := m.reauth(t, map[string]string{"password": pw}); r.status != 403 || r.problemCode(t) != "invalid_code" {
		t.Fatalf("password alone: %d %s", r.status, r.body)
	}
	if r := m.reauth(t, map[string]string{"password": pw, "code": totp(t, secret, h.now())}); r.status != 200 {
		t.Fatalf("password and code: %d %s", r.status, r.body)
	}
	passes("create a user after confirming with a code", m.do("POST", "/api/v1/users", map[string]string{"username": "u3", "password": pw, "role": "viewer"}))

	// The audit log records each confirmation.
	var n int
	h.st.Reader.QueryRow(`SELECT count(*) FROM audit_events WHERE action = 'auth.reauth' AND result = 'success'`).Scan(&n)
	if n < 2 {
		t.Fatalf("%d successful confirmations audited", n)
	}
}
