// Package compile turns network intent into a per-host, backend-independent
// policy (DESIGN §15.4–15.5): ordered input/output rules that reference
// named address sets, each rule carrying the reason it exists. The same
// model drives simulation and Explain Host (§19), and the management-path
// validator (§18). nftables rendering lives in package nftables.
package compile

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strings"

	"drool/internal/firewall/policy"
)

// HostRef is a managed host with its validated addresses.
type HostRef struct {
	ID    string
	Name  string
	Addrs []netip.Addr
}

// GroupRef is a group and its member host ids.
type GroupRef struct {
	ID      string
	Name    string
	Members []string
}

// NetworkRef is a named network object.
type NetworkRef struct {
	ID       string
	Name     string
	Prefixes []netip.Prefix
}

// World is everything the compiler reads.
type World struct {
	Hosts    map[string]HostRef
	Groups   map[string]GroupRef
	Networks map[string]NetworkRef
	Services map[string]policy.Service
	Policies []policy.Policy // in evaluation order
	Internal []netip.Prefix
}

// Outbound modes.
const (
	OutboundAllow    = "allow"
	OutboundRestrict = "restrict"
)

// MgmtFlow is connectivity a management path requires on this host
// (DESIGN §8, §18): inbound SSH from the controller or a jump host, or
// outbound to the controller's agent endpoint or to hosts behind this jump.
type MgmtFlow struct {
	Dir    string // in | out
	Proto  string // tcp
	Port   int
	Peers  []netip.Prefix
	Reason string
}

// Input is one host's compilation request.
type Input struct {
	World    *World
	HostID   string
	Outbound string // allow | restrict
	Mgmt     []MgmtFlow
	// OverrideMgmt is the audited emergency override (DESIGN §18): a deny
	// that conflicts with the management path is applied anyway (the
	// dead-man transaction still protects the host).
	OverrideMgmt bool
}

// Reason kinds.
const (
	ReasonBase       = "base"
	ReasonManagement = "management"
	ReasonPolicy     = "policy"
	ReasonDefault    = "default"
)

// Reason says why a rule exists.
type Reason struct {
	Kind       string `json:"kind"`
	PolicyID   string `json:"policy_id,omitempty"`
	PolicyName string `json:"policy_name,omitempty"`
	Text       string `json:"text"`
}

// Peer kinds.
const (
	PeerAny    = "any"
	PeerSet    = "set"
	PeerNotSet = "not_set"
	PeerCIDRs  = "cidrs"
)

// Peer is the other end of a rule: source on input, destination on output.
type Peer struct {
	Kind  string         `json:"kind"`
	Set   string         `json:"set,omitempty"`
	CIDRs []netip.Prefix `json:"cidrs,omitempty"`
	Label string         `json:"label"`
}

// Special base matches that can't be expressed as address/port matches.
const (
	SpecialInvalid     = "ct_invalid"
	SpecialEstablished = "ct_established"
	SpecialLoopback    = "loopback"
	SpecialICMPErrors  = "icmp_errors"
	SpecialICMPv6Ctl   = "icmpv6_control"
	SpecialDHCP        = "dhcp"
	SpecialDHCPv6      = "dhcpv6"
)

// Rule is one ordered match → verdict.
type Rule struct {
	Special  string             `json:"special,omitempty"`
	Family   int                `json:"family"` // 4, 6, or 0 for both
	Proto    string             `json:"proto,omitempty"`
	Ports    []policy.PortRange `json:"ports,omitempty"`
	ICMPType string             `json:"icmp_type,omitempty"`
	Peer     Peer               `json:"peer"`
	Verdict  string             `json:"verdict"` // accept | drop
	Reason   Reason             `json:"reason"`
	// Services are the named services the rule's protocol and ports come
	// from (display only: never rendered, never hashed).
	Services []string `json:"services,omitempty"`
}

// Chain is one direction's rules and default verdict.
type Chain struct {
	Default string `json:"default"` // accept | drop
	Rules   []Rule `json:"rules"`
}

// Set is a named address set. Dynamic sets (groups, hosts) change through
// set-element updates without recompiling rules.
type Set struct {
	Name     string         `json:"name"`
	Family   int            `json:"family"`
	Dynamic  bool           `json:"dynamic"`
	Elements []netip.Prefix `json:"elements"`
	Label    string         `json:"label"`
	Hosts    []string       `json:"hosts,omitempty"` // member host names, for display
}

// Conflict is a deny that would remove a management path.
type Conflict struct {
	Flow       MgmtFlow `json:"flow"`
	PolicyID   string   `json:"policy_id"`
	PolicyName string   `json:"policy_name"`
	Message    string   `json:"message"`
}

// HostPolicy is a host's compiled policy.
type HostPolicy struct {
	HostID    string     `json:"host_id"`
	Input     Chain      `json:"input"`
	Output    Chain      `json:"output"`
	Sets      []Set      `json:"sets"`
	Conflicts []Conflict `json:"conflicts,omitempty"`
	Overrode  []Conflict `json:"overrode,omitempty"` // conflicts applied under the emergency override
	Warnings  []string   `json:"warnings,omitempty"`
}

// SetByName returns a set.
func (hp *HostPolicy) SetByName(name string) *Set {
	for i := range hp.Sets {
		if hp.Sets[i].Name == name {
			return &hp.Sets[i]
		}
	}
	return nil
}

func short(id string) string { h := sha256.Sum256([]byte(id)); return hex.EncodeToString(h[:4]) }

func famOf(a netip.Addr) int {
	if a.Unmap().Is4() {
		return 4
	}
	return 6
}

func addrPrefix(a netip.Addr) netip.Prefix {
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen())
}

// NormalizePrefixes sorts, dedupes and drops prefixes covered by others,
// so interval sets never get overlapping elements.
func NormalizePrefixes(ps []netip.Prefix) []netip.Prefix {
	in := slices.Clone(ps)
	for i := range in {
		in[i] = in[i].Masked()
	}
	sort.Slice(in, func(i, j int) bool {
		if c := in[i].Addr().Compare(in[j].Addr()); c != 0 {
			return c < 0
		}
		return in[i].Bits() < in[j].Bits()
	})
	var out []netip.Prefix
	for _, p := range in {
		covered := false
		for _, q := range out {
			if q.Bits() <= p.Bits() && q.Contains(p.Addr()) {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, p)
		}
	}
	return out
}

type compiler struct {
	in   Input
	w    *World
	sets map[string]*Set
}

// hostsOf returns the managed hosts a destination/source selector covers
// (host, group, any); other kinds describe external peers.
func (c *compiler) hostsOf(sels []policy.Selector) map[string]bool {
	out := map[string]bool{}
	for _, s := range sels {
		switch s.Kind {
		case policy.SelHost:
			if _, ok := c.w.Hosts[s.ID]; ok {
				out[s.ID] = true
			}
		case policy.SelGroup:
			for _, m := range c.w.Groups[s.ID].Members {
				out[m] = true
			}
		case policy.SelAny:
			for id := range c.w.Hosts {
				out[id] = true
			}
		}
	}
	return out
}

func (c *compiler) addrSet(name, label string, dynamic bool, fam int, hosts []string, addrs []netip.Prefix) string {
	if s, ok := c.sets[name]; ok {
		return s.Name
	}
	var el []netip.Prefix
	for _, a := range addrs {
		if (fam == 4) == a.Addr().Unmap().Is4() {
			el = append(el, a)
		}
	}
	c.sets[name] = &Set{Name: name, Family: fam, Dynamic: dynamic, Elements: NormalizePrefixes(el), Label: label, Hosts: hosts}
	return name
}

// peers returns one Peer per family for a selector.
func (c *compiler) peers(s policy.Selector) []struct {
	fam  int
	peer Peer
} {
	type fp = struct {
		fam  int
		peer Peer
	}
	hostAddrs := func(ids []string) ([]netip.Prefix, []string) {
		var ps []netip.Prefix
		var names []string
		for _, id := range ids {
			h, ok := c.w.Hosts[id]
			if !ok {
				continue
			}
			names = append(names, h.Name)
			for _, a := range h.Addrs {
				ps = append(ps, addrPrefix(a))
			}
		}
		sort.Strings(names)
		return ps, names
	}
	var out []fp
	switch s.Kind {
	case policy.SelHost, policy.SelGroup:
		var ids []string
		var label, prefix string
		if s.Kind == policy.SelHost {
			ids = []string{s.ID}
			label, prefix = "host "+c.w.Hosts[s.ID].Name, "h_"+short(s.ID)
		} else {
			ids = c.w.Groups[s.ID].Members
			label, prefix = "group "+c.w.Groups[s.ID].Name, "g_"+short(s.ID)
		}
		ps, names := hostAddrs(ids)
		for _, fam := range []int{4, 6} {
			n := c.addrSet(fmt.Sprintf("%s_%d", prefix, fam), label, true, fam, names, ps)
			out = append(out, fp{fam, Peer{Kind: PeerSet, Set: n, Label: label}})
		}
	case policy.SelInternal, policy.SelInternet:
		for _, fam := range []int{4, 6} {
			n := c.addrSet(fmt.Sprintf("internal_%d", fam), "internal ranges", false, fam, nil, c.w.Internal)
			if s.Kind == policy.SelInternal {
				out = append(out, fp{fam, Peer{Kind: PeerSet, Set: n, Label: "internal networks"}})
			} else {
				out = append(out, fp{fam, Peer{Kind: PeerNotSet, Set: n, Label: "the internet"}})
			}
		}
	case policy.SelCIDR:
		p, err := netip.ParsePrefix(s.CIDR)
		if err == nil {
			p = p.Masked()
			fam := 6
			if p.Addr().Is4() {
				fam = 4
			}
			out = append(out, fp{fam, Peer{Kind: PeerCIDRs, CIDRs: []netip.Prefix{p}, Label: p.String()}})
		}
	case policy.SelNetwork:
		// Like literal CIDRs: a network's prefixes are part of the rules, so
		// changing them is a policy change (a transaction), not a set update.
		n := c.w.Networks[s.ID]
		for _, fam := range []int{4, 6} {
			var ps []netip.Prefix
			for _, p := range n.Prefixes {
				if (fam == 4) == p.Addr().Unmap().Is4() {
					ps = append(ps, p.Masked())
				}
			}
			if len(ps) > 0 {
				out = append(out, fp{fam, Peer{Kind: PeerCIDRs, CIDRs: NormalizePrefixes(ps), Label: "network " + n.Name}})
			}
		}
	case policy.SelAny:
		out = append(out, fp{0, Peer{Kind: PeerAny, Label: "anywhere"}})
	}
	return out
}

type entry struct {
	proto    string
	ports    []policy.PortRange
	icmpType string
	services []string // the named services it comes from
}

func (c *compiler) entries(services []string) ([]entry, []string) {
	var out []entry
	var unknown []string
	byProto := map[string][]policy.PortRange{}
	names := map[string][]string{}
	for _, n := range services {
		svc, ok := c.w.Services[n]
		if !ok {
			unknown = append(unknown, n)
			continue
		}
		for _, e := range svc.Entries {
			switch e.Protocol {
			case policy.TCP, policy.UDP:
				prs, err := policy.ParsePorts(e.Ports)
				if err == nil {
					byProto[e.Protocol] = append(byProto[e.Protocol], prs...)
					if !slices.Contains(names[e.Protocol], n) {
						names[e.Protocol] = append(names[e.Protocol], n)
					}
				}
			case policy.ICMP, policy.ICMPv6:
				out = append(out, entry{proto: e.Protocol, icmpType: e.ICMPType, services: []string{n}})
			}
		}
	}
	for _, p := range []string{policy.TCP, policy.UDP} {
		if prs := byProto[p]; len(prs) > 0 {
			out = append(out, entry{proto: p, ports: mergePorts(prs), services: names[p]})
		}
	}
	return out, unknown
}

func mergePorts(prs []policy.PortRange) []policy.PortRange {
	sort.Slice(prs, func(i, j int) bool { return prs[i].Lo < prs[j].Lo })
	var out []policy.PortRange
	for _, r := range prs {
		if n := len(out); n > 0 && r.Lo <= out[n-1].Hi+1 {
			out[n-1].Hi = max(out[n-1].Hi, r.Hi)
			continue
		}
		out = append(out, r)
	}
	return out
}

func (c *compiler) policyRules(p policy.Policy, peers []policy.Selector, verdict string) []Rule {
	ents, _ := c.entries(p.Services)
	reason := Reason{Kind: ReasonPolicy, PolicyID: p.ID, PolicyName: p.Name, Text: "policy " + p.Name}
	var out []Rule
	for _, sel := range peers {
		for _, fp := range c.peers(sel) {
			for _, e := range ents {
				fam := fp.fam
				switch e.proto {
				case policy.ICMP:
					if fam == 6 {
						continue
					}
					fam = 4
				case policy.ICMPv6:
					if fam == 4 {
						continue
					}
					fam = 6
				}
				out = append(out, Rule{Family: fam, Proto: e.proto, Ports: e.ports, ICMPType: e.icmpType, Peer: fp.peer,
					Verdict: verdict, Reason: reason, Services: e.services})
			}
		}
	}
	return out
}

func base(dir string, restrict bool) []Rule {
	b := func(special string, fam int, text, verdict string) Rule {
		return Rule{Special: special, Family: fam, Peer: Peer{Kind: PeerAny}, Verdict: verdict, Reason: Reason{Kind: ReasonBase, Text: text}}
	}
	if dir == "in" {
		return []Rule{
			b(SpecialInvalid, 0, "invalid packets", "drop"),
			b(SpecialEstablished, 0, "replies to allowed connections", "accept"),
			b(SpecialLoopback, 0, "loopback", "accept"),
			b(SpecialICMPErrors, 4, "ICMP errors (path MTU, unreachable)", "accept"),
			b(SpecialICMPv6Ctl, 6, "IPv6 neighbour discovery and ICMPv6 errors", "accept"),
			b(SpecialDHCP, 4, "DHCP replies", "accept"),
			b(SpecialDHCPv6, 6, "DHCPv6 replies", "accept"),
		}
	}
	rs := []Rule{
		b(SpecialEstablished, 0, "replies on allowed connections", "accept"),
		b(SpecialLoopback, 0, "loopback", "accept"),
	}
	if restrict {
		rs = append(rs,
			b(SpecialICMPErrors, 4, "ICMP errors", "accept"),
			b(SpecialICMPv6Ctl, 6, "IPv6 neighbour discovery and ICMPv6 errors", "accept"),
			b(SpecialDHCP, 4, "DHCP requests", "accept"),
			b(SpecialDHCPv6, 6, "DHCPv6 requests", "accept"))
	}
	return rs
}

// Compile builds one host's policy.
func Compile(in Input) *HostPolicy {
	c := &compiler{in: in, w: in.World, sets: map[string]*Set{}}
	hp := &HostPolicy{HostID: in.HostID}
	restrict := in.Outbound == OutboundRestrict

	var inDeny, inAllow, outDeny, outAllow []Rule
	for _, p := range in.World.Policies {
		if !p.Enabled {
			continue
		}
		if _, unknown := c.entries(p.Services); len(unknown) > 0 {
			hp.Warnings = append(hp.Warnings, fmt.Sprintf("policy %s uses unknown services %s", p.Name, strings.Join(unknown, ", ")))
		}
		verdict := "accept"
		if p.Action == policy.Deny {
			verdict = "drop"
		}
		if c.hostsOf(p.To)[in.HostID] {
			rs := c.policyRules(p, p.From, verdict)
			if verdict == "drop" {
				inDeny = append(inDeny, rs...)
			} else {
				inAllow = append(inAllow, rs...)
			}
		}
		if c.hostsOf(p.From)[in.HostID] && (verdict == "drop" || restrict) {
			rs := c.policyRules(p, p.To, verdict)
			if verdict == "drop" {
				outDeny = append(outDeny, rs...)
			} else {
				outAllow = append(outAllow, rs...)
			}
		}
	}

	// Management path: always before denies, unless a deny conflicts and
	// the emergency override was given (then the deny wins and the
	// dead-man transaction decides).
	mgmt := map[string][]Rule{}
	for _, f := range in.Mgmt {
		deny := inDeny
		if f.Dir == "out" {
			deny = outDeny
		}
		var hits []Conflict
		for _, d := range deny {
			for _, peer := range f.Peers {
				if c.ruleMatches(hp, d, Packet{Family: famOf(peer.Addr()), Proto: f.Proto, Port: f.Port, Peer: peer.Addr()}) {
					hits = append(hits, Conflict{Flow: f, PolicyID: d.Reason.PolicyID, PolicyName: d.Reason.PolicyName,
						Message: fmt.Sprintf("policy %q would remove the management path: %s", d.Reason.PolicyName, f.Reason)})
					break
				}
			}
			if len(hits) > 0 {
				break
			}
		}
		if len(hits) > 0 && in.OverrideMgmt {
			hp.Overrode = append(hp.Overrode, hits...)
			continue
		}
		hp.Conflicts = append(hp.Conflicts, hits...)
		for _, peer := range f.Peers {
			fam := 6
			if peer.Addr().Is4() {
				fam = 4
			}
			mgmt[f.Dir] = append(mgmt[f.Dir], Rule{Family: fam, Proto: f.Proto, Ports: []policy.PortRange{{Lo: f.Port, Hi: f.Port}},
				Peer: Peer{Kind: PeerCIDRs, CIDRs: []netip.Prefix{peer}, Label: peer.String()}, Verdict: "accept",
				Reason: Reason{Kind: ReasonManagement, Text: f.Reason}})
		}
	}

	hp.Input = Chain{Default: "drop", Rules: concat(base("in", restrict), mgmt["in"], inDeny, inAllow)}
	outDefault := "accept"
	if restrict {
		outDefault = "drop"
	}
	hp.Output = Chain{Default: outDefault, Rules: concat(base("out", restrict), mgmt["out"], outDeny, outAllow)}
	if restrict && !c.allowsOut(hp, policy.UDP, 53) {
		hp.Warnings = append(hp.Warnings, "outbound traffic is restricted and no policy allows DNS from this host: name resolution will fail")
	}
	names := make([]string, 0, len(c.sets))
	for n := range c.sets {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		hp.Sets = append(hp.Sets, *c.sets[n])
	}
	return hp
}

func concat(rs ...[]Rule) []Rule {
	var out []Rule
	for _, r := range rs {
		out = append(out, r...)
	}
	return out
}

func (c *compiler) allowsOut(hp *HostPolicy, proto string, port int) bool {
	for _, r := range hp.Output.Rules {
		if r.Verdict == "accept" && r.Special == "" && r.Proto == proto && r.Reason.Kind == ReasonPolicy && inPorts(r.Ports, port) {
			return true
		}
	}
	return false
}

func (c *compiler) ruleMatches(hp *HostPolicy, r Rule, p Packet) bool {
	// Sets aren't in hp yet during compilation: look them up in c.
	tmp := &HostPolicy{}
	for _, s := range c.sets {
		tmp.Sets = append(tmp.Sets, *s)
	}
	return matches(tmp, r, p)
}
