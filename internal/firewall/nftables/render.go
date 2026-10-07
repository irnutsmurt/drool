// Package nftables is the first firewall backend (DESIGN §15.5): it renders
// a compiled host policy as one atomic nft file that replaces only DROOL's
// own table (inet drool), produces set-element deltas for address changes,
// and reads the host's ruleset to decide whether enforcement is safe (§16).
package nftables

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strings"

	"drool/internal/firewall/compile"
	"drool/internal/firewall/policy"
)

// Table is the only nftables table DROOL owns.
const Table = "inet drool"

// Empty removes DROOL's table (no DROOL policy). It never touches others.
const Empty = "table inet drool\ndelete table inet drool\n"

// Rendered is a host's nft policy.
type Rendered struct {
	Full      string `json:"full"`       // the file applied with nft -f
	FullHash  string `json:"full_hash"`  // sha256 of Full: what the host verifies
	RulesHash string `json:"rules_hash"` // everything except dynamic set elements
	SetsHash  string `json:"sets_hash"`  // dynamic set elements only
}

func sum(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

var commentRE = regexp.MustCompile(`[^A-Za-z0-9 _.:/()@,+-]`)

func comment(s string) string {
	s = commentRE.ReplaceAllString(s, " ")
	if len(s) > 120 {
		s = s[:120]
	}
	return `comment "` + s + `"`
}

func elem(p netip.Prefix) string {
	if p.IsSingleIP() {
		return p.Addr().String()
	}
	return p.String()
}

func elems(ps []netip.Prefix) string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = elem(p)
	}
	return strings.Join(out, ", ")
}

func ports(rs []policy.PortRange) string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.String()
	}
	return "{ " + strings.Join(out, ", ") + " }"
}

func addrKw(fam int) string {
	if fam == 6 {
		return "ip6"
	}
	return "ip"
}

const (
	icmpErrs     = "icmp type { destination-unreachable, time-exceeded, parameter-problem }"
	icmpv6CtlIn  = "icmpv6 type { destination-unreachable, packet-too-big, time-exceeded, parameter-problem, nd-router-advert, nd-neighbor-solicit, nd-neighbor-advert, mld-listener-query }"
	icmpv6CtlOut = "icmpv6 type { destination-unreachable, packet-too-big, time-exceeded, parameter-problem, nd-router-solicit, nd-neighbor-solicit, nd-neighbor-advert, mld-listener-report, mld2-listener-report }"
)

// RuleText renders one rule.
func RuleText(dir string, r compile.Rule) string {
	var parts []string
	switch r.Special {
	case compile.SpecialInvalid:
		parts = append(parts, "ct state invalid")
	case compile.SpecialEstablished:
		parts = append(parts, "ct state established,related")
	case compile.SpecialLoopback:
		parts = append(parts, map[string]string{"in": `iif "lo"`, "out": `oif "lo"`}[dir])
	case compile.SpecialICMPErrors:
		parts = append(parts, icmpErrs)
	case compile.SpecialICMPv6Ctl:
		parts = append(parts, map[string]string{"in": icmpv6CtlIn, "out": icmpv6CtlOut}[dir])
	case compile.SpecialDHCP:
		parts = append(parts, map[string]string{"in": "udp sport 67 udp dport 68", "out": "udp sport 68 udp dport 67"}[dir])
	case compile.SpecialDHCPv6:
		parts = append(parts, map[string]string{"in": "udp sport 547 udp dport 546", "out": "udp sport 546 udp dport 547"}[dir])
	case "":
		field := "saddr"
		if dir == "out" {
			field = "daddr"
		}
		switch r.Peer.Kind {
		case compile.PeerSet:
			parts = append(parts, fmt.Sprintf("%s %s @%s", addrKw(r.Family), field, r.Peer.Set))
		case compile.PeerNotSet:
			parts = append(parts, fmt.Sprintf("%s %s != @%s", addrKw(r.Family), field, r.Peer.Set))
		case compile.PeerCIDRs:
			parts = append(parts, fmt.Sprintf("%s %s { %s }", addrKw(r.Family), field, elems(r.Peer.CIDRs)))
		}
		switch r.Proto {
		case policy.TCP, policy.UDP:
			if len(r.Ports) > 0 {
				parts = append(parts, r.Proto+" dport "+ports(r.Ports))
			} else {
				parts = append(parts, "meta l4proto "+r.Proto)
			}
		case policy.ICMP:
			parts = append(parts, "icmp type "+r.ICMPType)
		case policy.ICMPv6:
			parts = append(parts, "icmpv6 type "+r.ICMPType)
		}
	}
	text := r.Reason.Text
	if r.Reason.Kind == compile.ReasonManagement {
		text = "management: " + text
	} else if r.Reason.Kind == compile.ReasonBase {
		text = "base: " + text
	}
	parts = append(parts, r.Verdict, comment(text))
	return strings.Join(parts, " ")
}

func renderSet(b *strings.Builder, s compile.Set, withElements bool) {
	typ := "ipv4_addr"
	if s.Family == 6 {
		typ = "ipv6_addr"
	}
	fmt.Fprintf(b, "\tset %s {\n\t\ttype %s\n\t\tflags interval\n", s.Name, typ)
	if withElements && len(s.Elements) > 0 {
		fmt.Fprintf(b, "\t\telements = { %s }\n", elems(s.Elements))
	}
	b.WriteString("\t}\n")
}

func render(hp *compile.HostPolicy, dynamicElements bool) string {
	var b strings.Builder
	b.WriteString("# DROOL host firewall policy. Generated; do not edit (changes are detected as drift).\n")
	b.WriteString("table inet drool\ndelete table inet drool\ntable inet drool {\n")
	for _, s := range hp.Sets {
		renderSet(&b, s, dynamicElements || !s.Dynamic)
	}
	for _, c := range []struct {
		name, hook, dir string
		ch              compile.Chain
	}{{"input", "input", "in", hp.Input}, {"output", "output", "out", hp.Output}} {
		fmt.Fprintf(&b, "\tchain %s {\n\t\ttype filter hook %s priority filter; policy %s;\n", c.name, c.hook, c.ch.Default)
		for _, r := range c.ch.Rules {
			b.WriteString("\t\t" + RuleText(c.dir, r) + "\n")
		}
		b.WriteString("\t}\n")
	}
	b.WriteString("}\n")
	return b.String()
}

func setsDigest(hp *compile.HostPolicy) string {
	var b strings.Builder
	for _, s := range hp.Sets {
		if s.Dynamic {
			fmt.Fprintf(&b, "%s=%s\n", s.Name, elems(s.Elements))
		}
	}
	return sum(b.String())
}

// Render produces a host's nft file and its hashes.
func Render(hp *compile.HostPolicy) Rendered {
	full := render(hp, true)
	return Rendered{Full: full, FullHash: sum(full), RulesHash: sum(render(hp, false)), SetsHash: setsDigest(hp)}
}

// SetDelta returns the atomic set-element update from old to cur (same
// rules); old nil refills every dynamic set. Each set is flushed and
// refilled in one nft transaction, so removals and additions apply
// together (DESIGN §5.3).
func SetDelta(old, cur *compile.HostPolicy) string {
	prev := map[string]string{}
	if old != nil {
		for _, s := range old.Sets {
			prev[s.Name] = elems(s.Elements)
		}
	} else {
		for _, s := range cur.Sets {
			prev[s.Name] = "\x00" // differs from any element list
		}
	}
	var b strings.Builder
	names := []string{}
	for _, s := range cur.Sets {
		if s.Dynamic && prev[s.Name] != elems(s.Elements) {
			names = append(names, s.Name)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		s := cur.SetByName(n)
		fmt.Fprintf(&b, "flush set inet drool %s\n", n)
		if len(s.Elements) > 0 {
			fmt.Fprintf(&b, "add element inet drool %s { %s }\n", n, elems(s.Elements))
		}
	}
	return b.String()
}
