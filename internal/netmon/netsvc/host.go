package netsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"

	"drool/internal/discovery"
	"drool/internal/firewall/fwsvc"
	"drool/internal/firewall/observe"
	"drool/internal/netmon"
)

// Collection is how a host's connections are observed — stated honestly:
// socket sampling misses short-lived connections, and SSH hosts are never
// continuously monitored.
type Collection struct {
	// Mode: continuous (an agent reports), snapshot (SSH: on request only)
	// or none.
	Mode          string `json:"mode"`
	Source        string `json:"source"` // conntrack | sockets | snapshot | ""
	Label         string `json:"label"`  // "Continuous: conntrack", "Snapshot only: SSH", ...
	Detail        string `json:"detail"`
	Agent         bool   `json:"agent"`
	Enabled       bool   `json:"enabled"` // continuous collection is turned on for this host
	SampleSeconds int    `json:"sample_seconds,omitempty"`
	ReportedAt    string `json:"reported_at,omitempty"`
	SnapshotAt    string `json:"snapshot_at,omitempty"`
	EventsLost    int64  `json:"events_lost"`
	Dropped       int64  `json:"dropped"`
	Forwarded     int64  `json:"forwarded"`
}

func (s *Service) collection(ctx context.Context, hostID string) Collection {
	c := Collection{Mode: "none"}
	h, err := s.d.Store.R.GetHost(ctx, hostID)
	if err != nil {
		return c
	}
	c.Agent = h.ManagementMethod == "agent"
	nc, haveNC := s.d.Store.R.GetNetCollector(ctx, hostID)
	if haveNC == nil {
		if nc.SnapshotAt.Valid {
			c.SnapshotAt = ts(nc.SnapshotAt.Int64)
		}
		c.EventsLost, c.Dropped, c.Forwarded = nc.EventsLost, nc.Dropped, nc.Forwarded
	}
	set := s.Settings(ctx)
	mode := ""
	if s.d.Firewall != nil {
		mode = s.d.Firewall.HostConfig(ctx, hostID).Mode
	}
	c.Enabled = c.Agent && set.Collects(mode)
	switch {
	case c.Agent && c.Enabled:
		c.Mode = "continuous"
		if haveNC == nil && nc.ReportedAt.Valid && nc.Source != "" {
			c.Source, c.Detail, c.SampleSeconds, c.ReportedAt = nc.Source, nc.Detail, int(nc.SampleSeconds), ts(nc.ReportedAt.Int64)
		}
		switch c.Source {
		case observe.SourceConntrack:
			c.Label = "Continuous: conntrack"
		case observe.SourceSockets:
			every := c.SampleSeconds
			if every == 0 {
				every = 30
			}
			c.Label = fmt.Sprintf("Continuous: socket sampling (every %d s)", every)
			if c.Detail == "" {
				c.Detail = "connections that open and close between samples are not seen"
			} else {
				c.Detail += "; connections that open and close between samples are not seen"
			}
		default:
			c.Label = "Continuous: waiting for the agent's first report"
		}
	case c.Agent:
		c.Label = "Not collecting"
		c.Detail = "continuous collection is off for this host (Network settings)"
		if c.SnapshotAt != "" {
			c.Mode, c.Source, c.Label = "snapshot", observe.SourceSnapshot, "Snapshot only"
		}
	default:
		c.Mode, c.Source, c.Label = "snapshot", observe.SourceSnapshot, "Snapshot only: SSH"
		c.Detail = "SSH hosts are never continuously monitored: a snapshot shows the connections open at that moment"
		if c.SnapshotAt == "" {
			c.Detail += " (no snapshot taken yet)"
		}
	}
	return c
}

// Listening is a listening socket from the host's inventory, with what was
// observed arriving on it.
type Listening struct {
	Protocol string `json:"protocol"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Process  string `json:"process"` // "" = unknown
	Service  string `json:"service"`
	// Exposure: loopback (local only), or all/specific addresses.
	Exposure string `json:"exposure"`
	// Inbound: observed inbound flows to this port and their peers.
	Flows int64 `json:"flows"`
	Peers int64 `json:"peers"`
}

// HostNetwork is a host's Network summary.
type HostNetwork struct {
	Host        HostRef            `json:"host"`
	Collection  Collection         `json:"collection"`
	Firewall    fwsvc.HostFirewall `json:"firewall"`
	Listening   []Listening        `json:"listening"`
	ListeningAt string             `json:"listening_at,omitempty"` // when the inventory last collected them
	Counts      Counts             `json:"counts"`
	Enrichment  bool               `json:"enrichment"` // GeoIP/ASN data is available
	// EnrichmentState: per kind, off, unavailable (no database) or on.
	EnrichmentState EnrichState `json:"enrichment_state"`
}

// Counts summarise a set of flows.
type Counts struct {
	Flows        int64 `json:"flows"`
	Inbound      int64 `json:"inbound"`
	Outbound     int64 `json:"outbound"`
	Internal     int64 `json:"internal"`
	External     int64 `json:"external"`
	Unclassified int64 `json:"unclassified"`
	Denied       int64 `json:"denied"`
	NewExternal  int64 `json:"new_external_24h"` // public destinations first seen in the last 24 hours
}

// Host returns a host's network summary.
func (s *Service) Host(ctx context.Context, hostID string) (*HostNetwork, error) {
	if _, err := s.d.Store.R.GetHost(ctx, hostID); err != nil {
		return nil, fwsvc.ErrNotFound
	}
	v, err := s.newView(ctx)
	if err != nil {
		return nil, err
	}
	out := &HostNetwork{Host: v.hostRef(hostID), Collection: s.collection(ctx, hostID), Firewall: v.cl.Firewall(ctx, hostID),
		Listening: []Listening{}, Enrichment: s.geo.Available() && v.enrich, EnrichmentState: s.enrichState(v.enrich)}
	out.Counts = s.counts(ctx, v, Filter{Hosts: []string{hostID}})
	// Listening ports come from the inventory (one source of truth).
	inbound := map[string][2]int64{}
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT protocol, port, count(*), count(DISTINCT peer) FROM fw_observations
		WHERE host_id = ? AND direction = 'in' GROUP BY protocol, port`, hostID)
	if err == nil {
		for rows.Next() {
			var proto string
			var port, n, peers int64
			if rows.Scan(&proto, &port, &n, &peers) == nil {
				inbound[fmt.Sprintf("%s %d", proto, port)] = [2]int64{n, peers}
			}
		}
		rows.Close()
	}
	if f, err := s.d.Store.R.GetHostFacts(ctx, hostID); err == nil {
		var facts discovery.Facts
		if json.Unmarshal([]byte(f.FactsJson), &facts) == nil {
			out.ListeningAt = ts(f.CollectedAt / 1000) // milliseconds
			seen := map[string]bool{}
			for _, p := range facts.Ports {
				k := fmt.Sprintf("%s|%s|%d", p.Proto, p.Address, p.Port)
				if seen[k] {
					continue
				}
				seen[k] = true
				l := Listening{Protocol: p.Proto, Address: p.Address, Port: p.Port, Process: observe.CleanProcess(p.Process),
					Service: fwsvc.ServiceFor(v.cl.World().Services, p.Proto, p.Port)}
				a, err := netip.ParseAddr(strings.Trim(strings.Split(p.Address, "%")[0], "[]"))
				switch {
				case p.Address == "*" || (err == nil && a.IsUnspecified()):
					l.Exposure = "all addresses"
				case err == nil && a.IsLoopback():
					l.Exposure = "this host only (loopback)"
				default:
					l.Exposure = "one address"
				}
				in := inbound[fmt.Sprintf("%s %d", p.Proto, p.Port)]
				l.Flows, l.Peers = in[0], in[1]
				out.Listening = append(out.Listening, l)
			}
		}
	}
	sort.Slice(out.Listening, func(i, j int) bool {
		a, b := out.Listening[i], out.Listening[j]
		if a.Port != b.Port {
			return a.Port < b.Port
		}
		if a.Protocol != b.Protocol {
			return a.Protocol < b.Protocol
		}
		return a.Address < b.Address
	})
	return out, nil
}

// counts summarises flows matching f (database filters only).
func (s *Service) counts(ctx context.Context, v *view, f Filter) Counts {
	var c Counts
	where, args := f.sqlWhere()
	since := s.now().Add(-24 * time.Hour).Unix()
	q := `SELECT count(*),
		coalesce(sum(o.direction = 'in'), 0), coalesce(sum(o.direction = 'out'), 0),
		coalesce(sum(o.scope = 'internal'), 0), coalesce(sum(o.scope = 'public'), 0)
		FROM fw_observations o WHERE ` + where
	s.d.Store.Reader.QueryRowContext(ctx, q, args...).Scan(&c.Flows, &c.Inbound, &c.Outbound, &c.Internal, &c.External)
	nq := `SELECT count(DISTINCT o.host_id || ' ' || o.peer) FROM fw_observations o WHERE ` + where + ` AND o.scope = 'public' AND o.direction = 'out'
		AND (o.host_id, o.peer) IN (SELECT o2.host_id, o2.peer FROM fw_observations o2 WHERE o2.scope = 'public' GROUP BY o2.host_id, o2.peer HAVING min(o2.first_seen) >= ?)`
	s.d.Store.Reader.QueryRowContext(ctx, nq, append(append([]any{}, args...), since)...).Scan(&c.NewExternal)
	// Policy status needs the classifier: count over the most recent rows
	// (bounded), which is what the summary is for.
	rows, err := s.d.Store.Reader.QueryContext(ctx, "SELECT "+cols+" FROM fw_observations o WHERE "+where+" ORDER BY o.last_seen DESC LIMIT 20000", args...)
	if err != nil {
		return c
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			break
		}
		a, err := netip.ParseAddr(r.Peer)
		if err != nil {
			continue
		}
		cor := v.cl.Classify(ctx, r.HostID, r.Direction, r.Protocol, int(r.Port), a)
		if cor.Status == fwsvc.FlowUnclassified {
			c.Unclassified++
		}
		if cor.Status == fwsvc.FlowDenied || cor.WouldDeny {
			c.Denied++
		}
	}
	return c
}

// Summary is the fleet's Network Activity header.
type Summary struct {
	Counts     Counts     `json:"counts"`
	Hosts      HostCounts `json:"hosts"`
	Enrichment bool       `json:"enrichment"`
	// EnrichmentState: per kind, off, unavailable (no database) or on.
	EnrichmentState EnrichState     `json:"enrichment_state"`
	Settings        netmon.Settings `json:"settings"`
	LastPrune       PruneResult     `json:"last_prune"`
}

// HostCounts says how the fleet is observed.
type HostCounts struct {
	Total      int `json:"total"`
	Conntrack  int `json:"conntrack"`
	Sockets    int `json:"sockets"`
	Snapshot   int `json:"snapshot"`    // SSH hosts with a snapshot
	NoSnapshot int `json:"no_snapshot"` // SSH hosts never snapshotted
	Off        int `json:"off"`         // agent hosts with collection off or no report yet
}

// FleetSummary summarises the fleet (or the flows matching f).
func (s *Service) FleetSummary(ctx context.Context, f Filter) (*Summary, error) {
	if err := f.Validate(); err != nil {
		return nil, fwsvc.ValidationError("%v", err)
	}
	v, err := s.newView(ctx)
	if err != nil {
		return nil, err
	}
	out := &Summary{Counts: s.counts(ctx, v, f), Enrichment: s.geo.Available() && v.enrich, EnrichmentState: s.enrichState(v.enrich),
		Settings: s.Settings(ctx), LastPrune: s.LastPrune()}
	hosts, err := s.d.Store.R.ListHosts(ctx)
	if err != nil {
		return nil, err
	}
	for _, h := range hosts {
		c := s.collection(ctx, h.ID)
		out.Hosts.Total++
		switch {
		case c.Source == observe.SourceConntrack:
			out.Hosts.Conntrack++
		case c.Source == observe.SourceSockets:
			out.Hosts.Sockets++
		case c.Mode == "snapshot" && c.SnapshotAt != "":
			out.Hosts.Snapshot++
		case c.Mode == "snapshot":
			out.Hosts.NoSnapshot++
		default:
			out.Hosts.Off++
		}
	}
	return out, nil
}

// --- facets ---

// Facet is one value of a filter with the number of flows that have it.
type Facet struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Count int64  `json:"count"`
}

// Facets are the values the filters offer.
type Facets struct {
	Hosts     []Facet `json:"hosts"`
	Groups    []Facet `json:"groups"`
	Processes []Facet `json:"processes"`
	Ports     []Facet `json:"ports"`
	Countries []Facet `json:"countries"`
	Networks  []Facet `json:"networks"` // ASNs
	Services  []Facet `json:"services"`
	// Partial: country and network facets cover the most recent public
	// addresses only.
	Partial bool `json:"partial"`
}

const facetPeers = 20000

// Facets lists filter values (optionally for some hosts).
func (s *Service) Facets(ctx context.Context, hosts []string) (*Facets, error) {
	v, err := s.newView(ctx)
	if err != nil {
		return nil, err
	}
	out := &Facets{Hosts: []Facet{}, Groups: []Facet{}, Processes: []Facet{}, Ports: []Facet{}, Countries: []Facet{}, Networks: []Facet{}, Services: []Facet{}}
	where, args := (&Filter{Hosts: hosts}).sqlWhere()
	q := func(sqlq string, fn func(rs interface{ Scan(...any) error })) {
		rows, err := s.d.Store.Reader.QueryContext(ctx, sqlq, args...)
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			fn(rows)
		}
	}
	q("SELECT o.host_id, count(*) FROM fw_observations o WHERE "+where+" GROUP BY o.host_id ORDER BY count(*) DESC", func(r interface{ Scan(...any) error }) {
		var id string
		var n int64
		if r.Scan(&id, &n) == nil {
			out.Hosts = append(out.Hosts, Facet{Value: id, Label: v.cl.World().Hosts[id].Name, Count: n})
		}
	})
	sort.Slice(out.Hosts, func(i, j int) bool { return out.Hosts[i].Label < out.Hosts[j].Label })
	gcount := map[string]int64{}
	for _, h := range out.Hosts {
		for _, g := range v.groups[h.Value] {
			gcount[g] += h.Count
		}
	}
	for _, g := range v.cl.World().Groups {
		if n, ok := gcount[g.Name]; ok {
			out.Groups = append(out.Groups, Facet{Value: g.ID, Label: g.Name, Count: n})
		}
	}
	sort.Slice(out.Groups, func(i, j int) bool { return out.Groups[i].Label < out.Groups[j].Label })
	q("SELECT o.process, count(*) FROM fw_observations o WHERE "+where+" GROUP BY o.process ORDER BY count(*) DESC LIMIT 200", func(r interface{ Scan(...any) error }) {
		var p string
		var n int64
		if r.Scan(&p, &n) == nil {
			if p == "" {
				out.Processes = append(out.Processes, Facet{Value: "unknown", Label: "Unknown", Count: n})
			} else {
				out.Processes = append(out.Processes, Facet{Value: p, Label: p, Count: n})
			}
		}
	})
	svc := map[string]int64{}
	q("SELECT o.protocol, o.port, count(*) FROM fw_observations o WHERE "+where+" GROUP BY o.protocol, o.port ORDER BY count(*) DESC LIMIT 200", func(r interface{ Scan(...any) error }) {
		var proto string
		var port, n int64
		if r.Scan(&proto, &port, &n) == nil {
			name := fwsvc.ServiceFor(v.cl.World().Services, proto, int(port))
			label := fmt.Sprintf("%s %d", strings.ToUpper(proto), port)
			if name != "" {
				label += " (" + name + ")"
				svc[name] += n
			}
			out.Ports = append(out.Ports, Facet{Value: fmt.Sprintf("%s/%d", proto, port), Label: label, Count: n})
		}
	})
	for name, n := range svc {
		out.Services = append(out.Services, Facet{Value: name, Label: name, Count: n})
	}
	sort.Slice(out.Services, func(i, j int) bool { return out.Services[i].Count > out.Services[j].Count })
	if v.enrich && s.geo.Available() {
		countries, nets := map[string]*Facet{}, map[uint32]*Facet{}
		seen := 0
		q("SELECT o.peer, count(*) FROM fw_observations o WHERE "+where+" AND o.scope = 'public' GROUP BY o.peer ORDER BY max(o.last_seen) DESC LIMIT "+fmt.Sprint(facetPeers+1),
			func(r interface{ Scan(...any) error }) {
				var p string
				var n int64
				if r.Scan(&p, &n) != nil {
					return
				}
				if seen++; seen > facetPeers {
					out.Partial = true
					return
				}
				a, err := netip.ParseAddr(p)
				if err != nil {
					return
				}
				g := v.geo(a)
				if g == nil {
					return
				}
				if g.CountryCode != "" {
					if countries[g.CountryCode] == nil {
						countries[g.CountryCode] = &Facet{Value: g.CountryCode, Label: g.Country}
					}
					countries[g.CountryCode].Count += n
				}
				if g.ASN != 0 {
					if nets[g.ASN] == nil {
						nets[g.ASN] = &Facet{Value: fmt.Sprint(g.ASN), Label: fmt.Sprintf("AS%d %s", g.ASN, g.Org)}
					}
					nets[g.ASN].Count += n
				}
			})
		for _, f := range countries {
			if f.Label == "" {
				f.Label = f.Value
			}
			out.Countries = append(out.Countries, *f)
		}
		for _, f := range nets {
			out.Networks = append(out.Networks, *f)
		}
		sort.Slice(out.Countries, func(i, j int) bool { return out.Countries[i].Label < out.Countries[j].Label })
		sort.Slice(out.Networks, func(i, j int) bool { return out.Networks[i].Count > out.Networks[j].Count })
		if len(out.Networks) > 300 {
			out.Networks, out.Partial = out.Networks[:300], true
		}
	}
	return out, nil
}
