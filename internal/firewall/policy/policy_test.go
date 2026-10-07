package policy

import (
	"strings"
	"testing"
)

func TestParsePorts(t *testing.T) {
	got, err := ParsePorts(" 443, 80,8000-8010 ")
	if err != nil || len(got) != 3 || got[0] != (PortRange{80, 80}) || got[2] != (PortRange{8000, 8010}) {
		t.Fatalf("%v %v", got, err)
	}
	for _, bad := range []string{"", ",", "0", "65536", "10-5", "a", "1-x", "-5"} {
		if _, err := ParsePorts(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestBuiltinsAndServices(t *testing.T) {
	m := Services([]Service{{Name: "app", Entries: []Entry{{Protocol: TCP, Ports: "9000"}}}})
	for _, n := range []string{"ssh", "dns", "http", "https", "ntp", "postgres", "mysql", "redis", "smtp", "smb", "nfs", "app"} {
		if _, ok := m[n]; !ok {
			t.Errorf("missing %s", n)
		}
	}
	dns := m["dns"]
	if !dns.Builtin || len(dns.Entries) != 2 || dns.Entries[0].Protocol != UDP || dns.Entries[1].Protocol != TCP {
		t.Errorf("dns must be UDP and TCP 53: %+v", dns)
	}
	for _, b := range Builtin {
		for _, e := range b.Entries {
			if e.Protocol == TCP || e.Protocol == UDP {
				if _, err := ParsePorts(e.Ports); err != nil {
					t.Errorf("%s: %v", b.Name, err)
				}
			}
		}
	}
}

func TestValidateService(t *testing.T) {
	ok := Service{Name: "my-app", Entries: []Entry{{Protocol: TCP, Ports: "9000-9001"}, {Protocol: ICMPv6, ICMPType: "echo-request"}}}
	if err := ValidateService(ok); err != nil {
		t.Fatal(err)
	}
	cases := map[string]Service{
		"name":     {Name: "My App", Entries: ok.Entries},
		"built-in": {Name: "ssh", Entries: ok.Entries},
		"entries":  {Name: "x"},
		"protocol": {Name: "x", Entries: []Entry{{Protocol: "sctp", Ports: "1"}}},
		"ports":    {Name: "x", Entries: []Entry{{Protocol: UDP, Ports: "70000"}}},
		"icmp":     {Name: "x", Entries: []Entry{{Protocol: ICMP, ICMPType: "redirect"}}},
	}
	for name, s := range cases {
		if err := ValidateService(s); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestParseRanges(t *testing.T) {
	ps, err := ParseRanges(append([]string{"10.1.2.3/8 "}, DefaultInternalRanges...))
	if err != nil || ps[0].String() != "10.0.0.0/8" || len(ps) != len(DefaultInternalRanges)+1 {
		t.Fatalf("%v %v", ps, err)
	}
	if _, err := ParseRanges(nil); err == nil {
		t.Error("empty ranges accepted")
	}
	if _, err := ParseRanges([]string{"10.0.0.0/33"}); err == nil {
		t.Error("bad prefix accepted")
	}
}

func TestValidate(t *testing.T) {
	svc := Services(nil)
	good := Policy{Name: "web to db", Action: Allow, From: []Selector{{Kind: SelGroup, ID: "grp_1"}, {Kind: SelCIDR, CIDR: "2001:db8::/32"}},
		To: []Selector{{Kind: SelHost, ID: "hst_1"}}, Services: []string{"postgres"}}
	if err := Validate(good, svc); err != nil {
		t.Fatal(err)
	}
	mut := func(f func(*Policy)) Policy { p := good; f(&p); return p }
	cases := map[string]Policy{
		"no name":     mut(func(p *Policy) { p.Name = " " }),
		"action":      mut(func(p *Policy) { p.Action = "reject" }),
		"no from":     mut(func(p *Policy) { p.From = nil }),
		"no services": mut(func(p *Policy) { p.Services = nil }),
		"unknown svc": mut(func(p *Policy) { p.Services = []string{"gopher"} }),
		"host no id":  mut(func(p *Policy) { p.To = []Selector{{Kind: SelHost}} }),
		"bad cidr":    mut(func(p *Policy) { p.From = []Selector{{Kind: SelCIDR, CIDR: "10.0.0.0/40"}} }),
		"bad kind":    mut(func(p *Policy) { p.To = []Selector{{Kind: "planet"}} }),
	}
	for name, p := range cases {
		if err := Validate(p, svc); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if s := (Selector{Kind: SelInternet}).String(); !strings.Contains(s, "internet") {
		t.Errorf("%q", s)
	}
}
