package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"drool/internal/agent"
	"drool/internal/discovery"
	obs "drool/internal/firewall/observe"
	"drool/internal/netmon/collect"
	"drool/internal/netmon/geoip/mmdbtest"
)

// GeoIP/ASN fixtures: a country and an ASN database in MaxMind's layout.
func writeGeoDBs(t *testing.T, dir string) {
	t.Helper()
	c, err := mmdbtest.Build(mmdbtest.Options{DatabaseType: "GeoLite2-Country", BuildEpoch: 1790000000}, []mmdbtest.Record{
		{Prefix: "142.250.0.0/15", Data: mmdbtest.Country("US", "United States")},
		{Prefix: "185.10.0.0/16", Data: mmdbtest.Country("NL", "Netherlands")},
		{Prefix: "2a01:4f8::/32", Data: mmdbtest.Country("DE", "Germany")},
		{Prefix: "203.0.113.0/24", Data: mmdbtest.Country("AU", "Australia")}, // documentation space: must never be looked up
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := mmdbtest.Build(mmdbtest.Options{DatabaseType: "GeoLite2-ASN", BuildEpoch: 1790000000}, []mmdbtest.Record{
		{Prefix: "142.250.0.0/15", Data: mmdbtest.ASN(15169, "Google LLC")},
		{Prefix: "185.10.0.0/16", Data: mmdbtest.ASN(64512, "Example Hosting B.V.")},
		{Prefix: "2a01:4f8::/32", Data: mmdbtest.ASN(24940, "Hetzner Online GmbH")},
	})
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "GeoLite2-Country.mmdb"), c, 0o644)
	os.WriteFile(filepath.Join(dir, "GeoLite2-ASN.mmdb"), a, 0o644)
}

type netFlow struct {
	ID         int64  `json:"id"`
	Direction  string `json:"direction"`
	Protocol   string `json:"protocol"`
	LocalIP    string `json:"local_ip"`
	LocalPort  int    `json:"local_port"`
	RemoteIP   string `json:"remote_ip"`
	RemotePort int    `json:"remote_port"`
	Port       int    `json:"port"`
	Service    string `json:"service"`
	Process    string `json:"process"`
	Source     string `json:"source"`
	Count      int64  `json:"count"`
	FirstSeen  string `json:"first_seen"`
	LastSeen   string `json:"last_seen"`
	Scope      string `json:"scope"`
	ScopeRange string `json:"scope_range"`
	PeerNet    string `json:"peer_network"`
	Host       struct {
		ID, Name string
		Groups   []string
	} `json:"host"`
	PeerHost *struct {
		ID, Name string
		Groups   []string
	} `json:"peer_host"`
	Geo *struct {
		CountryCode string `json:"country_code"`
		Country     string `json:"country"`
		ASN         uint32 `json:"asn"`
		Org         string `json:"org"`
	} `json:"geo"`
	Policy struct {
		Status         string   `json:"status"`
		WouldDeny      bool     `json:"would_deny"`
		PolicyName     string   `json:"policy_name"`
		Reason         string   `json:"reason"`
		Enforcement    string   `json:"enforcement"`
		AppliedVerdict string   `json:"applied_verdict"`
		Notes          []string `json:"notes"`
	} `json:"policy"`
}

type netPage struct {
	Items      []netFlow `json:"items"`
	NextCursor string    `json:"next_cursor"`
	Total      *int64    `json:"total"`
	Truncated  bool      `json:"truncated"`
}

func (e *fwEnv) flows(t *testing.T, query string) netPage {
	t.Helper()
	r := e.c.do("GET", "/api/v1/network/flows?"+query, nil)
	if r.status != 200 {
		t.Fatalf("flows?%s: %d %s", query, r.status, r.body)
	}
	var p netPage
	r.json(t, &p)
	return p
}

func (e *fwEnv) report(t *testing.T, hostID, source string, fl []obs.Flow) {
	t.Helper()
	b, _ := json.Marshal(obs.Report{Source: source, Every: 30, Flows: fl})
	e.h.fw.OnTelemetry(hostID, "netflows", b)
}

func keyOf(f netFlow) string {
	return fmt.Sprintf("%s %s %s:%d %s:%d", f.Host.Name, f.Direction, f.LocalIP, f.LocalPort, f.RemoteIP, f.RemotePort)
}

func byKey(p netPage) map[string]netFlow {
	m := map[string]netFlow{}
	for _, f := range p.Items {
		m[keyOf(f)] = f
	}
	return m
}

func TestNetworkMonitoring(t *testing.T) {
	e := newFwEnv(t)
	ctx := context.Background()
	writeGeoDBs(t, e.h.geoDir)
	e.h.net.Enricher().Refresh()

	web, sw := e.sshFwHost(t, "web1")
	db1, _ := e.sshFwHost(t, "db1")
	_, pw := sw.Host()
	e.h.st.W.UpsertHostAddress(ctx, upsertAddr(web, "10.1.20.15"))
	e.h.st.W.UpsertHostAddress(ctx, upsertAddr(db1, "10.1.20.30"))
	var g struct{ ID string }
	e.c.do("POST", "/api/v1/groups", map[string]any{"name": "database", "host_ids": []string{db1}}).json(t, &g)
	// How the controller reaches web1 (the management path).
	observe(t, e, web, "ssh_in", "192.0.2.10", pw)
	// Listening ports from inventory.
	if err := e.h.srv.d.Inventory.StoreFactsTx(ctx, e.h.st.W, web, &discovery.Facts{Ports: []discovery.Port{
		{Proto: "tcp", Address: "0.0.0.0", Port: 22, Process: "sshd"}, {Proto: "tcp", Address: "127.0.0.1", Port: 5432},
		{Proto: "udp", Address: "0.0.0.0", Port: 68}}}); err != nil {
		t.Fatal(err)
	}
	// The policy: web1 may reach the internet over HTTPS and db1's
	// postgres; nothing else is said about web1.
	doc := `networks:
  admin-lan:
    cidr: 192.168.1.0/24
policy:
  - name: web-out
    from: web1
    to: internet
    allow: [https]
  - name: web-db
    from: web1
    to: db1
    allow: [postgres]
`
	var cur struct{ Revision string }
	e.c.do("GET", "/api/v1/firewall/document", nil).json(t, &cur)
	e.saveYAML(t, doc, cur.Revision)

	now := time.Now().Unix()
	flows := []obs.Flow{
		{Direction: "out", Protocol: "tcp", LocalIP: "10.1.20.15", LocalPort: 49322, Peer: "142.250.1.1", Port: 443, RemotePort: 443, Process: "curl", PID: 4242, Count: 6, FirstSeen: now - 120, LastSeen: now - 60},
		{Direction: "out", Protocol: "tcp", LocalIP: "10.1.20.15", LocalPort: 41292, Peer: "185.10.1.1", Port: 8812, RemotePort: 8812, Count: 1, FirstSeen: now - 50, LastSeen: now - 50},
		{Direction: "out", Protocol: "tcp", LocalIP: "10.1.20.15", LocalPort: 40001, Peer: "10.1.20.30", Port: 5432, RemotePort: 5432, Process: "app", Count: 3, FirstSeen: now - 300, LastSeen: now - 30},
		{Direction: "in", Protocol: "tcp", LocalIP: "10.1.20.15", LocalPort: 22, Peer: "192.168.1.25", Port: 22, RemotePort: 51234, Process: "sshd", Count: 2, FirstSeen: now - 400, LastSeen: now - 20},
		{Direction: "in", Protocol: "tcp", LocalIP: "10.1.20.15", LocalPort: pw, Peer: "192.0.2.10", Port: pw, RemotePort: 60000, Process: "sshd", Count: 9, FirstSeen: now - 500, LastSeen: now - 5},
		{Direction: "out", Protocol: "udp", LocalIP: "2001:db8:20::15", LocalPort: 5353, Peer: "2a01:4f8:1::53", Port: 53, RemotePort: 53, Process: "systemd-resolve", Count: 4, FirstSeen: now - 90, LastSeen: now - 10},
		{Direction: "out", Protocol: "tcp", LocalIP: "10.1.20.15", LocalPort: 40500, Peer: "fd12::5", Port: 443, RemotePort: 443, Count: 1, FirstSeen: now - 90, LastSeen: now - 90},
		{Direction: "out", Protocol: "tcp", LocalIP: "10.1.20.15", LocalPort: 40600, Peer: "203.0.113.9", Port: 443, RemotePort: 443, Count: 1, FirstSeen: now - 90, LastSeen: now - 90},
		// Rejected before storage: impossible or malformed.
		{Direction: "out", Protocol: "icmp", Peer: "8.8.8.8", Port: 1, Count: 1, FirstSeen: now, LastSeen: now},
		{Direction: "out", Protocol: "tcp", Peer: "8.8.8.8", Port: 0, Count: 1, FirstSeen: now, LastSeen: now},
		{Direction: "out", Protocol: "tcp", Peer: "8.8.8.8", Port: 70000, Count: 1, FirstSeen: now, LastSeen: now},
		{Direction: "out", Protocol: "tcp", Peer: "not-an-ip", Port: 443, Count: 1, FirstSeen: now, LastSeen: now},
		{Direction: "out", Protocol: "tcp", Peer: "127.0.0.1", Port: 443, Count: 1, FirstSeen: now, LastSeen: now},
		{Direction: "sideways", Protocol: "tcp", Peer: "8.8.8.8", Port: 443, Count: 1, FirstSeen: now, LastSeen: now},
		{Direction: "out", Protocol: "tcp", Peer: "8.8.8.8", Port: 443, Count: 1, FirstSeen: now + 86400, LastSeen: now + 86400},
		{Direction: "out", Protocol: "tcp", Peer: "8.8.8.8", Port: 443, LocalIP: "zzz", Count: 1, FirstSeen: now, LastSeen: now},
	}
	e.report(t, web, obs.SourceConntrack, flows)
	// The database sees the same connections from its end, and talked to
	// the hosting provider three days ago (so that address is new for web1,
	// not for the fleet).
	e.report(t, db1, obs.SourceSockets, []obs.Flow{
		{Direction: "in", Protocol: "tcp", LocalIP: "10.1.20.30", LocalPort: 5432, Peer: "10.1.20.15", Port: 5432, RemotePort: 40001, Process: "postgres", Count: 3, FirstSeen: now - 290, LastSeen: now - 30},
		{Direction: "out", Protocol: "tcp", LocalIP: "10.1.20.30", LocalPort: 40002, Peer: "185.10.1.1", Port: 443, RemotePort: 443, Count: 1, FirstSeen: now - 3*86400, LastSeen: now - 3*86400},
	})

	all := e.flows(t, "host="+web)
	if all.Total == nil || *all.Total != 8 || len(all.Items) != 8 {
		t.Fatalf("web1 flows: total %v, %d items", all.Total, len(all.Items))
	}
	m := byKey(all)
	curl := m["web1 out 10.1.20.15:49322 142.250.1.1:443"]
	if curl.Process != "curl" || curl.Count != 6 || curl.Scope != "public" || curl.Service != "https" || curl.Source != "conntrack" {
		t.Fatalf("curl flow: %+v", curl)
	}
	if curl.Geo == nil || curl.Geo.Country != "United States" || curl.Geo.ASN != 15169 || curl.Geo.Org != "Google LLC" {
		t.Fatalf("curl geo: %+v", curl.Geo)
	}
	if curl.Policy.Status != "covered" || curl.Policy.PolicyName != "web-out" {
		t.Fatalf("curl policy: %+v", curl.Policy)
	}
	hosting := m["web1 out 10.1.20.15:41292 185.10.1.1:8812"]
	if hosting.Process != "" || hosting.Policy.Status != "unclassified" || hosting.Geo == nil || hosting.Geo.CountryCode != "NL" {
		t.Fatalf("unknown process flow: %+v", hosting)
	}
	// GeoIP is context only: nothing calls a Dutch hosting provider
	// suspicious.
	if strings.Contains(strings.ToLower(fmt.Sprint(hosting.Policy)), "suspicious") {
		t.Fatal("country-based verdict")
	}
	pg := m["web1 out 10.1.20.15:40001 10.1.20.30:5432"]
	if pg.Scope != "internal" || pg.Geo != nil || pg.PeerHost == nil || pg.PeerHost.Name != "db1" || strings.Join(pg.PeerHost.Groups, ",") != "database" || pg.Policy.Status != "covered" {
		t.Fatalf("internal flow: %+v", pg)
	}
	sshIn := m["web1 in 10.1.20.15:22 192.168.1.25:51234"]
	if sshIn.PeerNet != "admin-lan" || sshIn.Scope != "internal" || sshIn.ScopeRange != "192.168.0.0/16" || sshIn.Policy.Status != "unclassified" || !sshIn.Policy.WouldDeny {
		t.Fatalf("inbound ssh: %+v", sshIn)
	}
	if sshIn.Policy.Enforcement != "unmanaged" || len(sshIn.Policy.Notes) == 0 {
		t.Fatalf("enforcement note: %+v", sshIn.Policy)
	}
	mgmt := m[fmt.Sprintf("web1 in 10.1.20.15:%d 192.0.2.10:60000", pw)]
	if mgmt.Policy.Status != "management" || mgmt.Scope != "special" || mgmt.Geo != nil {
		t.Fatalf("management flow: %+v", mgmt)
	}
	dns6 := m["web1 out 2001:db8:20::15:5353 2a01:4f8:1::53:53"]
	if dns6.Geo == nil || dns6.Geo.ASN != 24940 || dns6.Geo.CountryCode != "DE" || dns6.Protocol != "udp" {
		t.Fatalf("IPv6: %+v", dns6)
	}
	if ula := m["web1 out 10.1.20.15:40500 fd12::5:443"]; ula.Scope != "internal" || ula.Geo != nil {
		t.Fatalf("ULA: %+v", ula)
	}
	// Documentation space is in the test database but never looked up.
	if doc := m["web1 out 10.1.20.15:40600 203.0.113.9:443"]; doc.Scope != "special" || doc.ScopeRange != "documentation" || doc.Geo != nil {
		t.Fatalf("documentation address: %+v", doc)
	}

	// Duplicate reports aggregate: one row, first_seen stable, last_seen
	// advancing, counts adding up.
	e.report(t, web, obs.SourceConntrack, []obs.Flow{
		{Direction: "out", Protocol: "tcp", LocalIP: "10.1.20.15", LocalPort: 49999, Peer: "142.250.1.1", Port: 443, RemotePort: 443, Process: "curl", Count: 2, FirstSeen: now - 5, LastSeen: now - 1},
	})
	p := e.flows(t, "host="+web+"&peer=142.250.1.1")
	if len(p.Items) != 1 || p.Items[0].Count != 8 || p.Items[0].FirstSeen != curl.FirstSeen || p.Items[0].LastSeen <= curl.LastSeen || p.Items[0].LocalPort != 49999 {
		t.Fatalf("aggregation: %+v", p.Items)
	}

	// Filters (server side).
	for q, want := range map[string]int{
		"host=" + web + "&scope=external":                3, // google, hosting, hetzner
		"host=" + web + "&scope=internal":                3,
		"host=" + web + "&scope=special":                 2,
		"country=US":                                     1,
		"country=nl&host=" + web:                         1,
		"asn=AS15169":                                    1,
		"org=hosting&host=" + web:                        1,
		"port=22":                                        1,
		"local_port=22":                                  1,
		"remote_port=443&host=" + web:                    3,
		"process=curl":                                   1,
		"process=unknown&host=" + web:                    3,
		"policy=unclassified&host=" + web:                4,
		"policy=covered&host=" + web:                     3, // incl. 203.0.113.9: "internet" is everything outside internal_ranges
		"policy=management":                              1,
		"policy=denied&host=" + web:                      1, // the inbound SSH: would be denied once enforced
		"group=" + g.ID:                                  2,
		"direction=in":                                   3,
		"protocol=udp":                                   1,
		"peer=2a01:4f8::/32":                             1,
		"q=google":                                       1,
		"q=db1":                                          3,
		"service=postgres":                               2,
		"source=sockets":                                 2,
		"first_seen_after=200s&host=" + web:              5,
		"last_seen_after=" + fmt.Sprint(now-25):          4,
		"new=host&since=1h&host=" + web:                  3, // public destinations web1 first saw in the last hour
		"new=fleet&since=1h":                             2, // db1 saw 185.10.1.1 three days ago
		"scope=external&host=" + web + "&policy=covered": 1,
	} {
		got := e.flows(t, q)
		if len(got.Items) != want {
			t.Errorf("%s: %d flows, want %d: %v", q, len(got.Items), want, func() []string {
				var s []string
				for _, f := range got.Items {
					s = append(s, keyOf(f))
				}
				return s
			}())
		}
	}
	for _, bad := range []string{"direction=up", "scope=moon", "protocol=icmp", "port=70000", "policy=evil", "peer=nope", "limit=x", "cursor=bad!", "first_seen_after=yesterday", "country=USA", "new=galaxy"} {
		if r := e.c.do("GET", "/api/v1/network/flows?"+bad, nil); r.status != 422 && r.status != 400 {
			t.Errorf("%s accepted: %d", bad, r.status)
		}
	}

	// Pagination: database filters (with a total) and filters that need
	// the classifier (without), cursors walk every row exactly once.
	for _, q := range []string{"host=" + web, "host=" + web + "&policy=unclassified", "sort=first_seen", "sort=count"} {
		seen := map[int64]bool{}
		cursor := ""
		pages := 0
		for {
			p := e.flows(t, q+"&limit=2&cursor="+url.QueryEscape(cursor))
			pages++
			for _, f := range p.Items {
				if seen[f.ID] {
					t.Fatalf("%s: flow %d twice", q, f.ID)
				}
				seen[f.ID] = true
			}
			if p.NextCursor == "" {
				break
			}
			cursor = p.NextCursor
			if pages > 20 {
				t.Fatal("pagination doesn't end")
			}
		}
		want := len(e.flows(t, q+"&limit=500").Items)
		if len(seen) != want {
			t.Fatalf("%s: paged %d flows, want %d", q, len(seen), want)
		}
	}

	// Details: an unclassified flow offers a draft rule for the designer,
	// which the designer's own check accepts (a draft, never saved here).
	var det struct {
		netFlow
		Collection struct{ Label, Source, Mode string }
		Firewall   struct{ Enforcement string }
		Suggestion *struct {
			Title      string
			Rule       map[string]any `json:"rule"`
			NewService map[string]any `json:"new_service"`
		}
		Related        []netFlow `json:"related"`
		FleetFirstSeen string    `json:"fleet_first_seen"`
	}
	e.c.do("GET", fmt.Sprintf("/api/v1/network/flows/%d", hosting.ID), nil).json(t, &det)
	if det.Suggestion == nil || det.Suggestion.NewService == nil || det.Collection.Label != "Snapshot only: SSH" || det.FleetFirstSeen == "" {
		t.Fatalf("detail: %+v", det)
	}
	if fmt.Sprint(det.Suggestion.Rule["from"]) != "[host:web1]" || fmt.Sprint(det.Suggestion.Rule["to"]) != "[cidr:185.10.1.1/32]" || fmt.Sprint(det.Suggestion.Rule["services"]) != "[tcp-8812]" {
		t.Fatalf("suggestion: %+v", det.Suggestion)
	}
	var stored struct {
		Document map[string]any `json:"document"`
	}
	e.c.do("GET", "/api/v1/firewall/document", nil).json(t, &stored)
	draft := stored.Document
	draft["policy"] = append(draft["policy"].([]any), det.Suggestion.Rule)
	draft["services"] = append(draft["services"].([]any), det.Suggestion.NewService)
	if c := e.check(t, map[string]any{"document": draft}); !c.Valid || len(c.Problems) > 0 {
		t.Fatalf("suggested draft: %+v", c.Problems)
	}
	var mdet struct {
		Suggestion any `json:"suggestion"`
	}
	e.c.do("GET", fmt.Sprintf("/api/v1/network/flows/%d", mgmt.ID), nil).json(t, &mdet)
	if mdet.Suggestion != nil {
		t.Fatal("a suggestion for a management flow")
	}
	if r := e.c.do("GET", "/api/v1/network/flows/999999", nil); r.status != 404 {
		t.Fatalf("missing flow: %d", r.status)
	}

	// Stale policy: web1 enforces an older policy that still allows
	// 8812; the desired policy now denies it explicitly.
	e.setHost(t, web, map[string]any{"mode": "enforce"})
	plan, err := e.h.fw.Plan(ctx, web, false)
	if err != nil {
		t.Fatal(err)
	}
	old, _ := json.Marshal(plan.Policy)
	e.h.st.Writer.Exec(`UPDATE fw_host_state SET enforced = 1, applied_rules_hash = 'old', applied_policy_json = ? WHERE host_id = ?`, string(old), web)
	e.c.do("GET", "/api/v1/firewall/document", nil).json(t, &cur)
	e.saveYAML(t, doc+`  - name: no-8812
    from: web1
    to: cidr:185.10.0.0/16
    deny: [tcp-8812]
services:
  tcp-8812:
    protocol: tcp
    port: 8812
`, cur.Revision)
	p = e.flows(t, "host="+web+"&peer=185.10.1.1")
	// The status is the saved policy's; the host still allows it, so it
	// isn't presented as in force (source review before v1.0.0-rc.3).
	if f := p.Items[0]; f.Policy.Status != "denied" || f.Policy.PolicyName != "no-8812" || f.Policy.AppliedVerdict != "accept" || f.Policy.Enforcement != "changes_pending" ||
		!strings.Contains(strings.Join(f.Policy.Notes, " "), "not applied yet") {
		t.Fatalf("stale: %+v", f.Policy)
	}
	// On an enforced host the default drop is a denial, not "unclassified".
	if f := e.flows(t, "host="+web+"&port=22").Items[0]; f.Policy.Status != "denied" {
		t.Fatalf("default drop on an enforced host: %+v", f.Policy)
	}
	// Leaving Enforce keeps the committed revision on the host: it still
	// filters there, and the views say so rather than "not enforced".
	e.setHost(t, web, map[string]any{"mode": "monitor"})
	if f := e.flows(t, "host="+web+"&port=22").Items[0]; f.Policy.Enforcement != "enforced" || f.Policy.Status != "denied" ||
		!strings.Contains(strings.Join(f.Policy.Notes, " "), "monitor mode, but DROOL's last applied policy") {
		t.Fatalf("monitor mode with a committed policy: %+v", f.Policy)
	}
	var sim struct {
		Hops []struct {
			Filtered bool     `json:"filtered"`
			Notes    []string `json:"notes"`
		} `json:"hops"`
	}
	if err := e.h.st.W.UpsertHostAddress(ctx, upsertAddr(web, "10.1.20.15")); err != nil {
		t.Fatal(err)
	}
	r := e.c.do("POST", "/api/v1/firewall/simulate", map[string]any{"from_host": web, "to_address": "185.10.1.1", "protocol": "tcp", "port": 8812})
	if r.status != 200 {
		t.Fatalf("simulate: %d %s", r.status, r.body)
	}
	r.json(t, &sim)
	if len(sim.Hops) != 1 || !sim.Hops[0].Filtered || !strings.Contains(strings.Join(sim.Hops[0].Notes, " "), "last applied policy (revision") {
		t.Fatalf("simulation on a host out of Enforce, its policy still committed: %+v", sim)
	}
	// DROOL's rules missing on the host: nothing of its policy is in force,
	// so the default drop is what it would do, not a denial.
	e.h.st.Writer.Exec(`UPDATE fw_host_state SET drift = 'missing' WHERE host_id = ?`, web)
	if f := e.flows(t, "host="+web+"&port=22").Items[0]; f.Policy.Enforcement == "enforced" || f.Policy.Status != "unclassified" || !f.Policy.WouldDeny ||
		!strings.Contains(strings.Join(f.Policy.Notes, " "), "rules are missing") {
		t.Fatalf("rules missing on the host: %+v", f.Policy)
	}
	e.h.st.Writer.Exec(`UPDATE fw_host_state SET drift = '' WHERE host_id = ?`, web)
	e.setHost(t, web, map[string]any{"mode": "enforce"})

	// Host summary: listening ports from inventory, with observed use.
	var hn struct {
		Collection struct{ Mode, Label, Detail string }
		Listening  []struct {
			Port     int
			Protocol string
			Process  string
			Exposure string
			Flows    int64
			Peers    int64
		}
		Counts struct {
			Flows, Inbound, Outbound, Internal, External, Unclassified, Denied int64
			NewExternal                                                        int64 `json:"new_external_24h"`
		}
		Enrichment bool
	}
	e.c.do("GET", "/api/v1/hosts/"+web+"/network", nil).json(t, &hn)
	if hn.Collection.Mode != "snapshot" || !strings.Contains(hn.Collection.Detail, "never continuously") || len(hn.Listening) != 3 || !hn.Enrichment {
		t.Fatalf("host network: %+v", hn)
	}
	if l := hn.Listening[0]; l.Port != 22 || l.Process != "sshd" || l.Flows != 1 || l.Exposure != "all addresses" {
		t.Fatalf("listening 22: %+v", l)
	}
	if l := hn.Listening[2]; l.Port != 5432 || l.Process != "" || !strings.Contains(l.Exposure, "loopback") {
		t.Fatalf("listening 5432: %+v", l)
	}
	if hn.Counts.Flows != 8 || hn.Counts.Inbound != 2 || hn.Counts.External != 3 || hn.Counts.NewExternal != 3 {
		t.Fatalf("counts: %+v", hn.Counts)
	}

	// Facets for the filters.
	var fc struct {
		Countries, Networks, Processes, Groups []struct {
			Value, Label string
			Count        int64
		}
	}
	e.c.do("GET", "/api/v1/network/filters", nil).json(t, &fc)
	if len(fc.Countries) != 3 || len(fc.Networks) != 3 || len(fc.Groups) != 1 {
		t.Fatalf("facets: %+v", fc)
	}

	// Topology: grouped by network owner, both ends of web1 → db1 counted
	// once, management attached to the controller.
	var topo struct {
		Nodes []struct{ Key, Kind, Label, Detail string }
		Edges []struct {
			ID, Source, Target, Worst string
			Count                     int64
			Flows                     int
		}
		Collapsed int
	}
	e.c.do("GET", "/api/v1/network/topology", nil).json(t, &topo)
	edges := map[string]int64{}
	worst := map[string]string{}
	for _, ed := range topo.Edges {
		edges[ed.Source+"→"+ed.Target] = ed.Count
		worst[ed.Source+"→"+ed.Target] = ed.Worst
	}
	if edges["host:web1→asn:15169"] != 8 || worst["host:web1→asn:15169"] != "covered" {
		t.Fatalf("google edge: %+v", topo.Edges)
	}
	if edges["host:web1→host:db1"] != 3 {
		t.Fatalf("web1→db1 counted twice: %+v", topo.Edges)
	}
	if worst["host:web1→asn:64512"] != "denied" || edges["controller→host:web1"] != 9 || edges["network:admin-lan→host:web1"] != 2 {
		t.Fatalf("edges: %+v", topo.Edges)
	}
	labels := map[string]string{}
	for _, n := range topo.Nodes {
		labels[n.Key] = n.Label
	}
	if labels["asn:15169"] != "Google LLC" || labels["asn:64512"] != "Example Hosting B.V." {
		t.Fatalf("nodes: %+v", topo.Nodes)
	}
	e.c.do("GET", "/api/v1/network/topology?expand=asn:15169&max_external=2", nil).json(t, &topo)
	keys := map[string]bool{}
	for _, n := range topo.Nodes {
		keys[n.Key] = true
	}
	if !keys["ip:142.250.1.1"] || keys["asn:15169"] || !keys["asn:other"] || keys["asn:24940"] || topo.Collapsed != 1 {
		t.Fatalf("expanded/collapsed: %+v collapsed=%d", keys, topo.Collapsed)
	}

	// Enrichment off: no lookups at all; private addresses are never
	// looked up even when on.
	before := e.h.net.Enricher().Status().Cache
	e.flows(t, "host="+web+"&scope=internal")
	if after := e.h.net.Enricher().Status().Cache; after.Misses != before.Misses || after.Hits != before.Hits {
		t.Fatalf("internal addresses looked up: %+v → %+v", before, after)
	}
	set := map[string]any{"collection": "all", "retention_days": 30, "max_flows_per_host": 5000, "enrichment": false}
	if r := e.c.do("PUT", "/api/v1/network/settings", set); r.status != 200 {
		t.Fatalf("settings: %d %s", r.status, r.body)
	}
	if f := e.flows(t, "peer=142.250.1.1").Items[0]; f.Geo != nil {
		t.Fatalf("enrichment off but geo: %+v", f.Geo)
	}
	set["enrichment"] = true
	e.c.do("PUT", "/api/v1/network/settings", set)

	// internal_ranges is the one definition of internal: change it and
	// the scopes follow.
	if r := e.c.do("PUT", "/api/v1/firewall/settings", map[string]any{"internal_ranges": []string{"10.0.0.0/8", "fc00::/7", "185.10.0.0/16"}, "default_timeout_s": 120}); r.status != 200 {
		t.Fatalf("ranges: %d %s", r.status, r.body)
	}
	eventually(t, 5*time.Second, "rescope", func() bool {
		f := e.flows(t, "peer=185.10.1.1").Items[0]
		s := e.flows(t, "peer=192.168.1.25").Items[0]
		return f.Scope == "internal" && f.Geo == nil && s.Scope == "special" && s.ScopeRange == "private (RFC 1918)"
	})
}

func TestNetworkStorageBounds(t *testing.T) {
	e := newFwEnv(t)
	ctx := context.Background()
	h, _ := e.sshFwHost(t, "busy")
	now := time.Now().Unix()

	// Concurrent reports of the same flow: one row, every connection
	// counted.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				e.report(t, h, obs.SourceSockets, []obs.Flow{{Direction: "out", Protocol: "tcp", LocalIP: "10.0.0.1", LocalPort: 40000 + j, Peer: "8.8.8.8", Port: 53, Count: 1, FirstSeen: now - 10, LastSeen: now}})
			}
		}()
	}
	wg.Wait()
	p := e.flows(t, "host="+h)
	if len(p.Items) != 1 || p.Items[0].Count != 200 {
		t.Fatalf("concurrent: %+v", p.Items)
	}

	// Retention and the per-host cap, pruned in batches.
	var old []obs.Flow
	for i := 0; i < 50; i++ {
		old = append(old, obs.Flow{Direction: "out", Protocol: "tcp", LocalIP: "10.0.0.1", Peer: fmt.Sprintf("8.8.%d.%d", i/250, i%250+1), Port: 443, Count: 1,
			FirstSeen: now - 40*86400, LastSeen: now - 40*86400})
	}
	var recent []obs.Flow
	for i := 0; i < 150; i++ {
		recent = append(recent, obs.Flow{Direction: "out", Protocol: "tcp", LocalIP: "10.0.0.1", Peer: fmt.Sprintf("9.9.%d.%d", i/250, i%250+1), Port: 443, Count: 1,
			FirstSeen: now - int64(i), LastSeen: now - int64(i)})
	}
	e.report(t, h, obs.SourceSockets, old)
	e.report(t, h, obs.SourceSockets, recent)
	if r := e.c.do("PUT", "/api/v1/network/settings", map[string]any{"collection": "all", "retention_days": 30, "max_flows_per_host": 100, "enrichment": true}); r.status != 200 {
		t.Fatalf("settings: %d %s", r.status, r.body)
	}
	res := e.h.net.Prune(ctx)
	// 150 recent rows plus the 8.8.8.8 flow: the 51 least recently seen go.
	if res.Expired != 50 || res.Trimmed != 51 || res.Rows != 100 {
		t.Fatalf("prune: %+v", res)
	}
	// The newest rows survive.
	p = e.flows(t, "host="+h+"&limit=500")
	for _, f := range p.Items {
		if strings.HasPrefix(f.RemoteIP, "8.8.0.") || f.RemoteIP >= "9.9.0.100" && strings.HasPrefix(f.RemoteIP, "9.9.0.1") && len(f.RemoteIP) == 9 {
			t.Fatalf("old or least recent row kept: %+v", f)
		}
	}
	if len(p.Items) != 100 || p.Items[0].RemoteIP != "9.9.0.1" {
		t.Fatalf("after prune: %d, first %s", len(p.Items), p.Items[0].RemoteIP)
	}
	// Settings are validated and audited.
	if r := e.c.do("PUT", "/api/v1/network/settings", map[string]any{"collection": "maybe", "retention_days": 0, "max_flows_per_host": 1}); r.status != 422 {
		t.Fatalf("bad settings: %d", r.status)
	}
	var audits struct {
		Items []struct{ Action string }
	}
	e.c.do("GET", "/api/v1/audit?action=network.settings.update", nil).json(t, &audits)
	if len(audits.Items) == 0 {
		t.Fatal("settings change not audited")
	}
	// An M8 agent's report (socket sampling, no processes) still lands.
	b, _ := json.Marshal([]obs.Flow{{Direction: "in", Protocol: "tcp", Port: 22, Peer: "10.9.9.9", Count: 1, FirstSeen: now, LastSeen: now}})
	e.h.fw.OnTelemetry(h, "flows", b)
	if p := e.flows(t, "host="+h+"&peer=10.9.9.9"); len(p.Items) != 1 || p.Items[0].Source != "sockets" {
		t.Fatalf("M8 report: %+v", p.Items)
	}
}

func TestNetworkPermissionsAndNoDatabase(t *testing.T) {
	e := newFwEnv(t)
	h, _ := e.sshFwHost(t, "solo")
	now := time.Now().Unix()
	e.report(t, h, obs.SourceSockets, []obs.Flow{{Direction: "out", Protocol: "tcp", LocalIP: "10.0.0.1", Peer: "142.250.1.1", Port: 443, Count: 1, FirstSeen: now, LastSeen: now}})
	// No GeoIP database: monitoring works, enrichment says how to enable it.
	p := e.flows(t, "")
	if len(p.Items) != 1 || p.Items[0].Geo != nil || p.Items[0].Scope != "public" {
		t.Fatalf("without databases: %+v", p.Items)
	}
	var st struct {
		Directory    string
		Country, ASN bool
		Files        []any
	}
	e.c.do("GET", "/api/v1/network/enrichment", nil).json(t, &st)
	if st.Country || st.ASN || st.Directory == "" || len(st.Files) != 0 {
		t.Fatalf("enrichment: %+v", st)
	}
	// A corrupt database file: still no failure.
	os.WriteFile(filepath.Join(e.h.geoDir, "broken.mmdb"), []byte("not a database"), 0o644)
	if r := e.c.do("POST", "/api/v1/network/enrichment/reload", nil); r.status != 200 || !strings.Contains(string(r.body), "broken.mmdb") {
		t.Fatalf("reload: %d %s", r.status, r.body)
	}
	if len(e.flows(t, "").Items) != 1 {
		t.Fatal("flows failed with a corrupt database")
	}
	// Permissions: viewers don't see observed connections; operators read
	// and snapshot; only admins change settings.
	for role, want := range map[string][3]int{"viewer": {403, 403, 403}, "auditor": {200, 403, 403}, "operator": {200, 403, 0}, "admin": {200, 200, 0}} {
		e.h.mkUser("n"+role, role)
		c := e.h.login("n" + role)
		if r := c.do("GET", "/api/v1/network/flows", nil); r.status != want[0] {
			t.Errorf("%s flows: %d", role, r.status)
		}
		if r := c.do("GET", "/api/v1/firewall/hosts/"+h+"/observations", nil); r.status != want[0] {
			t.Errorf("%s M8 observations: %d", role, r.status)
		}
		if r := c.do("PUT", "/api/v1/network/settings", map[string]any{"collection": "all", "retention_days": 30, "max_flows_per_host": 5000, "enrichment": true}); r.status != want[1] {
			t.Errorf("%s settings: %d", role, r.status)
		}
		if want[2] != 0 {
			if r := c.do("POST", "/api/v1/hosts/"+h+"/network/snapshot", nil); r.status != want[2] {
				t.Errorf("%s snapshot: %d", role, r.status)
			}
		}
	}
}

// An agent end to end: socket sampling (the test process can't read
// conntrack events), processes attributed, reported as continuous.
func TestNetworkAgentCollection(t *testing.T) {
	e := newFwEnv(t)
	ss := `tcp LISTEN 0 128 0.0.0.0:22 0.0.0.0:* users:(("sshd",pid=900,fd=3))
tcp ESTAB 0 0 10.20.0.9:22 10.20.0.50:51000 users:(("sshd",pid=1234,fd=4))
tcp ESTAB 0 0 10.20.0.9:44000 142.250.1.1:443 users:(("curl",pid=77,fd=5))
`
	hostID, _ := e.agentNetHost(t, "agent9", ss)
	eventually(t, 20*time.Second, "agent flows", func() bool {
		return len(e.flows(t, "host="+hostID).Items) == 2
	})
	var out, in netFlow
	for _, f := range e.flows(t, "host="+hostID).Items {
		if f.Direction == "out" {
			out = f
		} else {
			in = f
		}
	}
	if out.RemoteIP != "142.250.1.1" || out.LocalPort != 44000 || out.Process != "curl" || out.Source != "sockets" {
		t.Fatalf("agent outbound flow: %+v", out)
	}
	if in.RemoteIP != "10.20.0.50" || in.Port != 22 || in.Process != "sshd" {
		t.Fatalf("agent inbound flow: %+v", in)
	}
	var hn struct {
		Collection struct{ Mode, Source, Label, Detail string }
	}
	e.c.do("GET", "/api/v1/hosts/"+hostID+"/network", nil).json(t, &hn)
	if hn.Collection.Mode != "continuous" || hn.Collection.Source != "sockets" || !strings.HasPrefix(hn.Collection.Label, "Continuous: socket sampling") ||
		!strings.Contains(hn.Collection.Detail, "not seen") {
		t.Fatalf("collection: %+v", hn.Collection)
	}
	// Collection follows the network settings, not only the firewall mode.
	e.c.do("PUT", "/api/v1/network/settings", map[string]any{"collection": "off", "retention_days": 30, "max_flows_per_host": 5000, "enrichment": true})
	e.c.do("GET", "/api/v1/hosts/"+hostID+"/network", nil).json(t, &hn)
	if hn.Collection.Label != "Not collecting" {
		t.Fatalf("collection off: %+v", hn.Collection)
	}
}

func (e *fwEnv) agentNetHost(t *testing.T, name, ssOut string) (string, *runningAgent) {
	t.Helper()
	id, ra, _, _ := e.agentFwHost(t, name, fullCaps(""), func(o *agent.Options) {
		o.NetCollect = &collect.Options{Every: time.Second, SS: func(context.Context) (string, error) { return ssOut, nil },
			LocalAddrs: func() []netip.Addr { return []netip.Addr{netip.MustParseAddr("10.20.0.9")} }}
	})
	return id, ra
}

// tupleCT is a fake kernel: it reports a connection as tracked only when
// asked about the agent's real controller connection (both ends set, the
// remote being the controller's agent endpoint).
type tupleCT struct {
	mu    sync.Mutex
	asked []string
	port  int
}

func (c *tupleCT) Usable() string { return "" }
func (c *tupleCT) Open() (collect.Events, error) {
	return &idleEvents{done: make(chan struct{})}, nil
}
func (c *tupleCT) Tracked(proto uint8, src, dst netip.AddrPort) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asked = append(c.asked, src.String()+"→"+dst.String())
	return proto == 6 && src.IsValid() && src.Port() != 0 && dst.IsValid() && int(dst.Port()) == c.port, nil
}

type idleEvents struct {
	done chan struct{}
	once sync.Once
}

func (e *idleEvents) Read(func(uint8, netip.AddrPort, netip.AddrPort)) error { <-e.done; return nil }
func (e *idleEvents) Close() error                                           { e.once.Do(func() { close(e.done) }); return nil }
func (e *idleEvents) Lost() int64                                            { return 0 }

// The agent proves conntrack with its own controller connection: it must
// know that connection's two ends while the session is open.
func TestNetworkAgentConntrackTuple(t *testing.T) {
	e := newFwEnv(t)
	ct := &tupleCT{port: agentPortOf(e)}
	id, _, _, _ := e.agentFwHost(t, "agent8", fullCaps(""), func(o *agent.Options) {
		o.NetCollect = &collect.Options{Every: time.Second, Conntrack: ct, SS: func(context.Context) (string, error) { return "", nil }}
	})
	var hn struct {
		Collection struct{ Source, Label string }
	}
	eventually(t, 20*time.Second, "conntrack collection", func() bool {
		e.c.do("GET", "/api/v1/hosts/"+id+"/network", nil).json(t, &hn)
		return hn.Collection.Source == "conntrack"
	})
	if hn.Collection.Label != "Continuous: conntrack" {
		t.Fatalf("collection: %+v", hn.Collection)
	}
	ct.mu.Lock()
	defer ct.mu.Unlock()
	if len(ct.asked) == 0 {
		t.Fatal("the kernel was never asked")
	}
}

func agentPortOf(e *fwEnv) int {
	ap, _ := netip.ParseAddrPort(e.addr)
	return int(ap.Port())
}
