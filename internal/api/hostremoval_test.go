package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"drool/internal/agent"
	"drool/internal/auth"
)

// setUpAccountHost adds a host over its setup account and lets DROOL
// create its own account there (as TestSSHAccountSetupThroughTheAPI does).
func setUpAccountHost(t *testing.T, h *harness, c *client, name string) (string, *accountHost) {
	t.Helper()
	var ck controllerKeyResponse
	c.do("GET", "/api/v1/secrets/controller-ssh-key", nil).json(t, &ck)
	ah := newAccountHost(t, ck.PublicKey)
	addr, port := ah.srv.Host()
	var host hostJSON
	r := c.do("POST", "/api/v1/hosts", map[string]any{"name": name, "address": addr, "port": port, "user": "admin",
		"setup_credentials": map[string]any{"ssh_password": setupLoginPW, "sudo_password": setupSudoPW}})
	if r.status != 201 {
		t.Fatalf("add %s: %d %s", name, r.status, r.body)
	}
	r.json(t, &host)
	c.do("POST", "/api/v1/hosts/"+host.ID+"/hostkeys/scan", nil)
	if r := c.do("POST", "/api/v1/hosts/"+host.ID+"/hostkeys/approve", map[string]string{"fingerprint": ssh.FingerprintSHA256(ah.key.PublicKey())}); r.status != 200 {
		t.Fatalf("approve: %s", r.body)
	}
	r = c.do("POST", "/api/v1/hosts/"+host.ID+"/ssh-account", map[string]any{})
	if r.status != 201 {
		t.Fatalf("set up the account on %s: %d %s", name, r.status, r.body)
	}
	var j jobJSON
	r.json(t, &j)
	if j = waitJob(t, c, j.ID); j.Counts["succeeded"] != 1 {
		t.Fatalf("setup job: %+v", j)
	}
	return host.ID, ah
}

type removalResult struct {
	Outcome     string `json:"outcome"`
	HostDeleted bool   `json:"host_deleted"`
	Steps       []struct {
		What    string `json:"what"`
		Outcome string `json:"outcome"`
		Message string `json:"message"`
	} `json:"steps"`
	Message string `json:"message"`
}

type removalPlanJSON struct {
	CanCleanUp bool `json:"can_clean_up"`
	Blocked    string
	Parts      []struct {
		What    string   `json:"what"`
		Label   string   `json:"label"`
		Ready   bool     `json:"ready"`
		Method  string   `json:"method"`
		Reason  string   `json:"reason"`
		Removes []string `json:"removes"`
	} `json:"parts"`
	Stays []string `json:"stays"`
}

// Removing a host with "Clean up DROOL access" (dogfooding correction
// batch): DROOL removes the account it created there (proven by the host:
// the marker, DROOL's key, the user ID) through a helper its last session
// starts, says it can't verify the end of it, and then forgets the host. An
// account it can't prove is refused before anything changes, and the host
// stays; "Remove from DROOL only" still forgets a host without touching it.
// With a Shepherd too, the Shepherd goes first; if the account's cleanup
// then doesn't start, the host stays and the result says what was done and
// what wasn't. High-impact: hosts.write and a recent sign-in. No secret in
// any result or audit record.
func TestHostRemovalCleanup(t *testing.T) {
	e := newAgentEnv(t)
	e.h.agents.SetRemovalWaits(2*time.Second, 5*time.Second, time.Second)
	h, c := e.h, e.c
	ctx := context.Background()
	plan := func(id string) removalPlanJSON {
		t.Helper()
		var p removalPlanJSON
		r := c.do("POST", "/api/v1/hosts/"+id+"/removal/plan", nil)
		if r.status != 200 {
			t.Fatalf("removal plan: %d %s", r.status, r.body)
		}
		r.json(t, &p)
		return p
	}
	remove := func(cl *client, id string) (removalResult, resp) {
		t.Helper()
		r := cl.do("POST", "/api/v1/hosts/"+id+"/removal", nil)
		var res removalResult
		if r.status == 200 {
			r.json(t, &res)
		}
		return res, r
	}
	exists := func(id string) bool { return c.do("GET", "/api/v1/hosts/"+id, nil).status == 200 }

	web, wa := setUpAccountHost(t, h, c, "web")
	p := plan(web)
	if !p.CanCleanUp || len(p.Parts) != 1 || p.Parts[0].What != "account" || !p.Parts[0].Ready || !strings.Contains(p.Parts[0].Label, "drool") ||
		len(p.Parts[0].Removes) != 4 || len(p.Stays) == 0 {
		t.Fatalf("plan: %+v", p)
	}

	// An account the host can't prove DROOL's (a marker with another key,
	// another user ID, none at all): refused, nothing changed, host kept.
	for _, code := range []string{"foreign", "replaced", "notours"} {
		wa.mu.Lock()
		wa.marker = code
		wa.mu.Unlock()
		if p := plan(web); p.CanCleanUp || p.Parts[0].Ready || !strings.Contains(p.Parts[0].Reason, "isn't provably DROOL's") {
			t.Fatalf("plan with a %s marker: %+v", code, p)
		}
		_, r := remove(c, web)
		if r.status != 409 || r.problemCode(t) != "cleanup_unavailable" || !strings.Contains(string(r.body), "remove the host from DROOL only") {
			t.Fatalf("cleanup with a %s marker: %d %s", code, r.status, r.body)
		}
		if !exists(web) || wa.cleanups != 0 {
			t.Fatalf("a refused cleanup removed something (%s)", code)
		}
	}
	assertAuditLike(t, h, "host.remove_cleanup", `"refused":"cleanup_unavailable"`)
	wa.mu.Lock()
	wa.marker = ""
	wa.mu.Unlock()

	// Who may: hosts.write; and a recent sign-in, since DROOL removes its
	// own way in.
	h.mkUser("aud", auth.RoleAuditor)
	if _, r := remove(h.login("aud"), web); r.status != 403 {
		t.Fatalf("auditor: %d", r.status)
	}
	h.clock.Store(int64(16 * time.Minute))
	if _, r := remove(c, web); r.status != 403 || r.problemCode(t) != "reauth_required" {
		t.Fatalf("without a recent sign-in: %d %s", r.status, r.body)
	}
	c.reauth(t, map[string]string{"password": pw})

	// Cleaned up: initiated (DROOL removed its own access and says it can't
	// verify the end), then forgotten.
	res, r := remove(c, web)
	if r.status != 200 || res.Outcome != "removed" || !res.HostDeleted || len(res.Steps) != 1 || res.Steps[0].What != "account" ||
		res.Steps[0].Outcome != "initiated" || !strings.Contains(res.Steps[0].Message, "Cleanup initiated") ||
		!strings.Contains(res.Steps[0].Message, "can't verify the final state through that account") {
		t.Fatalf("cleanup: %d %+v", r.status, res)
	}
	if exists(web) || wa.cleanups != 1 {
		t.Fatalf("after the cleanup: host exists %v, cleanups %d", exists(web), wa.cleanups)
	}
	assertAuditLike(t, h, "host.delete", `"cleanup":[{"what":"account","outcome":"initiated"`)

	// Remove from DROOL only: the host is forgotten, nothing runs on it.
	plain, pa := setUpAccountHost(t, h, c, "plain")
	if r := c.do("DELETE", "/api/v1/hosts/"+plain, nil); r.status != 204 {
		t.Fatalf("remove from DROOL only: %d", r.status)
	}
	if pa.cleanups != 0 || exists(plain) {
		t.Fatal("removing from DROOL only touched the host")
	}

	// A host DROOL didn't install anything on: nothing to clean up.
	other, srv := readyHostServer(t, h, c, "other")
	srv.SetExec(linuxExec)
	if p := plan(other); p.CanCleanUp || len(p.Parts) != 0 {
		t.Fatalf("nothing of DROOL's: %+v", p)
	}
	if _, r := remove(c, other); r.status != 409 || r.problemCode(t) != "nothing_to_clean_up" {
		t.Fatalf("nothing to clean up: %d %s", r.status, r.body)
	}

	// A Shepherd and DROOL's account: the Shepherd removes itself first,
	// then the account. If the account's cleanup then fails to start, the
	// host stays, and the result says what was done and what wasn't.
	both, ba := setUpAccountHost(t, h, c, "both")
	tok := e.token(t, map[string]any{"host_id": both, "preset": "monitor", "description": "both"})
	ap := agentPaths(t, monitorCaps())
	if _, err := agent.Enroll(ctx, ap, tok.Token, "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	rm := &testRemoval{}
	ra := startAgentWith(t, ap, "v1.0.0", rm)
	rm.set(func(r *testRemoval) { r.done = standIn(nil, "", ra.stop) })
	eventually(t, 10*time.Second, "both connected", func() bool { return e.connected(both) })
	p = plan(both)
	if !p.CanCleanUp || len(p.Parts) != 2 || p.Parts[0].What != "shepherd" || p.Parts[0].Method != "shepherd" || p.Parts[1].What != "account" {
		t.Fatalf("plan with a Shepherd and the account: %+v", p)
	}
	ba.mu.Lock()
	ba.failStart = true
	ba.mu.Unlock()
	res, _ = remove(c, both)
	if res.Outcome != "not_removed" || res.HostDeleted || len(res.Steps) != 2 || res.Steps[0].Outcome != "initiated" || res.Steps[1].Outcome != "refused" ||
		!strings.Contains(res.Message, "Done so far") || !strings.Contains(res.Message, "Not done: the cleanup of DROOL's SSH account didn't start") {
		t.Fatalf("partial cleanup: %+v", res)
	}
	if !exists(both) {
		t.Fatal("a partial cleanup forgot the host")
	}
	assertAuditLike(t, h, "host.remove_cleanup", `"host_deleted":false`)
	// Again, once the account's cleanup can start: the Shepherd is gone
	// already (nothing to remove), the account goes, the host is forgotten.
	ba.mu.Lock()
	ba.failStart = false
	ba.mu.Unlock()
	if p := plan(both); !p.CanCleanUp || len(p.Parts) != 1 || p.Parts[0].What != "account" {
		t.Fatalf("plan after a partial cleanup: %+v", p)
	}
	res, _ = remove(c, both)
	if res.Outcome != "removed" || !res.HostDeleted || ba.cleanups != 1 {
		t.Fatalf("finished: %+v", res)
	}

	// No secret anywhere.
	var all string
	h.st.Reader.QueryRow(`SELECT group_concat(details_json, ' ') FROM audit_events WHERE action IN ('host.delete', 'host.remove_cleanup', 'agent.uninstall')`).Scan(&all)
	for _, s := range []string{setupLoginPW, setupSudoPW, tok.Token} {
		if strings.Contains(all, s) {
			t.Fatal("a secret reached the audit log")
		}
	}
}

// The host's page stays behind the removal dialog while it shows the result
// (dogfooding correction batch), and keeps asking about the host: every
// host-scoped read answers a removed host with a 4xx, never a server error.
func TestRemovedHostReads(t *testing.T) {
	h := newHarness(t)
	h.mkUser("admin", auth.RoleAdmin)
	c := h.login("admin")
	var host hostJSON
	c.do("POST", "/api/v1/hosts", map[string]any{"name": "gone-01", "address": "192.0.2.61", "port": 22, "user": "deploy"}).json(t, &host)
	if r := c.do("DELETE", "/api/v1/hosts/"+host.ID, nil); r.status != 204 {
		t.Fatalf("delete: %d %s", r.status, r.body)
	}
	n := 0
	for _, rt := range h.srv.routes() {
		p := strings.TrimPrefix(rt.path, APIPrefix)
		if rt.method != "GET" || !strings.Contains(p, "hosts/{id}") {
			continue
		}
		p = strings.NewReplacer("{id}", host.ID, "{unit}", "ssh.service").Replace(p)
		if strings.Contains(p, "{") {
			t.Fatalf("a path parameter to fill in: %s", rt.path)
		}
		n++
		if r := c.do("GET", "/api/v1"+p, nil); r.status >= 500 {
			t.Errorf("GET %s for a removed host: %d %s", rt.path, r.status, r.body)
		}
	}
	for _, q := range []string{"/alerts?view=open&host_id=", "/alerts/summary?host_id=", "/maintenance?state=active&host_id=", "/network/flows?host_id=",
		"/jobs?host_id=", "/firewall/transactions?host_id=", "/monitoring/events?host_id="} {
		if r := c.do("GET", "/api/v1"+q+host.ID, nil); r.status >= 500 {
			t.Errorf("GET %s for a removed host: %d %s", q, r.status, r.body)
		}
	}
	// The plans the removal and uninstall dialogs read: not found.
	for _, p := range []string{"/removal/plan", "/agent/uninstall/plan"} {
		if r := c.do("POST", "/api/v1/hosts/"+host.ID+p, nil); r.status != 404 {
			t.Errorf("POST %s for a removed host: %d %s", p, r.status, r.body)
		}
	}
	if n < 20 {
		t.Fatalf("only %d host-scoped reads", n)
	}
}
