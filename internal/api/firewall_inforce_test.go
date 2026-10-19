package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	obs "drool/internal/firewall/observe"
)

// What is in force on a host, never assumed from the desired policy or the
// mode (source review before v1.0.0-rc.3): a connection's status and a
// simulated hop say whether DROOL's rules on the host decide it so now —
// never applied, in force, changes not applied, rules missing, rules
// changed outside DROOL, and out of Enforce with the last revision still
// on the host.
func TestFirewallWhatIsInForce(t *testing.T) {
	e := newFwEnv(t)
	ctx := context.Background()
	web, sw := e.sshFwHost(t, "web1")
	_, pw := sw.Host()
	e.h.st.W.UpsertHostAddress(ctx, upsertAddr(web, "10.1.20.15"))
	observe(t, e, web, "ssh_in", "192.0.2.10", pw)
	base := `networks:
  admin-lan:
    cidr: 192.168.1.0/24
policy:
  - name: web-out
    from: web1
    to: internet
    allow: [https]
`
	var cur struct{ Revision string }
	save := func(doc string) {
		t.Helper()
		e.c.do("GET", "/api/v1/firewall/document", nil).json(t, &cur)
		e.saveYAML(t, doc, cur.Revision)
	}
	save(base + `  - name: admin-ssh
    from: admin-lan
    to: web1
    allow: [ssh]
`)
	now := time.Now().Unix()
	e.report(t, web, obs.SourceConntrack, []obs.Flow{
		{Direction: "in", Protocol: "tcp", LocalIP: "10.1.20.15", LocalPort: 22, Peer: "192.168.1.25", Port: 22, RemotePort: 51234, Process: "sshd", Count: 2, FirstSeen: now - 60, LastSeen: now - 30},
		{Direction: "out", Protocol: "tcp", LocalIP: "10.1.20.15", LocalPort: 49322, Peer: "142.250.1.1", Port: 443, RemotePort: 443, Count: 1, FirstSeen: now - 60, LastSeen: now - 30},
	})
	ssh := func() netFlow { t.Helper(); return e.flows(t, "host="+web+"&port=22").Items[0] }
	https := func() netFlow { t.Helper(); return e.flows(t, "host="+web+"&port=443").Items[0] }
	type hop struct {
		Filtered bool     `json:"filtered"`
		Verdict  string   `json:"verdict"`
		Current  string   `json:"current"`
		Notes    []string `json:"notes"`
	}
	type simResult struct {
		Answer  string `json:"answer"`
		Summary string `json:"summary"`
		Hops    []hop  `json:"hops"`
	}
	// Can the admin LAN reach web1's SSH? Only web1 is a DROOL host here.
	sim := func() (simResult, hop) {
		t.Helper()
		var s simResult
		r := e.c.do("POST", "/api/v1/firewall/simulate", map[string]any{"from_address": "192.168.1.25", "to_host": web, "protocol": "tcp", "port": 22})
		if r.status != 200 {
			t.Fatalf("simulate: %d %s", r.status, r.body)
		}
		r.json(t, &s)
		if len(s.Hops) != 1 {
			t.Fatalf("hops: %+v", s)
		}
		return s, s.Hops[0]
	}
	notes := func(n []string) string { return strings.Join(n, " | ") }
	// commit records the desired policy as applied to web1, as a committed
	// transaction does.
	commit := func(rev int) {
		t.Helper()
		plan, err := e.h.fw.Plan(ctx, web, false)
		if err != nil {
			t.Fatal(err)
		}
		pj, _ := json.Marshal(plan.Policy)
		if _, err := e.h.st.Writer.Exec(`UPDATE fw_host_state SET enforced = 1, applied_revision = ?, applied_rules_hash = ?, applied_sets_hash = ?, applied_policy_json = ?, drift = '' WHERE host_id = ?`,
			rev, plan.Rendered.RulesHash, plan.Rendered.SetsHash, string(pj), web); err != nil {
			t.Fatal(err)
		}
	}
	setDrift := func(d string) {
		t.Helper()
		if _, err := e.h.st.Writer.Exec(`UPDATE fw_host_state SET drift = ? WHERE host_id = ?`, d, web); err != nil {
			t.Fatal(err)
		}
	}

	// 1. Enforce, never applied: nothing of DROOL's is in force. The policy
	// allows the admin LAN's SSH; the simulation's answer is the policy's,
	// and it isn't presented as filtering.
	e.setHost(t, web, map[string]any{"mode": "enforce"})
	if f := ssh(); f.Policy.Enforcement != "enforce_pending" || f.Policy.Status != "covered" {
		t.Fatalf("never applied, allowed: %+v", f.Policy)
	}
	if s, h := sim(); h.Filtered || h.Verdict != "accept" || h.Current != "" || s.Answer != "YES" {
		t.Fatalf("never applied, allowed: %+v %+v", s, h)
	}
	// Without the SSH rule, the default drop is what it would do: not a
	// denial, and not Filtered.
	save(base)
	if f := ssh(); f.Policy.Enforcement != "enforce_pending" || f.Policy.Status != "unclassified" || !f.Policy.WouldDeny {
		t.Fatalf("never applied, dropped: %+v", f.Policy)
	}
	s, h := sim()
	if h.Filtered || h.Verdict != "drop" || h.Current != "accept" || !strings.Contains(notes(h.Notes), "has not been applied yet") ||
		s.Answer != "NO" || !strings.Contains(s.Summary, "web1 would block") {
		t.Fatalf("never applied, dropped: %+v %+v", s, h)
	}

	// 2. In force and in sync: a definite denial.
	commit(3)
	if f := ssh(); f.Policy.Enforcement != "enforced" || f.Policy.Status != "denied" || f.Policy.AppliedVerdict != "" {
		t.Fatalf("in force: %+v", f.Policy)
	}
	if s, h := sim(); !h.Filtered || h.Verdict != "drop" || h.Current != "" || !strings.Contains(s.Summary, "web1 blocks") {
		t.Fatalf("in force: %+v %+v", s, h)
	}

	// 3. Changed, not applied: the saved policy allows SSH again, web1 still
	// runs revision 3, which drops it. The status is the saved policy's, but
	// it isn't presented as what happens: the host drops it now. A
	// connection both decide the same way is still in force.
	save(base + `  - name: admin-ssh
    from: admin-lan
    to: web1
    allow: [ssh]
`)
	if f := ssh(); f.Policy.Enforcement != "changes_pending" || f.Policy.Status != "covered" || f.Policy.AppliedVerdict != "drop" ||
		!strings.Contains(notes(f.Policy.Notes), "currently drops this") {
		t.Fatalf("changes not applied: %+v", f.Policy)
	}
	if f := https(); f.Policy.Enforcement != "enforced" || f.Policy.Status != "covered" || f.Policy.AppliedVerdict != "" {
		t.Fatalf("changes not applied, same verdict: %+v", f.Policy)
	}
	s, h = sim()
	if !h.Filtered || h.Verdict != "accept" || h.Current != "drop" || !strings.Contains(notes(h.Notes), "still runs revision 3, which blocks this") ||
		!strings.Contains(s.Summary, "web1 would allow") {
		t.Fatalf("changes not applied: %+v %+v", s, h)
	}
	// And the other way: applied allows, the saved policy drops.
	commit(4)
	save(base)
	if f := ssh(); f.Policy.Enforcement != "changes_pending" || f.Policy.Status != "denied" || f.Policy.AppliedVerdict != "accept" {
		t.Fatalf("changes not applied, a denial to come: %+v", f.Policy)
	}
	if _, h := sim(); !h.Filtered || h.Verdict != "drop" || h.Current != "accept" {
		t.Fatalf("changes not applied, a denial to come: %+v", h)
	}
	commit(5) // back in sync: revision 5 drops SSH

	// 4. Rules missing from the host: nothing of DROOL's in force.
	setDrift("missing")
	if f := ssh(); f.Policy.Enforcement != "enforce_pending" || f.Policy.Status != "unclassified" || !f.Policy.WouldDeny ||
		f.Policy.AppliedVerdict != "" || !strings.Contains(notes(f.Policy.Notes), "rules are missing") {
		t.Fatalf("rules missing: %+v", f.Policy)
	}
	if _, h := sim(); h.Filtered || h.Verdict != "drop" || h.Current != "accept" || !strings.Contains(notes(h.Notes), "missing on web1") {
		t.Fatalf("rules missing: %+v", h)
	}

	// 5. Rules changed outside DROOL: what the host does isn't known, and
	// nothing says otherwise — even with saved changes not applied.
	setDrift("drift")
	save(base + `  - name: admin-ssh
    from: admin-lan
    to: web1
    allow: [ssh]
`)
	for _, f := range []netFlow{ssh(), https()} {
		if f.Policy.Enforcement != "drift" || f.Policy.AppliedVerdict != "" || !strings.Contains(notes(f.Policy.Notes), "isn't known") {
			t.Fatalf("changed outside DROOL: %+v", f.Policy)
		}
	}
	if _, h := sim(); h.Filtered || h.Current != "unknown" || !strings.Contains(notes(h.Notes), "changed outside DROOL") {
		t.Fatalf("changed outside DROOL: %+v", h)
	}
	save(base)
	setDrift("")

	// 6. Out of Enforce with revision 5 still on the host: DROOL wants
	// nothing filtered, but the revision in force drops SSH, and says so.
	e.setHost(t, web, map[string]any{"mode": "monitor"})
	if f := ssh(); f.Policy.Enforcement != "enforced" || f.Policy.Status != "denied" {
		t.Fatalf("monitor, revision in force: %+v", f.Policy)
	}
	if _, h := sim(); !h.Filtered || h.Verdict != "accept" || h.Current != "drop" ||
		!strings.Contains(notes(h.Notes), "last applied policy (revision 5) is still on it and blocks this") {
		t.Fatalf("monitor, revision in force: %+v", h)
	}
}
