package api

import (
	"context"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// From saving a rule to applying it (firewall workflow correction): the
// save says which hosts it affected and changes nothing on them; the host's
// plan shows the saved policy as compiled for it, with named services, and
// what is committed separately; "Review and apply" switches a host to
// Enforce only as an explicit part of the apply, after every check has
// passed — a host refused before that keeps its mode.
func TestFirewallSaveReviewApply(t *testing.T) {
	e := newFwEnv(t)
	ctx := context.Background()
	hostID, _ := e.sshFwHost(t, "pizero")
	e.h.st.W.UpsertHostAddress(ctx, upsertAddr(hostID, "10.40.0.7"))
	e.setHost(t, hostID, map[string]any{"mode": "monitor", "rollback_timeout_s": 30})
	if r := e.c.do("POST", "/api/v1/firewall/hosts/"+hostID+"/detect", nil); r.status != 200 {
		t.Fatalf("detect: %d %s", r.status, r.body)
	}
	type allowance struct {
		Direction string   `json:"direction"`
		Verdict   string   `json:"verdict"`
		Service   string   `json:"service"`
		Peer      string   `json:"peer"`
		Services  []string `json:"services"`
		Reason    struct {
			Kind       string `json:"kind"`
			PolicyName string `json:"policy_name"`
		} `json:"reason"`
	}
	type summary struct {
		HostID   string `json:"host_id"`
		HostName string `json:"host_name"`
		Mode     string `json:"mode"`
		Status   string `json:"status"`
		Enforced bool   `json:"enforced"`
		Revision int64  `json:"revision"`
	}
	type plan struct {
		Config  struct{ Mode string } `json:"config"`
		Applied struct {
			StatusAt     string `json:"status_at"`
			Enforced     bool   `json:"enforced"`
			Revision     int64  `json:"revision"`
			RulesHash    string `json:"rules_hash"`
			RulesChanged bool   `json:"rules_changed"`
		} `json:"applied"`
		Rendered struct {
			RulesHash string `json:"rules_hash"`
		} `json:"rendered"`
		Explain        []allowance `json:"explain"`
		AppliedExplain []allowance `json:"applied_explain"`
		Summary        summary     `json:"summary"`
	}
	getPlan := func() plan {
		t.Helper()
		var p plan // a fresh value: fields a later response omits stay empty
		if r := e.c.do("GET", "/api/v1/firewall/hosts/"+hostID, nil); r.status != 200 {
			t.Fatalf("plan: %d %s", r.status, r.body)
		} else {
			r.json(t, &p)
		}
		return p
	}
	line := func(as []allowance, peer string) *allowance {
		for i := range as {
			if as[i].Peer == peer && as[i].Reason.Kind == "policy" {
				return &as[i]
			}
		}
		return nil
	}
	txCount := func() int {
		t.Helper()
		var n int
		e.h.st.Reader.QueryRow(`SELECT count(*) FROM fw_transactions WHERE host_id = ?`, hostID).Scan(&n)
		return n
	}
	save := func(y string) []summary {
		t.Helper()
		var cur struct{ Revision string }
		e.c.do("GET", "/api/v1/firewall/document", nil).json(t, &cur)
		r := e.c.do("PUT", "/api/v1/firewall/document", map[string]any{"yaml": y, "revision": cur.Revision})
		if r.status != 200 {
			t.Fatalf("save: %d %s", r.status, r.body)
		}
		var out struct {
			Hosts []summary `json:"hosts"`
		}
		r.json(t, &out)
		return out.Hosts
	}
	esphome := `services:
  esphome:
    protocol: tcp
    port: 6053
policy:
  - name: ha-to-esphome
    from: 192.168.3.132
    to: pizero
    allow: [esphome]
`

	// Saving: the host is affected, nothing is applied, the mode stays.
	hosts := save(esphome)
	if len(hosts) != 1 || hosts[0].HostName != "pizero" || hosts[0].Status != "not_enforced" || hosts[0].Mode != "monitor" {
		t.Fatalf("affected hosts after the save: %+v", hosts)
	}
	if n := txCount(); n != 0 {
		t.Fatalf("saving started %d transactions", n)
	}
	// The plan: the saved policy as compiled for this host, by name.
	p := getPlan()
	l := line(p.Explain, "192.168.3.132/32")
	if l == nil || l.Direction != "in" || l.Verdict != "accept" || l.Service != "TCP 6053" || !slices.Equal(l.Services, []string{"esphome"}) ||
		l.Reason.PolicyName != "ha-to-esphome" {
		t.Fatalf("the saved rule in the host's plan: %+v\n%+v", l, p.Explain)
	}
	var defaults, mgmt []string
	for _, a := range p.Explain {
		switch a.Reason.Kind {
		case "default":
			defaults = append(defaults, a.Direction+" "+a.Verdict)
		case "management":
			mgmt = append(mgmt, a.Service+" from "+a.Peer)
		}
	}
	if !slices.Equal(defaults, []string{"in drop", "out accept"}) || len(mgmt) == 0 {
		t.Fatalf("defaults %v, management %v", defaults, mgmt)
	}
	if p.Config.Mode != "monitor" || p.Applied.Enforced || len(p.AppliedExplain) != 0 || p.Summary.Status != "not_enforced" {
		t.Fatalf("before applying: %+v", p)
	}

	// Applying without the switch is refused, as before: Monitor stays.
	if jh, _ := e.apply(t, hostID, nil); jh.State != "failed" || !strings.Contains(jh.ErrorMessage, "Enforce mode") {
		t.Fatalf("apply in Monitor: %+v", jh)
	}
	// A check that refuses before the transaction leaves the mode alone:
	// the policy changed since the review...
	jh, _ := e.apply(t, hostID, map[string]any{"set_enforce": true, "expect_rules_hashes": map[string]string{hostID: "0000"}})
	if jh.State != "failed" || !strings.Contains(jh.ErrorMessage, "changed since it was reviewed") {
		t.Fatalf("stale review: %+v", jh)
	}
	// ...or the environment can't be enforced (Docker on the host).
	sock := filepath.Join(t.TempDir(), "docker.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DROOL_FW_DOCKER_SOCK", sock)
	if jh, _ := e.apply(t, hostID, map[string]any{"set_enforce": true}); jh.State != "failed" || !strings.Contains(jh.ErrorMessage, "Docker is running") {
		t.Fatalf("refused environment: %+v", jh)
	}
	ln.Close()
	t.Setenv("DROOL_FW_DOCKER_SOCK", filepath.Join(t.TempDir(), "none"))
	if m := getPlan().Config.Mode; m != "monitor" || auditCount(t, e.h, "firewall.host.update") != 1 {
		t.Fatalf("a refused apply changed the mode: %s, %d mode changes", m, auditCount(t, e.h, "firewall.host.update"))
	}

	// The reviewed apply: Enforce, then the transaction.
	p = getPlan()
	jh, tx := e.apply(t, hostID, map[string]any{"set_enforce": true, "expect_rules_hashes": map[string]string{hostID: p.Rendered.RulesHash}})
	if jh.State != "succeeded" || tx.State != "committed" {
		t.Fatalf("review and apply: %+v %+v\n%s", jh, tx, tx.timeline())
	}
	var details, actor string
	e.h.st.Reader.QueryRow(`SELECT details_json, actor_type FROM audit_events WHERE action = 'firewall.host.update' ORDER BY id DESC LIMIT 1`).Scan(&details, &actor)
	if !strings.Contains(details, `"via":"apply"`) || !strings.Contains(details, `"previous_mode":"monitor"`) || actor != "user" {
		t.Fatalf("the switch isn't audited as the user's: %s %s", actor, details)
	}
	p = getPlan()
	if p.Config.Mode != "enforce" || !p.Applied.Enforced || p.Summary.Status != "in_sync" {
		t.Fatalf("after the reviewed apply: %+v", p)
	}
	// The commit read the host's firewall state: that is when DROOL last
	// read it, not an older status report.
	if at, err := time.Parse(time.RFC3339, p.Applied.StatusAt); err != nil || time.Since(at) > time.Minute {
		t.Fatalf("status_at after the commit: %q", p.Applied.StatusAt)
	}
	if a := line(p.AppliedExplain, "192.168.3.132/32"); a == nil || !slices.Equal(a.Services, []string{"esphome"}) {
		t.Fatalf("what is committed: %+v", p.AppliedExplain)
	}

	// A further save: the host has changes not applied; the committed
	// policy is still what the host runs, and nothing else happens.
	before := txCount()
	hosts = save(esphome + `  - name: admin-ssh
    from: 192.168.3.10
    to: pizero
    allow: [ssh]
`)
	if len(hosts) != 1 || hosts[0].Status != "changes_pending" || !hosts[0].Enforced || hosts[0].Revision != tx.Revision {
		t.Fatalf("affected hosts after the second save: %+v", hosts)
	}
	p = getPlan()
	if line(p.Explain, "192.168.3.10/32") == nil || line(p.AppliedExplain, "192.168.3.10/32") != nil || !p.Applied.RulesChanged {
		t.Fatalf("saved vs committed: %+v / %+v", p.Explain, p.AppliedExplain)
	}
	if txCount() != before || p.Config.Mode != "enforce" {
		t.Fatal("saving applied something or changed the mode")
	}
	// A save that changes nothing for the host names no host.
	if hosts := save(esphome + `  - name: admin-ssh
    from: 192.168.3.10
    to: pizero
    allow: [ssh]
  - name: unrelated
    from: 192.168.3.10
    to: 192.168.9.9
    allow: [ssh]
`); len(hosts) != 0 {
		t.Fatalf("a save not affecting the host: %+v", hosts)
	}
}
