package fwsvc

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"drool/internal/firewall/compile"
	"drool/internal/firewall/policy"
)

// Where a rule goes (DESIGN §15): the compiler puts a rule on the hosts its
// destination names — a host, a pack or Anywhere — as an inbound rule, and,
// for denies and restricted outbound traffic, on the hosts its source names.
// A destination written as addresses (Internal networks, the internet, a
// network, a CIDR) names no host, even one whose address it contains: such
// a rule is a peer of outbound rules only. Saving one is valid, but a rule
// that lands on no host must say so, or it reads as protection that isn't
// there.

// RuleNotice is a rule that isn't where its wording suggests: on no host at
// all (NoHostRules), or not on a host whose address its destination
// contains (Plan.AddressOnly).
type RuleNotice struct {
	Rule    string `json:"rule"`
	Message string `json:"message"`
	// Hosts are the managed hosts whose addresses the rule's destination
	// contains: the hosts it was probably meant for.
	Hosts []string `json:"hosts"`
}

// placed is the ids of the policies the compiler put on some host, and
// for each host the policies on its inbound chain.
func placed(compiled map[string]*compile.HostPolicy) (onAny map[string]bool, inbound map[string]map[string]bool) {
	onAny, inbound = map[string]bool{}, map[string]map[string]bool{}
	for hostID, hp := range compiled {
		inbound[hostID] = map[string]bool{}
		for _, r := range hp.Input.Rules {
			if r.Reason.Kind == compile.ReasonPolicy {
				onAny[r.Reason.PolicyID] = true
				inbound[hostID][r.Reason.PolicyID] = true
			}
		}
		for _, r := range hp.Output.Rules {
			if r.Reason.Kind == compile.ReasonPolicy {
				onAny[r.Reason.PolicyID] = true
			}
		}
	}
	return onAny, inbound
}

// namesHosts says whether a selector names managed hosts (rather than
// addresses).
func namesHosts(s policy.Selector) bool {
	return s.Kind == policy.SelHost || s.Kind == policy.SelGroup || s.Kind == policy.SelAny
}

func selLabel(w *compile.World, s policy.Selector) string {
	switch s.Kind {
	case policy.SelHost:
		return w.Hosts[s.ID].Name
	case policy.SelGroup:
		return "pack " + w.Groups[s.ID].Name
	case policy.SelNetwork:
		return "network " + w.Networks[s.ID].Name
	case policy.SelCIDR:
		return s.CIDR
	case policy.SelInternal:
		return "Internal networks"
	case policy.SelInternet:
		return "the internet"
	}
	return "Anywhere"
}

func selLabels(w *compile.World, sels []policy.Selector) string {
	var out []string
	for _, s := range sels {
		out = append(out, selLabel(w, s))
	}
	return joinAnd(out)
}

func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

// covers says whether an address selector contains a.
func covers(w *compile.World, s policy.Selector, a netip.Addr) bool {
	in := func(ps []netip.Prefix) bool {
		for _, p := range ps {
			if p.Contains(a) {
				return true
			}
		}
		return false
	}
	switch s.Kind {
	case policy.SelInternal:
		return in(w.Internal)
	case policy.SelInternet:
		return !in(w.Internal)
	case policy.SelNetwork:
		return in(w.Networks[s.ID].Prefixes)
	case policy.SelCIDR:
		p, err := netip.ParsePrefix(s.CIDR)
		return err == nil && p.Masked().Contains(a)
	}
	return false
}

// coveredAddrs is the host's addresses an address selector of sels
// contains.
func coveredAddrs(w *compile.World, sels []policy.Selector, h compile.HostRef) []string {
	var out []string
	for _, a := range h.Addrs {
		for _, s := range sels {
			if !namesHosts(s) && covers(w, s, a) {
				out = append(out, a.String())
				break
			}
		}
	}
	return out
}

func coveredHosts(w *compile.World, sels []policy.Selector) []string {
	var out []string
	for _, h := range w.Hosts {
		if len(coveredAddrs(w, sels, h)) > 0 {
			out = append(out, h.Name)
		}
	}
	slices.SortFunc(out, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	return out
}

// sourceHosts are the managed hosts a rule's source names (Anywhere names
// them all).
func sourceHosts(w *compile.World, sels []policy.Selector) []string {
	set := map[string]bool{}
	for _, s := range sels {
		switch s.Kind {
		case policy.SelAny:
			for _, h := range w.Hosts {
				set[h.Name] = true
			}
		case policy.SelHost:
			if h, ok := w.Hosts[s.ID]; ok {
				set[h.Name] = true
			}
		case policy.SelGroup:
			for _, id := range w.Groups[s.ID].Members {
				if h, ok := w.Hosts[id]; ok {
					set[h.Name] = true
				}
			}
		}
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

func verb(p policy.Policy) string {
	if p.Action == policy.Deny {
		return "deny"
	}
	return "allow"
}

// noHostRules lists the enabled rules the compiler put on no host, saying
// why and what was probably meant.
func noHostRules(w *compile.World, compiled map[string]*compile.HostPolicy) []RuleNotice {
	out := []RuleNotice{}
	on, _ := placed(compiled)
	for _, p := range w.Policies {
		if !p.Enabled || on[p.ID] {
			continue
		}
		n := RuleNotice{Rule: p.Name, Hosts: coveredHosts(w, p.To)}
		if n.Hosts == nil {
			n.Hosts = []string{}
		}
		dest := selLabels(w, p.To)
		var b strings.Builder
		fmt.Fprintf(&b, "Rule %s applies to no host.", p.Name)
		if !slices.ContainsFunc(p.To, namesHosts) {
			fmt.Fprintf(&b, " Its destination, %s, is a set of addresses, not a DROOL host: DROOL puts a rule on the hosts its destination names (a host, a pack, or Anywhere).", dest)
			if len(n.Hosts) > 0 {
				whose, pron := joinAnd(clip(n.Hosts, 5))+"'s address is", "it"
				if len(n.Hosts) > 1 {
					whose, pron = "The addresses of "+joinAnd(clip(n.Hosts, 5))+" are", "them"
				}
				fmt.Fprintf(&b, " %s in %s: to %s %s to %s, make %s the destination.", whose, dest, verb(p), strings.Join(p.Services, ", "), pron, pron)
			}
		} else {
			fmt.Fprintf(&b, " Its destination, %s, has no managed host in it.", dest)
		}
		if src := sourceHosts(w, p.From); len(src) > 0 && p.Action == policy.Allow {
			fmt.Fprintf(&b, " For %s (its source) it would allow outbound connections, which aren't restricted there anyway.", joinAnd(clip(src, 5)))
		}
		n.Message = b.String()
		out = append(out, n)
	}
	return out
}

func clip(xs []string, n int) []string {
	if len(xs) <= n {
		return xs
	}
	return append(slices.Clone(xs[:n]), fmt.Sprintf("%d more", len(xs)-n))
}

// addressOnly lists the enabled rules whose destination contains one of
// the host's addresses but which aren't on its inbound chain: they name
// the host's address, not the host.
func addressOnly(w *compile.World, hp *compile.HostPolicy, hostID string) []RuleNotice {
	out := []RuleNotice{}
	h, ok := w.Hosts[hostID]
	if !ok {
		return out
	}
	_, inbound := placed(map[string]*compile.HostPolicy{hostID: hp})
	for _, p := range w.Policies {
		if !p.Enabled || inbound[hostID][p.ID] {
			continue
		}
		addrs := coveredAddrs(w, p.To, h)
		if len(addrs) == 0 {
			continue
		}
		var dests []string
		for _, s := range p.To {
			if !namesHosts(s) {
				for _, a := range h.Addrs {
					if covers(w, s, a) {
						dests = append(dests, selLabel(w, s))
						break
					}
				}
			}
		}
		out = append(out, RuleNotice{Rule: p.Name, Hosts: []string{h.Name}, Message: fmt.Sprintf(
			"Rule %s isn't part of this host's policy: its destination, %s, contains this host's address (%s) but doesn't name the host, and DROOL puts a rule on the hosts its destination names. To %s %s to %s, make %s the destination.",
			p.Name, joinAnd(dests), strings.Join(addrs, ", "), verb(p), strings.Join(p.Services, ", "), h.Name, h.Name)})
	}
	return out
}

// NoHostRules lists the saved policy's enabled rules that apply to no host.
func (s *Service) NoHostRules(ctx context.Context) ([]RuleNotice, error) {
	w, err := s.World(ctx)
	if err != nil {
		return nil, err
	}
	hosts, err := s.d.Store.R.ListHosts(ctx)
	if err != nil {
		return nil, err
	}
	compiled := make(map[string]*compile.HostPolicy, len(hosts))
	for _, h := range hosts {
		compiled[h.ID], _ = s.compileHost(ctx, w, h, false)
	}
	return noHostRules(w, compiled), nil
}
