package netsvc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"

	"drool/internal/ids"
)

// Classification of observed traffic (final After-M17 batch,
// ARCHITECTURE-ALPHA §38): what a flow is, in words, and how DROOL knows.
// In order:
//
//  1. DROOL's own management traffic, where DROOL has direct evidence that
//     it is DROOL's: the address a host sees DROOL's SSH connections come
//     from (measured over SSH, SSH_CONNECTION) and the sshd port they reach;
//     the controller endpoint a Shepherd reported connecting to (with the
//     Shepherd's process, when it is known); the connections a jump host
//     forwards for DROOL; and both sides of those on the host DROOL runs on.
//     A familiar port alone (22, 9443) is never evidence: other SSH clients
//     on port 22 stay whatever else they are.
//  2. A user's classification (Rule): the most specific match wins, a label
//     made from one observed connection first.
//  3. A known service, named after its port, as the network views already
//     did.
//  4. Unknown.
//
// Classifications are computed when flows are read: a new rule applies to
// traffic already seen and to traffic seen later; nothing is enforced.

// Classification kinds.
const (
	ClassDROOL    = "drool"    // DROOL's own management traffic
	ClassApproved = "approved" // a user's classification marked expected
	ClassUser     = "user"     // a user's classification
	ClassService  = "service"  // named after its port
	ClassUnknown  = "unknown"
)

// Classification is what a flow is.
type Classification struct {
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	Source string `json:"source"` // drool | rule | manual | port | none
	RuleID string `json:"rule_id,omitempty"`
	Detail string `json:"detail"`
}

// Rule is a user's classification of recurring traffic.
type Rule struct {
	ID            string `json:"id"`
	Label         string `json:"label"`
	Note          string `json:"note"`
	Expected      bool   `json:"expected"`
	Origin        string `json:"origin"` // rule | flow (made from one observed connection)
	HostID        string `json:"host_id,omitempty"`
	HostGroupID   string `json:"host_group_id,omitempty"`
	Direction     string `json:"direction,omitempty"`
	Protocol      string `json:"protocol,omitempty"`
	Port          int    `json:"port,omitempty"`
	RemoteHostID  string `json:"remote_host_id,omitempty"`
	RemoteGroupID string `json:"remote_group_id,omitempty"`
	RemoteCIDR    string `json:"remote_cidr,omitempty"`
	// Described is the match in words ("media-servers → nas-01, TCP 2049").
	Described string `json:"described"`
	CreatedBy string `json:"created_by,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// ErrRuleNotFound: no such classification.
var ErrRuleNotFound = errors.New("classification not found")

// MaxRules bounds the classifications.
const MaxRules = 1000

// droolPath is one piece of evidence of DROOL's management traffic: flows
// of a host, in a direction, to a port, with one of peers.
type droolPath struct {
	peers []netip.Prefix
	label string
	// agent: only the Shepherd's own process (or an unknown one).
	agent  bool
	detail string
}

type droolKey struct {
	host, dir string
	port      int
}

type ruleMatch struct {
	r      Rule
	prefix netip.Prefix
	score  int
}

// classifier holds the evidence and the rules for one view.
type classifier struct {
	drool map[droolKey][]droolPath
	rules []ruleMatch
}

func single(a netip.Addr) netip.Prefix { return netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()) }

// newClassifier gathers DROOL's evidence and the user's rules.
func (v *view) newClassifier(ctx context.Context) *classifier {
	c := &classifier{drool: map[droolKey][]droolPath{}}
	w := v.cl.World()
	db := v.s.d.Store.Reader
	name := func(id string) string {
		if h, ok := w.Hosts[id]; ok && h.Name != "" {
			return h.Name
		}
		return id
	}
	add := func(k droolKey, p droolPath) { c.drool[k] = append(c.drool[k], p) }
	// Where DROOL is: the addresses hosts see its SSH connections come
	// from, and the endpoints Shepherds connect to.
	type obs struct {
		host, kind string
		addr       netip.Addr
		port       int
		at         int64
	}
	var all []obs
	if rows, err := db.QueryContext(ctx, `SELECT host_id, kind, address, port, observed_at FROM fw_mgmt_observations`); err == nil {
		for rows.Next() {
			var o obs
			var a string
			if rows.Scan(&o.host, &o.kind, &a, &o.port, &o.at) != nil {
				continue
			}
			if ip, err := netip.ParseAddr(a); err == nil {
				o.addr = ip.Unmap()
				all = append(all, o)
			}
		}
		rows.Close()
	}
	// A host whose addresses include addr: where DROOL runs, if it is
	// a managed host.
	owner := func(addr netip.Addr) []string {
		var out []string
		for id, h := range w.Hosts {
			for _, a := range h.Addrs {
				if a.Unmap() == addr {
					out = append(out, id)
				}
			}
		}
		sort.Strings(out)
		return out
	}
	for _, o := range all {
		switch o.kind {
		case "ssh_in":
			add(droolKey{o.host, "in", o.port}, droolPath{peers: []netip.Prefix{single(o.addr)}, label: "DROOL's SSH to this host",
				detail: fmt.Sprintf("DROOL's own SSH connections to %s come from %s (how %s sees them, measured over SSH on %s) and reach sshd on port %d.",
					name(o.host), o.addr, name(o.host), time.UnixMilli(o.at).UTC().Format("2 Jan 2006"), o.port)})
			// The other end, on the host DROOL runs on.
			for _, ctl := range owner(o.addr) {
				add(droolKey{ctl, "out", o.port}, droolPath{peers: prefixes(w.Hosts[o.host].Addrs), label: "DROOL's SSH to " + name(o.host),
					detail: fmt.Sprintf("DROOL runs on this host and manages %s over SSH (%s sees DROOL coming from %s).", name(o.host), name(o.host), o.addr)})
			}
		case "agent_out":
			add(droolKey{o.host, "out", o.port}, droolPath{peers: []netip.Prefix{single(o.addr)}, agent: true, label: "Shepherd connection to DROOL",
				detail: fmt.Sprintf("%s's Shepherd connects to DROOL at %s, port %d: the endpoint it reported when it last connected.", name(o.host), o.addr, o.port)})
			for _, ctl := range owner(o.addr) {
				add(droolKey{ctl, "in", o.port}, droolPath{peers: prefixes(w.Hosts[o.host].Addrs), label: "Shepherd of " + name(o.host) + " connecting",
					detail: fmt.Sprintf("%s's Shepherd connects to DROOL's endpoint on this host (%s, port %d).", name(o.host), o.addr, o.port)})
			}
		}
	}
	// Jump hosts forward DROOL's SSH connections to the hosts behind them.
	if rows, err := db.QueryContext(ctx, `SELECT hh.host_id, hh.via_host_id, h.ssh_port, h.connect_address FROM host_hops hh JOIN hosts h ON h.id = hh.host_id`); err == nil {
		for rows.Next() {
			var target, jump, address string
			var port int
			if rows.Scan(&target, &jump, &port, &address) != nil {
				continue
			}
			peers := prefixes(w.Hosts[target].Addrs)
			if ip, err := netip.ParseAddr(address); err == nil {
				peers = append(peers, single(ip))
			}
			if len(peers) > 0 {
				add(droolKey{jump, "out", port}, droolPath{peers: peers, label: "DROOL's SSH to " + name(target) + " through this jump host",
					detail: fmt.Sprintf("DROOL reaches %s through %s: this is that connection, to %s's sshd on port %d.", name(target), name(jump), name(target), port)})
			}
		}
		rows.Close()
	}
	// The user's rules, most specific first, each described in words for
	// the connections it labels (without the names, the detail said "()").
	rules, _ := v.s.listRules(ctx)
	var names map[string]string
	if len(rules) > 0 {
		names = v.s.names(ctx)
	}
	for _, r := range rules {
		r.Described = describe(r, names)
		m := ruleMatch{r: r, score: specificity(r)}
		if r.RemoteCIDR != "" {
			if p, err := parsePeer(r.RemoteCIDR); err == nil {
				m.prefix = p
			} else {
				continue
			}
		}
		c.rules = append(c.rules, m)
	}
	sort.SliceStable(c.rules, func(i, j int) bool { return c.rules[i].score > c.rules[j].score })
	return c
}

func prefixes(addrs []netip.Addr) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, single(a))
	}
	return out
}

// specificity orders rules: a label made from one connection first, then
// the narrower match.
func specificity(r Rule) int {
	s := 0
	if r.Origin == "flow" {
		s += 100
	}
	switch {
	case r.HostID != "":
		s += 8
	case r.HostGroupID != "":
		s += 4
	}
	switch {
	case r.RemoteHostID != "":
		s += 8
	case r.RemoteCIDR != "" && !strings.Contains(r.RemoteCIDR, "/"):
		s += 8
	case r.RemoteCIDR != "" || r.RemoteGroupID != "":
		s += 4
	}
	if r.Port > 0 {
		s += 4
	}
	if r.Protocol != "" {
		s++
	}
	if r.Direction != "" {
		s++
	}
	return s
}

// classify says what a flow is.
func (v *view) classify(c *classifier, f *Flow) Classification {
	peer, err := netip.ParseAddr(f.RemoteIP)
	if err == nil {
		peer = peer.Unmap()
		for _, p := range c.drool[droolKey{f.Host.ID, f.Direction, f.Port}] {
			if f.Protocol != "tcp" || p.agent && f.Process != "" && f.Process != "drool-agent" {
				continue
			}
			for _, pr := range p.peers {
				if pr.Contains(peer) {
					return Classification{Kind: ClassDROOL, Label: p.label, Source: "drool", Detail: p.detail}
				}
			}
		}
	}
	w := v.cl.World()
	inGroup := func(group, host string) bool {
		for _, m := range w.Groups[group].Members {
			if m == host {
				return true
			}
		}
		return false
	}
	for _, m := range c.rules {
		r := m.r
		switch {
		case r.HostID != "" && r.HostID != f.Host.ID,
			r.HostGroupID != "" && !inGroup(r.HostGroupID, f.Host.ID),
			r.Direction != "" && r.Direction != f.Direction,
			r.Protocol != "" && r.Protocol != f.Protocol,
			r.Port > 0 && r.Port != f.Port,
			r.RemoteHostID != "" && (f.PeerHost == nil || f.PeerHost.ID != r.RemoteHostID),
			r.RemoteGroupID != "" && (f.PeerHost == nil || !inGroup(r.RemoteGroupID, f.PeerHost.ID)),
			r.RemoteCIDR != "" && (err != nil || !m.prefix.Contains(peer)):
			continue
		}
		cl := Classification{Kind: ClassUser, Label: r.Label, Source: "rule", RuleID: r.ID,
			Detail: "Matches the classification “" + r.Label + "” (" + r.Described + ")."}
		if r.Expected {
			cl.Kind = ClassApproved
			cl.Detail += " Marked as expected."
		}
		if r.Origin == "flow" {
			cl.Source = "manual"
			cl.Detail = "Labeled “" + r.Label + "” from this connection (" + r.Described + ")." + map[bool]string{true: " Marked as expected.", false: ""}[r.Expected]
		}
		return cl
	}
	if f.Service != "" {
		return Classification{Kind: ClassService, Label: f.Service, Source: "port",
			Detail: fmt.Sprintf("Named after its port (%s %d): no classification or DROOL evidence says what it is.", strings.ToUpper(f.Protocol), f.Port)}
	}
	return Classification{Kind: ClassUnknown, Label: "Unknown", Source: "none", Detail: "Nothing says what this is yet: classify it, or leave it unknown."}
}

// --- rules ---

const ruleCols = `id, label, note, expected, origin, ifnull(host_id, ''), ifnull(host_group_id, ''), direction, protocol, port,
	ifnull(remote_host_id, ''), ifnull(remote_group_id, ''), remote_cidr, ifnull(created_by, ''), created_at, updated_at`

type scanner interface{ Scan(dest ...any) error }

func scanRule(s scanner) (Rule, error) {
	var r Rule
	var exp int
	var created, updated int64
	err := s.Scan(&r.ID, &r.Label, &r.Note, &exp, &r.Origin, &r.HostID, &r.HostGroupID, &r.Direction, &r.Protocol, &r.Port,
		&r.RemoteHostID, &r.RemoteGroupID, &r.RemoteCIDR, &r.CreatedBy, &created, &updated)
	r.Expected = exp == 1
	r.CreatedAt, r.UpdatedAt = time.UnixMilli(created).UTC().Format(time.RFC3339), time.UnixMilli(updated).UTC().Format(time.RFC3339)
	return r, err
}

func (s *Service) listRules(ctx context.Context) ([]Rule, error) {
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT `+ruleCols+` FROM net_flow_rules ORDER BY created_at, id LIMIT ?`, MaxRules)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rule
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Rules lists the classifications, each described in words.
func (s *Service) Rules(ctx context.Context) ([]Rule, error) {
	rules, err := s.listRules(ctx)
	if err != nil {
		return nil, err
	}
	names := s.names(ctx)
	out := make([]Rule, 0, len(rules))
	for _, r := range rules {
		r.Described = describe(r, names)
		out = append(out, r)
	}
	return out, nil
}

// Rule returns one classification.
func (s *Service) Rule(ctx context.Context, id string) (Rule, error) {
	r, err := scanRule(s.d.Store.Reader.QueryRowContext(ctx, `SELECT `+ruleCols+` FROM net_flow_rules WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrRuleNotFound
	}
	r.Described = describe(r, s.names(ctx))
	return r, err
}

// names maps host and pack ids to names (for descriptions).
func (s *Service) names(ctx context.Context) map[string]string {
	out := map[string]string{}
	for _, q := range []string{`SELECT id, name FROM hosts`, `SELECT id, name FROM groups`} {
		rows, err := s.d.Store.Reader.QueryContext(ctx, q)
		if err != nil {
			continue
		}
		for rows.Next() {
			var id, n string
			if rows.Scan(&id, &n) == nil {
				out[id] = n
			}
		}
		rows.Close()
	}
	return out
}

// describe says what a rule matches, in words.
func describe(r Rule, names map[string]string) string {
	n := func(id string) string {
		if v := names[id]; v != "" {
			return v
		}
		return id
	}
	from := "any host"
	switch {
	case r.HostID != "":
		from = n(r.HostID)
	case r.HostGroupID != "":
		from = "pack " + n(r.HostGroupID)
	}
	remote := "anywhere"
	switch {
	case r.RemoteHostID != "":
		remote = n(r.RemoteHostID)
	case r.RemoteGroupID != "":
		remote = "pack " + n(r.RemoteGroupID)
	case r.RemoteCIDR != "":
		remote = r.RemoteCIDR
	}
	arrow := "↔"
	switch r.Direction {
	case "out":
		arrow = "→"
	case "in":
		arrow = "←"
	}
	svc := "any port"
	if r.Port > 0 {
		svc = fmt.Sprintf("port %d", r.Port)
	}
	if r.Protocol != "" {
		svc = strings.ToUpper(r.Protocol) + " " + strings.TrimPrefix(svc, "port ")
		if r.Port == 0 {
			svc = strings.ToUpper(r.Protocol) + ", any port"
		}
	}
	return fmt.Sprintf("%s %s %s, %s", from, arrow, remote, svc)
}

// RuleInput is a classification to store.
type RuleInput struct {
	Label         string `json:"label"`
	Note          string `json:"note"`
	Expected      bool   `json:"expected"`
	Origin        string `json:"origin"`
	HostID        string `json:"host_id"`
	HostGroupID   string `json:"host_group_id"`
	Direction     string `json:"direction"`
	Protocol      string `json:"protocol"`
	Port          int    `json:"port"`
	RemoteHostID  string `json:"remote_host_id"`
	RemoteGroupID string `json:"remote_group_id"`
	RemoteCIDR    string `json:"remote_cidr"`
}

// ValidationError is a refused classification.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func (in *RuleInput) validate(ctx context.Context, db *sql.Tx) error {
	bad := func(f string, a ...any) error { return &ValidationError{Msg: fmt.Sprintf(f, a...)} }
	in.Label, in.Note = strings.TrimSpace(in.Label), strings.TrimSpace(in.Note)
	in.RemoteCIDR = strings.TrimSpace(in.RemoteCIDR)
	switch {
	case in.Label == "" || len([]rune(in.Label)) > 120:
		return bad("give it a label of 1–120 characters (what this traffic is)")
	case len([]rune(in.Note)) > 500:
		return bad("the note is at most 500 characters")
	case in.HostID != "" && in.HostGroupID != "":
		return bad("match a host or a pack, not both")
	case in.Direction != "" && in.Direction != "in" && in.Direction != "out":
		return bad("direction is in, out or empty (either)")
	case in.Protocol != "" && in.Protocol != "tcp" && in.Protocol != "udp":
		return bad("protocol is tcp, udp or empty (either)")
	case in.Port < 0 || in.Port > 65535:
		return bad("port is 1–65535, or 0 for any")
	}
	remotes := 0
	for _, x := range []string{in.RemoteHostID, in.RemoteGroupID, in.RemoteCIDR} {
		if x != "" {
			remotes++
		}
	}
	if remotes > 1 {
		return bad("match one remote side: a DROOL host, a pack's hosts, or an address or CIDR")
	}
	if in.Port == 0 && remotes == 0 {
		return bad("say at least which port or which remote side: a label for all of a host's traffic would say nothing")
	}
	if in.RemoteCIDR != "" {
		p, err := parsePeer(in.RemoteCIDR)
		if err != nil {
			return bad("%q is not an address or a CIDR", in.RemoteCIDR)
		}
		if p.IsSingleIP() {
			in.RemoteCIDR = p.Addr().String()
		} else {
			in.RemoteCIDR = p.String()
		}
	}
	if in.Origin == "" {
		in.Origin = "rule"
	}
	if in.Origin != "rule" && in.Origin != "flow" {
		return bad("origin is rule or flow")
	}
	for _, c := range []struct{ id, table, what string }{{in.HostID, "hosts", "host"}, {in.RemoteHostID, "hosts", "remote host"},
		{in.HostGroupID, "groups", "pack"}, {in.RemoteGroupID, "groups", "remote pack"}} {
		if c.id == "" {
			continue
		}
		var n int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM `+c.table+` WHERE id = ?`, c.id).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return bad("no such %s: %s", c.what, c.id)
		}
	}
	return nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// PutRuleTx creates (id "") or replaces a classification; the caller
// audits. It returns the stored rule.
func (s *Service) PutRuleTx(ctx context.Context, tx *sql.Tx, id string, in RuleInput, by string) (Rule, error) {
	if err := in.validate(ctx, tx); err != nil {
		return Rule{}, err
	}
	now := s.now().UnixMilli()
	if id == "" {
		var n int
		tx.QueryRowContext(ctx, `SELECT count(*) FROM net_flow_rules`).Scan(&n)
		if n >= MaxRules {
			return Rule{}, &ValidationError{Msg: fmt.Sprintf("at most %d classifications", MaxRules)}
		}
		id = ids.New(ids.NetRule)
		if _, err := tx.ExecContext(ctx, `INSERT INTO net_flow_rules (id, label, note, expected, origin, host_id, host_group_id, direction, protocol, port,
			remote_host_id, remote_group_id, remote_cidr, created_by, created_at, updated_by, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, in.Label, in.Note, boolInt(in.Expected), in.Origin, nullable(in.HostID), nullable(in.HostGroupID), in.Direction, in.Protocol, in.Port,
			nullable(in.RemoteHostID), nullable(in.RemoteGroupID), in.RemoteCIDR, nullable(by), now, nullable(by), now); err != nil {
			return Rule{}, err
		}
	} else {
		res, err := tx.ExecContext(ctx, `UPDATE net_flow_rules SET label = ?, note = ?, expected = ?, origin = ?, host_id = ?, host_group_id = ?, direction = ?,
			protocol = ?, port = ?, remote_host_id = ?, remote_group_id = ?, remote_cidr = ?, updated_by = ?, updated_at = ? WHERE id = ?`,
			in.Label, in.Note, boolInt(in.Expected), in.Origin, nullable(in.HostID), nullable(in.HostGroupID), in.Direction, in.Protocol, in.Port,
			nullable(in.RemoteHostID), nullable(in.RemoteGroupID), in.RemoteCIDR, nullable(by), now, id)
		if err != nil {
			return Rule{}, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return Rule{}, ErrRuleNotFound
		}
	}
	r, err := scanRule(tx.QueryRowContext(ctx, `SELECT `+ruleCols+` FROM net_flow_rules WHERE id = ?`, id))
	if err != nil {
		return r, err
	}
	names := map[string]string{}
	for _, x := range []string{r.HostID, r.HostGroupID, r.RemoteHostID, r.RemoteGroupID} {
		if x == "" {
			continue
		}
		var n string
		if tx.QueryRowContext(ctx, `SELECT name FROM hosts WHERE id = ? UNION ALL SELECT name FROM groups WHERE id = ?`, x, x).Scan(&n) == nil {
			names[x] = n
		}
	}
	r.Described = describe(r, names)
	return r, nil
}

// DeleteRuleTx removes a classification; the caller audits.
func (s *Service) DeleteRuleTx(ctx context.Context, tx *sql.Tx, id string) (Rule, error) {
	r, err := scanRule(tx.QueryRowContext(ctx, `SELECT `+ruleCols+` FROM net_flow_rules WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrRuleNotFound
	}
	if err != nil {
		return r, err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM net_flow_rules WHERE id = ?`, id)
	return r, err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
