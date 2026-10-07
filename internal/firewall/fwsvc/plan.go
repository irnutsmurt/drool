package fwsvc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"sort"
	"strings"
	"time"

	"drool/internal/driver"
	"drool/internal/firewall/compile"
	"drool/internal/firewall/hostfw"
	"drool/internal/firewall/nftables"
	"drool/internal/hostexec"
	"drool/internal/hostops"
	"drool/internal/model"
	"drool/internal/store/db"
)

// FeatureFirewall is what an agent announces to receive firewall operations.
const FeatureFirewall = "firewall-1"

// conn runs host tooling actions over one management path.
type conn interface {
	Run(ctx context.Context, a hostfw.Action) (*hostfw.Result, error)
	Transport() string
	Close()
}

type nopSink struct{}

func (nopSink) Stdout() io.Writer { return io.Discard }
func (nopSink) Stderr() io.Writer { return io.Discard }

type agentConn struct {
	s      *Service
	hostID string
}

func (c *agentConn) Transport() string { return "agent" }
func (c *agentConn) Close()            {}

func (c *agentConn) Run(ctx context.Context, a hostfw.Action) (*hostfw.Result, error) {
	op := model.Operation{Type: model.OpFirewall, Firewall: &a}
	res, err := c.s.d.Agents.Execute(ctx, c.hostID, op, nopSink{}, nil, nil)
	if err != nil {
		return nil, err
	}
	return resultFromData(res)
}

// resultFromData decodes the agent's firewall result.
func resultFromData(res *driver.OpResult) (*hostfw.Result, error) {
	if res == nil || res.Data == nil {
		return nil, &model.OpError{Class: model.ClassExecution, Message: "the agent returned no firewall result"}
	}
	b, _ := json.Marshal(res.Data)
	var d struct {
		Values   map[string]string   `json:"values"`
		Multi    map[string][]string `json:"multi"`
		Sections map[string]string   `json:"sections"`
		ErrCode  string              `json:"err_code"`
		ErrMsg   string              `json:"err_msg"`
		Exit     int                 `json:"exit"`
		Stderr   string              `json:"stderr"`
	}
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, err
	}
	r := &hostfw.Result{Values: d.Values, Multi: d.Multi, Sections: d.Sections, ErrCode: d.ErrCode, ErrMsg: d.ErrMsg, Exit: d.Exit, Stderr: d.Stderr}
	if r.Values == nil {
		r.Values = map[string]string{}
	}
	if r.Multi == nil {
		r.Multi = map[string][]string{}
	}
	if r.Sections == nil {
		r.Sections = map[string]string{}
	}
	return r, nil
}

type sshConn struct {
	r       hostexec.Runner
	b       hostops.Become
	p       hostfw.Paths
	release func()
}

func (c *sshConn) Transport() string { return "ssh" }
func (c *sshConn) Close() {
	if c.release != nil {
		c.release()
	}
}
func (c *sshConn) Run(ctx context.Context, a hostfw.Action) (*hostfw.Result, error) {
	return hostfw.Run(ctx, c.r, c.b, c.p, a)
}

func (s *Service) sshConn(ctx context.Context, hostID string, fresh bool) (*sshConn, error) {
	r, b, release, err := s.d.Inv.RootRunner(ctx, hostID, fresh)
	if err != nil {
		return nil, &driver.NotStartedError{Err: err}
	}
	return &sshConn{r: r, b: b, p: s.toolPaths(), release: release}, nil
}

func hasFeature(fs []string, f string) bool {
	for _, x := range fs {
		if x == f {
			return true
		}
	}
	return false
}

// openConn picks the management path for firewall actions under the M7
// rules: the agent when it is preferred and connected, SSH when allowed.
// Blocked hosts (revoked, quarantined) get nothing.
func (s *Service) openConn(ctx context.Context, h db.Host) (conn, string, error) {
	d := s.d.Decide(ctx, h)
	if d.Blocked {
		return nil, "", &model.OpError{Class: model.ClassAuthorization, Message: d.Reason}
	}
	note := ""
	if d.UseAgent && s.d.Agents != nil {
		if fs, ok := s.d.Agents.HostFirewallSession(h.ID); ok {
			if hasFeature(fs.Features, FeatureFirewall) {
				return &agentConn{s: s, hostID: h.ID}, "", nil
			}
			note = "the agent on this host is too old for firewall management (update it)"
		}
	}
	if d.SSHAllowed && h.SshConfigured != 0 {
		c, err := s.sshConn(ctx, h.ID, false)
		if err != nil {
			return nil, "", err
		}
		if note == "" && h.ManagementMethod == "agent" {
			note = "SSH fallback: " + d.Reason
		}
		return c, note, nil
	}
	if note == "" {
		note = d.Reason
	}
	if note == "" {
		note = "no usable management path"
	}
	return nil, "", &driver.NotStartedError{Err: &model.OpError{Class: model.ClassConnection, Message: note}}
}

// --- management paths (DESIGN §8, §18) ---

func (s *Service) sshPathConfigured(h db.Host) bool {
	return h.SshConfigured != 0 && (h.ManagementMethod == "ssh" || h.ManagementFallback == "ssh")
}

// mgmtFlows is the connectivity the host's management paths need, from
// what the host itself observed (SSH_CONNECTION over a fresh connection;
// the agent's resolved controller endpoints) and the configured topology
// (hosts reached through this one as a jump host).
func (s *Service) mgmtFlows(ctx context.Context, w *compile.World, h db.Host) ([]compile.MgmtFlow, []string) {
	q := s.d.Store.R
	obs, _ := q.ListFwMgmtObservations(ctx, h.ID)
	var flows []compile.MgmtFlow
	var warnings []string
	group := func(kind string) map[int][]netip.Prefix {
		out := map[int][]netip.Prefix{}
		for _, o := range obs {
			if o.Kind != kind {
				continue
			}
			if a, err := netip.ParseAddr(o.Address); err == nil {
				a = a.Unmap()
				out[int(o.Port)] = append(out[int(o.Port)], netip.PrefixFrom(a, a.BitLen()))
			}
		}
		return out
	}
	ports := func(m map[int][]netip.Prefix) []int {
		var ps []int
		for p := range m {
			ps = append(ps, p)
		}
		sort.Ints(ps)
		return ps
	}
	if s.sshPathConfigured(h) {
		via := "the controller"
		if j := s.d.Inv.JumpHostID(ctx, h.ID); j != "" {
			via = "jump host " + w.Hosts[j].Name
		}
		in := group("ssh_in")
		if len(in) == 0 {
			warnings = append(warnings, "the SSH management path has not been observed yet; it is measured over a fresh connection when a policy is applied")
		}
		for _, p := range ports(in) {
			flows = append(flows, compile.MgmtFlow{Dir: "in", Proto: "tcp", Port: p, Peers: compile.NormalizePrefixes(in[p]),
				Reason: fmt.Sprintf("SSH management from %s (seen as %s, port %d)", via, prefixList(in[p]), p)})
		}
	}
	if h.ManagementMethod == "agent" {
		out := group("agent_out")
		if len(out) == 0 {
			warnings = append(warnings, "the agent's controller endpoints are not known yet (reported when the agent connects)")
		}
		for _, p := range ports(out) {
			flows = append(flows, compile.MgmtFlow{Dir: "out", Proto: "tcp", Port: p, Peers: compile.NormalizePrefixes(out[p]),
				Reason: fmt.Sprintf("agent connection to the controller (%s, port %d)", prefixList(out[p]), p)})
		}
	}
	// This host as a jump host: the controller reaches hosts behind it.
	if targets, err := q.ListHostsViaHop(ctx, h.ID); err == nil {
		for _, t := range targets {
			th, err := q.GetHost(ctx, t.ID)
			if err != nil {
				continue
			}
			var peers []netip.Prefix
			if a, err := netip.ParseAddr(th.ConnectAddress); err == nil {
				peers = append(peers, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
			} else {
				for _, a := range w.Hosts[th.ID].Addrs {
					peers = append(peers, netip.PrefixFrom(a, a.BitLen()))
				}
			}
			if len(peers) == 0 {
				warnings = append(warnings, fmt.Sprintf("%s is reached through this host, but its address isn't known", th.Name))
				continue
			}
			flows = append(flows, compile.MgmtFlow{Dir: "out", Proto: "tcp", Port: int(th.SshPort), Peers: compile.NormalizePrefixes(peers),
				Reason: fmt.Sprintf("SSH management of %s through this jump host", th.Name)})
		}
	}
	return flows, warnings
}

func prefixList(ps []netip.Prefix) string {
	var out []string
	for _, p := range compile.NormalizePrefixes(ps) {
		if p.IsSingleIP() {
			out = append(out, p.Addr().String())
		} else {
			out = append(out, p.String())
		}
	}
	return strings.Join(out, ", ")
}

// --- per-host plans ---

// Applied is what DROOL last committed on the host.
type Applied struct {
	Enforced       bool   `json:"enforced"`
	Revision       int64  `json:"revision"`
	RulesHash      string `json:"rules_hash"`
	SetsHash       string `json:"sets_hash"`
	OverrideMgmt   bool   `json:"override_mgmt"`
	SetsGeneration int64  `json:"sets_generation"`
	SetsApplied    int64  `json:"sets_generation_applied"`
	SetsError      string `json:"sets_error,omitempty"`
	Drift          string `json:"drift,omitempty"`
	StatusAt       string `json:"status_at,omitempty"`
	RulesChanged   bool   `json:"rules_changed"` // the desired rules differ: a transaction is needed
	SetsPending    bool   `json:"sets_pending"`  // only set elements differ: propagation pending
}

// Plan is a host's firewall picture: settings, environment, the policy
// DROOL wants there and how it compares with what is committed.
type Plan struct {
	HostID       string                `json:"host_id"`
	HostName     string                `json:"host_name"`
	Config       HostConfig            `json:"config"`
	TimeoutS     int                   `json:"rollback_timeout_s"`
	TimeoutFrom  string                `json:"rollback_timeout_from"`
	Policy       *compile.HostPolicy   `json:"policy"`
	Rendered     nftables.Rendered     `json:"rendered"`
	MgmtWarnings []string              `json:"management_warnings,omitempty"`
	Env          *nftables.Env         `json:"environment,omitempty"`
	Eligibility  *nftables.Eligibility `json:"eligibility,omitempty"`
	EnvChecked   string                `json:"environment_checked_at,omitempty"`
	Applied      Applied               `json:"applied"`
	Pending      *TxRow                `json:"pending_transaction,omitempty"`
	Transport    string                `json:"transport"`
	Learning     string                `json:"learning"` // continuous | snapshot | off
	Explain      []compile.Allowance   `json:"explain"`
	// Changes since the committed policy (empty when nothing is enforced).
	ChangesAdded   []string            `json:"changes_added"`
	ChangesRemoved []string            `json:"changes_removed"`
	AppliedHP      *compile.HostPolicy `json:"-"`
	state          *db.FwHostState
	host           db.Host
	world          *compile.World
}

func (s *Service) state(ctx context.Context, hostID string) (*db.FwHostState, error) {
	if err := s.d.Store.W.EnsureFwHostState(ctx, db.EnsureFwHostStateParams{HostID: hostID, UpdatedAt: s.now().UnixMilli()}); err != nil {
		return nil, err
	}
	st, err := s.d.Store.R.GetFwHostState(ctx, hostID)
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// compileHost compiles one host's desired policy.
func (s *Service) compileHost(ctx context.Context, w *compile.World, h db.Host, override bool) (*compile.HostPolicy, []string) {
	flows, warns := s.mgmtFlows(ctx, w, h)
	cfg := s.HostConfig(ctx, h.ID)
	hp := compile.Compile(compile.Input{World: w, HostID: h.ID, Outbound: cfg.Outbound, Mgmt: flows, OverrideMgmt: override})
	return hp, warns
}

// Plan builds a host's plan.
func (s *Service) Plan(ctx context.Context, hostID string, override bool) (*Plan, error) {
	h, err := s.d.Store.R.GetHost(ctx, hostID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	w, err := s.World(ctx)
	if err != nil {
		return nil, err
	}
	st, err := s.state(ctx, hostID)
	if err != nil {
		return nil, err
	}
	p := &Plan{HostID: h.ID, HostName: h.Name, Config: s.HostConfig(ctx, h.ID), state: st, host: h, world: w}
	p.TimeoutS, p.TimeoutFrom = s.EffectiveTimeout(ctx, h.ID)
	p.Policy, p.MgmtWarnings = s.compileHost(ctx, w, h, override)
	p.Rendered = nftables.Render(p.Policy)
	p.Explain = p.Policy.Explain()
	if p.Explain == nil {
		p.Explain = []compile.Allowance{}
	}
	if st.EnvJson != "" {
		p.Env = &nftables.Env{}
		json.Unmarshal([]byte(st.EnvJson), p.Env)
		p.Eligibility = &nftables.Eligibility{}
		json.Unmarshal([]byte(st.EligibilityJson), p.Eligibility)
		if st.EnvCheckedAt.Valid {
			p.EnvChecked = time.UnixMilli(st.EnvCheckedAt.Int64).UTC().Format(time.RFC3339)
		}
	}
	p.Applied = Applied{Enforced: st.Enforced != 0, Revision: st.AppliedRevision, RulesHash: st.AppliedRulesHash, SetsHash: st.AppliedSetsHash,
		OverrideMgmt: st.OverrideMgmt != 0, SetsGeneration: st.SetsGeneration, SetsApplied: st.SetsGenerationApplied,
		SetsError: st.SetsError, Drift: st.Drift}
	if st.StatusAt.Valid {
		p.Applied.StatusAt = time.UnixMilli(st.StatusAt.Int64).UTC().Format(time.RFC3339)
	}
	if p.Applied.Enforced {
		p.Applied.RulesChanged = p.Rendered.RulesHash != st.AppliedRulesHash
		p.Applied.SetsPending = !p.Applied.RulesChanged && p.Rendered.SetsHash != st.AppliedSetsHash
		if st.AppliedPolicyJson != "" {
			p.AppliedHP = &compile.HostPolicy{}
			json.Unmarshal([]byte(st.AppliedPolicyJson), p.AppliedHP)
		}
	}
	p.ChangesAdded, p.ChangesRemoved = p.AppliedChanges()
	if p.ChangesAdded == nil {
		p.ChangesAdded = []string{}
	}
	if p.ChangesRemoved == nil {
		p.ChangesRemoved = []string{}
	}
	if tx := s.openTx(ctx, hostID); tx != nil {
		r := toTxRow(*tx)
		p.Pending = &r
	}
	d := s.d.Decide(ctx, h)
	switch {
	case d.Blocked:
		p.Transport = "blocked"
	case d.UseAgent:
		p.Transport = "agent"
	case d.SSHAllowed && h.SshConfigured != 0:
		p.Transport = "ssh"
	default:
		p.Transport = "none"
	}
	p.Learning = "off"
	if p.Config.Mode != ModeOff {
		if h.ManagementMethod == "agent" {
			p.Learning = "continuous"
		} else {
			p.Learning = "snapshot"
		}
	}
	return p, nil
}

// PolicyImpact is one host a draft policy would change.
type PolicyImpact struct {
	HostID    string   `json:"host_id"`
	HostName  string   `json:"host_name"`
	Mode      string   `json:"mode"`
	Enforced  bool     `json:"enforced"`
	Supported *bool    `json:"supported"`
	Added     []string `json:"added"`
	Removed   []string `json:"removed"`
	Conflicts []string `json:"conflicts,omitempty"`
}

func allowanceLines(hp *compile.HostPolicy) map[string]bool {
	out := map[string]bool{}
	if hp == nil {
		return out
	}
	for _, a := range hp.Explain() {
		if a.Reason.Kind == compile.ReasonBase || a.Reason.Kind == compile.ReasonDefault {
			continue
		}
		dir := map[string]string{"in": "inbound", "out": "outbound"}[a.Direction]
		verb := map[string]string{"accept": "allow", "drop": "deny"}[a.Verdict]
		out[fmt.Sprintf("%s %s %s %s %s (%s)", verb, dir, a.Service, map[string]string{"in": "from", "out": "to"}[a.Direction], a.Peer, a.Reason.Text)] = true
	}
	return out
}

func diffLines(before, after map[string]bool) (added, removed []string) {
	added, removed = []string{}, []string{} // JSON [] rather than null
	for l := range after {
		if !before[l] {
			added = append(added, l)
		}
	}
	for l := range before {
		if !after[l] {
			removed = append(removed, l)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return
}

// PreviewPolicy shows which hosts a new or changed policy (or a deletion,
// del=true) would change, and how, before anything is saved.
func (s *Service) PreviewPolicy(ctx context.Context, p PolicyRow, del bool) ([]PolicyImpact, error) {
	w, err := s.World(ctx)
	if err != nil {
		return nil, err
	}
	next := *w
	next.Policies = nil
	replaced := false
	for _, cur := range w.Policies {
		if cur.ID != "" && cur.ID == p.ID {
			replaced = true
			if !del {
				next.Policies = append(next.Policies, p.Policy)
			}
			continue
		}
		next.Policies = append(next.Policies, cur)
	}
	if !replaced && !del {
		next.Policies = append(next.Policies, p.Policy)
	}
	hosts, err := s.d.Store.R.ListHosts(ctx)
	if err != nil {
		return nil, err
	}
	out := []PolicyImpact{}
	for _, h := range hosts {
		before, _ := s.compileHost(ctx, w, h, false)
		after, _ := s.compileHost(ctx, &next, h, false)
		if nftables.Render(before).RulesHash == nftables.Render(after).RulesHash {
			continue
		}
		im := PolicyImpact{HostID: h.ID, HostName: h.Name, Mode: s.HostConfig(ctx, h.ID).Mode}
		im.Added, im.Removed = diffLines(allowanceLines(before), allowanceLines(after))
		for _, c := range after.Conflicts {
			im.Conflicts = append(im.Conflicts, c.Message)
		}
		if st, err := s.d.Store.R.GetFwHostState(ctx, h.ID); err == nil {
			im.Enforced = st.Enforced != 0
			if st.EligibilityJson != "" {
				var el nftables.Eligibility
				json.Unmarshal([]byte(st.EligibilityJson), &el)
				im.Supported = &el.Supported
			}
		}
		out = append(out, im)
	}
	return out, nil
}

// AppliedChanges lists how the desired policy differs from what the host
// has committed (for the apply preview).
func (p *Plan) AppliedChanges() (added, removed []string) {
	return diffLines(allowanceLines(p.AppliedHP), allowanceLines(p.Policy))
}
