// Package observe turns what a host can see of its own connections into
// aggregated flows (DESIGN §19.1, M10): "web01 → database01:5432, 12,482
// connections", not raw connection noise. Agents collect continuously
// (conntrack events when available, otherwise socket sampling); SSH hosts
// only get on-demand snapshots, which are labelled as such.
//
// A flow's identity is host + direction + protocol + local address +
// service port + remote address: client-side ephemeral ports are kept as
// "last seen" details, not as identity, so one busy client is one row.
// Nothing here reads packet contents.
package observe

import (
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Collection sources, from most to least complete.
const (
	SourceConntrack = "conntrack" // continuous: every new connection the kernel tracks
	SourceSockets   = "sockets"   // continuous: socket sampling; short-lived connections can be missed
	SourceSnapshot  = "snapshot"  // one-off (SSH hosts)
)

// Socket is one line of `ss -Hntuap`.
type Socket struct {
	Proto   string // tcp | udp
	State   string // LISTEN, ESTAB, UNCONN, TIME-WAIT, ...
	Local   netip.AddrPort
	Remote  netip.AddrPort // the unspecified address with port 0 when not connected
	Process string         // "" when ss can't see the owner
	PID     int
}

// Conn is one established connection seen on the host.
type Conn struct {
	Proto      string
	Local      netip.AddrPort
	Remote     netip.AddrPort
	Inbound    bool // the remote end opened it
	ServerPort int  // the service port: local if inbound, remote if outbound
	Process    string
	PID        int
}

// Key identifies a connection (for counting new ones between samples).
func (c Conn) Key() string { return c.Proto + " " + c.Local.String() + " " + c.Remote.String() }

// Flow is an aggregate: one direction, protocol, local address, service
// port and peer. The JSON names of the first fields are those M8 agents
// send ("flows" telemetry), so old agents still report.
type Flow struct {
	Direction  string `json:"direction"` // in | out
	Protocol   string `json:"protocol"`
	Port       int    `json:"port"` // the service port
	Peer       string `json:"peer"` // the remote address
	Count      int64  `json:"count"`
	FirstSeen  int64  `json:"first_seen,omitempty"` // unix seconds
	LastSeen   int64  `json:"last_seen,omitempty"`
	LocalIP    string `json:"local_ip,omitempty"`
	LocalPort  int    `json:"local_port,omitempty"`  // last seen (the service port when inbound)
	RemotePort int    `json:"remote_port,omitempty"` // last seen (the service port when outbound)
	Process    string `json:"process,omitempty"`
	PID        int    `json:"pid,omitempty"`
}

// FlowKey is a flow's identity.
func (f Flow) FlowKey() string {
	return f.Direction + " " + f.Protocol + " " + f.LocalIP + " " + strconv.Itoa(f.Port) + " " + f.Peer
}

// parseAddrPort reads ss address forms: 10.0.0.1:22, [::1]:22, *:22,
// 127.0.0.53%lo:53, [fe80::1%eth0]:546, 0.0.0.0:*.
func parseAddrPort(s string) (netip.AddrPort, bool) {
	i := strings.LastIndexByte(s, ':')
	if i < 0 {
		return netip.AddrPort{}, false
	}
	host, port := s[:i], s[i+1:]
	p := 0
	if port != "*" {
		var err error
		if p, err = strconv.Atoi(port); err != nil || p < 0 || p > 65535 {
			return netip.AddrPort{}, false
		}
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if j := strings.IndexByte(host, '%'); j >= 0 {
		host = host[:j]
	}
	if host == "*" {
		host = "0.0.0.0"
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.AddrPort{}, false
	}
	return netip.AddrPortFrom(a.Unmap(), uint16(p)), true
}

var usersRE = regexp.MustCompile(`\("((?:[^"\\]|\\.)*)",pid=(\d+),`)

// parseUsers reads users:(("sshd",pid=1234,fd=4),...): the first owner.
func parseUsers(s string) (string, int) {
	m := usersRE.FindStringSubmatch(s)
	if m == nil {
		return "", 0
	}
	pid, _ := strconv.Atoi(m[2])
	return CleanProcess(m[1]), pid
}

// CleanProcess keeps a process name printable and short: it comes from
// the host and ends up in the UI.
func CleanProcess(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 0x20 && r != 0x7f && r != '"' && r != '\\' {
			b.WriteRune(r)
		}
		if b.Len() >= 64 {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

// ParseSockets reads `ss -Hntuap` output (netid, state, queues, local,
// peer, process). Unparseable lines are skipped.
func ParseSockets(out string) []Socket {
	var socks []Socket
	for _, l := range strings.Split(out, "\n") {
		proc := ""
		if i := strings.Index(l, " users:(("); i >= 0 {
			l, proc = l[:i], l[i+1:]
		}
		f := strings.Fields(l)
		if len(f) < 6 || (f[0] != "tcp" && f[0] != "udp") {
			continue
		}
		local, ok1 := parseAddrPort(f[4])
		remote, ok2 := parseAddrPort(f[5])
		if !ok1 || !ok2 {
			continue
		}
		s := Socket{Proto: f[0], State: f[1], Local: local, Remote: remote}
		s.Process, s.PID = parseUsers(proc)
		socks = append(socks, s)
	}
	return socks
}

// Listener is a listening socket (TCP LISTEN, or an unconnected UDP socket).
type Listener struct {
	Proto   string
	Addr    netip.Addr // the unspecified address for all addresses
	Port    int
	Process string
	PID     int
}

// Listeners picks the listening sockets.
func Listeners(socks []Socket) []Listener {
	var out []Listener
	for _, s := range socks {
		if (s.Proto == "tcp" && s.State == "LISTEN") || (s.Proto == "udp" && s.State == "UNCONN") {
			out = append(out, Listener{Proto: s.Proto, Addr: s.Local.Addr(), Port: int(s.Local.Port()), Process: s.Process, PID: s.PID})
		}
	}
	return out
}

// ListenIndex finds the listener for a local endpoint.
type ListenIndex map[string]Listener

func lkey(proto string, a netip.Addr, port int) string {
	return proto + " " + a.String() + " " + strconv.Itoa(port)
}

// NewListenIndex indexes listeners.
func NewListenIndex(ls []Listener) ListenIndex {
	ix := ListenIndex{}
	for _, l := range ls {
		k := lkey(l.Proto, l.Addr, l.Port)
		if prev, ok := ix[k]; !ok || prev.Process == "" {
			ix[k] = l
		}
		// Any address: ss prints dual-stack sockets as *:port, which
		// doesn't say which families it takes.
		pk := l.Proto + " any " + strconv.Itoa(l.Port)
		if prev, ok := ix[pk]; !ok || prev.Process == "" {
			ix[pk] = l
		}
	}
	return ix
}

// Lookup returns the socket listening on proto/addr:port (exact address
// first, then the wildcard of its family).
func (ix ListenIndex) Lookup(proto string, a netip.Addr, port int) (Listener, bool) {
	if l, ok := ix[lkey(proto, a, port)]; ok {
		return l, true
	}
	wild := netip.IPv4Unspecified()
	if a.Is6() {
		wild = netip.IPv6Unspecified()
	}
	if l, ok := ix[lkey(proto, wild, port)]; ok {
		return l, true
	}
	// A dual-stack [::]:port socket also takes IPv4.
	if l, ok := ix[lkey(proto, netip.IPv6Unspecified(), port)]; ok {
		return l, true
	}
	l, ok := ix[proto+" any "+strconv.Itoa(port)]
	return l, ok
}

func skipAddr(a netip.Addr) bool {
	return !a.IsValid() || a.IsLoopback() || a.IsUnspecified()
}

// Conns picks established connections and gives them a direction: one
// whose local port is a listening port was opened by the remote end. The
// owner of an inbound connection is the process listening on its port.
func Conns(socks []Socket) []Conn {
	ix := NewListenIndex(Listeners(socks))
	var out []Conn
	for _, s := range socks {
		if s.State != "ESTAB" || s.Remote.Port() == 0 || skipAddr(s.Local.Addr()) || skipAddr(s.Remote.Addr()) {
			continue
		}
		c := Conn{Proto: s.Proto, Local: s.Local, Remote: s.Remote, Process: s.Process, PID: s.PID}
		// A listener on the local port (TCP LISTEN, or for UDP an unconnected
		// socket on the same port) means a server answering.
		if l, ok := ix.Lookup(s.Proto, s.Local.Addr(), int(s.Local.Port())); ok {
			c.Inbound, c.ServerPort = true, int(s.Local.Port())
			// The service is the listener, even when it hands accepted
			// connections to another process (OpenSSH 9.8+: sshd-session).
			if l.Process != "" {
				c.Process, c.PID = l.Process, l.PID
			}
		} else {
			c.ServerPort = int(s.Remote.Port())
		}
		out = append(out, c)
	}
	return out
}

// Parse reads a snapshot's sections. "sockets" is `ss -Hntuap` (M10);
// the M8 sections (listen, tcp, udp: no process names) are still read.
func Parse(sections map[string]string) []Conn {
	if s, ok := sections["sockets"]; ok {
		return Conns(ParseSockets(s))
	}
	var socks []Socket
	for _, l := range strings.Split(sections["listen"], "\n") {
		f := strings.Fields(l)
		if len(f) >= 5 {
			if ap, ok := parseAddrPort(f[4]); ok {
				socks = append(socks, Socket{Proto: f[0], State: map[string]string{"tcp": "LISTEN", "udp": "UNCONN"}[f[0]], Local: ap})
			}
		}
	}
	for _, proto := range []string{"tcp", "udp"} {
		for _, l := range strings.Split(sections[proto], "\n") {
			f := strings.Fields(l)
			if len(f) < 2 {
				continue
			}
			local, ok1 := parseAddrPort(f[len(f)-2])
			remote, ok2 := parseAddrPort(f[len(f)-1])
			if ok1 && ok2 {
				socks = append(socks, Socket{Proto: proto, State: "ESTAB", Local: local, Remote: remote})
			}
		}
	}
	return Conns(socks)
}

// --- aggregation ---

// Aggregator counts connections into flows. Each connection is counted
// once: when a conntrack event announces it, or the first time a sample
// sees it.
type Aggregator struct {
	counted map[string]int64 // connection key → last time it was seen
	prev    map[string]bool  // connections in the previous sample
	flows   map[string]*Flow
	// Max bounds the flows held between drains (0: unbounded); flows
	// beyond it are dropped and counted.
	Max     int
	Dropped int64
}

// NewAggregator returns an empty aggregator.
func NewAggregator() *Aggregator {
	return &Aggregator{counted: map[string]int64{}, prev: map[string]bool{}, flows: map[string]*Flow{}}
}

func flowOf(c Conn) Flow {
	f := Flow{Direction: "out", Protocol: c.Proto, Port: c.ServerPort, Peer: c.Remote.Addr().String(), LocalIP: c.Local.Addr().String(),
		LocalPort: int(c.Local.Port()), RemotePort: int(c.Remote.Port())}
	if c.Inbound {
		f.Direction = "in"
	}
	return f
}

func (a *Aggregator) touch(c Conn, now int64, count bool) *Flow {
	f := flowOf(c)
	fk := f.FlowKey()
	agg, ok := a.flows[fk]
	if !ok {
		if a.Max > 0 && len(a.flows) >= a.Max {
			a.Dropped++
			return nil
		}
		f.FirstSeen = now
		agg = &f
		a.flows[fk] = agg
	}
	if now > agg.LastSeen {
		agg.LastSeen = now
	}
	agg.LocalPort, agg.RemotePort = f.LocalPort, f.RemotePort
	if c.Process != "" {
		agg.Process, agg.PID = c.Process, c.PID
	}
	if count {
		agg.Count++
	}
	return agg
}

// Add records one sample (socket sampling): connections not in the
// previous sample are new.
func (a *Aggregator) Add(conns []Conn, now int64) {
	cur := map[string]bool{}
	for _, c := range conns {
		k := c.Key()
		cur[k] = true
		_, counted := a.counted[k]
		a.touch(c, now, !a.prev[k] && !counted)
		a.counted[k] = now
	}
	a.prev = cur
	a.expire(now)
}

// Event records one new connection announced by conntrack.
func (a *Aggregator) Event(c Conn, now int64) {
	k := c.Key()
	a.touch(c, now, true)
	a.counted[k] = now
}

// Touch records a sample when conntrack does the counting: connections it
// already announced only advance last_seen and gain their process; ones
// it never announced (opened before collection started, or a lost event)
// are counted once.
func (a *Aggregator) Touch(conns []Conn, now int64) {
	for _, c := range conns {
		k := c.Key()
		_, counted := a.counted[k]
		a.touch(c, now, !counted)
		a.counted[k] = now
	}
	a.prev = map[string]bool{}
	a.expire(now)
}

// Attribute fills in the process of a flow whose connection a later
// socket scan found.
func (a *Aggregator) Attribute(c Conn) bool {
	if c.Process == "" {
		return false
	}
	f, ok := a.flows[flowOf(c).FlowKey()]
	if !ok || f.Process != "" {
		return false
	}
	f.Process, f.PID = c.Process, c.PID
	return true
}

// AttributeLocal names the owner of outbound flows that left from local
// (an unconnected UDP socket: DNS lookups and the like have no connected
// socket to match, but their source port belongs to one process).
func (a *Aggregator) AttributeLocal(proto string, local netip.AddrPort, process string, pid int) {
	if process == "" {
		return
	}
	ip, port := local.Addr().String(), int(local.Port())
	for _, f := range a.flows {
		if f.Process == "" && f.Direction == "out" && f.Protocol == proto && f.LocalPort == port && (f.LocalIP == ip || local.Addr().IsUnspecified()) {
			f.Process, f.PID = process, pid
		}
	}
}

// expire forgets connections not seen for ten minutes (the map is only
// there to avoid counting a connection twice).
func (a *Aggregator) expire(now int64) {
	for k, t := range a.counted {
		if now-t > 600 {
			delete(a.counted, k)
		}
	}
}

// Pending returns the number of flows waiting to be drained.
func (a *Aggregator) Pending() int { return len(a.flows) }

// Unattributed lists flows without a process.
func (a *Aggregator) Unattributed() int {
	n := 0
	for _, f := range a.flows {
		if f.Process == "" {
			n++
		}
	}
	return n
}

// Report is what an agent sends ("netflows" telemetry): the flows since
// the last report and how they were collected.
type Report struct {
	Source string `json:"source"` // conntrack | sockets
	// Detail explains the source: why conntrack isn't used, lost events.
	Detail     string `json:"detail,omitempty"`
	EventsLost int64  `json:"events_lost,omitempty"` // conntrack event buffer overflows
	Dropped    int64  `json:"dropped,omitempty"`     // flows over the agent's buffer limit
	Forwarded  int64  `json:"forwarded,omitempty"`   // connections routed through the host, not shown
	Every      int    `json:"sample_seconds,omitempty"`
	Flows      []Flow `json:"flows"`
}

// Drain returns the flows gathered since the last drain.
func (a *Aggregator) Drain() []Flow {
	out := make([]Flow, 0, len(a.flows))
	for _, f := range a.flows {
		out = append(out, *f)
	}
	a.flows = map[string]*Flow{}
	sort.Slice(out, func(i, j int) bool { return out[i].FlowKey() < out[j].FlowKey() })
	return out
}
