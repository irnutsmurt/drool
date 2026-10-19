package fwsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"

	"drool/internal/firewall/compile"
	"drool/internal/firewall/nftables"
	"drool/internal/store/db"
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
// policy actually applied. Status and Verdict are always the desired
// policy's; Enforcement says whether that is what the host does now.
type Correlation struct {
	Status     string `json:"status"`
	Verdict    string `json:"verdict"`    // accept | drop under the desired policy
	WouldDeny  bool   `json:"would_deny"` // the desired policy drops it (when enforced)
	ReasonKind string `json:"reason_kind"`
	Reason     string `json:"reason"`
	PolicyID   string `json:"policy_id,omitempty"`
	PolicyName string `json:"policy_name,omitempty"`
	Rule       string `json:"rule,omitempty"` // "TCP 443 to the internet"
	// Enforcement is whether the status is in force for this flow:
	// enforced (DROOL's rules on the host decide it so now); changes_pending
	// (DROOL's rules are in force, but decide it differently until the
	// desired policy is applied: AppliedVerdict); drift (DROOL's rules were
	// changed outside DROOL: what the host does isn't known); otherwise
	// nothing of DROOL's is in force and the status is what the policy would
	// do: unmanaged (mode off), monitor, guided, enforce_pending (enforce,
	// not applied yet, or its rules missing from the host).
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
	// applied: the policy in force, when it differs from the desired one
	// (enforced, with rules or address sets not applied yet); unreadable
	// when DROOL can't read it back.
	applied    *compile.HostPolicy
	unreadable bool
	// outbound: the host compiled as if outbound traffic were restricted.
	// With unrestricted outbound the compiler emits no outbound allows, so
	// this is how "a policy covers this outbound flow" is answered — by the
	// same compiler, never a second evaluator.
	outbound *compile.HostPolicy
	fw       HostFirewall
	st       db.FwHostState
}

// Enforcement states besides the modes (Correlation.Enforcement).
const (
	EnforcementDrift   = "drift"           // DROOL's rules changed outside DROOL: unknown
	EnforcementPending = "changes_pending" // in force, deciding this flow differently
)

// enforcement is whether DROOL's rules are in force on a host. The committed
// state decides, not the mode: leaving Enforce keeps the last committed
// revision on the host until someone removes it, and rules missing from the
// host (drift "missing") enforce nothing. DROOL's rules changed outside DROOL
// (any other drift) make what the host does unknown. Then the mode:
// unmanaged (off), monitor, guided, or enforce_pending (enforce, not applied
// yet).
func enforcement(mode string, st db.FwHostState) string {
	switch {
	case st.Drift != "" && st.Drift != DriftMissing:
		return EnforcementDrift
	case st.Enforced != 0 && st.Drift != DriftMissing:
		return "enforced"
	case mode == ModeOff:
		return "unmanaged"
	case mode != ModeEnforce:
		return mode
	default:
		return "enforce_pending"
	}
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
	he.st = st
	he.fw.Enforcement = enforcement(cfg.Mode, st)
	he.fw.Drift = st.Drift != ""
	if st.Enforced != 0 {
		rend := nftables.Render(he.desired)
		he.fw.RulesChanged = rend.RulesHash != st.AppliedRulesHash
		he.fw.SetsPending = !he.fw.RulesChanged && rend.SetsHash != st.AppliedSetsHash
		if he.fw.Enforcement == "enforced" && (he.fw.RulesChanged || he.fw.SetsPending) {
			he.applied, he.unreadable = appliedPolicy(st)
		}
	}
	return he
}

// appliedPolicy is the policy DROOL last applied to a host, as committed;
// unreadable when it can't be read back.
func appliedPolicy(st db.FwHostState) (*compile.HostPolicy, bool) {
	ap := &compile.HostPolicy{}
	if st.AppliedPolicyJson == "" || json.Unmarshal([]byte(st.AppliedPolicyJson), ap) != nil {
		return nil, true
	}
	return ap, false
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
	switch {
	case he.fw.Enforcement == EnforcementDrift:
		out.Notes = append(out.Notes, "DROOL's rules on the host were changed outside DROOL (drift): what it does with connections like this one isn't known; this is what the policy would do")
	case he.st.Enforced != 0 && he.st.Drift == DriftMissing:
		out.Notes = append(out.Notes, "DROOL's rules are missing on the host (changed outside DROOL): nothing of its policy is in force there; this is what the policy would do")
	case he.fw.Enforcement == "unmanaged":
		out.Notes = append(out.Notes, "DROOL does not manage this host's firewall (mode off): this is what the configured policy would do")
	case he.fw.Enforcement == ModeMonitor, he.fw.Enforcement == ModeGuided:
		out.Notes = append(out.Notes, fmt.Sprintf("the firewall is in %s mode: the policy is not enforced yet", he.fw.Enforcement))
	case he.fw.Enforcement == "enforce_pending":
		out.Notes = append(out.Notes, "the policy has not been applied to this host yet")
	case he.fw.Mode != ModeEnforce:
		out.Notes = append(out.Notes, fmt.Sprintf("the firewall is in %s mode, but DROOL's last applied policy (revision %d) is still on the host and filters it: changes aren't applied",
			he.fw.Mode, he.st.AppliedRevision))
	}
	// Changes not applied yet: the policy in force decides, and the status
	// is what the desired policy will do once applied.
	switch {
	case he.applied != nil:
		if av := he.applied.Evaluate(dir, pkt); av.Verdict != v.Verdict {
			out.Enforcement, out.AppliedVerdict = EnforcementPending, av.Verdict
			out.Notes = append(out.Notes, fmt.Sprintf("policy changes are not applied yet: the firewall on the host currently %s this (%s)",
				map[string]string{"accept": "allows", "drop": "drops"}[av.Verdict], av.Reason.Text))
		} else if he.fw.SetsPending {
			out.Notes = append(out.Notes, "address set updates for this host are still pending")
		}
	case he.unreadable:
		out.Enforcement = EnforcementPending
		out.Notes = append(out.Notes, "policy changes are not applied yet, and DROOL can't read back the policy it last applied: what the host does with this now isn't known")
	}
	return out
}
