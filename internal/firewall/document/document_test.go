package document

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"drool/internal/firewall/policy"
)

// The example from the M9 brief.
const example = `networks:
  admin-lan:
    cidr: 192.168.1.0/24

groups:
  ssh-managed:
    members:
      - server-a
      - server-b
      - server-c

services:
  app-8812:
    protocol: tcp
    port: 8812

policy:
  - name: admin-to-jump
    from: admin-lan
    to: jump-a
    allow:
      - ssh

  - name: jump-to-servers
    from: jump-a
    to: ssh-managed
    allow:
      - ssh

  - name: server-a-to-server-b
    from: server-a
    to: server-b
    allow:
      - app-8812
`

var names = Names{Hosts: []string{"jump-a", "server-a", "server-b", "server-c"}}

func inv() Inventory {
	i := Inventory{Hosts: map[string]string{}, HostNames: map[string]string{}, Groups: map[string]string{}, GroupNames: map[string]string{},
		GroupMembers: map[string][]string{}, Networks: map[string]string{}, Services: map[string]string{}, Policies: map[string]string{},
		BuiltinServices: policy.Services(nil)}
	for _, h := range names.Hosts {
		i.Hosts[h] = "hst_" + h
		i.HostNames["hst_"+h] = h
	}
	return i
}

func TestExampleYAML(t *testing.T) {
	d, probs := ParseYAML([]byte(example), names)
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	want := Document{
		Networks: []Network{{Name: "admin-lan", CIDRs: []string{"192.168.1.0/24"}}},
		Groups:   []Group{{Name: "ssh-managed", Members: []string{"server-a", "server-b", "server-c"}}},
		Services: []Service{{Name: "app-8812", Entries: []policy.Entry{{Protocol: "tcp", Ports: "8812"}}}},
		Policy: []Rule{
			{Name: "admin-to-jump", Enabled: true, Action: "allow", From: []string{"network:admin-lan"}, To: []string{"host:jump-a"}, Services: []string{"ssh"}},
			{Name: "jump-to-servers", Enabled: true, Action: "allow", From: []string{"host:jump-a"}, To: []string{"group:ssh-managed"}, Services: []string{"ssh"}},
			{Name: "server-a-to-server-b", Enabled: true, Action: "allow", From: []string{"host:server-a"}, To: []string{"host:server-b"}, Services: []string{"app-8812"}},
		},
	}
	if !reflect.DeepEqual(d, Normalize(want)) {
		a, _ := json.MarshalIndent(d, "", " ")
		t.Fatalf("parsed:\n%s", a)
	}
	res, probs := Resolve(d, inv())
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	if len(res.Policies) != 3 || res.Policies[1].To[0] != (policy.Selector{Kind: policy.SelGroup, ID: DraftGroup + "ssh-managed"}) ||
		res.Policies[0].From[0] != (policy.Selector{Kind: policy.SelNetwork, ID: DraftNetwork + "admin-lan"}) {
		t.Fatalf("%+v", res.Policies)
	}
	// Canonical rendering round-trips to the same document, and rendering
	// is stable.
	out := RenderYAML(d, names)
	d2, probs := ParseYAML(out, names)
	if len(probs) > 0 || !reflect.DeepEqual(d, d2) {
		t.Fatalf("round trip:\n%s\n%v", out, probs)
	}
	if string(RenderYAML(d2, names)) != string(out) {
		t.Fatal("rendering is not stable")
	}
	for _, s := range []string{"admin-lan:\n    cidr: 192.168.1.0/24", "port: 8812", "from: admin-lan", "to: ssh-managed", "allow:\n      - ssh"} {
		if !strings.Contains(string(out), s) {
			t.Errorf("canonical YAML lacks %q:\n%s", s, out)
		}
	}
}

func TestYAMLForms(t *testing.T) {
	src := `networks:
  mixed:
    cidrs: [10.1.0.0/16, "2001:db8::/32", 10.1.2.3/16]
services:
  web-alt:
    entries:
      - {protocol: tcp, port: "8080-8089"}
      - {protocol: udp, ports: [53, 5353]}
  pingable:
    entries:
      - {protocol: icmpv6, type: echo-request}
policy:
  - name: many
    from: [mixed, 203.0.113.7, internet, "2001:db8::1"]
    to: [server-a, server-b]
    allow: [web-alt, https, dns]
  - name: block
    enabled: false
    from: any
    to: server-c
    deny: pingable
`
	d, probs := ParseYAML([]byte(src), names)
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	if got := d.Networks[0].CIDRs; !reflect.DeepEqual(got, []string{"10.1.0.0/16", "2001:db8::/32"}) {
		t.Errorf("cidrs normalized: %v", got)
	}
	if got := d.Policy[0].From; !reflect.DeepEqual(got, []string{"network:mixed", "cidr:203.0.113.7/32", "internet", "cidr:2001:db8::1/128"}) {
		t.Errorf("from: %v", got)
	}
	if d.Policy[1].Enabled || d.Policy[1].Action != "deny" || d.Services[1].Entries[1].Ports != "53,5353" {
		t.Errorf("%+v %+v", d.Policy[1], d.Services)
	}
	if _, probs := Resolve(d, inv()); len(probs) > 0 {
		t.Fatal(probs)
	}
	out := string(RenderYAML(d, names))
	for _, s := range []string{"enabled: false", "deny:\n      - pingable", "- 2001:db8::/32", "- internet"} {
		if !strings.Contains(out, s) {
			t.Errorf("missing %q:\n%s", s, out)
		}
	}
	d2, probs := ParseYAML([]byte(out), names)
	if len(probs) > 0 || !reflect.DeepEqual(d, d2) {
		t.Fatalf("round trip: %v\n%s", probs, out)
	}
}

func TestAmbiguousNamesArePrefixed(t *testing.T) {
	n := Names{Hosts: []string{"web", "internet"}, Groups: []string{"web"}}
	d := Normalize(Document{Policy: []Rule{{Name: "p", Enabled: true, Action: "allow", From: []string{"host:web", "host:internet"}, To: []string{"group:web"}, Services: []string{"ssh"}}}})
	out := string(RenderYAML(d, n))
	if !strings.Contains(out, "- host:web") || !strings.Contains(out, "- host:internet") || !strings.Contains(out, "to: group:web") {
		t.Fatalf("%s", out)
	}
	back, probs := ParseYAML([]byte(out), n)
	if len(probs) > 0 || !reflect.DeepEqual(back, d) {
		t.Fatalf("%v %+v", probs, back)
	}
	_, probs = ParseYAML([]byte("policy:\n  - name: x\n    from: web\n    to: web\n    allow: ssh\n"), n)
	if len(probs) != 2 || !strings.Contains(probs[0].Message, "ambiguous") || probs[0].Line != 3 && probs[0].Line != 4 {
		t.Fatalf("%+v", probs)
	}
}

func TestInvalidYAML(t *testing.T) {
	cases := map[string]struct{ src, want string }{
		"syntax":          {"policy:\n  - name: x\n    from: [a\n", "line"},
		"unknown field":   {"policy:\n  - name: x\n    form: a\n", `unknown field "form" in a policy`},
		"top level":       {"rules: []\n", `unknown field "rules" at the top level`},
		"both actions":    {"policy:\n  - name: x\n    from: any\n    to: server-a\n    allow: ssh\n    deny: ssh\n", "either allows or denies"},
		"no action":       {"policy:\n  - name: x\n    from: any\n    to: server-a\n", "needs allow"},
		"unknown name":    {"policy:\n  - name: x\n    from: nowhere\n    to: server-a\n    allow: ssh\n", `unknown name "nowhere"`},
		"service form":    {"services:\n  x:\n    protocol: tcp\n    port: 1\n    entries: [{protocol: udp, port: 2}]\n", "not both"},
		"service no prot": {"services:\n  x:\n    port: 1\n", "needs a protocol"},
		"map as from":     {"policy:\n  - name: x\n    from: {a: b}\n    to: server-a\n    allow: ssh\n", "expected a name"},
	}
	for name, c := range cases {
		_, probs := ParseYAML([]byte(c.src), names)
		if len(probs) == 0 || !strings.Contains(probs[0].String(), c.want) {
			t.Errorf("%s: %+v", name, probs)
		}
		if name != "syntax" && name != "top level" && probs[0].Line == 0 {
			t.Errorf("%s: no line: %+v", name, probs)
		}
	}
}

func TestResolveProblems(t *testing.T) {
	base := func() Document {
		return Document{
			Networks: []Network{{Name: "lan", CIDRs: []string{"10.0.0.0/8"}}},
			Groups:   []Group{{Name: "web", Members: []string{"server-a"}}},
			Services: []Service{{Name: "app", Entries: []policy.Entry{{Protocol: "tcp", Ports: "8812"}}}},
			Policy:   []Rule{{Name: "r", Enabled: true, Action: "allow", From: []string{"network:lan"}, To: []string{"group:web"}, Services: []string{"app", "ssh"}}},
		}
	}
	if _, probs := Resolve(Normalize(base()), inv()); len(probs) > 0 {
		t.Fatal(probs)
	}
	cases := map[string]struct {
		mut  func(*Document)
		want string
	}{
		"port too big":        {func(d *Document) { d.Services[0].Entries[0].Ports = "70000" }, "invalid port"},
		"bad protocol":        {func(d *Document) { d.Services[0].Entries[0].Protocol = "sctp" }, "unsupported protocol"},
		"bad range":           {func(d *Document) { d.Services[0].Entries[0].Ports = "90-80" }, "invalid port range"},
		"builtin name":        {func(d *Document) { d.Services[0].Name = "ssh"; d.Policy[0].Services = []string{"ssh"} }, "built-in service"},
		"deleted service":     {func(d *Document) { d.Services = nil }, `unknown service "app"`},
		"deleted network":     {func(d *Document) { d.Networks = nil }, `unknown network "lan"`},
		"deleted group":       {func(d *Document) { d.Groups = nil }, `unknown group "web"`},
		"unknown member":      {func(d *Document) { d.Groups[0].Members = []string{"ghost"} }, `unknown host "ghost"`},
		"bad cidr":            {func(d *Document) { d.Networks[0].CIDRs = []string{"10.0.0.0/33"} }, "not a valid CIDR"},
		"empty network":       {func(d *Document) { d.Networks[0].CIDRs = nil }, "at least one CIDR"},
		"reserved name":       {func(d *Document) { d.Networks[0].Name = "internet"; d.Policy[0].From = []string{"internet"} }, "reserved"},
		"duplicate policy":    {func(d *Document) { d.Policy = append(d.Policy, d.Policy[0]) }, "defined twice"},
		"no services":         {func(d *Document) { d.Policy[0].Services = nil }, "at least one service"},
		"bad action":          {func(d *Document) { d.Policy[0].Action = "reject" }, "allow or deny"},
		"no destination":      {func(d *Document) { d.Policy[0].To = nil }, "source and one destination"},
		"deleted host ref":    {func(d *Document) { d.Policy[0].To = []string{"host:deleted-hst_1"} }, "unknown host"},
		"garbage ref":         {func(d *Document) { d.Policy[0].To = []string{"planet:x"} }, "unknown kind"},
		"policy without name": {func(d *Document) { d.Policy[0].Name = "" }, "needs a name"},
	}
	for name, c := range cases {
		d := base()
		c.mut(&d)
		_, probs := Resolve(Normalize(d), inv())
		if len(probs) == 0 || !strings.Contains(probs[0].String(), c.want) {
			t.Errorf("%s: %+v", name, probs)
		}
	}
}

func TestLocate(t *testing.T) {
	src := "groups:\n  web:\n    members: [ghost]\npolicy:\n  - name: r\n    from: any\n    to: [server-a, group:nope]\n    allow: ssh\n"
	d, probs := ParseYAML([]byte(src), names)
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	_, probs = Resolve(d, inv())
	probs = Locate([]byte(src), probs)
	if len(probs) != 2 || probs[0].Line != 2 || probs[1].Line != 7 {
		t.Fatalf("%+v", probs)
	}
}

// TestGolden is the Go half of the cross-view test: testdata/designer.json
// is the canonical model; the web tests (designer/model.test.ts) build the
// same JSON through Topology and Rules operations and render it as a
// topology. Here: model → YAML and YAML → model.
func TestGolden(t *testing.T) {
	var want Document
	b, err := os.ReadFile("testdata/designer.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}
	golden, _ := os.ReadFile("testdata/designer.yaml")
	n := Names{Hosts: []string{"jump-a", "server-a", "server-b", "server-c"}}
	if got := RenderYAML(want, n); string(got) != string(golden) {
		t.Fatalf("model → YAML:\n%s", got)
	}
	got, probs := ParseYAML(golden, n)
	if len(probs) > 0 || !reflect.DeepEqual(got, Normalize(want)) {
		t.Fatalf("YAML → model: %v %+v", probs, got)
	}
	if !reflect.DeepEqual(Normalize(want), want) {
		t.Fatal("the golden JSON is not in canonical form")
	}
	if _, probs := Resolve(got, inv()); len(probs) > 0 {
		t.Fatal(probs)
	}
}
