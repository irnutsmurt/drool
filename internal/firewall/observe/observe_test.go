package observe

import "testing"

// Captured from Debian 12 (ss 6.1) output shapes: the M8 snapshot sections,
// still read for snapshots taken by older tooling.
var sample = map[string]string{
	"listen": `udp   UNCONN 0      0       127.0.0.53%lo:53         0.0.0.0:*
udp   UNCONN 0      0       0.0.0.0%eth0:68          0.0.0.0:*
tcp   LISTEN 0      128           0.0.0.0:22         0.0.0.0:*
tcp   LISTEN 0      244         127.0.0.1:5432       0.0.0.0:*
tcp   LISTEN 0      244           0.0.0.0:5432       0.0.0.0:*
tcp   LISTEN 0      128              [::]:22            [::]:*
`,
	"tcp": `0      0          10.0.2.15:22          10.0.2.2:51234
0      0          10.0.2.10:5432        10.0.1.20:40001
0      0          10.0.2.10:5432        10.0.1.20:40002
0      0          10.0.2.10:44310   151.101.2.132:443
0      0         [fd00::10]:5432     [fd00::20]:50000
0      0          127.0.0.1:5432        127.0.0.1:39000
`,
	"udp": `0      0          10.0.2.10:41000         10.0.0.53:53
`,
}

func TestParseAndAggregate(t *testing.T) {
	cs := Parse(sample)
	if len(cs) != 6 {
		t.Fatalf("conns: %+v", cs)
	}
	a := NewAggregator()
	a.Add(cs, 100)
	a.Add(cs, 130) // the same connections again: not counted twice
	fl := a.Drain()
	byKey := map[string]Flow{}
	for _, f := range fl {
		byKey[f.FlowKey()] = f
	}
	for k, want := range map[string]int64{
		"in tcp 10.0.2.15 22 10.0.2.2":        1,
		"in tcp 10.0.2.10 5432 10.0.1.20":     2,
		"in tcp fd00::10 5432 fd00::20":       1,
		"out tcp 10.0.2.10 443 151.101.2.132": 1,
		"out udp 10.0.2.10 53 10.0.0.53":      1,
	} {
		if f, ok := byKey[k]; !ok || f.Count != want || f.LastSeen != 130 || f.FirstSeen != 100 {
			t.Errorf("%s: %+v", k, f)
		}
	}
	if len(fl) != 5 {
		t.Errorf("flows: %+v", fl)
	}
	// A new connection on an existing flow is counted.
	cs = append(cs, Conn{Proto: "tcp", Inbound: true, ServerPort: 5432,
		Local: cs[1].Local, Remote: cs[1].Remote})
	cs[len(cs)-1].Remote = cs[2].Remote // same 4-tuple as an existing one: not new
	a.Add(cs[:6], 160)
	if fl := a.Drain(); len(fl) != 5 {
		t.Errorf("after drain: %+v", fl)
	}
}

func TestInboundTakesTheListener(t *testing.T) {
	// OpenSSH 9.8+ hands each accepted connection to sshd-session; the
	// service a policy names is still the listening sshd.
	cs := Conns(ParseSockets(`tcp LISTEN 0 128 0.0.0.0:22 0.0.0.0:* users:(("sshd",pid=900,fd=3))
tcp ESTAB 0 0 10.0.0.5:22 10.0.0.9:51234 users:(("sshd-session",pid=1234,fd=4))
tcp ESTAB 0 0 10.0.0.5:44310 142.250.1.1:443 users:(("curl",pid=4242,fd=5))`))
	if len(cs) != 2 || !cs[0].Inbound || cs[0].Process != "sshd" || cs[0].PID != 900 {
		t.Fatalf("inbound: %+v", cs)
	}
	if cs[1].Inbound || cs[1].Process != "curl" {
		t.Fatalf("outbound keeps its own process: %+v", cs[1])
	}
}
