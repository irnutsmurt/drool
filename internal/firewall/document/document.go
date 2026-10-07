// Package document is the canonical, editable form of DROOL's firewall
// policy: named networks, groups (DROOL's shared host groups), custom
// services and the ordered policy. The Firewall Designer's three views —
// Topology, Rules and YAML — all edit this one document; nothing else is
// configuration. Objects refer to each other by name; Resolve turns a
// document into the compiler's world (package compile), so previews,
// simulation, Explain and the management-path checks run on exactly what
// would be saved.
package document

import (
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"sort"
	"strings"

	"drool/internal/firewall/policy"
)

// Document is the canonical model.
type Document struct {
	Networks []Network `json:"networks"`
	Groups   []Group   `json:"groups"`
	Services []Service `json:"services"` // custom services; built-in ones are implicit
	Policy   []Rule    `json:"policy"`   // evaluation order (denies win regardless)
}

// Network is a named set of CIDRs (IPv4 and/or IPv6).
type Network struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	CIDRs       []string `json:"cidrs"`
}

// Group is a DROOL host group; Members are host names.
type Group struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Members     []string `json:"members"`
}

// Service is a custom service.
type Service struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Entries     []policy.Entry `json:"entries"`
}

// Rule is one policy: From may (allow) or must not (deny) reach To on
// Services. From and To are references (see Ref).
type Rule struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Enabled     bool     `json:"enabled"`
	Action      string   `json:"action"`
	From        []string `json:"from"`
	To          []string `json:"to"`
	Services    []string `json:"services"`
}

// Problem is a validation error, located by path (and line, for YAML).
type Problem struct {
	Path    string `json:"path"`
	Line    int    `json:"line,omitempty"`
	Message string `json:"message"`
}

func (p Problem) String() string {
	loc := p.Path
	if p.Line > 0 {
		loc = fmt.Sprintf("line %d", p.Line)
		if p.Path != "" {
			loc += " (" + p.Path + ")"
		}
	}
	if loc == "" {
		return p.Message
	}
	return loc + ": " + p.Message
}

// --- references ---

// Reference kinds. A reference is "host:NAME", "group:NAME",
// "network:NAME", "cidr:PREFIX", or one of the keywords.
const (
	RefHost     = "host"
	RefGroup    = "group"
	RefNetwork  = "network"
	RefCIDR     = "cidr"
	RefInternet = "internet"
	RefInternal = "internal"
	RefAny      = "any"
)

// Keywords are references without a name.
var Keywords = []string{RefInternet, RefInternal, RefAny}

// Ref is a parsed reference.
type Ref struct {
	Kind string
	Name string // host/group/network name, or the prefix for cidr
}

func (r Ref) String() string {
	switch r.Kind {
	case RefInternet, RefInternal, RefAny:
		return r.Kind
	}
	return r.Kind + ":" + r.Name
}

// ParseRef parses a canonical reference.
func ParseRef(s string) (Ref, error) {
	s = strings.TrimSpace(s)
	if slices.Contains(Keywords, s) {
		return Ref{Kind: s}, nil
	}
	kind, name, ok := strings.Cut(s, ":")
	if !ok {
		return Ref{}, fmt.Errorf("%q is not a reference (host:NAME, group:NAME, network:NAME, cidr:PREFIX, internet, internal or any)", s)
	}
	name = strings.TrimSpace(name)
	switch kind {
	case RefHost, RefGroup, RefNetwork:
		if name == "" {
			return Ref{}, fmt.Errorf("%q has no name", s)
		}
		return Ref{Kind: kind, Name: name}, nil
	case RefCIDR:
		p, err := netip.ParsePrefix(name)
		if err != nil {
			return Ref{}, fmt.Errorf("%q is not a valid network (CIDR)", name)
		}
		return Ref{Kind: RefCIDR, Name: p.Masked().String()}, nil
	}
	return Ref{}, fmt.Errorf("unknown kind %q in %q", kind, s)
}

// --- normalisation ---

var (
	// Network names: like group names, plus reserved keywords.
	nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)
)

func lower(s string) string { return strings.ToLower(s) }

// Normalize returns the canonical form: objects sorted by name, members
// and CIDRs sorted and deduplicated, nil lists as empty lists. The policy
// keeps its order.
func Normalize(d Document) Document {
	out := Document{Networks: []Network{}, Groups: []Group{}, Services: []Service{}, Policy: []Rule{}}
	for _, n := range d.Networks {
		n.Name, n.Description = strings.TrimSpace(n.Name), strings.TrimSpace(n.Description)
		var cs []string
		for _, c := range n.CIDRs {
			c = strings.TrimSpace(c)
			if p, err := netip.ParsePrefix(c); err == nil {
				c = p.Masked().String()
			}
			if c != "" && !slices.Contains(cs, c) {
				cs = append(cs, c)
			}
		}
		sort.Slice(cs, func(i, j int) bool { return cidrLess(cs[i], cs[j]) })
		n.CIDRs = nonNil(cs)
		out.Networks = append(out.Networks, n)
	}
	for _, g := range d.Groups {
		g.Name, g.Description = strings.TrimSpace(g.Name), strings.TrimSpace(g.Description)
		var ms []string
		for _, m := range g.Members {
			m = strings.TrimSpace(m)
			if m != "" && !slices.ContainsFunc(ms, func(x string) bool { return strings.EqualFold(x, m) }) {
				ms = append(ms, m)
			}
		}
		sort.Slice(ms, func(i, j int) bool { return lower(ms[i]) < lower(ms[j]) })
		g.Members = nonNil(ms)
		out.Groups = append(out.Groups, g)
	}
	for _, s := range d.Services {
		s.Name, s.Description = strings.TrimSpace(s.Name), strings.TrimSpace(s.Description)
		es := []policy.Entry{}
		for _, e := range s.Entries {
			e.Protocol = lower(strings.TrimSpace(e.Protocol))
			e.Ports = strings.ReplaceAll(strings.TrimSpace(e.Ports), " ", "")
			e.ICMPType = strings.TrimSpace(e.ICMPType)
			es = append(es, e)
		}
		s.Entries = es
		out.Services = append(out.Services, s)
	}
	for _, r := range d.Policy {
		r.Name, r.Description = strings.TrimSpace(r.Name), strings.TrimSpace(r.Description)
		r.Action = lower(strings.TrimSpace(r.Action))
		r.From, r.To = normRefs(r.From), normRefs(r.To)
		var svcs []string
		for _, s := range r.Services {
			s = strings.TrimSpace(s)
			if s != "" && !slices.Contains(svcs, s) {
				svcs = append(svcs, s)
			}
		}
		r.Services = nonNil(svcs)
		out.Policy = append(out.Policy, r)
	}
	sort.SliceStable(out.Networks, func(i, j int) bool { return lower(out.Networks[i].Name) < lower(out.Networks[j].Name) })
	sort.SliceStable(out.Groups, func(i, j int) bool { return lower(out.Groups[i].Name) < lower(out.Groups[j].Name) })
	sort.SliceStable(out.Services, func(i, j int) bool { return lower(out.Services[i].Name) < lower(out.Services[j].Name) })
	return out
}

func normRefs(rs []string) []string {
	var out []string
	for _, r := range rs {
		if ref, err := ParseRef(r); err == nil {
			r = ref.String()
		} else {
			r = strings.TrimSpace(r)
		}
		if r != "" && !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return nonNil(out)
}

func cidrLess(a, b string) bool {
	pa, ea := netip.ParsePrefix(a)
	pb, eb := netip.ParsePrefix(b)
	if ea != nil || eb != nil {
		return a < b
	}
	if pa.Addr().Is4() != pb.Addr().Is4() {
		return pa.Addr().Is4()
	}
	if c := pa.Addr().Compare(pb.Addr()); c != 0 {
		return c < 0
	}
	return pa.Bits() < pb.Bits()
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// --- resolution and validation ---

// Inventory is what a document is resolved against: the hosts DROOL
// manages and the objects already stored (to keep their ids).
type Inventory struct {
	Hosts           map[string]string // lower(name) → id
	HostNames       map[string]string // id → name
	Groups          map[string]string // lower(name) → id, stored groups
	GroupNames      map[string]string // id → name
	GroupMembers    map[string][]string
	Networks        map[string]string // lower(name) → id, stored networks
	Services        map[string]string // lower(name) → id, stored custom services
	Policies        map[string]string // lower(name) → id, stored policies
	BuiltinServices map[string]policy.Service
}

// ResolvedNetwork is a network with its id ("" when new).
type ResolvedNetwork struct {
	ID       string
	Name     string
	Desc     string
	CIDRs    []string
	Prefixes []netip.Prefix
}

// ResolvedGroup is a group with its id ("" when new) and member host ids.
type ResolvedGroup struct {
	ID        string
	Name      string
	Desc      string
	MemberIDs []string
}

// Resolved is a document bound to ids, ready to compile or store.
type Resolved struct {
	Networks   []ResolvedNetwork
	Groups     []ResolvedGroup
	Services   []policy.Service
	ServiceIDs map[string]string // name → stored id ("" when new)
	Policies   []policy.Policy   // ids of stored policies kept; new ones get provisional ids
	NewPolicy  map[string]bool   // policy id → new
	// KeptGroups are stored groups the document doesn't mention: groups are
	// shared with the rest of DROOL, so the firewall designer never deletes
	// them.
	KeptGroups []string
}

// Provisional id prefixes for objects that don't exist yet.
const (
	DraftGroup   = "draft-group:"
	DraftNetwork = "draft-network:"
	DraftPolicy  = "draft-policy:"
)

// Resolve validates a (normalized) document against the inventory and
// binds it to ids. Problems are returned in document order; when there
// are any, the Resolved value must not be stored.
func Resolve(d Document, inv Inventory) (*Resolved, []Problem) {
	var probs []Problem
	add := func(path, f string, a ...any) {
		probs = append(probs, Problem{Path: path, Message: fmt.Sprintf(f, a...)})
	}
	r := &Resolved{ServiceIDs: map[string]string{}, NewPolicy: map[string]bool{}}

	networks := map[string]string{} // lower name → id (stored or provisional)
	for i, n := range d.Networks {
		path := fmt.Sprintf("networks.%s", n.Name)
		if n.Name == "" {
			path = fmt.Sprintf("networks[%d]", i)
		}
		switch {
		case !nameRE.MatchString(n.Name):
			add(path, "network name %q: use letters, digits, '.', '_' or '-' (at most 63 characters)", n.Name)
			continue
		case slices.Contains(Keywords, lower(n.Name)):
			add(path, "%q is reserved (it means %s)", n.Name, keywordMeaning(lower(n.Name)))
			continue
		case networks[lower(n.Name)] != "":
			add(path, "network %q is defined twice", n.Name)
			continue
		}
		if len(n.CIDRs) == 0 {
			add(path, "network %q needs at least one CIDR (for example 192.168.1.0/24 or 2001:db8::/32)", n.Name)
		}
		rn := ResolvedNetwork{ID: inv.Networks[lower(n.Name)], Name: n.Name, Desc: n.Description, CIDRs: n.CIDRs}
		for _, c := range n.CIDRs {
			p, err := netip.ParsePrefix(c)
			if err != nil {
				add(path, "%q is not a valid CIDR", c)
				continue
			}
			rn.Prefixes = append(rn.Prefixes, p.Masked())
		}
		id := rn.ID
		if id == "" {
			id = DraftNetwork + lower(n.Name)
		}
		networks[lower(n.Name)] = id
		rn.ID = inv.Networks[lower(n.Name)]
		r.Networks = append(r.Networks, rn)
	}

	groups := map[string]string{} // lower name → id
	for k, id := range inv.Groups {
		groups[k] = id
	}
	inDoc := map[string]bool{}
	for i, g := range d.Groups {
		path := fmt.Sprintf("groups.%s", g.Name)
		if g.Name == "" {
			path = fmt.Sprintf("groups[%d]", i)
		}
		if !nameRE.MatchString(g.Name) {
			add(path, "group name %q: use letters, digits, '.', '_' or '-'", g.Name)
			continue
		}
		if inDoc[lower(g.Name)] {
			add(path, "group %q is defined twice", g.Name)
			continue
		}
		inDoc[lower(g.Name)] = true
		rg := ResolvedGroup{ID: inv.Groups[lower(g.Name)], Name: g.Name, Desc: g.Description}
		for _, m := range g.Members {
			id, ok := inv.Hosts[lower(m)]
			if !ok {
				add(path+".members", "unknown host %q", m)
				continue
			}
			rg.MemberIDs = append(rg.MemberIDs, id)
		}
		if rg.ID == "" {
			groups[lower(g.Name)] = DraftGroup + lower(g.Name)
		}
		r.Groups = append(r.Groups, rg)
	}
	for name, id := range inv.Groups {
		if !inDoc[name] {
			r.KeptGroups = append(r.KeptGroups, inv.GroupNames[id])
		}
	}
	sort.Strings(r.KeptGroups)

	services := map[string]policy.Service{}
	for n, s := range inv.BuiltinServices {
		services[n] = s
	}
	custom := map[string]bool{}
	for i, s := range d.Services {
		path := fmt.Sprintf("services.%s", s.Name)
		if s.Name == "" {
			path = fmt.Sprintf("services[%d]", i)
		}
		if custom[lower(s.Name)] {
			add(path, "service %q is defined twice", s.Name)
			continue
		}
		if err := policy.ValidateService(s.toPolicy()); err != nil {
			add(path, "%v", err)
			// Known by name, so rules using it don't report it again.
			custom[lower(s.Name)] = true
			services[s.Name] = s.toPolicy()
			continue
		}
		custom[lower(s.Name)] = true
		ps := s.toPolicy()
		services[s.Name] = ps
		r.Services = append(r.Services, ps)
		r.ServiceIDs[s.Name] = inv.Services[lower(s.Name)]
	}

	resolveRef := func(path, s string) (policy.Selector, bool) {
		ref, err := ParseRef(s)
		if err != nil {
			add(path, "%v", err)
			return policy.Selector{}, false
		}
		switch ref.Kind {
		case RefHost:
			id, ok := inv.Hosts[lower(ref.Name)]
			if !ok {
				add(path, "unknown host %q", ref.Name)
				return policy.Selector{}, false
			}
			return policy.Selector{Kind: policy.SelHost, ID: id}, true
		case RefGroup:
			id, ok := groups[lower(ref.Name)]
			if !ok {
				add(path, "unknown group %q (define it under groups)", ref.Name)
				return policy.Selector{}, false
			}
			return policy.Selector{Kind: policy.SelGroup, ID: id}, true
		case RefNetwork:
			id, ok := networks[lower(ref.Name)]
			if !ok {
				add(path, "unknown network %q (define it under networks)", ref.Name)
				return policy.Selector{}, false
			}
			return policy.Selector{Kind: policy.SelNetwork, ID: id}, true
		case RefCIDR:
			return policy.Selector{Kind: policy.SelCIDR, CIDR: ref.Name}, true
		case RefInternet:
			return policy.Selector{Kind: policy.SelInternet}, true
		case RefInternal:
			return policy.Selector{Kind: policy.SelInternal}, true
		}
		return policy.Selector{Kind: policy.SelAny}, true
	}

	names := map[string]bool{}
	for i, rule := range d.Policy {
		path := fmt.Sprintf("policy[%d]", i)
		if rule.Name != "" {
			path = fmt.Sprintf("policy[%d] %s", i, rule.Name)
		}
		if names[lower(rule.Name)] && rule.Name != "" {
			add(path, "policy %q is defined twice (names must be unique)", rule.Name)
		}
		names[lower(rule.Name)] = true
		p := policy.Policy{Name: rule.Name, Description: rule.Description, Enabled: rule.Enabled, Action: rule.Action, Services: rule.Services}
		ok := true
		for j, f := range rule.From {
			sel, good := resolveRef(fmt.Sprintf("%s.from[%d]", path, j), f)
			ok = ok && good
			p.From = append(p.From, sel)
		}
		for j, t := range rule.To {
			sel, good := resolveRef(fmt.Sprintf("%s.to[%d]", path, j), t)
			ok = ok && good
			p.To = append(p.To, sel)
		}
		for _, sv := range rule.Services {
			if _, known := services[sv]; !known {
				add(path+".services", "unknown service %q (built-in services: %s; or define it under services)", sv, builtinList(inv.BuiltinServices))
				ok = false
			}
		}
		if !ok {
			continue
		}
		if err := policy.Validate(p, services); err != nil {
			add(path, "%v", err)
			continue
		}
		if id := inv.Policies[lower(rule.Name)]; id != "" {
			p.ID = id
		} else {
			p.ID = DraftPolicy + lower(rule.Name)
			r.NewPolicy[p.ID] = true
		}
		r.Policies = append(r.Policies, p)
	}
	// Bind new objects' provisional ids so selectors resolve in previews.
	for i := range r.Groups {
		if r.Groups[i].ID == "" {
			r.Groups[i].ID = DraftGroup + lower(r.Groups[i].Name)
		}
	}
	for i := range r.Networks {
		if r.Networks[i].ID == "" {
			r.Networks[i].ID = DraftNetwork + lower(r.Networks[i].Name)
		}
	}
	return r, probs
}

func (s Service) toPolicy() policy.Service {
	return policy.Service{Name: s.Name, Description: s.Description, Entries: s.Entries}
}

func keywordMeaning(k string) string {
	switch k {
	case RefInternet:
		return "everything outside the internal ranges"
	case RefInternal:
		return "the internal ranges"
	}
	return "anywhere"
}

func builtinList(b map[string]policy.Service) string {
	var ns []string
	for n := range b {
		ns = append(ns, n)
	}
	sort.Strings(ns)
	return strings.Join(ns, ", ")
}

// IsDraftID reports whether an id is provisional (the object is new).
func IsDraftID(id string) bool {
	return strings.HasPrefix(id, DraftGroup) || strings.HasPrefix(id, DraftNetwork) || strings.HasPrefix(id, DraftPolicy)
}
