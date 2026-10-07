package fwsvc

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"time"

	"drool/internal/audit"
	"drool/internal/firewall/compile"
	"drool/internal/firewall/document"
	"drool/internal/firewall/hostfw"
	"drool/internal/firewall/observe"
	"drool/internal/firewall/policy"
	"drool/internal/netmon"
	"drool/internal/store/db"
)

// --- ingestion (M10: one observation store for the firewall and the
// network views) ---

// maxFlowsPerReport bounds one report; the agent holds at most
// collect.MaxPending flows between reports.
const maxFlowsPerReport = 25000

// validFlow checks a reported flow before it is stored: malformed
// addresses, impossible port/protocol combinations and absurd times are
// rejected (DESIGN §19.1, M10).
func validFlow(f *observe.Flow, now int64) bool {
	if f.Direction != "in" && f.Direction != "out" {
		return false
	}
	if f.Protocol != "tcp" && f.Protocol != "udp" {
		return false
	}
	if f.Port < 1 || f.Port > 65535 || f.LocalPort < 0 || f.LocalPort > 65535 || f.RemotePort < 0 || f.RemotePort > 65535 {
		return false
	}
	peer, err := netip.ParseAddr(f.Peer)
	if err != nil || peer.Zone() != "" {
		return false
	}
	peer = peer.Unmap()
	if !peer.IsValid() || peer.IsUnspecified() || peer.IsLoopback() || peer.IsMulticast() {
		return false
	}
	f.Peer = peer.String()
	if f.LocalIP != "" {
		l, err := netip.ParseAddr(f.LocalIP)
		if err != nil || l.Zone() != "" || l.Unmap().IsUnspecified() {
			return false
		}
		f.LocalIP = l.Unmap().String()
	}
	if f.Count < 0 || f.Count > 1e9 || f.PID < 0 || f.PID > 1<<22 {
		return false
	}
	const minTime = 1577836800 // 2020-01-01
	if f.LastSeen < minTime || f.LastSeen > now+300 {
		return false
	}
	if f.FirstSeen < minTime || f.FirstSeen > f.LastSeen {
		f.FirstSeen = f.LastSeen
	}
	f.Process = observe.CleanProcess(f.Process)
	if f.Process == "" {
		f.PID = 0
	}
	return true
}

func (s *Service) internalPrefixes(ctx context.Context) []netip.Prefix {
	ps, err := policy.ParseRanges(s.InternalRanges(ctx))
	if err != nil {
		ps, _ = policy.ParseRanges(policy.DefaultInternalRanges)
	}
	return ps
}

// ingestFlows stores an M8 agent's "flows" report (socket sampling, no
// processes).
func (s *Service) ingestFlows(ctx context.Context, hostID string, payload []byte) {
	var fl []observe.Flow
	if err := json.Unmarshal(payload, &fl); err != nil {
		return
	}
	s.storeFlows(ctx, hostID, fl, observe.SourceSockets)
	s.d.Store.W.UpsertNetCollector(ctx, db.UpsertNetCollectorParams{HostID: hostID, Source: observe.SourceSockets,
		Detail:        "this agent version samples sockets every 30 seconds and reports no process names; update the agent for conntrack collection",
		SampleSeconds: 30, ReportedAt: sql.NullInt64{Int64: s.now().Unix(), Valid: true}})
}

// ingestReport stores a "netflows" report (M10 agents).
func (s *Service) ingestReport(ctx context.Context, hostID string, payload []byte) {
	var r observe.Report
	if err := json.Unmarshal(payload, &r); err != nil {
		return
	}
	src := r.Source
	if src != observe.SourceConntrack && src != observe.SourceSockets {
		return
	}
	s.storeFlows(ctx, hostID, r.Flows, src)
	detail := r.Detail
	if len(detail) > 500 {
		detail = detail[:500]
	}
	s.d.Store.W.UpsertNetCollector(ctx, db.UpsertNetCollectorParams{HostID: hostID, Source: src, Detail: detail,
		SampleSeconds: int64(max(0, min(r.Every, 3600))), EventsLost: max(0, r.EventsLost), Dropped: max(0, r.Dropped),
		Forwarded: max(0, r.Forwarded), ReportedAt: sql.NullInt64{Int64: s.now().Unix(), Valid: true}})
}

func (s *Service) storeFlows(ctx context.Context, hostID string, fl []observe.Flow, source string) int {
	if len(fl) > maxFlowsPerReport {
		s.log.Warn("network observation report truncated", "host", hostID, "flows", len(fl), "kept", maxFlowsPerReport)
		fl = fl[:maxFlowsPerReport]
	}
	internal := s.internalPrefixes(ctx)
	now := s.now().Unix()
	stored, rejected := 0, 0
	// One transaction per chunk: a commit per row would cap ingestion at a
	// few thousand flows a second for the whole fleet.
	const chunk = 500
	for start := 0; start < len(fl); start += chunk {
		part := fl[start:min(start+chunk, len(fl))]
		s.d.Store.InTx(ctx, func(q *db.Queries) error {
			for _, f := range part {
				if !validFlow(&f, now) {
					rejected++
					continue
				}
				scope := netmon.Classify(netip.MustParseAddr(f.Peer), internal).Scope
				err := q.UpsertFwObservation(ctx, db.UpsertFwObservationParams{HostID: hostID, Direction: f.Direction, Protocol: f.Protocol,
					LocalIp: f.LocalIP, Port: int64(f.Port), Peer: f.Peer, LocalPort: int64(f.LocalPort), RemotePort: int64(f.RemotePort),
					Scope: scope, Source: source, Process: f.Process, Pid: int64(f.PID), Count: f.Count, FirstSeen: f.FirstSeen, LastSeen: f.LastSeen})
				if err != nil {
					return err
				}
				stored++
			}
			return nil
		})
	}
	if rejected > 0 {
		// Counts only: observation records never go to the log.
		s.log.Warn("rejected malformed network observations", "host", hostID, "rejected", rejected, "stored", stored)
	}
	return stored
}

// Rescope recomputes every observation's scope after internal_ranges
// changed (one UPDATE per distinct remote address).
func (s *Service) Rescope(ctx context.Context) error {
	peers, err := s.d.Store.R.ListObservationPeers(ctx)
	if err != nil {
		return err
	}
	internal := s.internalPrefixes(ctx)
	done := map[string]bool{}
	for _, p := range peers {
		if done[p.Peer] {
			continue
		}
		done[p.Peer] = true
		a, err := netip.ParseAddr(p.Peer)
		if err != nil {
			continue
		}
		if sc := netmon.Classify(a, internal).Scope; sc != p.Scope {
			if err := s.d.Store.W.SetObservationScope(ctx, db.SetObservationScopeParams{Scope: sc, Peer: p.Peer, Scope_2: sc}); err != nil {
				return err
			}
		}
	}
	return nil
}

// Snapshot takes a one-off connection snapshot (ss) — the only kind of
// observation SSH hosts get (DESIGN §19.1: not "learning", never
// continuous).
func (s *Service) Snapshot(ctx context.Context, hostID string) (int, error) {
	h, err := s.d.Store.R.GetHost(ctx, hostID)
	if err != nil {
		return 0, ErrNotFound
	}
	c, _, err := s.openConn(ctx, h)
	if err != nil {
		return 0, err
	}
	defer c.Close()
	res, err := c.Run(ctx, hostfw.Action{Kind: hostfw.Snapshot})
	if err != nil {
		return 0, err
	}
	if sc, ok := c.(*sshConn); ok {
		s.recordSSHPath(ctx, sc.r, hostID) // so the snapshot's own connection reads as the management path
	}
	conns := observe.Parse(res.Sections)
	agg := observe.NewAggregator()
	agg.Add(conns, s.now().Unix())
	fl := agg.Drain()
	s.storeFlows(ctx, hostID, fl, observe.SourceSnapshot)
	s.d.Store.W.SetNetSnapshotAt(ctx, db.SetNetSnapshotAtParams{HostID: hostID, SnapshotAt: sql.NullInt64{Int64: s.now().Unix(), Valid: true}})
	s.audit(ctx, "firewall.snapshot", hostID, audit.Success, map[string]any{"connections": len(conns), "flows": len(fl)})
	return len(fl), nil
}

// --- M8 views: the host firewall card ---

// Observation is an aggregated flow with its classification.
type Observation struct {
	Direction  string `json:"direction"`
	Protocol   string `json:"protocol"`
	Port       int    `json:"port"`
	Peer       string `json:"peer"`
	PeerHostID string `json:"peer_host_id,omitempty"`
	PeerHost   string `json:"peer_host,omitempty"`
	Service    string `json:"service,omitempty"`
	Process    string `json:"process,omitempty"`
	Source     string `json:"source"` // conntrack | sockets (continuous) | snapshot
	Count      int64  `json:"count"`
	FirstSeen  string `json:"first_seen"`
	LastSeen   string `json:"last_seen"`
	// Classified: an explicit policy or the management path covers it.
	// Unclassified flows are what Guided mode suggests policies for.
	Classified bool   `json:"classified"`
	Verdict    string `json:"verdict"` // what the desired policy would do
	Reason     string `json:"reason"`
}

func addrIndex(w *compile.World) map[netip.Addr]compile.HostRef {
	m := map[netip.Addr]compile.HostRef{}
	for _, h := range w.Hosts {
		for _, a := range h.Addrs {
			m[a] = h
		}
	}
	return m
}

// ServiceFor names the service a protocol/port belongs to.
func ServiceFor(svcs map[string]policy.Service, proto string, port int) string {
	names := make([]string, 0, len(svcs))
	for n := range svcs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		for _, e := range svcs[n].Entries {
			if e.Protocol != proto {
				continue
			}
			if prs, err := policy.ParsePorts(e.Ports); err == nil {
				for _, r := range prs {
					if port >= r.Lo && port <= r.Hi {
						return n
					}
				}
			}
		}
	}
	return ""
}

// Observations lists a host's flows, classified against its desired policy.
func (s *Service) Observations(ctx context.Context, hostID string, limit int) ([]Observation, error) {
	if _, err := s.d.Store.R.GetHost(ctx, hostID); err != nil {
		return nil, ErrNotFound
	}
	rows, err := s.d.Store.R.ListFwObservations(ctx, db.ListFwObservationsParams{HostID: hostID, Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	cl, err := s.NewClassifier(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Observation, 0, len(rows))
	for _, r := range rows {
		o := Observation{Direction: r.Direction, Protocol: r.Protocol, Port: int(r.Port), Peer: r.Peer, Source: r.Source, Count: r.Count,
			Process: r.Process, FirstSeen: time.Unix(r.FirstSeen, 0).UTC().Format(time.RFC3339), LastSeen: time.Unix(r.LastSeen, 0).UTC().Format(time.RFC3339)}
		o.Service = ServiceFor(cl.World().Services, r.Protocol, int(r.Port))
		if a, err := netip.ParseAddr(r.Peer); err == nil {
			if ph, ok := cl.HostByAddr(a); ok {
				o.PeerHostID, o.PeerHost = ph.ID, ph.Name
			}
			c := cl.Classify(ctx, hostID, r.Direction, r.Protocol, int(r.Port), a)
			o.Verdict, o.Reason = c.Verdict, c.Reason
			o.Classified = c.Status == FlowCovered && c.ReasonKind == compile.ReasonPolicy || c.Status == FlowManagement
		}
		out = append(out, o)
	}
	return out, nil
}

// Suggestion is a policy Guided mode proposes from unclassified flows; an
// administrator approves it (creating the policy) or ignores it.
type Suggestion struct {
	Title      string          `json:"title"`
	Policy     policy.Policy   `json:"policy"`
	NewService *policy.Service `json:"new_service,omitempty"`
	Count      int64           `json:"count"`
	Peers      []string        `json:"peers"`
}

// Suggestions proposes allow policies for a host's unclassified inbound
// flows (and outbound ones when outbound traffic is restricted).
func (s *Service) Suggestions(ctx context.Context, hostID string) ([]Suggestion, error) {
	h, err := s.d.Store.R.GetHost(ctx, hostID)
	if err != nil {
		return nil, ErrNotFound
	}
	obs, err := s.Observations(ctx, hostID, 500)
	if err != nil {
		return nil, err
	}
	restrict := s.HostConfig(ctx, hostID).Outbound == compile.OutboundRestrict
	byKey := map[string]*Suggestion{}
	var order []string
	for _, o := range obs {
		if o.Classified || (o.Direction == "out" && !restrict) {
			continue
		}
		peer := policy.Selector{Kind: policy.SelCIDR, CIDR: o.Peer + "/32"}
		label := o.Peer
		if a, err := netip.ParseAddr(o.Peer); err == nil && a.Is6() {
			peer.CIDR = o.Peer + "/128"
		}
		if o.PeerHostID != "" {
			peer, label = policy.Selector{Kind: policy.SelHost, ID: o.PeerHostID}, o.PeerHost
		}
		self := policy.Selector{Kind: policy.SelHost, ID: h.ID}
		svc := o.Service
		var newSvc *policy.Service
		if svc == "" {
			svc = o.Protocol + "-" + strconv.Itoa(o.Port)
			newSvc = &policy.Service{Name: svc, Description: fmt.Sprintf("%s port %d (observed)", o.Protocol, o.Port),
				Entries: []policy.Entry{{Protocol: o.Protocol, Ports: strconv.Itoa(o.Port)}}}
		}
		from, to, title := peer, self, fmt.Sprintf("Allow %s → %s %s", label, h.Name, svc)
		if o.Direction == "out" {
			from, to, title = self, peer, fmt.Sprintf("Allow %s → %s %s", h.Name, label, svc)
		}
		key := o.Direction + from.String() + to.String() + svc
		sg, ok := byKey[key]
		if !ok {
			sg = &Suggestion{Title: title, NewService: newSvc, Policy: policy.Policy{Name: fmt.Sprintf("%s-%s-%s", h.Name, svc, short(label)),
				Description: "Suggested from observed connections", Enabled: true, Action: policy.Allow,
				From: []policy.Selector{from}, To: []policy.Selector{to}, Services: []string{svc}}}
			byKey[key] = sg
			order = append(order, key)
		}
		sg.Count += o.Count
		sg.Peers = append(sg.Peers, o.Peer)
	}
	out := make([]Suggestion, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out, nil
}

// DraftSuggestion is a rule for the Firewall Designer's draft, proposed
// from one observed flow (M10). It is never saved by DROOL: the designer
// adds it to a draft, which then goes through validation, the
// management-path checks, preview, save and a safe apply like any edit.
type DraftSuggestion struct {
	Title      string            `json:"title"`
	Rule       document.Rule     `json:"rule"`
	NewService *document.Service `json:"new_service,omitempty"`
}

// SuggestDraft proposes an allow rule for one flow: hostID's flow in
// direction dir, proto/port, with peer.
func (s *Service) SuggestDraft(ctx context.Context, cl *Classifier, hostID, dir, proto string, port int, peer netip.Addr) (*DraftSuggestion, error) {
	h, ok := cl.World().Hosts[hostID]
	if !ok {
		return nil, ErrNotFound
	}
	peer = peer.Unmap()
	peerRef, label := "cidr:"+netip.PrefixFrom(peer, peer.BitLen()).String(), peer.String()
	if ph, ok := cl.HostByAddr(peer); ok {
		peerRef, label = "host:"+ph.Name, ph.Name
	}
	self := "host:" + h.Name
	svc := ServiceFor(cl.World().Services, proto, port)
	var newSvc *document.Service
	if svc == "" {
		svc = proto + "-" + strconv.Itoa(port)
		for i := 2; cl.World().Services[svc].Name != ""; i++ {
			svc = fmt.Sprintf("%s-%d-%d", proto, port, i)
		}
		newSvc = &document.Service{Name: svc, Description: fmt.Sprintf("%s port %d (observed)", proto, port),
			Entries: []policy.Entry{{Protocol: proto, Ports: strconv.Itoa(port)}}}
	}
	from, to := peerRef, self
	if dir == "out" {
		from, to = self, peerRef
	}
	fromL, toL := label, h.Name
	if dir == "out" {
		fromL, toL = h.Name, label
	}
	name := short(fmt.Sprintf("%s-%s-%s", fromL, toL, svc))
	return &DraftSuggestion{Title: fmt.Sprintf("Allow %s → %s %s", fromL, toL, svc), NewService: newSvc,
		Rule: document.Rule{Name: name, Description: "Created from an observed connection", Enabled: true, Action: policy.Allow,
			From: []string{from}, To: []string{to}, Services: []string{svc}}}, nil
}

func short(s string) string {
	out := []rune{}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			out = append(out, r)
		} else if r >= 'A' && r <= 'Z' {
			out = append(out, r+32)
		} else if len(out) > 0 && out[len(out)-1] != '-' {
			out = append(out, '-')
		}
	}
	if len(out) > 48 {
		out = out[:48]
	}
	for len(out) > 0 && out[len(out)-1] == '-' {
		out = out[:len(out)-1]
	}
	return string(out)
}
