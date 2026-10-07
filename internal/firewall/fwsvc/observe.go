package fwsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"time"

	"drool/internal/audit"
	"drool/internal/firewall/compile"
	"drool/internal/firewall/hostfw"
	"drool/internal/firewall/observe"
	"drool/internal/firewall/policy"
	"drool/internal/store/db"
)

func (s *Service) ingestFlows(ctx context.Context, hostID string, payload []byte, source string) {
	var fl []observe.Flow
	if err := json.Unmarshal(payload, &fl); err != nil {
		return
	}
	s.storeFlows(ctx, hostID, fl, source)
}

func (s *Service) storeFlows(ctx context.Context, hostID string, fl []observe.Flow, source string) {
	for _, f := range fl {
		if f.Direction != "in" && f.Direction != "out" {
			continue
		}
		s.d.Store.W.UpsertFwObservation(ctx, db.UpsertFwObservationParams{HostID: hostID, Direction: f.Direction, Protocol: f.Protocol,
			Port: int64(f.Port), Peer: f.Peer, Source: source, Count: f.Count, FirstSeen: f.FirstSeen, LastSeen: f.LastSeen})
	}
}

// Snapshot takes a one-off connection snapshot (ss) — the only kind of
// observation SSH hosts get (DESIGN §19.1: not "learning").
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
	s.storeFlows(ctx, hostID, fl, "snapshot")
	s.audit(ctx, "firewall.snapshot", hostID, audit.Success, map[string]any{"connections": len(conns), "flows": len(fl)})
	return len(fl), nil
}

// Observation is an aggregated flow with its classification.
type Observation struct {
	Direction  string `json:"direction"`
	Protocol   string `json:"protocol"`
	Port       int    `json:"port"`
	Peer       string `json:"peer"`
	PeerHostID string `json:"peer_host_id,omitempty"`
	PeerHost   string `json:"peer_host,omitempty"`
	Service    string `json:"service,omitempty"`
	Source     string `json:"source"` // agent (continuous) | snapshot
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

// serviceFor names the service a protocol/port belongs to.
func serviceFor(svcs map[string]policy.Service, proto string, port int) string {
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
	h, err := s.d.Store.R.GetHost(ctx, hostID)
	if err != nil {
		return nil, ErrNotFound
	}
	rows, err := s.d.Store.R.ListFwObservations(ctx, db.ListFwObservationsParams{HostID: hostID, Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	w, err := s.World(ctx)
	if err != nil {
		return nil, err
	}
	hp, _ := s.compileHost(ctx, w, h, false)
	idx := addrIndex(w)
	out := make([]Observation, 0, len(rows))
	for _, r := range rows {
		o := Observation{Direction: r.Direction, Protocol: r.Protocol, Port: int(r.Port), Peer: r.Peer, Source: r.Source, Count: r.Count,
			FirstSeen: time.Unix(r.FirstSeen, 0).UTC().Format(time.RFC3339), LastSeen: time.Unix(r.LastSeen, 0).UTC().Format(time.RFC3339)}
		o.Service = serviceFor(w.Services, r.Protocol, int(r.Port))
		if a, err := netip.ParseAddr(r.Peer); err == nil {
			a = a.Unmap()
			if ph, ok := idx[a]; ok {
				o.PeerHostID, o.PeerHost = ph.ID, ph.Name
			}
			fam := 6
			if a.Is4() {
				fam = 4
			}
			v := hp.Evaluate(r.Direction, compile.Packet{Family: fam, Proto: r.Protocol, Port: int(r.Port), Peer: a})
			o.Verdict, o.Reason = v.Verdict, v.Reason.Text
			o.Classified = v.Reason.Kind == compile.ReasonPolicy || v.Reason.Kind == compile.ReasonManagement
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
	if len(out) > 24 {
		out = out[:24]
	}
	return string(out)
}
