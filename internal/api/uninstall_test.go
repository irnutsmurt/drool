package api

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"drool/internal/agent"
	"drool/internal/auth"
	"drool/internal/sshx/sshtest"
)

// simShepherdHost plays a host for the Shepherd removal script: it answers
// the script (read from stdin, MODE=check or remove) from its own state.
// The script never runs on the machine running the test: it stops services
// and deletes system folders (it runs against real hosts in the
// integration suite).
type simShepherdHost struct {
	mu      sync.Mutex
	present map[string]bool
	pids    int
	active  string // drool-agent.service
	// Faults: the next removal doesn't report back; leaves this path; SSH
	// as root fails.
	noAnswer, sudoFails bool
	keep                string
	removals            int
}

func newSimShepherdHost() *simShepherdHost {
	s := &simShepherdHost{present: map[string]bool{}, pids: 1, active: "active"}
	for _, it := range []string{"/etc/systemd/system/drool-agent.service", "/usr/libexec/drool-agent", "/etc/drool/agent.yaml",
		"/etc/drool/release-keys.d", "/var/lib/drool-agent", "/var/log/drool-agent"} {
		s.present[it] = true
	}
	return s
}

func (s *simShepherdHost) exec(cmd string, stdin []byte) (string, string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in := string(stdin)
	switch {
	case strings.Contains(in, "MODE=check") || strings.Contains(in, "MODE=remove"):
		if s.sudoFails {
			return "", "sudo: a password is required\n", 1
		}
		var b strings.Builder
		b.WriteString("@@systemd running\n")
		if strings.Contains(in, "MODE=remove") {
			s.removals++
			if s.noAnswer {
				s.noAnswer = false
				return "", "Connection to the host was lost\n", 255
			}
			for p := range s.present {
				if p != s.keep {
					delete(s.present, p)
				}
			}
			s.active = "inactive"
			if s.keep == "" {
				s.pids = 0
			}
			b.WriteString("@@removed 1\n")
		}
		for p := range s.present {
			b.WriteString("@@present " + p + "\n")
		}
		b.WriteString("@@active " + s.active + "\n@@enabled disabled\n")
		for range s.pids {
			b.WriteString("@@pid 4242\n")
		}
		b.WriteString("@@done 1\n")
		return b.String(), "", 0
	}
	// Anything else (a run's own commands) as in the other API tests; the
	// removal script never runs here.
	return sshtest.ExecLocal(cmd, stdin)
}

// Removing a Shepherd over SSH (final After-M17 batch; the Shepherd's own
// way is TestShepherdSelfUninstall): what goes is read from the host first;
// DROOL refuses where neither way works or SSH doesn't work as root; an
// answer that never came back changes nothing in DROOL; a removal
// confirmed on the host revokes the identity and the host's unused
// enrollment tokens, forgets the narrowing, leaves the host managed over
// SSH (which keeps working) and no session; what can't be confirmed is
// reported as partial; running it again finishes; a Shepherd no way
// reaches can be forgotten. High-impact: agents.manage and hosts.write, and
// a recent sign-in. No secret in any result or audit record.
func TestShepherdUninstall(t *testing.T) {
	e := newAgentEnv(t)
	ctx := context.Background()
	hostID, srv := readyHostServer(t, e.h, e.c, "shep-01")
	sim := newSimShepherdHost()
	srv.SetExec(sim.exec)
	if r := e.c.do("PATCH", "/api/v1/hosts/"+hostID, map[string]any{"become_method": "sudo"}); r.status != 200 {
		t.Fatalf("sudo: %d %s", r.status, r.body)
	}
	tok := e.token(t, map[string]any{"host_id": hostID, "preset": "full", "description": "shep-01"})
	p := agentPaths(t, fullCaps(""))
	if _, err := agent.Enroll(ctx, p, tok.Token, "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	startAgent(t, p, "v1.0.0")
	eventually(t, 10*time.Second, "agent session", func() bool { return e.connected(hostID) })
	spare := e.token(t, map[string]any{"host_id": hostID, "preset": "monitor", "description": "spare"})
	narrowed := fullCaps("")
	narrowed.Shell = false
	if r := e.c.do("PUT", "/api/v1/hosts/"+hostID+"/agent/caps", map[string]any{"caps": narrowed}); r.status != 204 {
		t.Fatalf("narrow: %d", r.status)
	}

	// The plan: read from the host, nothing changed.
	type plan struct {
		CanUninstall bool   `json:"can_uninstall"`
		RefusalCode  string `json:"refusal_code"`
		Refusal      string `json:"refusal"`
		Method       string `json:"method"`
		Checked      bool   `json:"checked"`
		Shepherd     struct {
			Available bool   `json:"available"`
			Code      string `json:"code"`
		} `json:"shepherd"`
		SSH struct {
			Available bool   `json:"available"`
			Code      string `json:"code"`
		} `json:"ssh"`
		SignIn   string `json:"sign_in"`
		Identity *struct {
			ID, Status string
			Connected  bool
		} `json:"identity"`
		Processes int `json:"processes"`
		Items     []struct {
			Path    string `json:"path"`
			Present bool   `json:"present"`
		} `json:"items"`
		Controller []string `json:"controller"`
	}
	var pl plan
	if r := e.c.do("POST", "/api/v1/hosts/"+hostID+"/agent/uninstall/plan", nil); r.status != 200 {
		t.Fatalf("plan: %d %s", r.status, r.body)
	} else {
		r.json(t, &pl)
	}
	if !pl.CanUninstall || pl.Identity == nil || !pl.Identity.Connected || pl.Processes != 1 || len(pl.Items) != 6 || !pl.Items[0].Present ||
		!strings.Contains(pl.SignIn, "deploy@") || !strings.Contains(strings.Join(pl.Controller, " "), "Revoke the Shepherd's identity") ||
		!pl.Checked || !pl.SSH.Available || !pl.Shepherd.Available || pl.Method != "shepherd" {
		t.Fatalf("plan: %+v", pl)
	}
	if sim.removals != 0 || !e.connected(hostID) {
		t.Fatal("the plan changed something")
	}

	// Who may: an operator can't; administrators confirm who they are.
	e.h.mkUser("op", auth.RoleOperator)
	if r := e.h.login("op").do("POST", "/api/v1/hosts/"+hostID+"/agent/uninstall", nil); r.status != 403 {
		t.Fatalf("operator: %d", r.status)
	}
	e.h.clock.Store(int64(16 * time.Minute))
	r := e.c.do("POST", "/api/v1/hosts/"+hostID+"/agent/uninstall", nil)
	var pr Problem
	r.json(t, &pr)
	if r.status != 403 || pr.Code != "reauth_required" || pr.Reauth == nil || pr.Reauth.Reason != reauthShepherd {
		t.Fatalf("without a recent sign-in: %d %s", r.status, r.body)
	}
	if sim.removals != 0 {
		t.Fatal("removed before the confirmation")
	}
	e.c.reauth(t, map[string]string{"password": pw})

	type result struct {
		Outcome         string   `json:"outcome"`
		Removed         []string `json:"removed"`
		Remaining       []string `json:"remaining"`
		StillRunning    int      `json:"still_running"`
		SessionLeft     bool     `json:"session_left"`
		IdentityRevoked bool     `json:"identity_revoked"`
		TokensRevoked   int      `json:"tokens_revoked"`
		Management      string   `json:"management"`
		Message         string   `json:"message"`
	}
	uninstallBy := func(host, method string) (result, resp) {
		t.Helper()
		var body any
		if method != "" {
			body = map[string]any{"method": method}
		}
		r := e.c.do("POST", "/api/v1/hosts/"+host+"/agent/uninstall", body)
		var res result
		if r.status == 200 {
			r.json(t, &res)
		}
		return res, r
	}
	uninstall := func(host string) (result, resp) { t.Helper(); return uninstallBy(host, "ssh") }

	// The answer never comes back: unknown, and nothing changed in DROOL.
	sim.noAnswer = true
	res, _ := uninstall(hostID)
	if res.Outcome != "unknown" || res.IdentityRevoked || !strings.Contains(res.Message, "Nothing changed in DROOL") {
		t.Fatalf("no answer: %+v", res)
	}
	if ha := e.hostAgent(t, hostID); ha.Management.Preferred != "agent" {
		t.Fatalf("management changed although the removal didn't report back: %+v", ha.Management)
	}
	assertAuditLike(t, e.h, "agent.uninstall", `"outcome":"unknown"`)

	// Again: removed and confirmed.
	res, _ = uninstall(hostID)
	if res.Outcome != "uninstalled" || len(res.Removed) != 6 || len(res.Remaining) != 0 || !res.IdentityRevoked || res.TokensRevoked != 1 ||
		res.Management != "ssh" || res.SessionLeft {
		t.Fatalf("uninstall: %+v", res)
	}
	if e.connected(hostID) {
		t.Fatal("a session is left")
	}
	ha := e.hostAgent(t, hostID)
	if ha.Management.Preferred != "ssh" || ha.Management.Active != "ssh" {
		t.Fatalf("management afterwards: %+v", ha.Management)
	}
	var revoked int
	e.h.st.Reader.QueryRow(`SELECT count(*) FROM agent_enrollment_tokens WHERE id = ? AND revoked_at IS NOT NULL`, spare.ID).Scan(&revoked)
	var caps string
	e.h.st.Reader.QueryRow(`SELECT requested_caps_json FROM hosts WHERE id = ?`, hostID).Scan(&caps)
	if revoked != 1 || caps != "" {
		t.Fatalf("spare token revoked %d, narrowing %q", revoked, caps)
	}
	// SSH keeps working.
	jh, out := e.runJob(t, hostID, map[string]any{"type": "command", "command": "echo via-ssh"})
	if jh.State != "succeeded" || jh.Transport != "ssh" || !strings.Contains(out, "via-ssh") {
		t.Fatalf("over SSH afterwards: %+v %q", jh, out)
	}
	assertAuditLike(t, e.h, "agent.uninstall", `"identity_revoked":true`, `"unused_enrollments_revoked":1`)

	// Once more: nothing left to remove.
	res, _ = uninstall(hostID)
	if res.Outcome != "nothing" {
		t.Fatalf("again: %+v", res)
	}

	// A host DROOL reaches only through its Shepherd, which isn't connected
	// now: no way works. Not over SSH (none is set up), not by itself; the
	// Shepherd can be forgotten after removing it by hand.
	tok2 := e.token(t, map[string]any{"preset": "monitor", "description": "agent-only"})
	p2 := agentPaths(t, monitorCaps())
	id2, err := agent.Enroll(ctx, p2, tok2.Token, "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	_, r2 := uninstall(id2.HostID)
	var p2r Problem
	r2.json(t, &p2r)
	if r2.status != 409 || p2r.Code != "no_ssh" {
		t.Fatalf("agent-only host, over SSH: %d %s", r2.status, r2.body)
	}
	_, r2 = uninstallBy(id2.HostID, "")
	r2.json(t, &p2r)
	if r2.status != 409 || p2r.Code != "no_path" || !strings.Contains(p2r.Detail, "by hand") || !strings.Contains(p2r.Detail, "isn't connected") {
		t.Fatalf("agent-only host, offline: %d %s", r2.status, r2.body)
	}
	assertAuditLike(t, e.h, "agent.uninstall", `"refused":"no_path"`)
	fg, rf := uninstallBy(id2.HostID, "forget")
	if rf.status != 200 || fg.Outcome != "forgotten" || !fg.IdentityRevoked || fg.Management != "none" || !strings.Contains(fg.Message, "nothing on the host changed") {
		t.Fatalf("forget: %d %+v", rf.status, fg)
	}
	if ha := e.hostAgent(t, id2.HostID); ha.Management.Active != "none" || !strings.Contains(ha.Management.Reason, "no way to manage this host") {
		t.Fatalf("a host with no management path left: %+v", ha.Management)
	}
	assertAuditLike(t, e.h, "agent.uninstall", `"method":"forget"`, `"outcome":"forgotten"`)

	// SSH doesn't work as root: refused before anything changes.
	host3, srv3 := readyHostServer(t, e.h, e.c, "shep-03")
	sim3 := newSimShepherdHost()
	sim3.sudoFails = true
	srv3.SetExec(sim3.exec)
	e.c.do("PATCH", "/api/v1/hosts/"+host3, map[string]any{"become_method": "sudo"})
	tok3 := e.token(t, map[string]any{"host_id": host3, "preset": "monitor", "description": "shep-03"})
	if _, err := agent.Enroll(ctx, agentPaths(t, monitorCaps()), tok3.Token, "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	_, r3 := uninstall(host3)
	var p3 Problem
	r3.json(t, &p3)
	if r3.status != 409 || p3.Code != "ssh_unavailable" || sim3.removals != 0 {
		t.Fatalf("SSH not working as root: %d %s", r3.status, r3.body)
	}

	// Partial: a folder that can't be removed, a process that stays.
	host4, srv4 := readyHostServer(t, e.h, e.c, "shep-04")
	sim4 := newSimShepherdHost()
	sim4.keep = "/var/log/drool-agent"
	srv4.SetExec(sim4.exec)
	e.c.do("PATCH", "/api/v1/hosts/"+host4, map[string]any{"become_method": "sudo"})
	tok4 := e.token(t, map[string]any{"host_id": host4, "preset": "monitor", "description": "shep-04"})
	if _, err := agent.Enroll(ctx, agentPaths(t, monitorCaps()), tok4.Token, "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	res, _ = uninstall(host4)
	if res.Outcome != "partial" || strings.Join(res.Remaining, ",") != "/var/log/drool-agent" || res.StillRunning != 1 || !res.IdentityRevoked ||
		!strings.Contains(res.Message, "Not everything could be confirmed") {
		t.Fatalf("partial: %+v", res)
	}
	assertAuditLike(t, e.h, "agent.uninstall", `"remaining":["/var/log/drool-agent"]`)
	sim4.mu.Lock()
	sim4.keep = ""
	sim4.mu.Unlock()
	res, _ = uninstall(host4)
	if res.Outcome != "uninstalled" || res.IdentityRevoked {
		t.Fatalf("finished on the second run: %+v", res)
	}

	// No secret anywhere.
	var all string
	e.h.st.Reader.QueryRow(`SELECT group_concat(details_json, ' ') FROM audit_events WHERE action = 'agent.uninstall'`).Scan(&all)
	for _, s := range []string{tok.Token, spare.Token, tok2.Token, tok3.Token, tok4.Token} {
		if strings.Contains(all, s) {
			t.Fatal("an enrollment token reached the audit log")
		}
	}
}
