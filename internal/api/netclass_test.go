package api

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"drool/internal/auth"
	obs "drool/internal/firewall/observe"
)

type netClassFlow struct {
	ID             int64  `json:"id"`
	Direction      string `json:"direction"`
	Protocol       string `json:"protocol"`
	LocalIP        string `json:"local_ip"`
	RemoteIP       string `json:"remote_ip"`
	Port           int    `json:"port"`
	Process        string `json:"process"`
	Host           struct{ ID, Name string }
	Classification struct {
		Kind   string `json:"kind"`
		Label  string `json:"label"`
		Source string `json:"source"`
		RuleID string `json:"rule_id"`
		Detail string `json:"detail"`
	} `json:"classification"`
}

// TestNetworkClassification (final After-M17 batch): what observed traffic
// is. DROOL's own management traffic is recognized from evidence only: the
// address a host sees DROOL's SSH come from, the endpoint a Shepherd
// reported (with its own process), a jump host's forwarded connection, and
// both ends of those on the host DROOL runs on. The same ports used by
// anything else stay what they are. A user's classifications match by host
// or Pack, direction, protocol, port and remote side, the most specific
// first; they apply to traffic already seen and seen later, are audited, and
// never touch the firewall.
func TestNetworkClassification(t *testing.T) {
	e := newFwEnv(t)
	ctx := context.Background()
	add := func(name, addr string) string {
		t.Helper()
		r := e.c.do("POST", "/api/v1/hosts", map[string]any{"name": name, "address": addr, "port": 22, "user": "alex"})
		if r.status != 201 {
			t.Fatalf("add %s: %d %s", name, r.status, r.body)
		}
		var h hostJSON
		r.json(t, &h)
		if err := e.h.st.W.UpsertHostAddress(ctx, upsertAddr(h.ID, addr)); err != nil {
			t.Fatal(err)
		}
		return h.ID
	}
	ctl := add("ctl", "10.1.0.5") // the host DROOL runs on
	web := add("web1", "10.1.20.15")
	db1 := add("db1", "10.1.20.30")
	nas := add("nas1", "10.1.20.40")
	jump := add("jump1", "10.1.30.1")
	app := add("app1", "10.1.30.5")
	if r := e.c.do("PATCH", "/api/v1/hosts/"+app, map[string]any{"via_host_id": jump}); r.status != 200 {
		t.Fatalf("via: %d %s", r.status, r.body)
	}
	var storage struct{ ID string }
	e.c.do("POST", "/api/v1/groups", map[string]any{"name": "storage", "host_ids": []string{nas}}).json(t, &storage)
	// DROOL's evidence: web1 sees DROOL's SSH come from 10.1.0.5 to its
	// sshd on 22; db1's Shepherd reported connecting to 10.1.0.5:9443.
	observe(t, e, web, "ssh_in", "10.1.0.5", 22)
	observe(t, e, db1, "agent_out", "10.1.0.5", 9443)
	var fwBefore struct{ Revision string }
	e.c.do("GET", "/api/v1/firewall/document", nil).json(t, &fwBefore)

	now := time.Now().Unix()
	fl := func(dir, proto, local, peer string, port int, process string) obs.Flow {
		return obs.Flow{Direction: dir, Protocol: proto, LocalIP: local, LocalPort: 40000 + port%1000, Peer: peer, Port: port, RemotePort: port,
			Process: process, Count: 1, FirstSeen: now - 60, LastSeen: now - 10}
	}
	e.report(t, web, obs.SourceConntrack, []obs.Flow{
		fl("in", "tcp", "10.1.20.15", "10.1.0.5", 22, "sshd"),        // DROOL's SSH
		fl("in", "tcp", "10.1.20.15", "192.168.1.25", 22, "sshd"),    // someone else's SSH
		fl("in", "udp", "10.1.20.15", "10.1.0.5", 22, ""),            // not SSH at all
		fl("out", "tcp", "10.1.20.15", "10.1.20.40", 2049, ""),       // NFS to nas1
		fl("out", "tcp", "10.1.20.15", "203.0.113.50", 9443, "curl"), // a stranger's 9443
		fl("out", "tcp", "10.1.20.15", "185.10.1.1", 8812, ""),       // unknown
	})
	e.report(t, db1, obs.SourceSockets, []obs.Flow{
		fl("out", "tcp", "10.1.20.30", "10.1.0.5", 9443, "drool-agent"), // the Shepherd
		fl("out", "tcp", "10.1.20.31", "10.1.0.5", 9443, "curl"),        // another process to the same endpoint
		fl("out", "tcp", "10.1.20.30", "10.1.0.99", 9443, ""),           // another endpoint
	})
	e.report(t, ctl, obs.SourceConntrack, []obs.Flow{
		fl("out", "tcp", "10.1.0.5", "10.1.20.15", 22, ""),    // DROOL → web1
		fl("in", "tcp", "10.1.0.5", "10.1.20.30", 9443, ""),   // db1's Shepherd → DROOL
		fl("out", "tcp", "10.1.0.5", "10.1.20.40", 22, "ssh"), // nas1: no evidence DROOL manages it from here
	})
	e.report(t, jump, obs.SourceConntrack, []obs.Flow{
		fl("out", "tcp", "10.1.30.1", "10.1.30.5", 22, ""),  // forwarding DROOL's SSH to app1
		fl("out", "tcp", "10.1.30.1", "10.1.20.15", 22, ""), // web1 isn't behind jump1
	})

	flows := func(q string) map[string]netClassFlow {
		t.Helper()
		r := e.c.do("GET", "/api/v1/network/flows?limit=500&"+q, nil)
		if r.status != 200 {
			t.Fatalf("flows?%s: %d %s", q, r.status, r.body)
		}
		var p struct{ Items []netClassFlow }
		r.json(t, &p)
		m := map[string]netClassFlow{}
		for _, f := range p.Items {
			m[fmt.Sprintf("%s %s %s %s:%d", f.Host.Name, f.Direction, f.Protocol, f.RemoteIP, f.Port)+map[bool]string{true: " " + f.LocalIP}[f.LocalIP == "10.1.20.31"]] = f
		}
		return m
	}
	keys := func(m map[string]netClassFlow) []string {
		var out []string
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	kind := func(m map[string]netClassFlow, k string) string {
		t.Helper()
		f, ok := m[k]
		if !ok {
			t.Fatalf("no flow %q among %v", k, keys(m))
		}
		return f.Classification.Kind + "/" + f.Classification.Source + "/" + f.Classification.Label
	}

	all := flows("")
	want := map[string]string{
		"web1 in tcp 10.1.0.5:22":              "drool/drool/DROOL's SSH to this host",
		"web1 in tcp 192.168.1.25:22":          "service/port/ssh",
		"web1 in udp 10.1.0.5:22":              "unknown/none/Unknown",
		"web1 out tcp 10.1.20.40:2049":         "service/port/nfs",
		"web1 out tcp 203.0.113.50:9443":       "unknown/none/Unknown",
		"web1 out tcp 185.10.1.1:8812":         "unknown/none/Unknown",
		"db1 out tcp 10.1.0.5:9443":            "drool/drool/Shepherd connection to DROOL",
		"db1 out tcp 10.1.0.5:9443 10.1.20.31": "unknown/none/Unknown",
		"db1 out tcp 10.1.0.99:9443":           "unknown/none/Unknown",
		"ctl out tcp 10.1.20.15:22":            "drool/drool/DROOL's SSH to web1",
		"ctl in tcp 10.1.20.30:9443":           "drool/drool/Shepherd of db1 connecting",
		"ctl out tcp 10.1.20.40:22":            "service/port/ssh",
		"jump1 out tcp 10.1.30.5:22":           "drool/drool/DROOL's SSH to app1 through this jump host",
		"jump1 out tcp 10.1.20.15:22":          "service/port/ssh",
	}
	for k, w := range want {
		if got := kind(all, k); got != w {
			t.Errorf("%s: %s, want %s", k, got, w)
		}
	}
	if t.Failed() {
		t.FailNow()
	}
	// The evidence, in words, on the connection's details.
	var detail netClassFlow
	e.c.do("GET", fmt.Sprintf("/api/v1/network/flows/%d", all["web1 in tcp 10.1.0.5:22"].ID), nil).json(t, &detail)
	if detail.Classification.Kind != "drool" || !strings.Contains(detail.Classification.Detail, "10.1.0.5") || !strings.Contains(detail.Classification.Detail, "port 22") {
		t.Fatalf("detail: %+v", detail.Classification)
	}
	// Visible and filterable: exactly the evidenced ones.
	if got := keys(flows("class=drool")); !slices.Equal(got, []string{"ctl in tcp 10.1.20.30:9443", "ctl out tcp 10.1.20.15:22", "db1 out tcp 10.1.0.5:9443",
		"jump1 out tcp 10.1.30.5:22", "web1 in tcp 10.1.0.5:22"}) {
		t.Fatalf("class=drool: %v", got)
	}
	if r := e.c.do("GET", "/api/v1/network/flows?class=bogus", nil); r.status != 422 {
		t.Fatalf("bad class: %d", r.status)
	}

	// Classifications.
	type rule struct {
		ID, Label, Origin, Described string
		Expected                     bool
	}
	put := func(method, path string, body map[string]any, status int) rule {
		t.Helper()
		r := e.c.do(method, path, body)
		if r.status != status {
			t.Fatalf("%s %s %v: %d %s", method, path, body, r.status, r.body)
		}
		var out rule
		if status == 200 || status == 201 {
			r.json(t, &out)
		}
		return out
	}
	const rules = "/api/v1/network/classifications"
	nfs := put("POST", rules, map[string]any{"label": "NFS media storage", "expected": true, "direction": "out", "protocol": "tcp", "port": 2049, "remote_group_id": storage.ID}, 201)
	if nfs.Described != "any host → pack storage, TCP 2049" || nfs.Origin != "rule" {
		t.Fatalf("described: %+v", nfs)
	}
	hosting := put("POST", rules, map[string]any{"label": "Example hosting", "remote_cidr": "185.10.0.0/16"}, 201)
	put("POST", rules, map[string]any{"label": "SSH logins", "direction": "in", "port": 22}, 201)
	laptop := put("POST", rules, map[string]any{"label": "Admin laptop", "origin": "flow", "host_id": web, "direction": "in", "protocol": "tcp", "port": 22, "remote_cidr": "192.168.1.25"}, 201)
	put("POST", rules, map[string]any{"label": "Ops tooling on db1", "host_id": db1, "port": 9443}, 201)
	// Even a classification matching DROOL's own traffic exactly doesn't
	// take it over.
	put("POST", rules, map[string]any{"label": "Not DROOL", "origin": "flow", "host_id": web, "direction": "in", "protocol": "tcp", "port": 22, "remote_cidr": "10.1.0.5"}, 201)

	all = flows("")
	for k, w := range map[string]string{
		"web1 out tcp 10.1.20.40:2049":         "approved/rule/NFS media storage",
		"web1 out tcp 185.10.1.1:8812":         "user/rule/Example hosting",
		"web1 in tcp 192.168.1.25:22":          "user/manual/Admin laptop", // the label from one connection beats "SSH logins"
		"web1 in tcp 10.1.0.5:22":              "drool/drool/DROOL's SSH to this host",
		"db1 out tcp 10.1.0.5:9443":            "drool/drool/Shepherd connection to DROOL",
		"db1 out tcp 10.1.0.5:9443 10.1.20.31": "user/rule/Ops tooling on db1",
		"db1 out tcp 10.1.0.99:9443":           "user/rule/Ops tooling on db1",
		"web1 out tcp 203.0.113.50:9443":       "unknown/none/Unknown", // the db1 classification is db1's only
		"web1 in udp 10.1.0.5:22":              "user/rule/SSH logins", // not DROOL's (UDP), and "SSH logins" says any protocol
	} {
		if got := kind(all, k); got != w {
			t.Errorf("%s: %s, want %s", k, got, w)
		}
	}
	if f := all["web1 in tcp 192.168.1.25:22"]; f.Classification.RuleID != laptop.ID || !strings.Contains(f.Classification.Detail, "from this connection") {
		t.Errorf("provenance: %+v", f.Classification)
	}
	// The detail says what the classification matches, in words (it once
	// said "()": the rules were described only when listed).
	if d := all["web1 out tcp 10.1.20.40:2049"].Classification.Detail; !strings.Contains(d, "(any host → pack storage, TCP 2049)") || strings.Contains(d, "()") {
		t.Errorf("rule detail: %q", d)
	}
	if d := all["web1 in tcp 192.168.1.25:22"].Classification.Detail; !strings.Contains(d, "(web1 ← 192.168.1.25, TCP 22)") {
		t.Errorf("connection label detail: %q", d)
	}
	if t.Failed() {
		t.FailNow()
	}
	if got := keys(flows("class=approved")); !slices.Equal(got, []string{"web1 out tcp 10.1.20.40:2049"}) {
		t.Fatalf("class=approved: %v", got)
	}
	if got := keys(flows("class=unknown")); !slices.Equal(got, []string{"web1 out tcp 203.0.113.50:9443"}) {
		t.Fatalf("class=unknown: %v", got)
	}
	if n := len(flows("class=classified")); n != len(want)-1 {
		t.Fatalf("class=classified: %d", n)
	}
	// Future observations are classified the moment they are seen.
	e.report(t, web, obs.SourceConntrack, []obs.Flow{fl("out", "tcp", "10.1.20.15", "185.10.2.2", 443, "")})
	if got := kind(flows("host="+web), "web1 out tcp 185.10.2.2:443"); got != "user/rule/Example hosting" {
		t.Fatalf("a new connection: %s", got)
	}

	// Changes: no longer expected; deleted goes back to what it was.
	put("PUT", rules+"/"+nfs.ID, map[string]any{"label": "NFS media storage", "direction": "out", "protocol": "tcp", "port": 2049, "remote_group_id": storage.ID}, 200)
	if got := kind(flows(""), "web1 out tcp 10.1.20.40:2049"); got != "user/rule/NFS media storage" {
		t.Fatalf("after update: %s", got)
	}
	put("DELETE", rules+"/"+hosting.ID, nil, 204)
	if got := kind(flows(""), "web1 out tcp 185.10.1.1:8812"); got != "unknown/none/Unknown" {
		t.Fatalf("after delete: %s", got)
	}
	put("PUT", rules+"/nrl_missing", map[string]any{"label": "x", "port": 1}, 404)
	put("DELETE", rules+"/nrl_missing", nil, 404)

	// Refused: nothing to match on, two hosts sides, two remote sides, bad
	// values, things that don't exist.
	for _, bad := range []map[string]any{
		{"label": "everything web1 does", "host_id": web},
		{"label": "", "port": 22},
		{"label": strings.Repeat("x", 121), "port": 22},
		{"label": "x", "port": 22, "host_id": web, "host_group_id": storage.ID},
		{"label": "x", "remote_cidr": "10.0.0.0/8", "remote_host_id": nas},
		{"label": "x", "remote_cidr": "not-an-address"},
		{"label": "x", "port": 70000},
		{"label": "x", "port": 22, "direction": "sideways"},
		{"label": "x", "port": 22, "protocol": "icmp"},
		{"label": "x", "port": 22, "host_id": "host_missing"},
		{"label": "x", "remote_group_id": "grp_missing"},
	} {
		put("POST", rules, bad, 422)
	}

	// Who may: operators classify, auditors only look, viewers see no
	// network data at all.
	e.h.mkUser("op", auth.RoleOperator)
	e.h.mkUser("aud", auth.RoleAuditor)
	e.h.mkUser("view", auth.RoleViewer)
	if r := e.h.login("op").do("POST", rules, map[string]any{"label": "Printer", "port": 9100}); r.status != 201 {
		t.Fatalf("operator: %d %s", r.status, r.body)
	}
	aud := e.h.login("aud")
	if r := aud.do("GET", rules, nil); r.status != 200 {
		t.Fatalf("auditor list: %d", r.status)
	}
	if r := aud.do("POST", rules, map[string]any{"label": "x", "port": 1}); r.status != 403 {
		t.Fatalf("auditor create: %d", r.status)
	}
	if r := e.h.login("view").do("GET", rules, nil); r.status != 403 {
		t.Fatalf("viewer list: %d", r.status)
	}

	// Audited, with what it matched.
	assertAuditLike(t, e.h, "network.classification.create", `"label":"NFS media storage"`, `"matches":"any host → pack storage, TCP 2049"`, `"expected":true`)
	assertAuditLike(t, e.h, "network.classification.update", `"expected":false`, `"before":`)
	assertAuditLike(t, e.h, "network.classification.delete", `"label":"Example hosting"`)

	// A Pack a classification names can't be deleted out from under it.
	r := e.c.do("DELETE", "/api/v1/groups/"+storage.ID, nil)
	if r.status != 409 || !strings.Contains(string(r.body), "network_rule") || !strings.Contains(string(r.body), "NFS media storage") {
		t.Fatalf("pack delete: %d %s", r.status, r.body)
	}
	// Removing a host takes its own classifications along.
	if r := e.c.do("DELETE", "/api/v1/hosts/"+db1, nil); r.status != 204 {
		t.Fatalf("remove db1: %d %s", r.status, r.body)
	}
	var list struct{ Items []rule }
	e.c.do("GET", rules, nil).json(t, &list)
	for _, x := range list.Items {
		if x.Label == "Ops tooling on db1" {
			t.Fatal("a removed host's classification stayed")
		}
	}

	// Labels only: the firewall policy never changed.
	var fwAfter struct{ Revision string }
	e.c.do("GET", "/api/v1/firewall/document", nil).json(t, &fwAfter)
	if fwAfter.Revision != fwBefore.Revision {
		t.Fatal("classifying traffic changed the firewall policy")
	}
}
