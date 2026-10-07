package fwsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"

	"drool/internal/firewall/compile"
	"drool/internal/firewall/nftables"
)

// Policy statuses of an observed flow (M10). The firewall compiler is the
// only judge: the network views never decide firewall semantics.
const (
	FlowManagement   = "management"   // a protected DROOL management path
	FlowCovered      = "covered"      // allowed by a configured policy (or a base rule)
	FlowUnclassified = "unclassified" // no policy says anything about it
	FlowDenied       = "denied"       // a deny policy, or the default drop on an enforced host
)

// Correlation is the firewall's view of one observed flow, against the
// desired policy (what DROOL would enforce) and, on enforced hosts, the
// policy actually applied.
type Correlation struct {
	Status     string `json:"status"`
	Verdict    string `json:"verdict"`    // accept | drop under the desired policy
	WouldDeny  bool   `json:"would_deny"` // the desired policy drops it (when enforced)
	ReasonKind string `json:"reason_kind"`
	Reason     string `json:"reason"`
	PolicyID   string `json:"policy_id,omitempty"`
	PolicyName string `json:"policy_name,omitempty"`
	Rule       string `json:"rule,omitempty"` // "TCP 443 to the internet"
	// Enforcement is the host's firewall state: unmanaged (mode off),
	// monitor, guided, enforce_pending (enforce, not applied yet), enforced.
	Enforcement string `json:"enforcement"`
	// Applied is what the policy applied on the host does, when it differs
	// from the desired one (changes not applied yet).
	AppliedVerdict string   `json:"applied_verdict,omitempty"`
	Notes          []string `json:"notes"`
}

// HostFirewall summarises a host's firewall state for the network views.
type HostFirewall struct {
	Mode        string `json:"mode"`
	Enforcement string `json:"enforcement"`
	Outbound    string `json:"outbound"`
	// Stale: enforced, but the desired rules differ from the applied ones
	// (or only address sets are behind).
	RulesChanged bool `json:"rules_changed"`
	SetsPending  bool `json:"sets_pending"`
	Drift        bool `json:"drift"`
}

type hostEval struct {
	desired *compile.HostPolicy
	applied *compile.HostPolicy // nil unless enforced with different rules
	// outbound: the host compiled as if outbound traffic were restricted.
	// With unrestricted outbound the compiler emits no outbound allows, so
	// this is how "a policy covers this outbound flow" is answered — by the
	// same compiler, never a second evaluator.
	outbound *compile.HostPolicy
	fw       HostFirewall
}

// Classifier correlates observations with the firewall policy. It compiles
// each host at most once (lazily), from one consistent snapshot of the
// world; make a new one per request.
type Classifier struct {
	s     *Service
	w     *compile.World
	hosts map[string]*hostEval
	idx   map[netip.Addr]compile.HostRef
}

// NewClassifier loads the world.
func (s *Service) NewClassifier(ctx context.Context) (*Classifier, error) {
	w, err := s.World(ctx)
	if err != nil {
		return nil, err
	}
	return &Classifier{s: s, w: w, hosts: map[string]*hostEval{}, idx: addrIndex(w)}, nil
}

// World is the snapshot the classifier uses.
func (c *Classifier) World() *compile.World { return c.w }

// HostByAddr finds the DROOL host that owns an address.
func (c *Classifier) HostByAddr(a netip.Addr) (compile.HostRef, bool) {
	h, ok := c.idx[a.Unmap()]
	return h, ok
}

func (c *Classifier) host(ctx context.Context, hostID string) *hostEval {
	if he, ok := c.hosts[hostID]; ok {
		return he
	}
	he := &hostEval{}
	c.hosts[hostID] = he
	h, err := c.s.d.Store.R.GetHost(ctx, hostID)
	if err != nil {
		return he
	}
	cfg := c.s.HostConfig(ctx, hostID)
	he.desired, _ = c.s.compileHost(ctx, c.w, h, false)
	if cfg.Outbound != compile.OutboundRestrict {
		flows, _ := c.s.mgmtFlows(ctx, c.w, h)
		he.outbound = compile.Compile(compile.Input{World: c.w, HostID: h.ID, Outbound: compile.OutboundRestrict, Mgmt: flows})
	}
	he.fw = HostFirewall{Mode: cfg.Mode, Outbound: cfg.Outbound}
	st, _ := c.s.d.Store.R.GetFwHostState(ctx, hostID)
	switch {
	case cfg.Mode == ModeOff:
		he.fw.Enforcement = "unmanaged"
	case cfg.Mode != ModeEnforce:
		he.fw.Enforcement = cfg.Mode
	case st.Enforced == 0:
		he.fw.Enforcement = "enforce_pending"
	default:
		he.fw.Enforcement = "enforced"
	}
	he.fw.Drift = st.Drift != ""
	if st.Enforced != 0 {
		rend := nftables.Render(he.desired)
		he.fw.RulesChanged = rend.RulesHash != st.AppliedRulesHash
		he.fw.SetsPending = !he.fw.RulesChanged && rend.SetsHash != st.AppliedSetsHash
		if he.fw.RulesChanged && st.AppliedPolicyJson != "" {
			ap := &compile.HostPolicy{}
			if json.Unmarshal([]byte(st.AppliedPolicyJson), ap) == nil {
				he.applied = ap
			}
		}
	}
	return he
}

// Firewall returns a host's firewall summary.
func (c *Classifier) Firewall(ctx context.Context, hostID string) HostFirewall {
	return c.host(ctx, hostID).fw
}

func ruleText(v compile.Verdict, dir string) string {
	if v.Rule == nil || v.Rule.Special != "" {
		return ""
	}
	return compile.DescribeService(*v.Rule) + map[string]string{"in": " from ", "out": " to "}[dir] + v.Rule.Peer.Label
}

// Classify correlates one flow: host, direction (in/out), protocol, the
// service port and the remote address.
func (c *Classifier) Classify(ctx context.Context, hostID, dir, proto string, port int, peer netip.Addr) Correlation {
	he := c.host(ctx, hostID)
	out := Correlation{Enforcement: he.fw.Enforcement, Notes: []string{}}
	if he.desired == nil {
		out.Status, out.Reason = FlowUnclassified, "the host is unknown"
		return out
	}
	peer = peer.Unmap()
	pkt := compile.Packet{Family: famOf(peer), Proto: proto, Port: port, Peer: peer}
	v := he.desired.Evaluate(dir, pkt)
	out.Verdict, out.ReasonKind, out.Reason = v.Verdict, v.Reason.Kind, v.Reason.Text
	out.PolicyID, out.PolicyName, out.Rule = v.Reason.PolicyID, v.Reason.PolicyName, ruleText(v, dir)
	out.WouldDeny = v.Verdict == "drop"
	enforced := he.fw.Enforcement == "enforced"
	switch v.Reason.Kind {
	case compile.ReasonManagement:
		out.Status = FlowManagement
	case compile.ReasonPolicy:
		out.Status = FlowCovered
		if v.Verdict == "drop" {
			out.Status = FlowDenied
		}
	case compile.ReasonDefault:
		// The default: nothing in the policy speaks for this flow. On an
		// enforced host a dropping default is a denial; elsewhere it is
		// unclassified — what Guided mode suggests policies for.
		out.Status = FlowUnclassified
		if v.Verdict == "drop" && enforced {
			out.Status = FlowDenied
		}
		// Unrestricted outbound: is it one a policy names anyway?
		if dir == "out" && he.outbound != nil && v.Verdict == "accept" {
			if ov := he.outbound.Evaluate(dir, pkt); ov.Reason.Kind == compile.ReasonPolicy && ov.Verdict == "accept" {
				out.Status, out.ReasonKind = FlowCovered, ov.Reason.Kind
				out.Reason = ov.Reason.Text
				out.PolicyID, out.PolicyName, out.Rule = ov.Reason.PolicyID, ov.Reason.PolicyName, ruleText(ov, dir)
				out.Notes = append(out.Notes, "outbound traffic is not restricted on this host: the policy describes this connection, the firewall doesn't need it")
			}
		}
	default: // base rules (DHCP, ICMP errors, loopback)
		out.Status = FlowCovered
	}
	switch he.fw.Enforcement {
	case "unmanaged":
		out.Notes = append(out.Notes, "DROOL does not manage this host's firewall (mode off): this is what the configured policy would do")
	case ModeMonitor, ModeGuided:
		out.Notes = append(out.Notes, fmt.Sprintf("the firewall is in %s mode: the policy is not enforced yet", he.fw.Enforcement))
	case "enforce_pending":
		out.Notes = append(out.Notes, "the policy has not been applied to this host yet")
	}
	if he.applied != nil {
		av := he.applied.Evaluate(dir, pkt)
		if av.Verdict != v.Verdict {
			out.AppliedVerdict = av.Verdict
			out.Notes = append(out.Notes, fmt.Sprintf("policy changes are not applied yet: the firewall on the host currently %s this (%s)",
				map[string]string{"accept": "allows", "drop": "drops"}[av.Verdict], av.Reason.Text))
		}
	} else if he.fw.SetsPending {
		out.Notes = append(out.Notes, "address set updates for this host are still pending")
	}
	if he.fw.Drift {
		out.Notes = append(out.Notes, "the host's firewall was changed outside DROOL (drift)")
	}
	return out
}
