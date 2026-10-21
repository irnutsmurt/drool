package api

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The SSH rule missing from PiZeroWopenwx's review (real-host report): the
// Designer showed "Allow ssh from Internal networks", yet review and apply
// said "Nothing compared with revision 2" and listed only DROOL's
// management path and TCP 6053. The rule's destination named addresses
// (Internal networks, a network, a CIDR containing the Pi's address) — or
// the Pi as its source — instead of the Pi itself, and DROOL puts a rule
// on the hosts its destination names: the rule was valid, on no host, and
// nothing said so. The check, the save and the host's plan now say so; with
// the Pi as destination the rule is compiled, hashed, reviewed, rendered and
// committed.
func TestFirewallRuleDestinationNamesAddresses(t *testing.T) {
	e := newFwEnv(t)
	ctx := context.Background()
	// The Pi: a Shepherd host at 192.168.3.50, in Enforce, running revision 2
	// of a policy that allows only TCP 6053 from Home Assistant.
	pi, _, fake, _ := e.agentFwHost(t, "agent7", fullCaps(""))
	if _, err := e.h.st.Writer.Exec(`UPDATE hosts SET name = 'PiZeroWopenwx' WHERE id = ?`, pi); err != nil {
		t.Fatal(err)
	}
	// Configured, so the in-process Shepherd's own address reports (this
	// machine's) don't replace it.
	addr := upsertAddr(pi, "192.168.3.50")
	addr.Source = "configured"
	e.h.st.W.UpsertHostAddress(ctx, addr)
	e.setHost(t, pi, map[string]any{"mode": "enforce", "rollback_timeout_s": 30})
	if r := e.c.do("POST", "/api/v1/firewall/hosts/"+pi+"/detect", nil); r.status != 200 {
		t.Fatalf("detect: %d %s", r.status, r.body)
	}
	const base = `networks:
  lan:
    cidr: 192.168.3.0/24
services:
  esphome:
    protocol: tcp
    port: 6053
policy:
  - name: ha-to-esphome
    from: 192.168.3.132
    to: PiZeroWopenwx
    allow: [esphome]
`
	type notice struct {
		Rule    string   `json:"rule"`
		Message string   `json:"message"`
		Hosts   []string `json:"hosts"`
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
	type plan struct {
		Applied struct {
			Revision     int64 `json:"revision"`
			RulesChanged bool  `json:"rules_changed"`
		} `json:"applied"`
		Rendered struct {
			Full      string `json:"full"`
			RulesHash string `json:"rules_hash"`
		} `json:"rendered"`
		Explain        []allowance `json:"explain"`
		AppliedExplain []allowance `json:"applied_explain"`
		Added          []string    `json:"changes_added"`
		Removed        []string    `json:"changes_removed"`
		AddressOnly    []notice    `json:"address_only_rules"`
	}
	getPlan := func() plan {
		t.Helper()
		var p plan
		if r := e.c.do("GET", "/api/v1/firewall/hosts/"+pi, nil); r.status != 200 {
			t.Fatalf("plan: %d %s", r.status, r.body)
		} else {
			r.json(t, &p)
		}
		return p
	}
	type saved struct {
		Hosts []struct {
			HostName string `json:"host_name"`
			Status   string `json:"status"`
		} `json:"hosts"`
		NoHost []notice `json:"no_host_rules"`
	}
	save := func(y string) saved {
		t.Helper()
		var cur struct{ Revision string }
		e.c.do("GET", "/api/v1/firewall/document", nil).json(t, &cur)
		r := e.c.do("PUT", "/api/v1/firewall/document", map[string]any{"yaml": y, "revision": cur.Revision})
		if r.status != 200 {
			t.Fatalf("save: %d %s", r.status, r.body)
		}
		var out saved
		r.json(t, &out)
		return out
	}
	check := func(y string) (valid bool, impact int, noHost []notice) {
		t.Helper()
		var c struct {
			Valid  bool             `json:"valid"`
			Impact []map[string]any `json:"impact"`
			NoHost []notice         `json:"no_host_rules"`
		}
		r := e.c.do("POST", "/api/v1/firewall/document/check", map[string]any{"yaml": y})
		if r.status != 200 {
			t.Fatalf("check: %d %s", r.status, r.body)
		}
		r.json(t, &c)
		return c.Valid, len(c.Impact), c.NoHost
	}
	sshLine := func(as []allowance) *allowance {
		for i := range as {
			if as[i].Reason.PolicyName == "ssh-internal" {
				return &as[i]
			}
		}
		return nil
	}

	if s := save(base); len(s.Hosts) != 1 || len(s.NoHost) != 0 {
		t.Fatalf("saving the 6053 rule: %+v", s)
	}
	for rev := 1; rev <= 2; rev++ {
		if jh, tx := e.apply(t, pi, nil); jh.State != "succeeded" || tx.State != "committed" || tx.Revision != int64(rev) {
			t.Fatalf("apply %d: %+v %+v\n%s", rev, jh, tx, tx.timeline())
		}
	}

	// The rule as it may have been written: the destination is addresses
	// (or the Pi is the source). Each is valid and changes no host — and
	// now says so, everywhere the rule is looked at.
	for _, c := range []struct{ name, fromTo string }{
		{"Internal networks", "from: internal\n    to: internal"},
		{"a network containing the Pi", "from: internal\n    to: lan"},
		{"the Pi's address", "from: internal\n    to: 192.168.3.50"},
		{"the Pi as the source", "from: PiZeroWopenwx\n    to: internal"},
	} {
		y := base + "  - name: ssh-internal\n    " + c.fromTo + "\n    allow: [ssh]\n"
		valid, impact, noHost := check(y)
		if !valid || impact != 0 || len(noHost) != 1 || noHost[0].Rule != "ssh-internal" || !slices.Equal(noHost[0].Hosts, []string{"PiZeroWopenwx"}) ||
			!strings.Contains(noHost[0].Message, "applies to no host") || !strings.Contains(noHost[0].Message, "to allow ssh to it, make it the destination") {
			t.Fatalf("%s: check valid=%v impact=%d no-host %+v", c.name, valid, impact, noHost)
		}
		s := save(y)
		if len(s.Hosts) != 0 || len(s.NoHost) != 1 || s.NoHost[0].Rule != "ssh-internal" {
			t.Fatalf("%s: save %+v", c.name, s)
		}
		p := getPlan()
		// What the review showed: nothing compared with revision 2, and no
		// SSH rule — now with the reason beside it.
		if p.Applied.Revision != 2 || p.Applied.RulesChanged || len(p.Added)+len(p.Removed) != 0 || sshLine(p.Explain) != nil {
			t.Fatalf("%s: plan %+v", c.name, p)
		}
		if len(p.AddressOnly) != 1 || p.AddressOnly[0].Rule != "ssh-internal" || !strings.Contains(p.AddressOnly[0].Message, "192.168.3.50") ||
			!strings.Contains(p.AddressOnly[0].Message, "make PiZeroWopenwx the destination") {
			t.Fatalf("%s: the host's plan doesn't say why: %+v", c.name, p.AddressOnly)
		}
	}
	// The source side is said too: from the Pi it would only allow
	// outbound connections, which aren't restricted.
	if _, _, nh := check(base + "  - name: ssh-internal\n    from: PiZeroWopenwx\n    to: internal\n    allow: [ssh]\n"); len(nh) != 1 || !strings.Contains(nh[0].Message, "For PiZeroWopenwx (its source)") {
		t.Fatalf("reversed rule: %+v", nh)
	}
	// So is a source of Anywhere: every host, outbound.
	if _, _, nh := check(base + "  - name: web-out\n    from: any\n    to: internet\n    allow: [https]\n"); len(nh) != 1 || !strings.Contains(nh[0].Message, "For PiZeroWopenwx (its source)") {
		t.Fatalf("a rule from anywhere: %+v", nh)
	}
	// A disabled rule is on no host either, visibly so: no notice.
	if _, _, nh := check(base + "  - name: ssh-internal\n    from: internal\n    to: internal\n    allow: [ssh]\n    enabled: false\n"); len(nh) != 0 {
		t.Fatalf("disabled rule: %+v", nh)
	}

	// The correction: the Pi as destination. Internal networks resolves to
	// the configured internal ranges; the rule is in the Pi's compiled
	// inbound policy, the hashes and change detection see it, and the
	// generated nftables rules are what the review shows.
	fixed := base + "  - name: ssh-internal\n    from: internal\n    to: PiZeroWopenwx\n    allow: [ssh]\n"
	if valid, impact, nh := check(fixed); !valid || impact != 1 || len(nh) != 0 {
		t.Fatalf("corrected rule: check valid=%v impact=%d no-host %+v", valid, impact, nh)
	}
	if s := save(fixed); len(s.Hosts) != 1 || s.Hosts[0].HostName != "PiZeroWopenwx" || s.Hosts[0].Status != "changes_pending" || len(s.NoHost) != 0 {
		t.Fatalf("saving the corrected rule: %+v", s)
	}
	p := getPlan()
	l := sshLine(p.Explain)
	if l == nil || l.Direction != "in" || l.Verdict != "accept" || l.Service != "TCP 22" || l.Peer != "internal networks" || !slices.Equal(l.Services, []string{"ssh"}) {
		t.Fatalf("the corrected rule in the Pi's plan: %+v\n%+v", l, p.Explain)
	}
	if !p.Applied.RulesChanged || !slices.Equal(p.Added, []string{"allow inbound TCP 22 from internal networks (policy ssh-internal)"}) || len(p.Removed) != 0 || len(p.AddressOnly) != 0 {
		t.Fatalf("change detection: changed=%v added=%v removed=%v address-only=%v", p.Applied.RulesChanged, p.Added, p.Removed, p.AddressOnly)
	}
	if !strings.Contains(p.Rendered.Full, `ip saddr @internal_4 tcp dport { 22 } accept comment "policy ssh-internal"`) ||
		!strings.Contains(p.Rendered.Full, `ip6 saddr @internal_6 tcp dport { 22 } accept comment "policy ssh-internal"`) {
		t.Fatalf("generated rules:\n%s", p.Rendered.Full)
	}
	var settings struct {
		InternalRanges []string `json:"internal_ranges"`
	}
	e.c.do("GET", "/api/v1/firewall/settings", nil).json(t, &settings)
	set := setElements(p.Rendered.Full, "internal_4")
	for _, r := range settings.InternalRanges {
		if !strings.Contains(r, ":") && !slices.Contains(set, r) {
			t.Fatalf("internal_4 %v lacks the configured range %s", set, r)
		}
	}
	// Applied with the reviewed hash: the host runs it, and it is what DROOL
	// committed.
	jh, tx := e.apply(t, pi, map[string]any{"expect_rules_hashes": map[string]string{pi: p.Rendered.RulesHash}})
	if jh.State != "succeeded" || tx.State != "committed" || tx.Revision != 3 {
		t.Fatalf("applying the corrected rule: %+v %+v\n%s", jh, tx, tx.timeline())
	}
	if live := fileText(filepath.Join(fake, "live")); !strings.Contains(live, `tcp dport { 22 } accept comment "policy ssh-internal"`) {
		t.Fatalf("the host's ruleset:\n%s", live)
	}
	if p := getPlan(); p.Applied.RulesChanged || sshLine(p.AppliedExplain) == nil {
		t.Fatalf("after applying: %+v", p)
	}
}

// setElements reads a set's elements from rendered nftables.
func setElements(full, name string) []string {
	_, rest, ok := strings.Cut(full, "set "+name+" {")
	if !ok {
		return nil
	}
	body, _, _ := strings.Cut(rest, "}\n")
	_, el, ok := strings.Cut(body, "elements = { ")
	if !ok {
		return nil
	}
	el, _, _ = strings.Cut(el, "}")
	var out []string
	for _, x := range strings.Split(el, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// Review and apply with hosts in different modes: only the hosts whose
// review chose Enforce switch; a host left in its mode is never applied
// to; changing a host to Monitor or Guided applies nothing.
func TestFirewallApplyMixedModes(t *testing.T) {
	e := newFwEnv(t)
	// Shepherd hosts: each has its own (fake) firewall state.
	ids := map[string]string{}
	for i, n := range []string{"enf-1", "mon-1", "off-1"} {
		id, _, _, _ := e.agentFwHost(t, "agent"+itoa(i+1), fullCaps(""))
		if _, err := e.h.st.Writer.Exec(`UPDATE hosts SET name = ? WHERE id = ?`, n, id); err != nil {
			t.Fatal(err)
		}
		ids[n] = id
		if r := e.c.do("POST", "/api/v1/firewall/hosts/"+id+"/detect", nil); r.status != 200 {
			t.Fatalf("detect: %d %s", r.status, r.body)
		}
	}
	e.setHost(t, ids["enf-1"], map[string]any{"mode": "enforce", "rollback_timeout_s": 30})
	e.setHost(t, ids["mon-1"], map[string]any{"mode": "monitor", "rollback_timeout_s": 30})
	// off-1 has no firewall settings: Off.
	var cur struct{ Revision string }
	e.c.do("GET", "/api/v1/firewall/document", nil).json(t, &cur)
	e.saveYAML(t, `policy:
  - name: web
    from: internal
    to: [enf-1, mon-1, off-1]
    allow: [https]
`, cur.Revision)
	mode := func(n string) string {
		var p struct{ Config struct{ Mode string } }
		e.c.do("GET", "/api/v1/firewall/hosts/"+ids[n], nil).json(t, &p)
		return p.Config.Mode
	}
	txs := func(n string) int {
		var c int
		e.h.st.Reader.QueryRow(`SELECT count(*) FROM fw_transactions WHERE host_id = ?`, ids[n]).Scan(&c)
		return c
	}
	if mode("off-1") != "off" {
		t.Fatalf("off-1 starts in %s", mode("off-1"))
	}
	modeChanges := auditCount(t, e.h, "firewall.host.update")

	// The review: enf-1 as it is, mon-1 switched to Enforce, off-1 kept Off
	// (not a target). One run, one host at a time.
	r := e.c.do("POST", "/api/v1/firewall/apply", map[string]any{"host_ids": []string{ids["enf-1"], ids["mon-1"]}, "set_enforce_hosts": []string{ids["mon-1"]}})
	if r.status != 201 {
		t.Fatalf("apply: %d %s", r.status, r.body)
	}
	var j jobJSON
	r.json(t, &j)
	eventually(t, 2*time.Minute, "the apply run", func() bool {
		var cur jobJSON
		e.c.do("GET", "/api/v1/jobs/"+j.ID, nil).json(t, &cur)
		return cur.Status == "completed"
	})
	for _, n := range []string{"enf-1", "mon-1"} {
		var jh jobHostResp
		e.c.do("GET", "/api/v1/jobs/"+j.ID+"/hosts/"+ids[n], nil).json(t, &jh)
		if jh.State != "succeeded" || e.lastTx(t, ids[n]).State != "committed" || mode(n) != "enforce" {
			t.Fatalf("%s: %+v, transaction %s, mode %s", n, jh, e.lastTx(t, ids[n]).State, mode(n))
		}
	}
	if mode("off-1") != "off" || txs("off-1") != 0 || auditCount(t, e.h, "firewall.host.update") != modeChanges+1 {
		t.Fatalf("off-1 changed: mode %s, %d transactions; %d mode changes", mode("off-1"), txs("off-1"), auditCount(t, e.h, "firewall.host.update")-modeChanges)
	}

	// Off → Guided (the review's other choice): a mode change only, no
	// transaction, nothing enforced.
	e.setHost(t, ids["off-1"], map[string]any{"mode": "guided", "rollback_timeout_s": 0})
	var p struct {
		Config  struct{ Mode string }
		Applied struct{ Enforced bool }
	}
	e.c.do("GET", "/api/v1/firewall/hosts/"+ids["off-1"], nil).json(t, &p)
	if p.Config.Mode != "guided" || p.Applied.Enforced || txs("off-1") != 0 {
		t.Fatalf("off-1 to Guided: %+v, %d transactions", p, txs("off-1"))
	}
	// And an apply that doesn't name it for the switch is refused there.
	if jh, _ := e.apply(t, ids["off-1"], map[string]any{"set_enforce_hosts": []string{ids["mon-1"]}}); jh.State != "failed" || !strings.Contains(jh.ErrorMessage, "Enforce mode") {
		t.Fatalf("apply to a Guided host without its switch: %+v", jh)
	}
	if mode("off-1") != "guided" || txs("off-1") != 0 {
		t.Fatalf("a refused apply changed off-1: %s, %d transactions", mode("off-1"), txs("off-1"))
	}
}
