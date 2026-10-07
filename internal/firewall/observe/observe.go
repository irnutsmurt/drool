// Package observe turns connection snapshots (ss output) into aggregated
// flows (DESIGN §19.1): "web01 → database01:5432, 12,482 connections", not
// raw connection noise. Agents sample continuously; SSH hosts only get
// on-demand snapshots, which are labelled as such.
package observe

import (
	"net/netip"
	"sort"
	"strconv"
	"strings"
)

// Conn is one established connection seen on the host.
type Conn struct {
	Proto      string
	Local      netip.AddrPort
	Remote     netip.AddrPort
	Inbound    bool // the local port is a listening port
	ServerPort int  // the service port: local if inbound, remote if outbound
}

// Key identifies a connection (for counting new ones between samples).
func (c Conn) Key() string { return c.Proto + " " + c.Local.String() + " " + c.Remote.String() }

// Flow is an aggregate: one direction, protocol, service port and peer.
type Flow struct {
	Direction string `json:"direction"` // in | out
	Protocol  string `json:"protocol"`
	Port      int    `json:"port"`
	Peer      string `json:"peer"`
	Count     int64  `json:"count"`
	FirstSeen int64  `json:"first_seen,omitempty"` // unix seconds
	LastSeen  int64  `json:"last_seen,omitempty"`
}

// FlowKey is a flow's identity.
func (f Flow) FlowKey() string {
	return f.Direction + " " + f.Protocol + " " + strconv.Itoa(f.Port) + " " + f.Peer
}

// parseAddrPort reads ss address forms: 10.0.0.1:22, [::1]:22, *:22,
// 127.0.0.53%lo:53, [fe80::1%eth0]:546.
func parseAddrPort(s string) (netip.AddrPort, bool) {
	i := strings.LastIndexByte(s, ':')
	if i < 0 {
		return netip.AddrPort{}, false
	}
	host, port := s[:i], s[i+1:]
	p, err := strconv.Atoi(port)
	if err != nil {
		return netip.AddrPort{}, false
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

// Parse reads a snapshot's sections (listen, tcp, udp).
func Parse(sections map[string]string) []Conn {
	listening := map[string]bool{} // "tcp 22"
	for _, l := range strings.Split(sections["listen"], "\n") {
		f := strings.Fields(l)
		if len(f) < 5 {
			continue
		}
		if ap, ok := parseAddrPort(f[4]); ok {
			listening[f[0]+" "+strconv.Itoa(int(ap.Port()))] = true
		}
	}
	var out []Conn
	for _, proto := range []string{"tcp", "udp"} {
		for _, l := range strings.Split(sections[proto], "\n") {
			f := strings.Fields(l)
			if len(f) < 2 {
				continue
			}
			local, ok1 := parseAddrPort(f[len(f)-2])
			remote, ok2 := parseAddrPort(f[len(f)-1])
			if !ok1 || !ok2 || remote.Addr().IsLoopback() || local.Addr().IsLoopback() {
				continue
			}
			c := Conn{Proto: proto, Local: local, Remote: remote}
			if listening[proto+" "+strconv.Itoa(int(local.Port()))] {
				c.Inbound, c.ServerPort = true, int(local.Port())
			} else {
				c.ServerPort = int(remote.Port())
			}
			out = append(out, c)
		}
	}
	return out
}

// Aggregator counts connections into flows across samples. Each
// connection is counted once, the first time it is seen.
type Aggregator struct {
	seen  map[string]bool
	flows map[string]*Flow
}

// NewAggregator returns an empty aggregator.
func NewAggregator() *Aggregator {
	return &Aggregator{seen: map[string]bool{}, flows: map[string]*Flow{}}
}

// Add records one sample taken at unix time now.
func (a *Aggregator) Add(conns []Conn, now int64) {
	cur := map[string]bool{}
	for _, c := range conns {
		k := c.Key()
		cur[k] = true
		dir := "out"
		if c.Inbound {
			dir = "in"
		}
		f := Flow{Direction: dir, Protocol: c.Proto, Port: c.ServerPort, Peer: c.Remote.Addr().String()}
		fk := f.FlowKey()
		agg, ok := a.flows[fk]
		if !ok {
			f.FirstSeen = now
			agg = &f
			a.flows[fk] = agg
		}
		agg.LastSeen = now
		if !a.seen[k] {
			agg.Count++
		}
	}
	a.seen = cur
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
