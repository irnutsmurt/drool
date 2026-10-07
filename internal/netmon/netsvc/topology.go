package netsvc

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"drool/internal/firewall/fwsvc"
	"drool/internal/netmon"
)

// Observed topology (M10): the Firewall Designer's canvas overlays what was
// actually seen on the configured policy. Node keys are the designer's
// references ("host:web1", "network:admin-lan", "internal", "internet",
// "controller"), plus "asn:15169" for external networks and "ip:…" for
// single addresses of an expanded group. External addresses are grouped by
// network owner (ASN), never one node per address unless expanded; country
// is a filter, not a node (it isn't a route).

// TopoNode is a node the configured topology doesn't have.
type TopoNode struct {
	Key     string       `json:"key"`
	Kind    string       `json:"kind"` // asn | ip | more | internal | internet | special
	Label   string       `json:"label"`
	Detail  string       `json:"detail"`
	Members []TopoMember `json:"members"` // addresses behind a grouped node (bounded)
	Total   int          `json:"member_total"`
	Parent  string       `json:"parent,omitempty"` // ip nodes: the group they were expanded from
}

// TopoMember is one address behind a grouped node.
type TopoMember struct {
	IP      string `json:"ip"`
	Country string `json:"country,omitempty"`
	Flows   int    `json:"flows"`
}

// TopoService is one protocol/port on an observed edge.
type TopoService struct {
	Protocol string `json:"protocol"`
	Port     int    `json:"port"`
	Service  string `json:"service"`
	Count    int64  `json:"count"`
}

// TopoEdge is observed traffic from one node to another.
type TopoEdge struct {
	ID       string         `json:"id"`
	Source   string         `json:"source"`
	Target   string         `json:"target"`
	Services []TopoService  `json:"services"`
	Count    int64          `json:"count"` // connections (max of the two ends when both report)
	Flows    int            `json:"flows"`
	Status   map[string]int `json:"status"` // flows per policy status
	// Worst: the most notable status (denied > unclassified > covered >
	// management) — what the canvas draws.
	Worst     string   `json:"worst"`
	WouldDeny int      `json:"would_deny"`
	Processes []string `json:"processes"`
	FirstSeen string   `json:"first_seen"`
	LastSeen  string   `json:"last_seen"`
	FlowIDs   []int64  `json:"flow_ids"` // a few, for details
}

// Topology is the observed graph.
type Topology struct {
	Nodes []TopoNode `json:"nodes"`
	Edges []TopoEdge `json:"edges"`
	// Considered rows; Truncated: older flows were left out.
	Considered int  `json:"considered"`
	Truncated  bool `json:"truncated"`
	// Collapsed external networks beyond MaxExternal, drawn as one node.
	Collapsed int `json:"collapsed"`
}

// TopoOptions shape the graph.
type TopoOptions struct {
	Filter Filter
	// Expand: grouped nodes (asn:N, internal, internet) to show as addresses.
	Expand map[string]bool
	// MaxExternal: external network nodes kept; the rest become "Other
	// external networks"; 0 folds them all into one. Negative: 25.
	MaxExternal int
}

const topoMaxRows = 50000

var statusRank = map[string]int{fwsvc.FlowManagement: 0, fwsvc.FlowCovered: 1, fwsvc.FlowUnclassified: 2, fwsvc.FlowDenied: 3}

type edgeAgg struct {
	e     *TopoEdge
	byObs map[string]map[string]int64 // service key → observer host → count
	procs map[string]bool
}

// Topology builds the observed graph.
func (s *Service) Topology(ctx context.Context, o TopoOptions) (*Topology, error) {
	f := o.Filter
	f.Limit = MaxLimit
	if err := f.Validate(); err != nil {
		return nil, fwsvc.ValidationError("%v", err)
	}
	if o.MaxExternal < 0 {
		o.MaxExternal = 25
	}
	v, err := s.newView(ctx)
	if err != nil {
		return nil, err
	}
	where, args := f.sqlWhere()
	rows, err := s.d.Store.Reader.QueryContext(ctx, "SELECT "+cols+" FROM fw_observations o WHERE "+where+
		fmt.Sprintf(" ORDER BY o.last_seen DESC LIMIT %d", topoMaxRows+1), args...)
	if err != nil {
		return nil, err
	}
	var flows []Flow
	out := &Topology{Nodes: []TopoNode{}, Edges: []TopoEdge{}}
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		if out.Considered++; out.Considered > topoMaxRows {
			out.Truncated, out.Considered = true, topoMaxRows
			break
		}
		fl := v.flow(ctx, r)
		if v.matches(&f, &fl) {
			flows = append(flows, fl)
		}
	}
	rows.Close()

	// External networks by volume; the smallest beyond MaxExternal fold
	// into one node.
	asnFlows := map[uint32]int{}
	for _, fl := range flows {
		if fl.Scope == netmon.ScopePublic && fl.Geo != nil && fl.Geo.ASN != 0 && fl.Policy.Status != fwsvc.FlowManagement {
			asnFlows[fl.Geo.ASN]++
		}
	}
	keepASN := map[uint32]bool{}
	type kv struct {
		asn uint32
		n   int
	}
	var ranked []kv
	for a, n := range asnFlows {
		ranked = append(ranked, kv{a, n})
	}
	sort.Slice(ranked, func(i, j int) bool {
		return ranked[i].n > ranked[j].n || ranked[i].n == ranked[j].n && ranked[i].asn < ranked[j].asn
	})
	for i, r := range ranked {
		if i < o.MaxExternal {
			keepASN[r.asn] = true
		} else {
			out.Collapsed++
		}
	}

	nodes := map[string]*TopoNode{}
	members := map[string]map[string]*TopoMember{}
	addMember := func(group string, fl Flow) {
		m := members[group]
		if m == nil {
			m = map[string]*TopoMember{}
			members[group] = m
		}
		tm := m[fl.RemoteIP]
		if tm == nil {
			tm = &TopoMember{IP: fl.RemoteIP}
			if fl.Geo != nil {
				tm.Country = fl.Geo.CountryCode
			}
			m[fl.RemoteIP] = tm
		}
		tm.Flows++
	}
	// An expanded group shows at most maxExpanded addresses (the most
	// recently seen); the rest stay together as "N more addresses".
	const maxExpanded = 40
	expandedIPs := map[string]map[string]bool{}
	expandIP := func(group, ip string) bool {
		m := expandedIPs[group]
		if m == nil {
			m = map[string]bool{}
			expandedIPs[group] = m
		}
		if m[ip] {
			return true
		}
		if len(m) >= maxExpanded {
			return false
		}
		m[ip] = true
		return true
	}
	node := func(key, kind, label, detail string) string {
		if _, ok := nodes[key]; !ok {
			nodes[key] = &TopoNode{Key: key, Kind: kind, Label: label, Detail: detail, Members: []TopoMember{}}
		}
		return key
	}
	// peerKey places the remote end of a flow on the canvas.
	peerKey := func(fl Flow) string {
		switch {
		case fl.PeerHost != nil:
			return "host:" + fl.PeerHost.Name
		case fl.Policy.Status == fwsvc.FlowManagement:
			return "controller" // the management path's other end: the overlay's node
		case fl.Scope == netmon.ScopePublic:
			group := "internet"
			label, detail := "Internet", "public addresses without network data"
			kind := "internet"
			if fl.Geo != nil && fl.Geo.ASN != 0 {
				if keepASN[fl.Geo.ASN] {
					group, kind = fmt.Sprintf("asn:%d", fl.Geo.ASN), "asn"
					label = fl.Geo.Org
					if label == "" {
						label = fmt.Sprintf("AS%d", fl.Geo.ASN)
					}
					detail = fmt.Sprintf("AS%d", fl.Geo.ASN)
				} else if o.MaxExternal == 0 {
					group, kind, label, detail = "asn:other", "asn", "External networks", "every external network, grouped"
				} else {
					group, kind, label, detail = "asn:other", "asn", "Other external networks", "smaller networks, grouped"
				}
			}
			addMember(group, fl)
			if o.Expand[group] {
				if !expandIP(group, fl.RemoteIP) {
					k := node("more:"+group, "more", "More addresses", "the less recently seen addresses of "+label)
					nodes[k].Parent = group
					return k
				}
				nodes[node("ip:"+fl.RemoteIP, "ip", fl.RemoteIP, ipDetail(fl))].Parent = group
				return "ip:" + fl.RemoteIP
			}
			return node(group, kind, label, detail)
		case fl.PeerNet != "":
			return "network:" + fl.PeerNet // a named network the designer knows
		case fl.Scope == netmon.ScopeInternal:
			addMember("internal", fl)
			if o.Expand["internal"] {
				if !expandIP("internal", fl.RemoteIP) {
					k := node("more:internal", "more", "More addresses", "the less recently seen internal addresses")
					nodes[k].Parent = "internal"
					return k
				}
				nodes[node("ip:"+fl.RemoteIP, "ip", fl.RemoteIP, "internal · "+fl.ScopeRange)].Parent = "internal"
				return "ip:" + fl.RemoteIP
			}
			return node("internal", "internal", "Internal", "addresses in the internal ranges")
		default:
			rng := fl.ScopeRange
			if rng == "" {
				rng = "special"
			}
			key := "special:" + rng
			addMember(key, fl)
			return node(key, "special", strings.ToUpper(rng[:1])+rng[1:], "not public, not internal")
		}
	}

	edges := map[string]*edgeAgg{}
	for _, fl := range flows {
		self := "host:" + fl.Host.Name
		peer := peerKey(fl)
		src, dst := self, peer
		if fl.Direction == "in" {
			src, dst = peer, self
		}
		id := "obs|" + src + "|" + dst
		ea := edges[id]
		if ea == nil {
			ea = &edgeAgg{e: &TopoEdge{ID: id, Source: src, Target: dst, Status: map[string]int{}, FlowIDs: []int64{}, Processes: []string{},
				FirstSeen: fl.FirstSeen, LastSeen: fl.LastSeen}, byObs: map[string]map[string]int64{}, procs: map[string]bool{}}
			edges[id] = ea
		}
		e := ea.e
		e.Flows++
		e.Status[fl.Policy.Status]++
		if fl.Policy.WouldDeny {
			e.WouldDeny++
		}
		if e.Worst == "" || statusRank[fl.Policy.Status] > statusRank[e.Worst] {
			e.Worst = fl.Policy.Status
		}
		if fl.FirstSeen < e.FirstSeen {
			e.FirstSeen = fl.FirstSeen
		}
		if fl.LastSeen > e.LastSeen {
			e.LastSeen = fl.LastSeen
		}
		if len(e.FlowIDs) < 10 {
			e.FlowIDs = append(e.FlowIDs, fl.ID)
		}
		if fl.Process != "" && !ea.procs[fl.Process] && len(ea.procs) < 5 {
			ea.procs[fl.Process] = true
			e.Processes = append(e.Processes, fl.Process)
		}
		// When both ends report the same connections (two agents), count
		// them once: per service, the busier observer's count.
		sk := fmt.Sprintf("%s|%d|%s", fl.Protocol, fl.Port, fl.Service)
		if ea.byObs[sk] == nil {
			ea.byObs[sk] = map[string]int64{}
		}
		ea.byObs[sk][fl.Host.ID] += fl.Count
	}
	for _, ea := range edges {
		e := ea.e
		for sk, obs := range ea.byObs {
			var n int64
			for _, c := range obs {
				n = max(n, c)
			}
			parts := strings.SplitN(sk, "|", 3)
			var port int
			fmt.Sscan(parts[1], &port)
			e.Services = append(e.Services, TopoService{Protocol: parts[0], Port: port, Service: parts[2], Count: n})
			e.Count += n
		}
		sort.Slice(e.Services, func(i, j int) bool {
			a, b := e.Services[i], e.Services[j]
			if a.Count != b.Count {
				return a.Count > b.Count
			}
			return a.Port < b.Port
		})
		sort.Strings(e.Processes)
		out.Edges = append(out.Edges, *e)
	}
	sort.Slice(out.Edges, func(i, j int) bool { return out.Edges[i].ID < out.Edges[j].ID })
	for key, n := range nodes {
		if n.Kind == "more" {
			if m := members[n.Parent]; m != nil {
				n.Total = len(m) - len(expandedIPs[n.Parent])
				n.Label = fmt.Sprintf("%d more addresses", n.Total)
			}
		}
		if m := members[key]; m != nil {
			n.Total = len(m)
			for _, tm := range m {
				n.Members = append(n.Members, *tm)
			}
			sort.Slice(n.Members, func(i, j int) bool {
				if n.Members[i].Flows != n.Members[j].Flows {
					return n.Members[i].Flows > n.Members[j].Flows
				}
				return n.Members[i].IP < n.Members[j].IP
			})
			if len(n.Members) > 50 {
				n.Members = n.Members[:50]
			}
		}
		if n.Kind == "asn" || n.Kind == "internet" {
			n.Detail = strings.TrimSpace(n.Detail + countriesOf(n.Members))
		}
		out.Nodes = append(out.Nodes, *n)
	}
	sort.Slice(out.Nodes, func(i, j int) bool { return out.Nodes[i].Key < out.Nodes[j].Key })
	return out, nil
}

func ipDetail(fl Flow) string {
	if fl.Geo == nil {
		return "public"
	}
	parts := []string{}
	if fl.Geo.ASN != 0 {
		parts = append(parts, fmt.Sprintf("AS%d %s", fl.Geo.ASN, fl.Geo.Org))
	}
	if fl.Geo.Country != "" {
		parts = append(parts, fl.Geo.Country)
	}
	return strings.Join(parts, " · ")
}

// countriesOf summarises member countries: " · US, SE".
func countriesOf(ms []TopoMember) string {
	seen := map[string]bool{}
	var cs []string
	for _, m := range ms {
		if m.Country != "" && !seen[m.Country] {
			seen[m.Country] = true
			cs = append(cs, m.Country)
		}
	}
	if len(cs) == 0 {
		return ""
	}
	sort.Strings(cs)
	if len(cs) > 4 {
		cs = append(cs[:4], fmt.Sprintf("+%d", len(cs)-4))
	}
	return " · " + strings.Join(cs, ", ")
}
