package compile_test

import (
	"net/netip"
	"strings"
	"testing"

	"drool/internal/firewall/compile"
	"drool/internal/firewall/nftables"
	"drool/internal/firewall/policy"
)

func addrs(ss ...string) []netip.Addr {
	var out []netip.Addr
	for _, s := range ss {
		out = append(out, netip.MustParseAddr(s))
	}
	return out
}

func world() *compile.World {
	internal, _ := policy.ParseRanges(policy.DefaultInternalRanges)
	return &compile.World{
		Hosts: map[string]compile.HostRef{
			"web01":  {ID: "web01", Name: "web01", Addrs: addrs("10.0.1.20", "fd00::20")},
			"web02":  {ID: "web02", Name: "web02", Addrs: addrs("10.0.1.21")},
			"db01":   {ID: "db01", Name: "db01", Addrs: addrs("10.0.2.10", "fd00::10")},
			"jump01": {ID: "jump01", Name: "jump01", Addrs: addrs("10.0.0.5")},
		},
		Groups: map[string]compile.GroupRef{
			"web":      {ID: "web", Name: "web", Members: []string{"web01", "web02"}},
			"database": {ID: "database", Name: "database", Members: []string{"db01"}},
			"jump":     {ID: "jump", Name: "jump", Members: []string{"jump01"}},
		},
		Services: policy.Services([]policy.Service{{Name: "jellyfin", Entries: []policy.Entry{{Protocol: "tcp", Ports: "8096"}}}}),
		Policies: []policy.Policy{
			{ID: "p1", Name: "web-database", Enabled: true, Action: policy.Allow,
				From: []policy.Selector{{Kind: policy.SelGroup, ID: "web"}}, To: []policy.Selector{{Kind: policy.SelGroup, ID: "database"}},
				Services: []string{"postgres"}},
			{ID: "p2", Name: "management", Enabled: true, Action: policy.Allow,
				From: []policy.Selector{{Kind: policy.SelGroup, ID: "jump"}}, To: []policy.Selector{{Kind: policy.SelGroup, ID: "web"}, {Kind: policy.SelGroup, ID: "database"}},
				Services: []string{"ssh"}},
			{ID: "p3", Name: "public-web", Enabled: true, Action: policy.Allow,
				From: []policy.Selector{{Kind: policy.SelInternet}}, To: []policy.Selector{{Kind: policy.SelGroup, ID: "web"}},
				Services: []string{"https"}},
			{ID: "p4", Name: "dns-for-all", Enabled: true, Action: policy.Allow,
				From: []policy.Selector{{Kind: policy.SelAny}}, To: []policy.Selector{{Kind: policy.SelCIDR, CIDR: "10.0.0.53/32"}},
				Services: []string{"dns"}},
			{ID: "p5", Name: "disabled", Enabled: false, Action: policy.Allow,
				From: []policy.Selector{{Kind: policy.SelAny}}, To: []policy.Selector{{Kind: policy.SelAny}}, Services: []string{"jellyfin"}},
		},
		Internal: internal,
	}
}

func pkt(proto string, port int, peer string) compile.Packet {
	a := netip.MustParseAddr(peer)
	fam := 6
	if a.Is4() {
		fam = 4
	}
	return compile.Packet{Family: fam, Proto: proto, Port: port, Peer: a}
}

var sshMgmt = compile.MgmtFlow{Dir: "in", Proto: "tcp", Port: 22, Peers: []netip.Prefix{netip.MustParsePrefix("10.0.2.2/32")},
	Reason: "SSH management from the controller (10.0.2.2)"}

func TestDatabaseInbound(t *testing.T) {
	hp := compile.Compile(compile.Input{World: world(), HostID: "db01", Outbound: compile.OutboundAllow, Mgmt: []compile.MgmtFlow{sshMgmt}})
	cases := []struct {
		p      compile.Packet
		want   string
		reason string
	}{
		{pkt("tcp", 5432, "10.0.1.20"), "accept", "web-database"},
		{pkt("tcp", 5432, "10.0.1.21"), "accept", "web-database"},
		{pkt("tcp", 5432, "fd00::20"), "accept", "web-database"},
		{pkt("tcp", 5432, "10.9.9.9"), "drop", ""},
		{pkt("tcp", 5433, "10.0.1.20"), "drop", ""},
		{pkt("tcp", 22, "10.0.0.5"), "accept", "management"},
		{pkt("tcp", 22, "10.0.2.2"), "accept", ""}, // management path
		{pkt("tcp", 22, "10.0.1.20"), "drop", ""},
		{pkt("udp", 5432, "10.0.1.20"), "drop", ""},
		{pkt("tcp", 8096, "10.0.1.20"), "drop", ""}, // disabled policy
	}
	for _, c := range cases {
		v := hp.Evaluate("in", c.p)
		if v.Verdict != c.want || (c.reason != "" && v.Reason.PolicyName != c.reason) {
			t.Errorf("%+v: got %s (%+v)", c.p, v.Verdict, v.Reason)
		}
	}
	if v := hp.Evaluate("in", pkt("tcp", 22, "10.0.2.2")); v.Reason.Kind != compile.ReasonManagement {
		t.Errorf("management reason: %+v", v.Reason)
	}
	if v := hp.Evaluate("out", pkt("tcp", 443, "1.1.1.1")); v.Verdict != "accept" {
		t.Errorf("outbound not restricted: %+v", v)
	}
	g := hp.SetByName(hp.Input.Rules[len(hp.Input.Rules)-4].Peer.Set)
	if g == nil || !g.Dynamic {
		t.Fatalf("group set: %+v", hp.Input.Rules)
	}
}

func TestInternetAndDNS(t *testing.T) {
	w := world()
	hp := compile.Compile(compile.Input{World: w, HostID: "web01", Outbound: compile.OutboundRestrict})
	for _, c := range []struct {
		p    compile.Packet
		want string
	}{
		{pkt("tcp", 443, "8.8.8.8"), "accept"},
		{pkt("tcp", 443, "2001:db8::1"), "accept"},
		{pkt("tcp", 443, "192.168.1.5"), "drop"}, // internal is not "the internet"
		{pkt("tcp", 443, "fd00::99"), "drop"},
		{pkt("tcp", 443, "100.100.1.1"), "drop"}, // CGNAT/Tailscale is internal
	} {
		if v := hp.Evaluate("in", c.p); v.Verdict != c.want {
			t.Errorf("in %+v: got %s", c.p, v.Verdict)
		}
	}
	// Outbound restricted: DNS to the resolver (UDP and TCP), PostgreSQL to the database.
	for _, c := range []struct {
		p    compile.Packet
		want string
	}{
		{pkt("udp", 53, "10.0.0.53"), "accept"},
		{pkt("tcp", 53, "10.0.0.53"), "accept"},
		{pkt("udp", 53, "1.1.1.1"), "drop"},
		{pkt("tcp", 5432, "10.0.2.10"), "accept"},
		{pkt("tcp", 5432, "fd00::10"), "accept"},
		{pkt("tcp", 80, "93.184.216.34"), "drop"},
	} {
		if v := hp.Evaluate("out", c.p); v.Verdict != c.want {
			t.Errorf("out %+v: got %s (%+v)", c.p, v.Verdict, v.Reason)
		}
	}
	if len(hp.Warnings) != 0 {
		t.Errorf("warnings: %v", hp.Warnings)
	}
	// Without the DNS policy a restricted host gets a warning.
	w.Policies = w.Policies[:3]
	hp = compile.Compile(compile.Input{World: w, HostID: "web01", Outbound: compile.OutboundRestrict})
	if len(hp.Warnings) == 0 || !strings.Contains(hp.Warnings[0], "DNS") {
		t.Errorf("no DNS warning: %v", hp.Warnings)
	}
}

func TestManagementProtection(t *testing.T) {
	w := world()
	w.Policies = append([]policy.Policy{{ID: "deny", Name: "lock-down", Enabled: true, Action: policy.Deny,
		From: []policy.Selector{{Kind: policy.SelAny}}, To: []policy.Selector{{Kind: policy.SelHost, ID: "db01"}},
		Services: []string{"ssh"}}}, w.Policies...)
	hp := compile.Compile(compile.Input{World: w, HostID: "db01", Mgmt: []compile.MgmtFlow{sshMgmt}})
	if len(hp.Conflicts) != 1 || hp.Conflicts[0].PolicyName != "lock-down" || !strings.Contains(hp.Conflicts[0].Message, "management path") {
		t.Fatalf("conflicts: %+v", hp.Conflicts)
	}
	// Without the override the management path still wins; the deny applies to everyone else.
	if v := hp.Evaluate("in", pkt("tcp", 22, "10.0.2.2")); v.Verdict != "accept" {
		t.Fatalf("management blocked without override: %+v", v)
	}
	if v := hp.Evaluate("in", pkt("tcp", 22, "10.0.0.5")); v.Verdict != "drop" || v.Reason.PolicyName != "lock-down" {
		t.Fatalf("deny beats allow: %+v", v)
	}
	// The emergency override removes the protected rule; the deny applies.
	hp = compile.Compile(compile.Input{World: w, HostID: "db01", Mgmt: []compile.MgmtFlow{sshMgmt}, OverrideMgmt: true})
	if len(hp.Conflicts) != 0 || len(hp.Overrode) != 1 {
		t.Fatalf("override: %+v %+v", hp.Conflicts, hp.Overrode)
	}
	if v := hp.Evaluate("in", pkt("tcp", 22, "10.0.2.2")); v.Verdict != "drop" {
		t.Fatalf("override not applied: %+v", v)
	}
	// Outbound management (agent → controller) on a restricted host.
	agent := compile.MgmtFlow{Dir: "out", Proto: "tcp", Port: 9443, Peers: []netip.Prefix{netip.MustParsePrefix("192.0.2.10/32")}, Reason: "agent connection to the controller"}
	hp = compile.Compile(compile.Input{World: world(), HostID: "web01", Outbound: compile.OutboundRestrict, Mgmt: []compile.MgmtFlow{agent}})
	if v := hp.Evaluate("out", pkt("tcp", 9443, "192.0.2.10")); v.Verdict != "accept" || v.Reason.Kind != compile.ReasonManagement {
		t.Fatalf("agent path: %+v", v)
	}
}

func TestRenderAndAddressChurn(t *testing.T) {
	w := world()
	hp := compile.Compile(compile.Input{World: w, HostID: "db01", Mgmt: []compile.MgmtFlow{sshMgmt}})
	r := nftables.Render(hp)
	for _, want := range []string{
		"table inet drool\ndelete table inet drool\ntable inet drool {",
		"type filter hook input priority filter; policy drop;",
		"type filter hook output priority filter; policy accept;",
		"ct state established,related accept",
		"ip saddr { 10.0.2.2 } tcp dport { 22 } accept comment \"management: SSH management from the controller (10.0.2.2)\"",
		"tcp dport { 5432 } accept comment \"policy web-database\"",
		"elements = { 10.0.1.20, 10.0.1.21 }",
		"elements = { fd00::20 }",
		"ip6 saddr @g_",
	} {
		if !strings.Contains(r.Full, want) {
			t.Errorf("missing %q in\n%s", want, r.Full)
		}
	}
	if strings.Contains(r.Full, "flush ruleset") {
		t.Fatal("flush ruleset")
	}
	// A new web host: only set elements change.
	w.Hosts["web03"] = compile.HostRef{ID: "web03", Name: "web03", Addrs: addrs("10.0.1.22", "fd00::22")}
	g := w.Groups["web"]
	g.Members = append(g.Members, "web03")
	w.Groups["web"] = g
	hp2 := compile.Compile(compile.Input{World: w, HostID: "db01", Mgmt: []compile.MgmtFlow{sshMgmt}})
	r2 := nftables.Render(hp2)
	if r2.RulesHash != r.RulesHash || r2.SetsHash == r.SetsHash || r2.FullHash == r.FullHash {
		t.Fatalf("hashes: rules %v sets %v", r2.RulesHash == r.RulesHash, r2.SetsHash == r.SetsHash)
	}
	d := nftables.SetDelta(hp, hp2)
	if !strings.Contains(d, "add element inet drool g_") || !strings.Contains(d, "10.0.1.20, 10.0.1.21, 10.0.1.22") ||
		!strings.Contains(d, "fd00::20, fd00::22") || strings.Count(d, "flush set") != 2 {
		t.Fatalf("delta:\n%s", d)
	}
	if v := hp2.Evaluate("in", pkt("tcp", 5432, "10.0.1.22")); v.Verdict != "accept" {
		t.Fatal("new member not allowed")
	}
	// An address leaves: removal is part of the same atomic update.
	w.Hosts["web02"] = compile.HostRef{ID: "web02", Name: "web02", Addrs: addrs("10.0.1.99")}
	hp3 := compile.Compile(compile.Input{World: w, HostID: "db01", Mgmt: []compile.MgmtFlow{sshMgmt}})
	d = nftables.SetDelta(hp2, hp3)
	if strings.Contains(d, "10.0.1.21") || !strings.Contains(d, "10.0.1.99") || strings.Contains(d, "_6") {
		t.Fatalf("delta after move:\n%s", d)
	}
	if v := hp3.Evaluate("in", pkt("tcp", 5432, "10.0.1.21")); v.Verdict != "drop" {
		t.Fatal("old address keeps access")
	}
	// A policy change changes the rules hash.
	w.Policies[0].Services = []string{"postgres", "redis"}
	if nftables.Render(compile.Compile(compile.Input{World: w, HostID: "db01", Mgmt: []compile.MgmtFlow{sshMgmt}})).RulesHash == r.RulesHash {
		t.Fatal("rules hash ignores policy changes")
	}
	// Management path changes are rule changes too (full transaction).
	other := sshMgmt
	other.Peers = []netip.Prefix{netip.MustParsePrefix("10.0.2.3/32")}
	if nftables.Render(compile.Compile(compile.Input{World: world(), HostID: "db01", Mgmt: []compile.MgmtFlow{other}})).RulesHash == r.RulesHash {
		t.Fatal("rules hash ignores management changes")
	}
}

func TestExplain(t *testing.T) {
	hp := compile.Compile(compile.Input{World: world(), HostID: "db01", Mgmt: []compile.MgmtFlow{sshMgmt}})
	ex := hp.Explain()
	var lines []string
	for _, a := range ex {
		lines = append(lines, a.Direction+" "+a.Verdict+" "+a.Service+" from "+a.Peer+" ("+a.Reason.Text+")")
	}
	all := strings.Join(lines, "\n")
	for _, want := range []string{
		"in accept TCP 5432 from group web (policy web-database)",
		"in accept TCP 22 from group jump (policy management)",
		"in accept TCP 22 from 10.0.2.2/32 (SSH management from the controller (10.0.2.2))",
		"in drop everything else from anywhere",
		"out accept everything else from anywhere",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in\n%s", want, all)
		}
	}
	for _, a := range ex {
		if a.Peer == "group web" && (len(a.Members) != 2 || a.Members[0] != "web01") {
			t.Errorf("members: %+v", a)
		}
		// A policy's line names the services behind its ports; the
		// management path and the defaults name none.
		want := map[string]string{"policy web-database": "postgres", "policy management": "ssh"}[a.Reason.Text]
		if got := strings.Join(a.Services, ","); got != want {
			t.Errorf("services of %s %s (%s): %q, want %q", a.Service, a.Peer, a.Reason.Text, got, want)
		}
	}
	// Several services on one protocol become one line naming them all.
	w := world()
	w.Policies = append(w.Policies, policy.Policy{ID: "p6", Name: "web-both", Enabled: true, Action: policy.Allow,
		From: []policy.Selector{{Kind: policy.SelCIDR, CIDR: "192.168.3.132/32"}}, To: []policy.Selector{{Kind: policy.SelGroup, ID: "database"}},
		Services: []string{"http", "https", "dns"}})
	for _, a := range compile.Compile(compile.Input{World: w, HostID: "db01"}).Explain() {
		if a.Reason.Text != "policy web-both" {
			continue
		}
		got := a.Service + " = " + strings.Join(a.Services, ",")
		if got != "TCP 53, 80, 443 = http,https,dns" && got != "UDP 53 = dns" {
			t.Errorf("web-both: %s", got)
		}
	}
	// Names are display only: the rendered rules (and their hash) don't
	// change with them.
	plain := compile.Compile(compile.Input{World: world(), HostID: "db01", Mgmt: []compile.MgmtFlow{sshMgmt}})
	for _, ch := range []*compile.Chain{&plain.Input, &plain.Output} {
		for i := range ch.Rules {
			ch.Rules[i].Services = nil
		}
	}
	if nftables.Render(plain).FullHash != nftables.Render(hp).FullHash {
		t.Error("service names changed the rendered policy")
	}
	// Each IPv4/IPv6 pair is listed once.
	if strings.Count(all, "TCP 5432 from group web") != 1 {
		t.Errorf("duplicates:\n%s", all)
	}
}

func TestValidation(t *testing.T) {
	svcs := policy.Services(nil)
	bad := []policy.Policy{
		{Name: "", Action: "allow"},
		{Name: "x", Action: "maybe", From: []policy.Selector{{Kind: "any"}}, To: []policy.Selector{{Kind: "any"}}, Services: []string{"ssh"}},
		{Name: "x", Action: "allow", From: []policy.Selector{{Kind: "cidr", CIDR: "300.1.1.1/8"}}, To: []policy.Selector{{Kind: "any"}}, Services: []string{"ssh"}},
		{Name: "x", Action: "allow", From: []policy.Selector{{Kind: "any"}}, To: []policy.Selector{{Kind: "any"}}, Services: []string{"nope"}},
	}
	for i, p := range bad {
		if policy.Validate(p, svcs) == nil {
			t.Errorf("policy %d accepted", i)
		}
	}
	if err := policy.ValidateService(policy.Service{Name: "ssh", Entries: []policy.Entry{{Protocol: "tcp", Ports: "22"}}}); err == nil {
		t.Error("built-in name reused")
	}
	if err := policy.ValidateService(policy.Service{Name: "app", Entries: []policy.Entry{{Protocol: "tcp", Ports: "70000"}}}); err == nil {
		t.Error("bad port accepted")
	}
	if ps := compile.NormalizePrefixes([]netip.Prefix{netip.MustParsePrefix("10.1.0.0/16"), netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8")}); len(ps) != 1 {
		t.Errorf("normalize: %v", ps)
	}
	dns := policy.Services(nil)["dns"]
	if len(dns.Entries) != 2 || dns.Entries[0].Protocol == dns.Entries[1].Protocol {
		t.Errorf("dns must be UDP and TCP: %+v", dns)
	}
}
