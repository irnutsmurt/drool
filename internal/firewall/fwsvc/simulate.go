package fwsvc

import (
	"context"
	"fmt"
	"net/netip"
	"strings"

	"drool/internal/firewall/compile"
	"drool/internal/firewall/nftables"
	"drool/internal/firewall/policy"
)

// SimRequest asks "can From reach To on Protocol/Port?" (DESIGN §19.2).
// Each side is a managed host or a literal address.
type SimRequest struct {
	FromHost    string `json:"from_host,omitempty"`
	FromAddress string `json:"from_address,omitempty"`
	ToHost      string `json:"to_host,omitempty"`
	ToAddress   string `json:"to_address,omitempty"`
	Protocol    string `json:"protocol"`
	Port        int    `json:"port"`
}

// SimHop is one host's decision.
type SimHop struct {
	HostID    string         `json:"host_id"`
	Host      string         `json:"host"`
	Direction string         `json:"direction"` // out (source) | in (destination)
	Mode      string         `json:"mode"`
	Filtered  bool           `json:"filtered"` // DROOL enforces a policy on this host
	Verdict   string         `json:"verdict"`  // accept | drop
	Reason    compile.Reason `json:"reason"`
	Rule      string         `json:"rule,omitempty"`
	Notes     []string       `json:"notes,omitempty"`
}

// SimResult is the answer with its reasons.
type SimResult struct {
	Allowed bool     `json:"allowed"`
	Answer  string   `json:"answer"` // YES | NO | PARTLY
	Summary string   `json:"summary"`
	From    string   `json:"from"`
	To      string   `json:"to"`
	Hops    []SimHop `json:"hops"`
	Pairs   []string `json:"pairs"` // the address pairs evaluated
}

type endpoint struct {
	host  *compile.HostRef
	addrs []netip.Addr
	label string
}

func (s *Service) endpoint(w *compile.World, hostID, addr string) (endpoint, error) {
	if hostID != "" {
		h, ok := w.Hosts[hostID]
		if !ok {
			return endpoint{}, ValidationError("unknown host %s", hostID)
		}
		if len(h.Addrs) == 0 {
			return endpoint{}, ValidationError("DROOL doesn't know an address of %s yet (run a discovery)", h.Name)
		}
		return endpoint{host: &h, addrs: h.Addrs, label: h.Name}, nil
	}
	a, err := netip.ParseAddr(strings.TrimSpace(addr))
	if err != nil {
		return endpoint{}, ValidationError("give a host or an IP address")
	}
	return endpoint{addrs: []netip.Addr{a.Unmap()}, label: a.Unmap().String()}, nil
}

func famOf(a netip.Addr) int {
	if a.Is4() {
		return 4
	}
	return 6
}

// Simulate answers a reachability question with the desired policy.
func (s *Service) Simulate(ctx context.Context, req SimRequest) (*SimResult, error) {
	if req.Protocol != policy.TCP && req.Protocol != policy.UDP {
		return nil, ValidationError("protocol must be tcp or udp")
	}
	if req.Port < 1 || req.Port > 65535 {
		return nil, ValidationError("port must be 1-65535")
	}
	w, err := s.World(ctx)
	if err != nil {
		return nil, err
	}
	from, err := s.endpoint(w, req.FromHost, req.FromAddress)
	if err != nil {
		return nil, err
	}
	to, err := s.endpoint(w, req.ToHost, req.ToAddress)
	if err != nil {
		return nil, err
	}
	res := &SimResult{From: from.label, To: to.label, Hops: []SimHop{}, Pairs: []string{}}
	type pair struct{ src, dst netip.Addr }
	var pairs []pair
	for _, a := range from.addrs {
		for _, b := range to.addrs {
			if famOf(a) == famOf(b) {
				pairs = append(pairs, pair{a, b})
			}
		}
	}
	if len(pairs) == 0 {
		return nil, ValidationError("%s and %s have no addresses of the same IP family", from.label, to.label)
	}
	compiled := map[string]*compile.HostPolicy{}
	hostPolicy := func(id string) *compile.HostPolicy {
		if hp, ok := compiled[id]; ok {
			return hp
		}
		h, err := s.d.Store.R.GetHost(ctx, id)
		if err != nil {
			return nil
		}
		hp, _ := s.compileHost(ctx, w, h, false)
		compiled[id] = hp
		return hp
	}
	decide := func(ep endpoint, dir string, peer netip.Addr) SimHop {
		hop := SimHop{HostID: ep.host.ID, Host: ep.host.Name, Direction: dir}
		cfg := s.HostConfig(ctx, ep.host.ID)
		hop.Mode = cfg.Mode
		st, _ := s.d.Store.R.GetFwHostState(ctx, ep.host.ID)
		hp := hostPolicy(ep.host.ID)
		if cfg.Mode != ModeEnforce || hp == nil {
			hop.Verdict = "accept"
			hop.Reason = compile.Reason{Kind: compile.ReasonDefault, Text: fmt.Sprintf("DROOL doesn't filter %s (mode %s)", ep.host.Name, cfg.Mode)}
			return hop
		}
		hop.Filtered = true
		v := hp.Evaluate(dir, compile.Packet{Family: famOf(peer), Proto: req.Protocol, Port: req.Port, Peer: peer})
		hop.Verdict, hop.Reason = v.Verdict, v.Reason
		if v.Rule != nil {
			hop.Rule = compile.DescribeService(*v.Rule) + " " + map[string]string{"in": "from ", "out": "to "}[dir] + v.Rule.Peer.Label
		}
		if st.Enforced == 0 {
			hop.Notes = append(hop.Notes, "this is the policy DROOL wants on "+ep.host.Name+"; it has not been applied yet")
		} else {
			rend := nftables.Render(hp)
			switch {
			case rend.RulesHash != st.AppliedRulesHash:
				hop.Notes = append(hop.Notes, "policy changes for "+ep.host.Name+" are not applied yet")
			case rend.SetsHash != st.AppliedSetsHash:
				hop.Notes = append(hop.Notes, fmt.Sprintf("address sets on %s are still at generation %d (propagation pending)", ep.host.Name, st.SetsGenerationApplied))
			}
			if st.Drift != "" {
				hop.Notes = append(hop.Notes, "DROOL's rules on "+ep.host.Name+" were changed outside DROOL")
			}
		}
		return hop
	}
	yes, no := 0, 0
	for _, p := range pairs {
		res.Pairs = append(res.Pairs, p.src.String()+" → "+p.dst.String())
		hops := []SimHop{}
		if from.host != nil {
			hops = append(hops, decide(from, "out", p.dst))
		}
		if to.host != nil {
			hops = append(hops, decide(to, "in", p.src))
		}
		ok := true
		for _, h := range hops {
			ok = ok && h.Verdict == "accept"
		}
		if ok {
			yes++
		} else {
			no++
		}
		if len(res.Hops) == 0 || !ok {
			res.Hops = hops
		}
	}
	switch {
	case no == 0:
		res.Allowed, res.Answer = true, "YES"
	case yes == 0:
		res.Answer = "NO"
	default:
		res.Answer = "PARTLY"
	}
	var why []string
	for _, h := range res.Hops {
		why = append(why, fmt.Sprintf("%s %s: %s", h.Host, map[string]string{"accept": "allows", "drop": "blocks"}[h.Verdict], h.Reason.Text))
	}
	res.Summary = fmt.Sprintf("%s: %s → %s %s/%d. %s", res.Answer, from.label, to.label, strings.ToUpper(req.Protocol), req.Port, strings.Join(why, "; "))
	if res.Answer == "PARTLY" {
		res.Summary += fmt.Sprintf(" (%d of %d address pairs allowed)", yes, len(pairs))
	}
	return res, nil
}
