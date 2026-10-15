package api

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"drool/internal/agent"
)

// helperValue reads a NAME='value' line of a self-removal helper script.
func helperValue(script, name string) string {
	for _, ln := range strings.Split(script, "\n") {
		if v, ok := strings.CutPrefix(ln, name+"="); ok {
			return strings.Trim(v, "'")
		}
	}
	return ""
}

// standIn plays the Shepherd's removal helper in a test: it waits until
// the agent signals it (the go file), then does to the simulated host what
// the helper does (keeping keep, if set) and stops the in-process agent,
// as the helper stops the real one.
func standIn(sim *simShepherdHost, keep string, stop func()) func(string) {
	return func(script string) {
		dir := helperValue(script, "DIR")
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(filepath.Join(dir, "go")); err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if sim != nil {
			sim.mu.Lock()
			for p := range sim.present {
				if p != keep {
					delete(sim.present, p)
				}
			}
			sim.pids, sim.active = 0, "inactive"
			sim.mu.Unlock()
		}
		stop()
	}
}

// A Shepherd removes itself when asked (dogfooding correction batch): no
// SSH needed. DROOL sends a typed request over the Shepherd's session; the
// Shepherd starts its helper and answers; DROOL revokes and forgets it in
// one audited transaction and waits for it to disconnect. Without SSH the
// outcome is "initiated" and the host is left with no management path,
// said so; with SSH, DROOL looks afterwards: "uninstalled" (verified) or
// "partial". A lost answer changes nothing in DROOL, and trying again
// finishes over SSH; a Shepherd too old to remove itself is told apart
// (SSH instead); a refusal changes nothing. The helper never runs here
// (testRemoval).
func TestShepherdSelfUninstall(t *testing.T) {
	e := newAgentEnv(t)
	e.h.agents.SetRemovalWaits(2*time.Second, 5*time.Second, 2*time.Second)
	ctx := context.Background()
	type result struct {
		Method          string   `json:"method"`
		Outcome         string   `json:"outcome"`
		Removed         []string `json:"removed"`
		Remaining       []string `json:"remaining"`
		IdentityRevoked bool     `json:"identity_revoked"`
		TokensRevoked   int      `json:"tokens_revoked"`
		Management      string   `json:"management"`
		Message         string   `json:"message"`
		Helper          string   `json:"helper"`
		Disconnected    bool     `json:"disconnected"`
		Verified        bool     `json:"verified"`
	}
	uninstall := func(host, method string) (result, resp) {
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
	type plan struct {
		Method   string `json:"method"`
		After    string `json:"after"`
		Version  string `json:"version"`
		Shepherd struct {
			Available bool
			Code      string
			Reason    string
		} `json:"shepherd"`
		SSH struct {
			Available bool
			Code      string
		} `json:"ssh"`
	}
	planOf := func(host string) plan {
		t.Helper()
		var p plan
		r := e.c.do("POST", "/api/v1/hosts/"+host+"/agent/uninstall/plan", nil)
		if r.status != 200 {
			t.Fatalf("plan: %d %s", r.status, r.body)
		}
		r.json(t, &p)
		return p
	}
	// sshHost: a host DROOL reaches over SSH (sudo) whose Shepherd is
	// connected, removal played by rm.
	sshHost := func(name string, rm *testRemoval) (string, *simShepherdHost, *runningAgent) {
		t.Helper()
		id, srv := readyHostServer(t, e.h, e.c, name)
		sim := newSimShepherdHost()
		srv.SetExec(sim.exec)
		if r := e.c.do("PATCH", "/api/v1/hosts/"+id, map[string]any{"become_method": "sudo"}); r.status != 200 {
			t.Fatalf("sudo: %d", r.status)
		}
		tok := e.token(t, map[string]any{"host_id": id, "preset": "full", "description": name})
		p := agentPaths(t, fullCaps(""))
		if _, err := agent.Enroll(ctx, p, tok.Token, "v1.0.0"); err != nil {
			t.Fatal(err)
		}
		ra := startAgentWith(t, p, "v1.0.0", rm)
		eventually(t, 10*time.Second, name+" connected", func() bool { return e.connected(id) })
		return id, sim, ra
	}

	// With SSH: the Shepherd removes itself, and SSH confirms it afterwards.
	rm1 := &testRemoval{}
	h1, sim1, ra1 := sshHost("self-01", rm1)
	rm1.set(func(r *testRemoval) { r.done = standIn(sim1, "", ra1.stop) })
	if p := planOf(h1); p.Method != "shepherd" || !p.Shepherd.Available || !p.SSH.Available || !strings.Contains(p.After, "over SSH as deploy@") {
		t.Fatalf("plan with SSH: %+v", p)
	}
	res, r := uninstall(h1, "")
	if r.status != 200 || res.Method != "shepherd" || res.Outcome != "uninstalled" || !res.Verified || !res.Disconnected || res.Helper != "test stand-in" ||
		!res.IdentityRevoked || res.Management != "ssh" || len(res.Removed) != 6 || !strings.Contains(res.Message, "Verified removed") {
		t.Fatalf("self-removal with SSH: %d %+v", r.status, res)
	}
	var given []string
	rm1.set(func(r *testRemoval) { given = slices.Clone(r.scripts) })
	if len(given) != 1 || !strings.HasPrefix(given[0], "MODE=self\nGO=") || !strings.Contains(given[0], "rm -rf \"$I\" /var/lib/drool-agent") {
		t.Fatalf("the helper it was given: %q", given)
	}
	if ha := e.hostAgent(t, h1); ha.Management.Preferred != "ssh" || ha.Management.Active != "ssh" {
		t.Fatalf("managed over SSH afterwards: %+v", ha.Management)
	}
	assertAuditLike(t, e.h, "agent.uninstall", `"method":"shepherd"`, `"outcome":"initiated"`, `"helper":"test stand-in"`)

	// Without SSH: initiated (DROOL can't look), the Shepherd disconnected
	// as expected, and the host has no management path left: said so.
	tok2 := e.token(t, map[string]any{"preset": "monitor", "description": "self-02"})
	p2 := agentPaths(t, monitorCaps())
	id2, err := agent.Enroll(ctx, p2, tok2.Token, "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	rm2 := &testRemoval{}
	ra2 := startAgentWith(t, p2, "v1.0.0", rm2)
	rm2.set(func(r *testRemoval) { r.done = standIn(nil, "", ra2.stop) })
	eventually(t, 10*time.Second, "self-02 connected", func() bool { return e.connected(id2.HostID) })
	if p := planOf(id2.HostID); p.Method != "shepherd" || p.SSH.Code != "no_ssh" || !strings.Contains(p.After, "no way to manage") {
		t.Fatalf("plan without SSH: %+v", p)
	}
	res, _ = uninstall(id2.HostID, "")
	if res.Outcome != "initiated" || res.Verified || !res.Disconnected || !res.IdentityRevoked || res.Management != "none" ||
		!strings.Contains(res.Message, "Self-removal initiated") || !strings.Contains(res.Message, "disconnected as expected") ||
		!strings.Contains(res.Message, "can't check that every file is gone") {
		t.Fatalf("self-removal without SSH: %+v", res)
	}
	if ha := e.hostAgent(t, id2.HostID); ha.Management.Active != "none" || !strings.Contains(ha.Management.Reason, "no way to manage this host") {
		t.Fatalf("no management path afterwards: %+v", ha.Management)
	}

	// The answer is lost (the helper took too long to start): unknown,
	// nothing changed in DROOL. Trying again, once the Shepherd is gone,
	// finishes over SSH.
	rm3 := &testRemoval{}
	h3, sim3, ra3 := sshHost("self-03", rm3)
	release := make(chan struct{})
	rm3.set(func(r *testRemoval) { r.done, r.block = standIn(sim3, "", ra3.stop), release })
	res, _ = uninstall(h3, "")
	if res.Outcome != "unknown" || res.IdentityRevoked || !strings.Contains(res.Message, "didn't confirm the removal") {
		t.Fatalf("lost answer: %+v", res)
	}
	if ha := e.hostAgent(t, h3); ha.Management.Preferred != "agent" || ha.Identity == nil || ha.Identity.Status != "active" {
		t.Fatalf("something changed without an answer: %+v", ha)
	}
	assertAuditLike(t, e.h, "agent.uninstall", `"method":"shepherd"`, `"outcome":"unknown"`)
	close(release) // the helper starts after all, and the Shepherd goes
	eventually(t, 15*time.Second, "self-03 gone", func() bool { return !e.connected(h3) })
	if p := planOf(h3); p.Shepherd.Code != "not_connected" || p.Method != "ssh" {
		t.Fatalf("plan after a lost answer: %+v", p)
	}
	res, _ = uninstall(h3, "")
	if res.Method != "ssh" || res.Outcome != "uninstalled" || !res.IdentityRevoked {
		t.Fatalf("finished over SSH: %+v", res)
	}

	// Too old to remove itself (no uninstall-1): told apart, SSH instead.
	saved := agent.Features
	agent.Features = slices.DeleteFunc(slices.Clone(saved), func(f string) bool { return f == "uninstall-1" })
	h4, _, _ := sshHost("self-04", &testRemoval{})
	agent.Features = saved
	p4 := planOf(h4)
	if p4.Shepherd.Code != "too_old" || p4.Method != "ssh" || !strings.Contains(p4.Shepherd.Reason, "Update it") {
		t.Fatalf("plan for an older Shepherd: %+v", p4)
	}
	var pr Problem
	_, r = uninstall(h4, "shepherd")
	r.json(t, &pr)
	if r.status != 409 || pr.Code != "too_old" || !strings.Contains(pr.Detail, "over SSH instead") {
		t.Fatalf("asking an older Shepherd: %d %s", r.status, r.body)
	}
	if res, _ = uninstall(h4, ""); res.Method != "ssh" || res.Outcome != "uninstalled" {
		t.Fatalf("an older Shepherd, over SSH: %+v", res)
	}

	// Partial: the helper leaves something; SSH says what.
	rm5 := &testRemoval{}
	h5, sim5, ra5 := sshHost("self-05", rm5)
	rm5.set(func(r *testRemoval) { r.done = standIn(sim5, "/var/log/drool-agent", ra5.stop) })
	res, _ = uninstall(h5, "")
	if res.Outcome != "partial" || !res.Verified || strings.Join(res.Remaining, ",") != "/var/log/drool-agent" || !res.IdentityRevoked ||
		!strings.Contains(res.Message, "still finds") {
		t.Fatalf("partial self-removal: %+v", res)
	}

	// The Shepherd refuses (its helper couldn't start): nothing changed.
	rm6 := &testRemoval{fail: true}
	h6, _, _ := sshHost("self-06", rm6)
	res, _ = uninstall(h6, "shepherd")
	if res.Outcome != "refused" || res.IdentityRevoked || !strings.Contains(res.Message, "refused to remove itself") {
		t.Fatalf("refused: %+v", res)
	}
	if !e.connected(h6) {
		t.Fatal("a refused removal disconnected the Shepherd")
	}
	assertAuditLike(t, e.h, "agent.uninstall", `"outcome":"refused"`, `"refusal":"start"`)
}
