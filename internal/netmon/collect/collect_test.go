package collect

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"drool/internal/firewall/observe"
)

type fakeCT struct {
	mu      sync.Mutex
	usable  string
	openErr error
	tracked bool
	ev      *fakeEvents
}

func (f *fakeCT) Usable() string { return f.usable }
func (f *fakeCT) Open() (Events, error) {
	if f.openErr != nil {
		return nil, f.openErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ev = &fakeEvents{ch: make(chan [3]any, 100), done: make(chan struct{})}
	return f.ev, nil
}
func (f *fakeCT) Tracked(uint8, netip.AddrPort, netip.AddrPort) (bool, error) { return f.tracked, nil }

type fakeEvents struct {
	ch   chan [3]any
	done chan struct{}
	once sync.Once
	lost int64
}

func (e *fakeEvents) Read(fn func(uint8, netip.AddrPort, netip.AddrPort)) error {
	for {
		select {
		case <-e.done:
			return nil
		case v := <-e.ch:
			fn(v[0].(uint8), v[1].(netip.AddrPort), v[2].(netip.AddrPort))
		}
	}
}
func (e *fakeEvents) Close() error { e.once.Do(func() { close(e.done) }); return nil }
func (e *fakeEvents) Lost() int64  { return e.lost }

func ap(s string) netip.AddrPort { return netip.MustParseAddrPort(s) }

// A host with sshd and postgres listening, an inbound SSH session, a
// postgres client, an HTTPS download by curl and a DNS lookup.
const ssOut = `tcp LISTEN 0 128 0.0.0.0:22 0.0.0.0:* users:(("sshd",pid=900,fd=3))
tcp LISTEN 0 244 [::]:5432 [::]:* users:(("postgres",pid=700,fd=5))
udp UNCONN 0 0 10.0.0.5:41000 0.0.0.0:* users:(("systemd-resolve",pid=600,fd=13))
tcp ESTAB 0 0 10.0.0.5:22 10.0.0.9:51234 users:(("sshd",pid=1234,fd=4),("sshd",pid=1240,fd=4))
tcp ESTAB 0 0 [::ffff:10.0.0.5]:5432 [::ffff:10.0.1.20]:40001 users:(("postgres",pid=7001,fd=9))
tcp ESTAB 0 0 10.0.0.5:44310 142.250.1.1:443 users:(("curl",pid=4242,fd=5))
tcp ESTAB 0 0 [2001:db8::5]:50000 [2a00:1450::1]:443
tcp ESTAB 0 0 127.0.0.1:5432 127.0.0.1:39000 users:(("psql",pid=1,fd=1))
tcp TIME-WAIT 0 0 10.0.0.5:44300 151.101.2.132:443
`

func TestParseSockets(t *testing.T) {
	socks := observe.ParseSockets(ssOut)
	if len(socks) != 9 {
		t.Fatalf("sockets: %d", len(socks))
	}
	if s := socks[3]; s.Process != "sshd" || s.PID != 1234 {
		t.Fatalf("process: %+v", s)
	}
	conns := observe.Conns(socks)
	got := map[string]observe.Conn{}
	for _, c := range conns {
		got[c.Key()] = c
	}
	if len(conns) != 4 {
		t.Fatalf("conns: %+v", conns)
	}
	if c := got["tcp 10.0.0.5:22 10.0.0.9:51234"]; !c.Inbound || c.ServerPort != 22 || c.Process != "sshd" {
		t.Fatalf("ssh: %+v", c)
	}
	// IPv4 on a dual-stack listener is inbound to postgres.
	if c := got["tcp 10.0.0.5:5432 10.0.1.20:40001"]; !c.Inbound || c.Process != "postgres" {
		t.Fatalf("postgres: %+v", c)
	}
	if c := got["tcp 10.0.0.5:44310 142.250.1.1:443"]; c.Inbound || c.ServerPort != 443 || c.Process != "curl" {
		t.Fatalf("curl: %+v", c)
	}
	// No owner visible: unknown, not guessed.
	if c := got["tcp [2001:db8::5]:50000 [2a00:1450::1]:443"]; c.Process != "" || c.Inbound {
		t.Fatalf("ipv6: %+v", c)
	}
}

func newTest(ct *fakeCT, ss *string, mu *sync.Mutex, now *time.Time) *Collector {
	return New(Options{
		Every: 30 * time.Second, Conntrack: ct,
		Control: func() (netip.AddrPort, netip.AddrPort, bool) {
			return ap("10.0.0.5:40000"), ap("10.0.0.1:9443"), true
		},
		SS: func(context.Context) (string, error) { mu.Lock(); defer mu.Unlock(); return *ss, nil },
		LocalAddrs: func() []netip.Addr {
			return []netip.Addr{netip.MustParseAddr("10.0.0.5"), netip.MustParseAddr("2001:db8::5")}
		},
		Now: func() time.Time { mu.Lock(); defer mu.Unlock(); return *now },
	})
}

func flows(r observe.Report) map[string]observe.Flow {
	m := map[string]observe.Flow{}
	for _, f := range r.Flows {
		m[f.FlowKey()] = f
	}
	return m
}

func TestSocketSamplingFallback(t *testing.T) {
	for name, ct := range map[string]*fakeCT{
		"no conntrack":  {usable: "connection tracking is not active on this host"},
		"no permission": {openErr: errors.New("no permission to read conntrack events (needs CAP_NET_ADMIN)")},
		"not tracking":  {tracked: false},
	} {
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			ss := ssOut
			now := time.Unix(1000, 0)
			c := newTest(ct, &ss, &mu, &now)
			c.sample(context.Background(), now)
			c.checkConntrack(context.Background())
			src, detail := c.Status()
			if src != observe.SourceSockets || detail == "" {
				t.Fatalf("source %q %q", src, detail)
			}
			// The same connections in the next sample are not counted again.
			now = now.Add(30 * time.Second)
			c.sample(context.Background(), now)
			r := c.Report()
			if r.Source != observe.SourceSockets || r.Detail == "" {
				t.Fatalf("report: %+v", r)
			}
			f := flows(r)
			if len(f) != 4 {
				t.Fatalf("flows: %+v", r.Flows)
			}
			ssh := f["in tcp 10.0.0.5 22 10.0.0.9"]
			if ssh.Count != 1 || ssh.FirstSeen != 1000 || ssh.LastSeen != 1030 || ssh.Process != "sshd" || ssh.RemotePort != 51234 {
				t.Fatalf("ssh flow: %+v", ssh)
			}
		})
	}
}

func TestConntrackEvents(t *testing.T) {
	var mu sync.Mutex
	ss := ssOut
	now := time.Unix(1000, 0)
	ct := &fakeCT{tracked: true}
	c := newTest(ct, &ss, &mu, &now)
	c.sample(context.Background(), now) // counts the four established connections once
	c.checkConntrack(context.Background())
	if src, _ := c.Status(); src != observe.SourceConntrack {
		t.Fatalf("source %q", src)
	}
	// Short-lived connections that no socket sample would ever see: five
	// HTTPS requests to one address, an inbound SSH probe from a new
	// client, IPv6 and UDP. Plus traffic routed through the host.
	for i := 0; i < 5; i++ {
		c.event(6, netip.AddrPortFrom(netip.MustParseAddr("10.0.0.5"), uint16(50000+i)), ap("185.10.1.1:8812"))
	}
	c.event(6, ap("10.0.0.77:60000"), ap("10.0.0.5:22"))
	c.event(6, ap("[2001:db8::5]:40000"), ap("[2a00:1450::1]:443"))
	c.event(17, ap("10.0.0.5:41000"), ap("1.1.1.1:53"))
	c.event(6, ap("172.17.0.2:40000"), ap("8.8.8.8:443"))   // a container's connection, routed
	c.event(6, ap("127.0.0.1:40000"), ap("127.0.0.1:5432")) // loopback
	c.event(1, ap("10.0.0.5:0"), ap("8.8.8.8:0"))           // ICMP: not a flow
	// A socket scan attributes the UDP lookup to its unconnected socket.
	c.attribute(context.Background())
	// The next sample sees the long-lived connections again: not counted twice.
	now = now.Add(30 * time.Second)
	c.sample(context.Background(), now)
	r := c.Report()
	if r.Source != observe.SourceConntrack || r.Forwarded != 1 {
		t.Fatalf("report: %+v", r)
	}
	f := flows(r)
	for k, want := range map[string]int64{
		"out tcp 10.0.0.5 8812 185.10.1.1":     5,
		"in tcp 10.0.0.5 22 10.0.0.77":         1,
		"in tcp 10.0.0.5 22 10.0.0.9":          1,
		"out tcp 2001:db8::5 443 2a00:1450::1": 2, // seen by the first sample, then a new connection
		"out udp 10.0.0.5 53 1.1.1.1":          1,
		"out tcp 10.0.0.5 443 142.250.1.1":     1,
		"in tcp 10.0.0.5 5432 10.0.1.20":       1,
	} {
		if f[k].Count != want {
			t.Errorf("%s: count %d, want %d (%+v)", k, f[k].Count, want, f[k])
		}
	}
	if len(f) != 7 {
		t.Errorf("flows: %+v", r.Flows)
	}
	// Inbound: the listening process. UDP: its socket. The short-lived
	// outbound connections closed before any scan: unknown.
	if p := f["in tcp 10.0.0.5 22 10.0.0.77"].Process; p != "sshd" {
		t.Errorf("inbound process %q", p)
	}
	if p := f["out udp 10.0.0.5 53 1.1.1.1"].Process; p != "systemd-resolve" {
		t.Errorf("udp process %q", p)
	}
	if p := f["out tcp 10.0.0.5 8812 185.10.1.1"].Process; p != "" {
		t.Errorf("short-lived outbound guessed %q", p)
	}
	// Conntrack stops tracking (the firewall was removed): sockets again.
	ct.tracked = false
	c.checkConntrack(context.Background())
	if src, detail := c.Status(); src != observe.SourceSockets || !strings.Contains(detail, "not tracking") {
		t.Fatalf("after losing conntrack: %q %q", src, detail)
	}
}

func TestEventsLostReported(t *testing.T) {
	var mu sync.Mutex
	ss := ssOut
	now := time.Unix(1000, 0)
	ct := &fakeCT{tracked: true}
	c := newTest(ct, &ss, &mu, &now)
	c.sample(context.Background(), now)
	c.checkConntrack(context.Background())
	ct.ev.lost = 3
	r := c.Report()
	if r.EventsLost != 3 || !strings.Contains(r.Detail, "lost") {
		t.Fatalf("report: %+v", r)
	}
	if r := c.Report(); r.EventsLost != 0 {
		t.Fatalf("lost counted twice: %+v", r)
	}
}

func TestBoundedPending(t *testing.T) {
	var mu sync.Mutex
	ss := ""
	now := time.Unix(1000, 0)
	c := newTest(&fakeCT{tracked: true}, &ss, &mu, &now)
	c.sample(context.Background(), now)
	c.checkConntrack(context.Background())
	for i := 0; i < MaxPending+50; i++ {
		c.event(6, ap("10.0.0.5:40000"), netip.AddrPortFrom(netip.AddrFrom4([4]byte{9, byte(i >> 16), byte(i >> 8), byte(i)}), 443))
	}
	r := c.Report()
	if len(r.Flows) != MaxPending || r.Dropped != 50 {
		t.Fatalf("flows %d dropped %d", len(r.Flows), r.Dropped)
	}
}
