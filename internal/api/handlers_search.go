package api

import (
	"context"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"drool/internal/auth"
)

// The navigation search's records (1.0 UI/UX correction): hosts, Packs,
// services, active alerts and recent runs, found by what people type —
// a name, an address, a tag, a unit, words of an alert's title. Read-only;
// each kind only for a principal allowed to read it, so the search never
// reveals a record its page wouldn't show. Every query is bounded: a few
// results per kind, a short query, no full-text index.

type searchResult struct {
	Kind     string `json:"kind"`  // host, pack, service, alert, run
	ID       string `json:"id"`    // the record (a service: its unit name)
	Title    string `json:"title"` // what it is called
	Detail   string `json:"detail"`
	Match    string `json:"match,omitempty"` // why it matched, when not by its title
	HostID   string `json:"host_id,omitempty"`
	HostName string `json:"host_name,omitempty"`
	State    string `json:"state,omitempty"` // host status, service state, alert severity, run status
	At       string `json:"at,omitempty"`    // a run: when it started
}

type searchResponse struct {
	Query   string         `json:"query"`
	Results []searchResult `json:"results"`
	// Kinds with more matches than shown: refine the query, or open the
	// kind's own page.
	More []string `json:"more"`
}

const (
	searchMaxQuery  = 100
	searchMaxTokens = 5
)

// searchTokens splits a query into at most a few words; each must match.
func searchTokens(q string) []string {
	var out []string
	for _, t := range strings.Fields(strings.ToLower(q)) {
		if len(out) == searchMaxTokens {
			break
		}
		out = append(out, t)
	}
	return out
}

// likeArg is a LIKE pattern matching s anywhere (ESCAPE '\').
func likeArg(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(s) + "%"
}

// prefixArg is a LIKE pattern matching s at the start (ESCAPE '\').
func prefixArg(s string) string { return strings.TrimPrefix(likeArg(s), "%") }

// addressLike: a token that could be (part of) an address, worth looking
// for in a host's interface addresses.
var addressLike = regexp.MustCompile(`^[0-9a-f:.]*[0-9][0-9a-f:.]*$`)

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || utf8.RuneCountInString(q) > searchMaxQuery {
		writeProblem(w, r, errValidation("q must be 1 to 100 characters"))
		return
	}
	limit := 5
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 10 {
			writeProblem(w, r, errValidation("limit must be between 1 and 10"))
			return
		}
		limit = n
	}
	tokens := searchTokens(q)
	p := principalFrom(r.Context())
	out := searchResponse{Query: q, Results: []searchResult{}, More: []string{}}
	type finder struct {
		kind string
		perm auth.Permission
		find func(context.Context, []string, int) ([]searchResult, error)
	}
	for _, f := range []finder{
		{"host", auth.PermHostsRead, s.searchHosts},
		{"pack", auth.PermHostsRead, s.searchPacks},
		{"service", auth.PermMonitoringRead, s.searchServices},
		{"alert", auth.PermAlertsRead, s.searchAlerts},
		{"run", auth.PermJobsRead, s.searchRuns},
	} {
		if !p.Can(f.perm) {
			continue
		}
		// One more than shown tells whether there are more.
		res, err := f.find(r.Context(), tokens, limit+1)
		if err != nil {
			s.log.Error("search", "kind", f.kind, "err", err)
			writeProblem(w, r, errInternal)
			return
		}
		if len(res) > limit {
			res = res[:limit]
			out.More = append(out.More, f.kind)
		}
		out.Results = append(out.Results, res...)
	}
	writeJSON(w, http.StatusOK, out)
}

// allTokens builds "(a LIKE ? OR b LIKE ?) AND (…)" over columns, one
// group per token, with its arguments.
func allTokens(tokens []string, cols ...string) (string, []any) {
	var b strings.Builder
	var args []any
	for i, t := range tokens {
		if i > 0 {
			b.WriteString(" AND ")
		}
		b.WriteString("(")
		for j, c := range cols {
			if j > 0 {
				b.WriteString(" OR ")
			}
			b.WriteString(c + ` LIKE ? ESCAPE '\'`)
			args = append(args, likeArg(t))
		}
		b.WriteString(")")
	}
	return b.String(), args
}

func (s *Server) searchHosts(ctx context.Context, tokens []string, limit int) ([]searchResult, error) {
	var where strings.Builder
	var args []any
	for i, t := range tokens {
		if i > 0 {
			where.WriteString(" AND ")
		}
		where.WriteString(`(h.name LIKE ? ESCAPE '\' OR h.connect_address LIKE ? ESCAPE '\' OR h.description LIKE ? ESCAPE '\'
			OR COALESCE(f.hostname, '') LIKE ? ESCAPE '\'
			OR EXISTS (SELECT 1 FROM host_tags tg WHERE tg.host_id = h.id AND tg.tag LIKE ? ESCAPE '\')`)
		a := likeArg(t)
		args = append(args, a, a, a, a, a)
		// Interface addresses live in the facts document; only an
		// address-like word is looked for there (not loopback or "any",
		// which every host listens on).
		if addressLike.MatchString(t) && strings.ContainsAny(t, ".:") && !strings.HasPrefix(t, "127.") && t != "0.0.0.0" && t != "::" && t != "::1" {
			where.WriteString(` OR COALESCE(f.facts_json, '') LIKE ? ESCAPE '\'`)
			args = append(args, likeArg(`"address":"`+t))
		}
		where.WriteString(")")
	}
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT h.id, h.name, h.connect_address, h.description, h.status, h.jump_only,
		COALESCE(f.hostname, ''), COALESCE(f.distro_name, ''),
		COALESCE((SELECT group_concat(tag, ' ') FROM host_tags tg WHERE tg.host_id = h.id), '')
		FROM hosts h LEFT JOIN host_facts f ON f.host_id = h.id
		WHERE `+where.String()+`
		ORDER BY h.name LIKE ? ESCAPE '\' DESC, h.name COLLATE NOCASE LIMIT ?`, append(args, prefixArg(tokens[0]), limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []searchResult
	for rows.Next() {
		var id, name, addr, desc, status, hostname, distro, tags string
		var jump bool
		if err := rows.Scan(&id, &name, &addr, &desc, &status, &jump, &hostname, &distro, &tags); err != nil {
			return nil, err
		}
		detail := addr
		if distro != "" {
			detail = distro + " · " + addr
		}
		if jump {
			detail += " · jump host"
		}
		out = append(out, searchResult{Kind: "host", ID: id, Title: name, Detail: detail, State: status,
			Match: hostMatch(tokens, name, addr, desc, hostname, tags)})
	}
	return out, rows.Err()
}

// hostMatch says which field matched when the name doesn't explain it.
func hostMatch(tokens []string, name, addr, desc, hostname, tags string) string {
	lname := strings.ToLower(name)
	for _, t := range tokens {
		if strings.Contains(lname, t) {
			continue
		}
		switch {
		case strings.Contains(strings.ToLower(addr), t):
			return "address " + addr
		case strings.Contains(strings.ToLower(hostname), t):
			return "host name " + hostname
		case tags != "" && strings.Contains(strings.ToLower(tags), t):
			for _, tag := range strings.Fields(tags) {
				if strings.Contains(strings.ToLower(tag), t) {
					return "tag " + tag
				}
			}
		case strings.Contains(strings.ToLower(desc), t):
			return "description"
		default:
			return "interface address " + t
		}
	}
	return ""
}

func (s *Server) searchPacks(ctx context.Context, tokens []string, limit int) ([]searchResult, error) {
	where, args := allTokens(tokens, "g.name", "g.description")
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT g.id, g.name, g.description,
		(SELECT count(*) FROM group_members m WHERE m.group_id = g.id)
		FROM groups g WHERE `+where+` ORDER BY g.name COLLATE NOCASE LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []searchResult
	for rows.Next() {
		var id, name, desc string
		var n int
		if err := rows.Scan(&id, &name, &desc, &n); err != nil {
			return nil, err
		}
		detail := strconv.Itoa(n) + " host"
		if n != 1 {
			detail += "s"
		}
		if desc != "" {
			detail += " · " + desc
		}
		out = append(out, searchResult{Kind: "pack", ID: id, Title: name, Detail: detail})
	}
	return out, rows.Err()
}

func (s *Server) searchServices(ctx context.Context, tokens []string, limit int) ([]searchResult, error) {
	// Every word matches the unit, its description or its host; at least
	// one the service itself (a host's name alone isn't a service search).
	where, args := allTokens(tokens, "sv.unit", "sv.description", "h.name")
	own, ownArgs := anyToken(tokens, "sv.unit", "sv.description")
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT sv.host_id, h.name, sv.unit, sv.description, sv.active_state, sv.sub_state, sv.known
		FROM mon_services sv JOIN hosts h ON h.id = sv.host_id
		WHERE sv.transient = 0 AND `+where+` AND (`+own+`)
		ORDER BY sv.active_state = 'failed' DESC, sv.unit, h.name COLLATE NOCASE LIMIT ?`, append(append(args, ownArgs...), limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []searchResult
	for rows.Next() {
		var hostID, hostName, unit, desc, active, sub string
		var known bool
		if err := rows.Scan(&hostID, &hostName, &unit, &desc, &active, &sub, &known); err != nil {
			return nil, err
		}
		state := active
		if !known {
			state = "unknown"
		} else if sub != "" && sub != active {
			state = active + " (" + sub + ")"
		}
		out = append(out, searchResult{Kind: "service", ID: unit, Title: unit, Detail: desc, HostID: hostID, HostName: hostName, State: state})
	}
	return out, rows.Err()
}

// anyToken builds "(a LIKE ? OR … )" over every token and column: some
// word matches some column.
func anyToken(tokens []string, cols ...string) (string, []any) {
	var parts []string
	var args []any
	for _, t := range tokens {
		for _, c := range cols {
			parts = append(parts, c+` LIKE ? ESCAPE '\'`)
			args = append(args, likeArg(t))
		}
	}
	return strings.Join(parts, " OR "), args
}

func (s *Server) searchAlerts(ctx context.Context, tokens []string, limit int) ([]searchResult, error) {
	// Open alerts only (active or acknowledged): the words may come from
	// the title, the check or its category ("disk full"), the subject or
	// the host.
	where, args := allTokens(tokens, "a.title", "a.category", "a.check_id", "a.subject", "a.host_name")
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT a.id, a.title, a.host_id, a.host_name, a.severity, a.state
		FROM alerts a WHERE a.state != 'resolved' AND `+where+`
		ORDER BY a.severity = 'critical' DESC, a.opened_at DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []searchResult
	for rows.Next() {
		var id, title, hostID, hostName, severity, state string
		if err := rows.Scan(&id, &title, &hostID, &hostName, &severity, &state); err != nil {
			return nil, err
		}
		detail := hostName + " · " + severity
		if state == "acknowledged" {
			detail += " · acknowledged"
		}
		out = append(out, searchResult{Kind: "alert", ID: id, Title: title, Detail: detail, HostID: hostID, HostName: hostName, State: severity})
	}
	return out, rows.Err()
}

func (s *Server) searchRuns(ctx context.Context, tokens []string, limit int) ([]searchResult, error) {
	where, args := allTokens(tokens, "j.name", "j.id")
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT j.id, j.name, j.status, j.created_at,
		(SELECT count(*) FROM job_hosts jh WHERE jh.job_id = j.id)
		FROM jobs j WHERE `+where+` ORDER BY j.created_at DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []searchResult
	for rows.Next() {
		var id, name, status string
		var created int64
		var hosts int
		if err := rows.Scan(&id, &name, &status, &created, &hosts); err != nil {
			return nil, err
		}
		detail := status + " · " + strconv.Itoa(hosts) + " host"
		if hosts != 1 {
			detail += "s"
		}
		out = append(out, searchResult{Kind: "run", ID: id, Title: name, Detail: detail, State: status, At: msTime(created)})
	}
	return out, rows.Err()
}
