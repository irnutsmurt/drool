package compile

import (
	"fmt"
	"net/netip"
	"strings"

	"drool/internal/firewall/policy"
)

// Packet is a new connection attempt, for simulation.
type Packet struct {
	Family   int
	Proto    string
	Port     int // destination port
	ICMPType string
	Peer     netip.Addr // source on input, destination on output
}

func inPorts(rs []policy.PortRange, p int) bool {
	for _, r := range rs {
		if p >= r.Lo && p <= r.Hi {
			return true
		}
	}
	return false
}

var icmpErrors = map[string]bool{"destination-unreachable": true, "time-exceeded": true, "parameter-problem": true}

func (s *Set) contains(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range s.Elements {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func matches(hp *HostPolicy, r Rule, p Packet) bool {
	switch r.Special {
	case "":
	case SpecialLoopback:
		return p.Peer.IsLoopback()
	case SpecialICMPErrors:
		return p.Proto == policy.ICMP && icmpErrors[p.ICMPType]
	case SpecialICMPv6Ctl:
		return p.Proto == policy.ICMPv6 && (icmpErrors[p.ICMPType] || strings.HasPrefix(p.ICMPType, "nd-") || p.ICMPType == "packet-too-big")
	case SpecialDHCP:
		return p.Proto == policy.UDP && (p.Port == 67 || p.Port == 68)
	case SpecialDHCPv6:
		return p.Proto == policy.UDP && (p.Port == 546 || p.Port == 547)
	default:
		return false // connection-tracking state: a new connection never matches
	}
	if r.Family != 0 && r.Family != p.Family {
		return false
	}
	if r.Proto != "" && r.Proto != p.Proto {
		return false
	}
	if len(r.Ports) > 0 && !inPorts(r.Ports, p.Port) {
		return false
	}
	if r.ICMPType != "" && r.ICMPType != p.ICMPType {
		return false
	}
	switch r.Peer.Kind {
	case PeerAny:
		return true
	case PeerSet, PeerNotSet:
		s := hp.SetByName(r.Peer.Set)
		in := s != nil && s.contains(p.Peer)
		return in == (r.Peer.Kind == PeerSet)
	case PeerCIDRs:
		for _, c := range r.Peer.CIDRs {
			if c.Contains(p.Peer.Unmap()) {
				return true
			}
		}
	}
	return false
}

// Verdict is the outcome of evaluating one chain.
type Verdict struct {
	Verdict string `json:"verdict"` // accept | drop
	Rule    *Rule  `json:"rule,omitempty"`
	Reason  Reason `json:"reason"`
}

// Evaluate runs a new connection through a chain ("in" or "out").
func (hp *HostPolicy) Evaluate(dir string, p Packet) Verdict {
	ch := hp.Input
	if dir == "out" {
		ch = hp.Output
	}
	for i := range ch.Rules {
		if matches(hp, ch.Rules[i], p) {
			r := ch.Rules[i]
			return Verdict{Verdict: r.Verdict, Rule: &r, Reason: r.Reason}
		}
	}
	text := "no rule allows it (default: drop)"
	if ch.Default == "accept" {
		text = "outbound traffic is not restricted on this host"
		if dir == "in" {
			text = "default: accept"
		}
	}
	return Verdict{Verdict: ch.Default, Reason: Reason{Kind: ReasonDefault, Text: text}}
}

// Allowance is one line of Explain Host (DESIGN §19.3).
type Allowance struct {
	Direction string   `json:"direction"` // in | out
	Verdict   string   `json:"verdict"`   // accept | drop
	Service   string   `json:"service"`   // "TCP 5432", "UDP/TCP 53", "ICMP echo-request", or a base description
	Peer      string   `json:"peer"`      // "group web", "the internet", "10.0.2.2/32", "anywhere"
	Members   []string `json:"members,omitempty"`
	Family    int      `json:"family"`
	Reason    Reason   `json:"reason"`
	// Services: the named services behind Service ("esphome" for
	// "TCP 6053"), for a policy's rules.
	Services []string `json:"services,omitempty"`
}

// DescribeService renders a rule's protocol and ports.
func DescribeService(r Rule) string {
	switch {
	case r.Special != "":
		return r.Reason.Text
	case r.ICMPType != "":
		return strings.ToUpper(r.Proto) + " " + r.ICMPType
	case r.Proto == "":
		return "all traffic"
	}
	ps := make([]string, len(r.Ports))
	for i, p := range r.Ports {
		ps[i] = p.String()
	}
	return strings.ToUpper(r.Proto) + " " + strings.Join(ps, ", ")
}

// Explain lists the host's allowances and denies with their reasons. The
// two families of one rule are shown once.
func (hp *HostPolicy) Explain() []Allowance {
	var out []Allowance
	seen := map[string]bool{}
	for _, dir := range []string{"in", "out"} {
		ch := hp.Input
		if dir == "out" {
			ch = hp.Output
		}
		for _, r := range ch.Rules {
			a := Allowance{Direction: dir, Verdict: r.Verdict, Service: DescribeService(r), Peer: r.Peer.Label, Family: r.Family, Reason: r.Reason,
				Services: r.Services}
			if r.Peer.Kind == PeerAny || a.Peer == "" {
				a.Peer = "anywhere"
			}
			if s := hp.SetByName(r.Peer.Set); s != nil {
				a.Members = s.Hosts
			}
			key := fmt.Sprintf("%s|%s|%s|%s|%s|%s", dir, a.Verdict, a.Service, a.Peer, a.Reason.Kind, a.Reason.PolicyID+a.Reason.Text)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, a)
		}
		def := Allowance{Direction: dir, Verdict: ch.Default, Service: "everything else", Peer: "anywhere",
			Reason: Reason{Kind: ReasonDefault, Text: "default for " + map[string]string{"in": "inbound", "out": "outbound"}[dir] + " traffic"}}
		out = append(out, def)
	}
	return out
}
