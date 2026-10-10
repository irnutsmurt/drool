package fwsvc

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"drool/internal/firewall/compile"
	"drool/internal/firewall/document"
	"drool/internal/firewall/nftables"
	"drool/internal/firewall/policy"
	"drool/internal/ids"
	"drool/internal/store/db"
)

// The Firewall Designer edits one canonical document (package document).
// Everything here goes through the existing compiler: a draft is resolved
// into a compile.World and compiled per host exactly as a saved policy
// would be; previews, Explain, simulation and the management-path checks
// read those compilations. There is no second evaluator.

// ErrRevisionConflict means the stored document changed since the editor
// loaded it.
var ErrRevisionConflict = errors.New("the firewall policy was changed by someone else since you loaded it")

// DocView is the stored document in both forms.
type DocView struct {
	Document document.Document `json:"document"`
	YAML     string            `json:"yaml"`
	Revision string            `json:"revision"`
}

// DocInput is a draft as the designer sends it: the document, or YAML.
type DocInput struct {
	Document *document.Document `json:"document,omitempty"`
	YAML     *string            `json:"yaml,omitempty"`
}

func readQ(s *Service, q *db.Queries) *db.Queries {
	if q != nil {
		return q
	}
	return s.d.Store.R
}

// inventory is what documents are resolved against.
func (s *Service) inventory(ctx context.Context, q *db.Queries) (document.Inventory, document.Names, error) {
	q = readQ(s, q)
	inv := document.Inventory{Hosts: map[string]string{}, HostNames: map[string]string{}, Groups: map[string]string{},
		GroupNames: map[string]string{}, GroupMembers: map[string][]string{}, Networks: map[string]string{},
		Services: map[string]string{}, Policies: map[string]string{}, BuiltinServices: policy.Services(nil)}
	var names document.Names
	hosts, err := q.ListHosts(ctx)
	if err != nil {
		return inv, names, err
	}
	for _, h := range hosts {
		inv.Hosts[strings.ToLower(h.Name)] = h.ID
		inv.HostNames[h.ID] = h.Name
		names.Hosts = append(names.Hosts, h.Name)
	}
	groups, err := q.ListGroups(ctx)
	if err != nil {
		return inv, names, err
	}
	for _, g := range groups {
		inv.Groups[strings.ToLower(g.Name)] = g.ID
		inv.GroupNames[g.ID] = g.Name
		names.Groups = append(names.Groups, g.Name)
	}
	members, err := q.ListGroupMembers(ctx)
	if err != nil {
		return inv, names, err
	}
	for _, m := range members {
		inv.GroupMembers[m.GroupID] = append(inv.GroupMembers[m.GroupID], m.HostID)
	}
	nets, err := q.ListFwNetworks(ctx)
	if err != nil {
		return inv, names, err
	}
	for _, n := range nets {
		inv.Networks[strings.ToLower(n.Name)] = n.ID
	}
	svcs, err := q.ListFwServices(ctx)
	if err != nil {
		return inv, names, err
	}
	for _, sv := range svcs {
		inv.Services[strings.ToLower(sv.Name)] = sv.ID
	}
	pols, err := q.ListFwPolicies(ctx)
	if err != nil {
		return inv, names, err
	}
	for _, p := range pols {
		inv.Policies[strings.ToLower(p.Name)] = p.ID
	}
	return inv, names, nil
}

// storedDocument builds the document from what is stored.
func (s *Service) storedDocument(ctx context.Context, q *db.Queries) (document.Document, error) {
	q = readQ(s, q)
	inv, _, err := s.inventory(ctx, q)
	if err != nil {
		return document.Document{}, err
	}
	var d document.Document
	nets, err := q.ListFwNetworks(ctx)
	if err != nil {
		return d, err
	}
	netNames := map[string]string{}
	for _, n := range nets {
		var cidrs []string
		json.Unmarshal([]byte(n.CidrsJson), &cidrs)
		d.Networks = append(d.Networks, document.Network{Name: n.Name, Description: n.Description, CIDRs: cidrs})
		netNames[n.ID] = n.Name
	}
	groups, err := q.ListGroups(ctx)
	if err != nil {
		return d, err
	}
	for _, g := range groups {
		dg := document.Group{Name: g.Name, Description: g.Description}
		for _, id := range inv.GroupMembers[g.ID] {
			dg.Members = append(dg.Members, inv.HostNames[id])
		}
		d.Groups = append(d.Groups, dg)
	}
	svcs, err := q.ListFwServices(ctx) // custom only: built-ins are implicit
	if err != nil {
		return d, err
	}
	for _, sv := range svcs {
		var es []policy.Entry
		json.Unmarshal([]byte(sv.EntriesJson), &es)
		d.Services = append(d.Services, document.Service{Name: sv.Name, Description: sv.Description, Entries: es})
	}
	pols, err := q.ListFwPolicies(ctx)
	if err != nil {
		return d, err
	}
	ref := func(sel policy.Selector) string {
		switch sel.Kind {
		case policy.SelHost:
			if n, ok := inv.HostNames[sel.ID]; ok {
				return "host:" + n
			}
			return "host:deleted-" + sel.ID // fails validation: the policy must be fixed
		case policy.SelGroup:
			if n, ok := inv.GroupNames[sel.ID]; ok {
				return "group:" + n
			}
			return "group:deleted-" + sel.ID
		case policy.SelNetwork:
			if n, ok := netNames[sel.ID]; ok {
				return "network:" + n
			}
			return "network:deleted-" + sel.ID
		case policy.SelCIDR:
			return "cidr:" + sel.CIDR
		}
		return sel.Kind
	}
	for _, r := range pols {
		p := toPolicy(r)
		dr := document.Rule{Name: p.Name, Description: p.Description, Enabled: p.Enabled, Action: p.Action, Services: p.Services}
		for _, f := range p.From {
			dr.From = append(dr.From, ref(f))
		}
		for _, t := range p.To {
			dr.To = append(dr.To, ref(t))
		}
		d.Policy = append(d.Policy, dr)
	}
	return document.Normalize(d), nil
}

// Revision identifies a document's content.
func Revision(d document.Document) string {
	b, _ := json.Marshal(document.Normalize(d))
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}

// LoadDocument returns the stored document.
func (s *Service) LoadDocument(ctx context.Context) (*DocView, error) {
	d, err := s.storedDocument(ctx, nil)
	if err != nil {
		return nil, err
	}
	_, names, err := s.inventory(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &DocView{Document: d, YAML: string(document.RenderYAML(d, names)), Revision: Revision(d)}, nil
}

// parseInput turns a draft into a normalized document (and its problems).
func (s *Service) parseInput(ctx context.Context, in DocInput) (document.Document, []document.Problem, []byte, error) {
	_, names, err := s.inventory(ctx, nil)
	if err != nil {
		return document.Document{}, nil, nil, err
	}
	switch {
	case in.YAML != nil:
		src := []byte(*in.YAML)
		d, probs := document.ParseYAML(src, names)
		return d, probs, src, nil
	case in.Document != nil:
		return document.Normalize(*in.Document), nil, nil, nil
	}
	return document.Document{}, []document.Problem{{Message: "send a document or YAML"}}, nil, nil
}

// draftWorld is the compiler's world with the draft in place of the stored
// networks, services, policies and the groups it defines.
func (s *Service) draftWorld(ctx context.Context, res *document.Resolved) (*compile.World, error) {
	base, err := s.World(ctx)
	if err != nil {
		return nil, err
	}
	w := *base
	w.Groups = map[string]compile.GroupRef{}
	for id, g := range base.Groups {
		w.Groups[id] = g
	}
	for _, g := range res.Groups {
		w.Groups[g.ID] = compile.GroupRef{ID: g.ID, Name: g.Name, Members: g.MemberIDs}
	}
	w.Networks = map[string]compile.NetworkRef{}
	for _, n := range res.Networks {
		w.Networks[n.ID] = compile.NetworkRef{ID: n.ID, Name: n.Name, Prefixes: n.Prefixes}
	}
	w.Services = policy.Services(res.Services)
	w.Policies = res.Policies
	return &w, nil
}

// --- check ---

// HostImpact is how a draft changes one host.
type HostImpact struct {
	HostID    string   `json:"host_id"`
	HostName  string   `json:"host_name"`
	Mode      string   `json:"mode"`
	Enforced  bool     `json:"enforced"`
	Supported *bool    `json:"supported"`
	Refused   string   `json:"refused,omitempty"` // why enforcement is refused on this host
	Added     []string `json:"added"`             // new allowances
	NewDenies []string `json:"new_denies"`
	Removed   []string `json:"removed"`
	Conflicts []string `json:"conflicts"` // management-path conflicts
}

// MgmtHop is one segment of a DROOL management path.
type MgmtHop struct {
	From      string   `json:"from"` // host id, or "controller"
	FromName  string   `json:"from_name"`
	To        string   `json:"to"`
	ToName    string   `json:"to_name"`
	Protocol  string   `json:"protocol"`
	Port      int      `json:"port"`
	Status    string   `json:"status"` // protected | blocked | unverified
	Detail    string   `json:"detail"`
	BlockedBy []string `json:"blocked_by"`
	// AtRisk: unmeasured, and the host's compiled rules drop the port DROOL
	// connects to (from some sources): it may be cut, DROOL can't tell yet.
	AtRisk bool `json:"at_risk"`
}

// MgmtPath is the connectivity DROOL needs to manage one host.
type MgmtPath struct {
	HostID     string    `json:"host_id"`
	Host       string    `json:"host"`
	Kind       string    `json:"kind"` // ssh | agent
	Mode       string    `json:"mode"`
	Hops       []MgmtHop `json:"hops"`
	Blocked    bool      `json:"blocked"`
	Unverified bool      `json:"unverified"`
	AtRisk     bool      `json:"at_risk"`
}

// Controller is the pseudo-node for the DROOL controller.
const Controller = "controller"

// CheckResult is the designer's live validation and preview of a draft.
type CheckResult struct {
	Document   document.Document  `json:"document"`
	YAML       string             `json:"yaml"`
	Valid      bool               `json:"valid"`
	Problems   []document.Problem `json:"problems"`
	Notices    []string           `json:"notices"`
	Changed    bool               `json:"changed"`  // differs from what is stored
	Revision   string             `json:"revision"` // of what is stored
	Impact     []HostImpact       `json:"impact"`
	Management []MgmtPath         `json:"management"`
	// Blockers are why applying this draft would be refused (management
	// path conflicts); saving is still possible.
	Blockers []string `json:"blockers"`
}

func (s *Service) resolveDraft(ctx context.Context, in DocInput) (document.Document, *document.Resolved, []document.Problem, error) {
	d, probs, src, err := s.parseInput(ctx, in)
	if err != nil {
		return d, nil, nil, err
	}
	if len(probs) > 0 {
		return d, nil, probs, nil
	}
	inv, _, err := s.inventory(ctx, nil)
	if err != nil {
		return d, nil, nil, err
	}
	res, probs := document.Resolve(d, inv)
	if src != nil {
		probs = document.Locate(src, probs)
	}
	return d, res, probs, nil
}

// CheckDocument validates a draft and previews it: per-host changes,
// management paths and apply blockers.
func (s *Service) CheckDocument(ctx context.Context, in DocInput) (*CheckResult, error) {
	d, res, probs, err := s.resolveDraft(ctx, in)
	if err != nil {
		return nil, err
	}
	stored, err := s.storedDocument(ctx, nil)
	if err != nil {
		return nil, err
	}
	_, names, err := s.inventory(ctx, nil)
	if err != nil {
		return nil, err
	}
	out := &CheckResult{Document: d, Problems: nonNilProblems(probs), Notices: []string{}, Revision: Revision(stored),
		Impact: []HostImpact{}, Management: []MgmtPath{}, Blockers: []string{}}
	if in.YAML != nil && d.Policy == nil {
		// The YAML didn't parse: there is no document to show.
		out.Document = document.Normalize(document.Document{})
	} else {
		out.YAML = string(document.RenderYAML(d, names))
		out.Changed = Revision(d) != out.Revision
	}
	if len(out.Problems) > 0 || res == nil {
		return out, nil
	}
	out.Valid = true
	for _, g := range res.KeptGroups {
		out.Notices = append(out.Notices, fmt.Sprintf("pack %s (group:%s) is not in the document; it is kept (packs are shared with the rest of DROOL: delete it on the Packs page)", g, g))
	}
	draft, err := s.draftWorld(ctx, res)
	if err != nil {
		return nil, err
	}
	saved, err := s.World(ctx)
	if err != nil {
		return nil, err
	}
	hosts, err := s.d.Store.R.ListHosts(ctx)
	if err != nil {
		return nil, err
	}
	compiled := map[string]*compile.HostPolicy{}
	for _, h := range hosts {
		before, _ := s.compileHost(ctx, saved, h, false)
		after, _ := s.compileHost(ctx, draft, h, false)
		compiled[h.ID] = after
		cfg := s.HostConfig(ctx, h.ID)
		im := HostImpact{HostID: h.ID, HostName: h.Name, Mode: cfg.Mode, Added: []string{}, NewDenies: []string{}, Removed: []string{}, Conflicts: []string{}}
		added, removed := diffLines(allowanceLines(before), allowanceLines(after))
		for _, l := range added {
			if strings.HasPrefix(l, "deny ") {
				im.NewDenies = append(im.NewDenies, l)
			} else {
				im.Added = append(im.Added, l)
			}
		}
		im.Removed = removed
		for _, c := range after.Conflicts {
			im.Conflicts = append(im.Conflicts, c.Message)
			out.Blockers = append(out.Blockers, h.Name+": "+c.Message)
		}
		if st, err := s.d.Store.R.GetFwHostState(ctx, h.ID); err == nil {
			im.Enforced = st.Enforced != 0
			if st.EligibilityJson != "" {
				var el nftables.Eligibility
				json.Unmarshal([]byte(st.EligibilityJson), &el)
				sup := el.Supported
				im.Supported = &sup
				if !sup {
					im.Refused = strings.Join(el.Reasons, "; ")
				}
			}
		}
		if len(im.Added)+len(im.NewDenies)+len(im.Removed)+len(im.Conflicts) > 0 {
			out.Impact = append(out.Impact, im)
		}
	}
	out.Management, err = s.managementPaths(ctx, hosts, compiled)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func nonNilProblems(p []document.Problem) []document.Problem {
	if p == nil {
		return []document.Problem{}
	}
	return p
}

// managementPaths lays out every host's management paths and whether the
// compiled (draft) policies keep them.
func (s *Service) managementPaths(ctx context.Context, hosts []db.Host, compiled map[string]*compile.HostPolicy) ([]MgmtPath, error) {
	q := s.d.Store.R
	byID := map[string]db.Host{}
	for _, h := range hosts {
		byID[h.ID] = h
	}
	observed := func(hostID, kind string) []db.FwMgmtObservation {
		obs, _ := q.ListFwMgmtObservations(ctx, hostID)
		var out []db.FwMgmtObservation
		for _, o := range obs {
			if o.Kind == kind {
				out = append(out, o)
			}
		}
		return out
	}
	conflicts := func(hostID, dir string, port int, about string) []compile.Conflict {
		var out []compile.Conflict
		hp := compiled[hostID]
		if hp == nil {
			return nil
		}
		for _, c := range hp.Conflicts {
			if c.Flow.Dir == dir && c.Flow.Port == port && (about == "" || strings.Contains(c.Flow.Reason, about)) {
				out = append(out, c)
			}
		}
		return out
	}
	name := func(id string) string {
		if id == Controller {
			return "DROOL Controller"
		}
		return byID[id].Name
	}
	var out []MgmtPath
	for _, h := range hosts {
		cfg := s.HostConfig(ctx, h.ID)
		if s.sshPathConfigured(h) {
			chain := []string{h.ID}
			for cur := h.ID; len(chain) < 10; {
				via, err := q.GetHostHop(ctx, cur)
				if err != nil || via == "" || slices.Contains(chain, via) {
					break
				}
				chain = append([]string{via}, chain...)
				cur = via
			}
			chain = append([]string{Controller}, chain...)
			p := MgmtPath{HostID: h.ID, Host: h.Name, Kind: "ssh", Mode: cfg.Mode, Hops: []MgmtHop{}}
			for i := 1; i < len(chain); i++ {
				from, to := chain[i-1], chain[i]
				// The port the controller (or jump host) dials and the port the
				// host's own firewall sees can differ (port forwarding, NAT):
				// the host's inbound rule protects the port it observed.
				dialPort := int(byID[to].SshPort)
				obs := observed(to, "ssh_in")
				inPorts := []int{}
				for _, o := range obs {
					if !slices.Contains(inPorts, int(o.Port)) {
						inPorts = append(inPorts, int(o.Port))
					}
				}
				port := dialPort
				if len(inPorts) > 0 {
					port = inPorts[0]
				}
				hop := MgmtHop{From: from, FromName: name(from), To: to, ToName: name(to), Protocol: "tcp", Port: port, Status: "protected", BlockedBy: []string{}}
				var hits []compile.Conflict
				for _, p := range inPorts {
					hits = append(hits, conflicts(to, "in", p, "SSH management")...)
				}
				if from != Controller {
					hits = append(hits, conflicts(from, "out", dialPort, byID[to].Name)...)
				}
				switch {
				case len(hits) > 0:
					hop.Status = "blocked"
					for _, c := range hits {
						if !slices.Contains(hop.BlockedBy, c.PolicyName) {
							hop.BlockedBy = append(hop.BlockedBy, c.PolicyName)
						}
					}
					hop.Detail = hits[0].Message
					p.Blocked = true
				case len(obs) == 0:
					hop.Status = "unverified"
					hop.Detail = fmt.Sprintf("DROOL hasn't measured how %s sees this connection yet: click Check on the host (it is measured again before every apply)", name(to))
					p.Unverified = true
					if names := dropsPort(compiled[to], "tcp", dialPort); len(names) > 0 {
						hop.AtRisk, p.AtRisk = true, true
						hop.BlockedBy = names
						hop.Detail = fmt.Sprintf("%s denies TCP %d on %s, and DROOL hasn't measured where its connection comes from, so it can't tell whether this cuts it off: click Check on %s",
							strings.Join(names, ", "), dialPort, name(to), name(to))
					}
				default:
					var srcs []string
					for _, o := range obs {
						srcs = append(srcs, o.Address)
					}
					sort.Strings(srcs)
					hop.Detail = fmt.Sprintf("%s sees this connection from %s; DROOL always allows it ahead of every deny", name(to), strings.Join(srcs, ", "))
				}
				p.Hops = append(p.Hops, hop)
			}
			out = append(out, p)
		}
		if h.ManagementMethod == "agent" {
			p := MgmtPath{HostID: h.ID, Host: h.Name, Kind: "agent", Mode: cfg.Mode, Hops: []MgmtHop{}}
			obs := observed(h.ID, "agent_out")
			ports := map[int][]string{}
			for _, o := range obs {
				ports[int(o.Port)] = append(ports[int(o.Port)], o.Address)
			}
			if len(ports) == 0 {
				p.Unverified = true
				p.Hops = append(p.Hops, MgmtHop{From: h.ID, FromName: h.Name, To: Controller, ToName: name(Controller), Protocol: "tcp",
					Status: "unverified", BlockedBy: []string{}, Detail: "the agent hasn't reported its controller endpoints yet (it does when it connects)"})
			}
			var ps []int
			for port := range ports {
				ps = append(ps, port)
			}
			sort.Ints(ps)
			for _, port := range ps {
				hop := MgmtHop{From: h.ID, FromName: h.Name, To: Controller, ToName: name(Controller), Protocol: "tcp", Port: port, Status: "protected", BlockedBy: []string{},
					Detail: fmt.Sprintf("the agent connects to %s; DROOL always allows it ahead of every deny", strings.Join(ports[port], ", "))}
				if hits := conflicts(h.ID, "out", port, "agent"); len(hits) > 0 {
					hop.Status, hop.Detail, p.Blocked = "blocked", hits[0].Message, true
					for _, c := range hits {
						if !slices.Contains(hop.BlockedBy, c.PolicyName) {
							hop.BlockedBy = append(hop.BlockedBy, c.PolicyName)
						}
					}
				}
				p.Hops = append(p.Hops, hop)
			}
			out = append(out, p)
		}
	}
	if out == nil {
		out = []MgmtPath{}
	}
	return out, nil
}

// dropsPort lists the policies whose compiled inbound denies cover a port
// (for any source): reading the compiler's output, not re-evaluating policy.
func dropsPort(hp *compile.HostPolicy, proto string, port int) []string {
	var out []string
	if hp == nil {
		return out
	}
	for _, r := range hp.Input.Rules {
		if r.Verdict != "drop" || r.Reason.Kind != compile.ReasonPolicy || r.Proto != proto {
			continue
		}
		for _, pr := range r.Ports {
			if port >= pr.Lo && port <= pr.Hi && !slices.Contains(out, r.Reason.PolicyName) {
				out = append(out, r.Reason.PolicyName)
			}
		}
	}
	return out
}

// --- save ---

// SaveSummary says what a save changed.
type SaveSummary struct {
	Networks      []string `json:"networks"` // "+name", "~name", "-name"
	Groups        []string `json:"groups"`
	Services      []string `json:"services"`
	Policies      []string `json:"policies"`
	GroupsChanged bool     `json:"-"`
	Notices       []string `json:"notices"`
}

// ErrGroupsForbidden means the draft changes host groups and the caller may
// not (hosts.write).
var ErrGroupsForbidden = errors.New("changing host groups needs the hosts.write permission")

// SaveDocumentTx stores a draft as the policy: networks, custom services
// and policies become exactly the document's; the groups it lists get its
// descriptions and members (groups it omits are kept). base is the
// revision the editor started from.
func (s *Service) SaveDocumentTx(ctx context.Context, q *db.Queries, in DocInput, base, by string, canWriteGroups bool) (*SaveSummary, error) {
	d, probs, src, err := s.parseInput(ctx, in)
	if err != nil {
		return nil, err
	}
	if len(probs) > 0 {
		return nil, problemsError(probs)
	}
	stored, err := s.storedDocument(ctx, q)
	if err != nil {
		return nil, err
	}
	if base != "" && base != Revision(stored) {
		return nil, ErrRevisionConflict
	}
	inv, _, err := s.inventory(ctx, q)
	if err != nil {
		return nil, err
	}
	res, probs := document.Resolve(d, inv)
	if src != nil {
		probs = document.Locate(src, probs)
	}
	if len(probs) > 0 {
		return nil, problemsError(probs)
	}
	sum := &SaveSummary{Networks: []string{}, Groups: []string{}, Services: []string{}, Policies: []string{}, Notices: []string{}}
	now := s.now().UnixMilli()

	// Groups first (policies refer to them). Membership edits need
	// hosts.write: groups are DROOL-wide.
	storedGroups := map[string]document.Group{}
	for _, g := range stored.Groups {
		storedGroups[strings.ToLower(g.Name)] = g
	}
	groupIDs := map[string]string{} // provisional → real
	for i, g := range res.Groups {
		dg := d.Groups[slices.IndexFunc(d.Groups, func(x document.Group) bool { return strings.EqualFold(x.Name, g.Name) })]
		old, exists := storedGroups[strings.ToLower(g.Name)]
		if exists && old.Description == dg.Description && slices.Equal(lowerAll(old.Members), lowerAll(dg.Members)) {
			continue
		}
		if !canWriteGroups {
			return nil, ErrGroupsForbidden
		}
		sum.GroupsChanged = true
		if !exists {
			gv, err := s.d.Inv.CreateGroupTx(ctx, q, g.Name, g.Desc, g.MemberIDs)
			if err != nil {
				return nil, ValidationError("group %s: %v", g.Name, err)
			}
			groupIDs[g.ID] = gv.ID
			res.Groups[i].ID = gv.ID
			sum.Groups = append(sum.Groups, "+"+g.Name)
			continue
		}
		if err := s.d.Inv.SetMembersTx(ctx, q, g.ID, g.MemberIDs); err != nil {
			return nil, ValidationError("group %s: %v", g.Name, err)
		}
		desc := g.Desc
		if _, err := s.d.Inv.UpdateGroupTx(ctx, q, g.ID, nil, &desc); err != nil {
			return nil, ValidationError("group %s: %v", g.Name, err)
		}
		sum.Groups = append(sum.Groups, "~"+g.Name)
	}
	for _, name := range res.KeptGroups {
		sum.Notices = append(sum.Notices, "pack "+name+" was kept (delete packs on the Packs page)")
	}

	// Networks.
	storedNets, err := q.ListFwNetworks(ctx)
	if err != nil {
		return nil, err
	}
	keepNet := map[string]bool{}
	networkIDs := map[string]string{}
	for _, n := range res.Networks {
		cj, _ := json.Marshal(n.CIDRs)
		idx := slices.IndexFunc(storedNets, func(x db.FwNetwork) bool { return strings.EqualFold(x.Name, n.Name) })
		if idx < 0 {
			id := ids.New(ids.FwNetwork)
			if err := q.InsertFwNetwork(ctx, db.InsertFwNetworkParams{ID: id, Name: n.Name, Description: n.Desc, CidrsJson: string(cj), CreatedAt: now, UpdatedAt: now}); err != nil {
				return nil, err
			}
			networkIDs[n.ID] = id
			keepNet[id] = true
			sum.Networks = append(sum.Networks, "+"+n.Name)
			continue
		}
		cur := storedNets[idx]
		keepNet[cur.ID] = true
		if cur.Name != n.Name || cur.Description != n.Desc || cur.CidrsJson != string(cj) {
			if err := q.UpdateFwNetwork(ctx, db.UpdateFwNetworkParams{Name: n.Name, Description: n.Desc, CidrsJson: string(cj), UpdatedAt: now, ID: cur.ID}); err != nil {
				return nil, err
			}
			sum.Networks = append(sum.Networks, "~"+n.Name)
		}
	}

	// Custom services.
	storedSvcs, err := q.ListFwServices(ctx)
	if err != nil {
		return nil, err
	}
	keepSvc := map[string]bool{}
	for _, sv := range res.Services {
		ej, _ := json.Marshal(sv.Entries)
		idx := slices.IndexFunc(storedSvcs, func(x db.FwService) bool { return strings.EqualFold(x.Name, sv.Name) })
		if idx < 0 {
			id := ids.New(ids.FwService)
			if err := q.InsertFwService(ctx, db.InsertFwServiceParams{ID: id, Name: sv.Name, Description: sv.Description, EntriesJson: string(ej), CreatedAt: now, UpdatedAt: now}); err != nil {
				return nil, err
			}
			keepSvc[id] = true
			sum.Services = append(sum.Services, "+"+sv.Name)
			continue
		}
		cur := storedSvcs[idx]
		keepSvc[cur.ID] = true
		if cur.Description != sv.Description || cur.EntriesJson != string(ej) {
			if err := q.UpdateFwService(ctx, db.UpdateFwServiceParams{Description: sv.Description, EntriesJson: string(ej), UpdatedAt: now, ID: cur.ID}); err != nil {
				return nil, err
			}
			sum.Services = append(sum.Services, "~"+sv.Name)
		}
	}

	// Policies: by name; ids of kept policies don't change.
	storedPols, err := q.ListFwPolicies(ctx)
	if err != nil {
		return nil, err
	}
	keepPol := map[string]bool{}
	fix := func(sels []policy.Selector) []policy.Selector {
		out := make([]policy.Selector, len(sels))
		for i, sel := range sels {
			if real, ok := groupIDs[sel.ID]; ok && sel.Kind == policy.SelGroup {
				sel.ID = real
			}
			if real, ok := networkIDs[sel.ID]; ok && sel.Kind == policy.SelNetwork {
				sel.ID = real
			}
			out[i] = sel
		}
		return out
	}
	for i, p := range res.Policies {
		p.From, p.To = fix(p.From), fix(p.To)
		from, _ := json.Marshal(p.From)
		to, _ := json.Marshal(p.To)
		svcs, _ := json.Marshal(p.Services)
		enabled := int64(0)
		if p.Enabled {
			enabled = 1
		}
		pos := int64(i+1) * 10
		idx := slices.IndexFunc(storedPols, func(x db.FwPolicy) bool { return strings.EqualFold(x.Name, p.Name) })
		if idx < 0 {
			id := ids.New(ids.FwPolicy)
			if err := q.InsertFwPolicy(ctx, db.InsertFwPolicyParams{ID: id, Name: p.Name, Description: p.Description, Enabled: enabled,
				Action: p.Action, FromJson: string(from), ToJson: string(to), ServicesJson: string(svcs), Position: pos,
				CreatedBy: sql.NullString{String: by, Valid: by != ""}, CreatedAt: now, UpdatedAt: now}); err != nil {
				return nil, err
			}
			keepPol[id] = true
			sum.Policies = append(sum.Policies, "+"+p.Name)
			continue
		}
		cur := storedPols[idx]
		keepPol[cur.ID] = true
		if cur.Name != p.Name || cur.Description != p.Description || cur.Enabled != enabled || cur.Action != p.Action ||
			cur.FromJson != string(from) || cur.ToJson != string(to) || cur.ServicesJson != string(svcs) || cur.Position != pos {
			if err := q.UpdateFwPolicy(ctx, db.UpdateFwPolicyParams{Name: p.Name, Description: p.Description, Enabled: enabled, Action: p.Action,
				FromJson: string(from), ToJson: string(to), ServicesJson: string(svcs), Position: pos, UpdatedAt: now, ID: cur.ID}); err != nil {
				return nil, err
			}
			if cur.Position != pos && cur.FromJson == string(from) && cur.ToJson == string(to) && cur.ServicesJson == string(svcs) &&
				cur.Action == p.Action && cur.Enabled == enabled && cur.Description == p.Description {
				continue // only reordered
			}
			sum.Policies = append(sum.Policies, "~"+p.Name)
		}
	}
	for _, p := range storedPols {
		if !keepPol[p.ID] {
			if err := q.DeleteFwPolicy(ctx, p.ID); err != nil {
				return nil, err
			}
			sum.Policies = append(sum.Policies, "-"+p.Name)
		}
	}
	// Unreferenced objects last.
	for _, sv := range storedSvcs {
		if !keepSvc[sv.ID] {
			if err := q.DeleteFwService(ctx, sv.ID); err != nil {
				return nil, err
			}
			sum.Services = append(sum.Services, "-"+sv.Name)
		}
	}
	for _, n := range storedNets {
		if !keepNet[n.ID] {
			if err := q.DeleteFwNetwork(ctx, n.ID); err != nil {
				return nil, err
			}
			sum.Networks = append(sum.Networks, "-"+n.Name)
		}
	}
	return sum, nil
}

// SaveDone runs after a save committed.
func (s *Service) SaveDone() { s.Kick() }

func lowerAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, x := range ss {
		out[i] = strings.ToLower(x)
	}
	return out
}

// ProblemsError carries validation problems.
type ProblemsError struct{ Problems []document.Problem }

func (e *ProblemsError) Error() string {
	var parts []string
	for _, p := range e.Problems {
		parts = append(parts, p.String())
	}
	return "the firewall document is invalid: " + strings.Join(parts, "; ")
}

func problemsError(p []document.Problem) error { return &ProblemsError{Problems: p} }

// --- explain and simulate on a draft ---

// DraftExplain is Explain Host on a draft.
type DraftExplain struct {
	HostID     string              `json:"host_id"`
	Host       string              `json:"host"`
	Mode       string              `json:"mode"`
	Explain    []compile.Allowance `json:"explain"`
	Conflicts  []string            `json:"conflicts"`
	Warnings   []string            `json:"warnings"`
	Management []MgmtPath          `json:"management"`
}

// draftOrStored returns the world of a draft (or the stored one).
func (s *Service) draftOrStored(ctx context.Context, in *DocInput) (*compile.World, error) {
	if in == nil || (in.Document == nil && in.YAML == nil) {
		return s.World(ctx)
	}
	_, res, probs, err := s.resolveDraft(ctx, *in)
	if err != nil {
		return nil, err
	}
	if len(probs) > 0 {
		return nil, problemsError(probs)
	}
	return s.draftWorld(ctx, res)
}

// ExplainDraft explains one host under a draft (or the stored policy).
func (s *Service) ExplainDraft(ctx context.Context, in *DocInput, hostID string) (*DraftExplain, error) {
	h, err := s.d.Store.R.GetHost(ctx, hostID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	w, err := s.draftOrStored(ctx, in)
	if err != nil {
		return nil, err
	}
	hp, warns := s.compileHost(ctx, w, h, false)
	out := &DraftExplain{HostID: h.ID, Host: h.Name, Mode: s.HostConfig(ctx, h.ID).Mode, Explain: hp.Explain(), Conflicts: []string{}, Warnings: append(warns, hp.Warnings...)}
	if out.Explain == nil {
		out.Explain = []compile.Allowance{}
	}
	if out.Warnings == nil {
		out.Warnings = []string{}
	}
	for _, c := range hp.Conflicts {
		out.Conflicts = append(out.Conflicts, c.Message)
	}
	// This host's paths need the policies of the hosts in front of it too.
	hosts, err := s.d.Store.R.ListHosts(ctx)
	if err != nil {
		return nil, err
	}
	compiled := map[string]*compile.HostPolicy{h.ID: hp}
	for _, o := range hosts {
		if o.ID != h.ID {
			compiled[o.ID], _ = s.compileHost(ctx, w, o, false)
		}
	}
	all, err := s.managementPaths(ctx, hosts, compiled)
	if err != nil {
		return nil, err
	}
	out.Management = []MgmtPath{}
	for _, p := range all {
		if p.HostID == h.ID {
			out.Management = append(out.Management, p)
		}
	}
	return out, nil
}

// --- layout ---

const settingLayout = "firewall.layout"

// Layout returns the designer's saved layout (node positions, expanded
// groups): presentation only, never policy.
func (s *Service) Layout(ctx context.Context) json.RawMessage {
	row, err := s.d.Store.R.GetSetting(ctx, settingLayout)
	if err != nil || !json.Valid([]byte(row.ValueJson)) {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(row.ValueJson)
}

// SetLayoutTx stores the layout.
func (s *Service) SetLayoutTx(ctx context.Context, q *db.Queries, layout json.RawMessage, by string) error {
	if len(layout) > 512<<10 {
		return ValidationError("the layout is too large")
	}
	var obj map[string]any
	if err := json.Unmarshal(layout, &obj); err != nil {
		return ValidationError("the layout must be a JSON object")
	}
	return q.UpsertSetting(ctx, db.UpsertSettingParams{Key: settingLayout, ValueJson: string(layout),
		UpdatedBy: sql.NullString{String: by, Valid: by != ""}, UpdatedAt: s.now().UnixMilli()})
}
