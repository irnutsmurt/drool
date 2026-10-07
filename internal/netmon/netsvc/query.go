package netsvc

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"drool/internal/firewall/fwsvc"
	"drool/internal/netmon"
	"drool/internal/netmon/geoip"
	"drool/internal/store/db"
)

// Filter selects observations. Empty fields don't filter.
type Filter struct {
	Hosts      []string // host ids
	Group      string   // group id: its members' flows
	Direction  string   // in | out
	Scope      string   // internal | public | special (external = public)
	Protocol   string   // tcp | udp
	Port       int      // the service port, either direction
	LocalPort  int      // inbound flows to this local port
	RemotePort int      // outbound flows to this remote port
	Process    string   // substring, case-insensitive; "unknown" = no process
	Service    string   // a named service (built-in or custom)
	Country    string   // ISO code
	ASN        uint32
	Org        string // substring of the AS organization
	Peer       string // an address or a CIDR
	Policy     string // management | covered | unclassified | denied (incl. would be denied)
	Source     string // conntrack | sockets | snapshot
	FirstAfter int64  // unix seconds
	LastAfter  int64
	// NewDest: only public destinations first seen (within retention) after
	// NewSince: "host" = new for that host, "fleet" = new for every host.
	NewDest  string
	NewSince int64
	Q        string // free text: address, host, process, organization
	Sort     string // last_seen (default) | first_seen | count
	Limit    int
	Cursor   string
}

// Geo is a public address's enrichment.
type Geo struct {
	CountryCode string `json:"country_code,omitempty"`
	Country     string `json:"country,omitempty"`
	ASN         uint32 `json:"asn,omitempty"`
	Org         string `json:"org,omitempty"`
}

// HostRef names a DROOL host.
type HostRef struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Groups []string `json:"groups"`
}

// Flow is one observation, enriched and correlated.
type Flow struct {
	ID         int64   `json:"id"`
	Host       HostRef `json:"host"`
	Direction  string  `json:"direction"`
	Protocol   string  `json:"protocol"`
	LocalIP    string  `json:"local_ip"`
	LocalPort  int     `json:"local_port"`
	RemoteIP   string  `json:"remote_ip"`
	RemotePort int     `json:"remote_port"`
	Port       int     `json:"port"`    // the service port
	Service    string  `json:"service"` // the named service the port belongs to, if any
	Process    string  `json:"process"` // "" = unknown
	PID        int     `json:"pid,omitempty"`
	Source     string  `json:"source"` // conntrack | sockets | snapshot
	Count      int64   `json:"count"`
	FirstSeen  string  `json:"first_seen"`
	LastSeen   string  `json:"last_seen"`
	// Scope of the remote address; ScopeRange the internal range or the
	// kind of special address.
	Scope      string   `json:"scope"`
	ScopeRange string   `json:"scope_range,omitempty"`
	PeerHost   *HostRef `json:"peer_host"`              // the DROOL host owning the remote address
	PeerNet    string   `json:"peer_network,omitempty"` // a named firewall network containing it
	Geo        *Geo     `json:"geo"`                    // public addresses with enrichment only
	// PeerFirstSeen: when this host first talked to the remote address
	// (any port, retained history).
	PeerFirstSeen string            `json:"peer_first_seen,omitempty"`
	Policy        fwsvc.Correlation `json:"policy"`
}

// Page is a page of flows.
type Page struct {
	Items      []Flow `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
	// Total is known when every filter ran in the database; with filters
	// that need the policy or the enrichment (policy, country, ASN,
	// organization, service, peer CIDR, free text) only "more" is.
	Total *int64 `json:"total"`
	// Scanned rows; Truncated: the scan limit was reached before the page
	// filled (continue with NextCursor).
	Scanned   int  `json:"scanned"`
	Truncated bool `json:"truncated"`
}

// Limits.
const (
	MaxLimit  = 500
	scanChunk = 1000
	maxScan   = 200000
)

func (f *Filter) sortCol() string {
	switch f.Sort {
	case "first_seen":
		return "first_seen"
	case "count":
		return "count"
	}
	return "last_seen"
}

// cursor: "<sort value>.<id>", base64url.
func encodeCursor(v, id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(v, 10) + "." + strconv.FormatInt(id, 10)))
}

func decodeCursor(s string) (int64, int64, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid cursor")
	}
	a, c, ok := strings.Cut(string(b), ".")
	v, err1 := strconv.ParseInt(a, 10, 64)
	id, err2 := strconv.ParseInt(c, 10, 64)
	if !ok || err1 != nil || err2 != nil {
		return 0, 0, fmt.Errorf("invalid cursor")
	}
	return v, id, nil
}

// Validate normalises and checks a filter.
func (f *Filter) Validate() error {
	switch f.Direction {
	case "", "in", "out":
	default:
		return fmt.Errorf("direction must be in or out")
	}
	if f.Scope == "external" {
		f.Scope = netmon.ScopePublic
	}
	switch f.Scope {
	case "", netmon.ScopeInternal, netmon.ScopePublic, netmon.ScopeSpecial:
	default:
		return fmt.Errorf("scope must be internal, external or special")
	}
	switch f.Protocol {
	case "", "tcp", "udp":
	default:
		return fmt.Errorf("protocol must be tcp or udp")
	}
	for _, p := range []int{f.Port, f.LocalPort, f.RemotePort} {
		if p < 0 || p > 65535 {
			return fmt.Errorf("ports must be between 1 and 65535")
		}
	}
	switch f.Policy {
	case "", fwsvc.FlowManagement, fwsvc.FlowCovered, fwsvc.FlowUnclassified, fwsvc.FlowDenied:
	default:
		return fmt.Errorf("policy must be management, covered, unclassified or denied")
	}
	switch f.Source {
	case "", "conntrack", "sockets", "snapshot":
	default:
		return fmt.Errorf("source must be conntrack, sockets or snapshot")
	}
	switch f.NewDest {
	case "", "host", "fleet":
	default:
		return fmt.Errorf("new must be host or fleet")
	}
	if f.NewDest != "" && f.NewSince == 0 {
		return fmt.Errorf("new needs a since time")
	}
	if f.Country != "" {
		f.Country = strings.ToUpper(f.Country)
		if len(f.Country) != 2 {
			return fmt.Errorf("country must be a two-letter code")
		}
	}
	if f.Peer != "" {
		if _, err := parsePeer(f.Peer); err != nil {
			return err
		}
	}
	switch f.Sort {
	case "", "last_seen", "first_seen", "count":
	default:
		return fmt.Errorf("sort must be last_seen, first_seen or count")
	}
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > MaxLimit {
		f.Limit = MaxLimit
	}
	if f.Cursor != "" {
		if _, _, err := decodeCursor(f.Cursor); err != nil {
			return err
		}
	}
	return nil
}

func parsePeer(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("peer must be an address or a CIDR")
		}
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("peer must be an address or a CIDR")
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// sqlWhere builds the database part of a filter.
func (f *Filter) sqlWhere() (string, []any) {
	var w []string
	var args []any
	add := func(cond string, a ...any) { w = append(w, cond); args = append(args, a...) }
	if len(f.Hosts) > 0 {
		add("o.host_id IN ("+strings.TrimSuffix(strings.Repeat("?,", len(f.Hosts)), ",")+")", anys(f.Hosts)...)
	}
	if f.Group != "" {
		add("o.host_id IN (SELECT host_id FROM group_members WHERE group_id = ?)", f.Group)
	}
	if f.Direction != "" {
		add("o.direction = ?", f.Direction)
	}
	if f.Scope != "" {
		add("o.scope = ?", f.Scope)
	}
	if f.Protocol != "" {
		add("o.protocol = ?", f.Protocol)
	}
	if f.Port > 0 {
		add("o.port = ?", f.Port)
	}
	if f.LocalPort > 0 {
		add("((o.direction = 'in' AND o.port = ?) OR (o.direction = 'out' AND o.local_port = ?))", f.LocalPort, f.LocalPort)
	}
	if f.RemotePort > 0 {
		add("((o.direction = 'out' AND o.port = ?) OR (o.direction = 'in' AND o.remote_port = ?))", f.RemotePort, f.RemotePort)
	}
	switch strings.ToLower(f.Process) {
	case "":
	case "unknown":
		add("o.process = ''")
	default:
		add("o.process LIKE ? ESCAPE '\\'", "%"+likeEscape(f.Process)+"%")
	}
	if f.Source != "" {
		add("o.source = ?", f.Source)
	}
	if f.FirstAfter > 0 {
		add("o.first_seen >= ?", f.FirstAfter)
	}
	if f.LastAfter > 0 {
		add("o.last_seen >= ?", f.LastAfter)
	}
	if f.Peer != "" {
		if p, err := parsePeer(f.Peer); err == nil && p.IsSingleIP() {
			add("o.peer = ?", p.Addr().String())
		}
	}
	if f.Country != "" || f.ASN != 0 || f.Org != "" {
		add("o.scope = 'public'") // only public addresses have a country or network
	}
	// New destinations: one grouped pass over the table (materialised
	// once), not a lookup per row.
	switch f.NewDest {
	case "host":
		add("o.scope = 'public' AND (o.host_id, o.peer) IN (SELECT o2.host_id, o2.peer FROM fw_observations o2 WHERE o2.scope = 'public' GROUP BY o2.host_id, o2.peer HAVING min(o2.first_seen) >= ?)", f.NewSince)
	case "fleet":
		add("o.scope = 'public' AND o.peer IN (SELECT o2.peer FROM fw_observations o2 WHERE o2.scope = 'public' GROUP BY o2.peer HAVING min(o2.first_seen) >= ?)", f.NewSince)
	}
	if len(w) == 0 {
		return "1=1", nil
	}
	return strings.Join(w, " AND "), args
}

func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

func anys(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// goFilters reports whether the filter needs per-row evaluation in Go.
func (f *Filter) goFilters() bool {
	if f.Peer != "" {
		if p, err := parsePeer(f.Peer); err == nil && !p.IsSingleIP() {
			return true
		}
	}
	return f.Policy != "" || f.Country != "" || f.ASN != 0 || f.Org != "" || f.Service != "" || f.Q != ""
}

const cols = `o.id, o.host_id, o.direction, o.protocol, o.local_ip, o.port, o.peer, o.local_port, o.remote_port, o.scope, o.source, o.process, o.pid, o.count, o.first_seen, o.last_seen`

func scanRow(rows *sql.Rows) (db.FwObservation, error) {
	var r db.FwObservation
	err := rows.Scan(&r.ID, &r.HostID, &r.Direction, &r.Protocol, &r.LocalIp, &r.Port, &r.Peer, &r.LocalPort, &r.RemotePort, &r.Scope, &r.Source,
		&r.Process, &r.Pid, &r.Count, &r.FirstSeen, &r.LastSeen)
	return r, err
}

// view builds API flows from rows with one consistent world.
type view struct {
	s        *Service
	cl       *fwsvc.Classifier
	internal []netip.Prefix
	groups   map[string][]string // host id → group names
	enrich   bool
}

func (s *Service) newView(ctx context.Context) (*view, error) {
	cl, err := s.d.Firewall.NewClassifier(ctx)
	if err != nil {
		return nil, err
	}
	v := &view{s: s, cl: cl, internal: cl.World().Internal, groups: map[string][]string{}, enrich: s.Settings(ctx).Enrichment}
	for _, g := range cl.World().Groups {
		for _, m := range g.Members {
			v.groups[m] = append(v.groups[m], g.Name)
		}
	}
	for k := range v.groups {
		sort.Strings(v.groups[k])
	}
	return v, nil
}

func (v *view) hostRef(id string) HostRef {
	h := v.cl.World().Hosts[id]
	g := v.groups[id]
	if g == nil {
		g = []string{}
	}
	return HostRef{ID: id, Name: h.Name, Groups: g}
}

// geo enriches a public address (never anything else).
func (v *view) geo(a netip.Addr) *Geo {
	if !v.enrich || !netmon.IsPublic(a) {
		return nil
	}
	i := v.s.geo.Lookup(a)
	if i.Empty() {
		return nil
	}
	return &Geo{CountryCode: i.CountryCode, Country: i.Country, ASN: i.ASN, Org: i.Org}
}

func (v *view) network(a netip.Addr) string {
	names := make([]string, 0)
	for _, n := range v.cl.World().Networks {
		for _, p := range n.Prefixes {
			if p.Contains(a) {
				names = append(names, n.Name)
				break
			}
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

func ts(u int64) string { return time.Unix(u, 0).UTC().Format(time.RFC3339) }

func (v *view) flow(ctx context.Context, r db.FwObservation) Flow {
	f := Flow{ID: r.ID, Host: v.hostRef(r.HostID), Direction: r.Direction, Protocol: r.Protocol, LocalIP: r.LocalIp, LocalPort: int(r.LocalPort),
		RemoteIP: r.Peer, RemotePort: int(r.RemotePort), Port: int(r.Port), Process: r.Process, PID: int(r.Pid), Source: r.Source,
		Count: r.Count, FirstSeen: ts(r.FirstSeen), LastSeen: ts(r.LastSeen)}
	// The service port is the local port inbound, the remote port outbound.
	if f.Direction == "in" {
		f.LocalPort = f.Port
	} else {
		f.RemotePort = f.Port
	}
	f.Service = fwsvc.ServiceFor(v.cl.World().Services, r.Protocol, int(r.Port))
	a, err := netip.ParseAddr(r.Peer)
	if err != nil {
		f.Scope = r.Scope
		return f
	}
	cls := netmon.Classify(a, v.internal)
	f.Scope, f.ScopeRange = cls.Scope, cls.Range
	if h, ok := v.cl.HostByAddr(a); ok {
		ref := v.hostRef(h.ID)
		f.PeerHost = &ref
	}
	if f.Scope != netmon.ScopePublic {
		f.PeerNet = v.network(a)
	}
	if f.Scope == netmon.ScopePublic {
		f.Geo = v.geo(a)
	}
	f.Policy = v.cl.Classify(ctx, r.HostID, r.Direction, r.Protocol, int(r.Port), a)
	return f
}

// matches applies the filters the database can't.
func (v *view) matches(f *Filter, fl *Flow) bool {
	if f.Policy != "" {
		st := fl.Policy.Status
		if f.Policy == fwsvc.FlowDenied {
			if st != fwsvc.FlowDenied && !fl.Policy.WouldDeny {
				return false
			}
		} else if st != f.Policy {
			return false
		}
	}
	if f.Country != "" && (fl.Geo == nil || fl.Geo.CountryCode != f.Country) {
		return false
	}
	if f.ASN != 0 && (fl.Geo == nil || fl.Geo.ASN != f.ASN) {
		return false
	}
	if f.Org != "" && (fl.Geo == nil || !strings.Contains(strings.ToLower(fl.Geo.Org), strings.ToLower(f.Org))) {
		return false
	}
	if f.Service != "" && fl.Service != f.Service {
		return false
	}
	if f.Peer != "" {
		p, _ := parsePeer(f.Peer)
		if a, err := netip.ParseAddr(fl.RemoteIP); err != nil || !p.Contains(a) {
			return false
		}
	}
	if q := strings.ToLower(strings.TrimSpace(f.Q)); q != "" {
		hay := []string{fl.RemoteIP, fl.LocalIP, fl.Host.Name, fl.Process, fl.Service, fl.PeerNet}
		if fl.PeerHost != nil {
			hay = append(hay, fl.PeerHost.Name)
		}
		if fl.Geo != nil {
			hay = append(hay, fl.Geo.Org, fl.Geo.Country, geoip.ASNString(fl.Geo.ASN))
		}
		found := false
		for _, h := range hay {
			if h != "" && strings.Contains(strings.ToLower(h), q) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// List returns a page of flows, newest first (or by first seen, count).
// Filtering and pagination happen here, never in the browser: SQL narrows
// the rows, then the classifier and enrichment check the rest, scanning
// in chunks until the page is full.
func (s *Service) List(ctx context.Context, f Filter) (*Page, error) {
	if err := f.Validate(); err != nil {
		return nil, fwsvc.ValidationError("%v", err)
	}
	v, err := s.newView(ctx)
	if err != nil {
		return nil, err
	}
	where, args := f.sqlWhere()
	col := f.sortCol()
	page := &Page{Items: []Flow{}}
	if !f.goFilters() {
		var n int64
		if err := s.d.Store.Reader.QueryRowContext(ctx, "SELECT count(*) FROM fw_observations o WHERE "+where, args...).Scan(&n); err == nil {
			page.Total = &n
		}
	}
	curV, curID := int64(0), int64(0)
	haveCur := false
	if f.Cursor != "" {
		curV, curID, _ = decodeCursor(f.Cursor)
		haveCur = true
	}
	for page.Scanned < maxScan {
		q := "SELECT " + cols + " FROM fw_observations o WHERE " + where
		qa := append([]any{}, args...)
		if haveCur {
			q += fmt.Sprintf(" AND (o.%s < ? OR (o.%s = ? AND o.id < ?))", col, col)
			qa = append(qa, curV, curV, curID)
		}
		q += fmt.Sprintf(" ORDER BY o.%s DESC, o.id DESC LIMIT %d", col, scanChunk)
		rows, err := s.d.Store.Reader.QueryContext(ctx, q, qa...)
		if err != nil {
			return nil, err
		}
		var batch []db.FwObservation
		for rows.Next() {
			r, err := scanRow(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			batch = append(batch, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		for _, r := range batch {
			page.Scanned++
			sv := map[string]int64{"last_seen": r.LastSeen, "first_seen": r.FirstSeen, "count": r.Count}[col]
			curV, curID, haveCur = sv, r.ID, true
			fl := v.flow(ctx, r)
			if !v.matches(&f, &fl) {
				continue
			}
			if len(page.Items) == f.Limit {
				// One more match exists: the page ends at the previous row.
				last := page.Items[len(page.Items)-1]
				page.NextCursor = encodeCursor(map[string]int64{"last_seen": unix(last.LastSeen), "first_seen": unix(last.FirstSeen), "count": last.Count}[col], last.ID)
				s.addPeerFirstSeen(ctx, page.Items)
				return page, nil
			}
			page.Items = append(page.Items, fl)
		}
		if len(batch) < scanChunk {
			s.addPeerFirstSeen(ctx, page.Items)
			return page, nil
		}
	}
	// Scan limit: let the caller continue from where the scan stopped.
	page.Truncated = true
	page.NextCursor = encodeCursor(curV, curID)
	s.addPeerFirstSeen(ctx, page.Items)
	return page, nil
}

func unix(s string) int64 {
	t, _ := time.Parse(time.RFC3339, s)
	return t.Unix()
}

func (s *Service) addPeerFirstSeen(ctx context.Context, fl []Flow) {
	for i := range fl {
		if t, err := s.d.Store.R.HostFirstSeenPeer(ctx, db.HostFirstSeenPeerParams{HostID: fl[i].Host.ID, Peer: fl[i].RemoteIP}); err == nil && t > 0 {
			fl[i].PeerFirstSeen = ts(t)
		}
	}
}

// Detail is one flow with everything DROOL knows about it.
type Detail struct {
	Flow
	// FleetFirstSeen: when any host first talked to the remote address.
	FleetFirstSeen string             `json:"fleet_first_seen,omitempty"`
	Firewall       fwsvc.HostFirewall `json:"firewall"`
	Collection     Collection         `json:"collection"`
	// Suggestion: an allow rule for the Firewall Designer's draft, when no
	// policy covers the flow.
	Suggestion *fwsvc.DraftSuggestion `json:"suggestion"`
	// Related: the same host's other flows with this remote address.
	Related []Flow `json:"related"`
}

// Get returns one flow's details.
func (s *Service) Get(ctx context.Context, id int64) (*Detail, error) {
	r, err := s.d.Store.R.GetFwObservation(ctx, id)
	if err != nil {
		return nil, fwsvc.ErrNotFound
	}
	v, err := s.newView(ctx)
	if err != nil {
		return nil, err
	}
	d := &Detail{Flow: v.flow(ctx, r), Firewall: v.cl.Firewall(ctx, r.HostID), Related: []Flow{}}
	s.addPeerFirstSeen(ctx, []Flow{d.Flow})
	if t, err := s.d.Store.R.HostFirstSeenPeer(ctx, db.HostFirstSeenPeerParams{HostID: r.HostID, Peer: r.Peer}); err == nil && t > 0 {
		d.PeerFirstSeen = ts(t)
	}
	if t, err := s.d.Store.R.FleetFirstSeenPeer(ctx, r.Peer); err == nil && t > 0 {
		d.FleetFirstSeen = ts(t)
	}
	d.Collection = s.collection(ctx, r.HostID)
	if st := d.Policy.Status; st == fwsvc.FlowUnclassified || st == fwsvc.FlowDenied {
		if a, err := netip.ParseAddr(r.Peer); err == nil {
			d.Suggestion, _ = s.d.Firewall.SuggestDraft(ctx, v.cl, r.HostID, r.Direction, r.Protocol, int(r.Port), a)
		}
	}
	page, err := s.List(ctx, Filter{Hosts: []string{r.HostID}, Peer: r.Peer, Limit: 20})
	if err == nil {
		for _, f := range page.Items {
			if f.ID != r.ID {
				d.Related = append(d.Related, f)
			}
		}
	}
	return d, nil
}
