package alerts

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// Views of the alert list.
var Views = []string{"active", "acknowledged", "open", "resolved", "all"}

// Filter selects alerts.
type Filter struct {
	View         string // active (unacknowledged) | acknowledged | open (both) | resolved | all
	Severity     string
	HostID       string
	GroupID      string
	Category     string
	Check        string
	Acknowledged *bool
	Suppressed   *bool // open views: suppressed now; history: was suppressed
	Since, Until int64 // opened_at range (ms)
	Limit        int
	Cursor       string
}

// Page is one page of alerts.
type Page struct {
	Items      []*Alert `json:"items"`
	NextCursor string   `json:"next_cursor,omitempty"`
}

// cursor: open views sort by severity then newest; history by newest.
type cursor struct {
	rank int
	at   int64
	id   string
}

func (c cursor) encode() string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d|%d|%s", c.rank, c.at, c.id)))
}

func decodeCursor(s string) (*cursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, &ValidationError{"invalid cursor"}
	}
	p := strings.SplitN(string(b), "|", 3)
	if len(p) != 3 {
		return nil, &ValidationError{"invalid cursor"}
	}
	r, err1 := strconv.Atoi(p[0])
	at, err2 := strconv.ParseInt(p[1], 10, 64)
	if err1 != nil || err2 != nil {
		return nil, &ValidationError{"invalid cursor"}
	}
	return &cursor{r, at, p[2]}, nil
}

const rankExpr = `(CASE severity WHEN 'critical' THEN 0 ELSE 1 END)`

// List pages through alerts (at most 200 per page).
func (s *Service) List(ctx context.Context, f Filter) (*Page, error) {
	where, args := []string{"1=1"}, []any{}
	add := func(cond string, a ...any) { where = append(where, cond); args = append(args, a...) }
	history := false
	switch f.View {
	case "", "active":
		add(`state = 'active'`)
	case "acknowledged":
		add(`state = 'acknowledged'`)
	case "open":
		add(`state IN ('active','acknowledged')`)
	case "resolved":
		add(`state = 'resolved'`)
		history = true
	case "all":
		history = true
	default:
		return nil, &ValidationError{"view must be active, acknowledged, open, resolved or all"}
	}
	if f.Severity != "" {
		if !actionable(f.Severity) {
			return nil, &ValidationError{"severity must be warning or critical"}
		}
		add(`severity = ?`, f.Severity)
	}
	if f.HostID != "" {
		add(`host_id = ?`, f.HostID)
	}
	if f.GroupID != "" {
		add(`host_id IN (SELECT host_id FROM group_members WHERE group_id = ?)`, f.GroupID)
	}
	if f.Category != "" {
		add(`category = ?`, f.Category)
	}
	if f.Check != "" {
		add(`check_id = ?`, f.Check)
	}
	if f.Acknowledged != nil {
		if *f.Acknowledged {
			add(`acknowledged_at IS NOT NULL`)
		} else {
			add(`acknowledged_at IS NULL`)
		}
	}
	if f.Suppressed != nil {
		col := "suppressed"
		if history {
			col = "ever_suppressed"
		}
		add(col+` = ?`, b2i(*f.Suppressed))
	}
	if f.Since > 0 {
		add(`opened_at >= ?`, f.Since)
	}
	if f.Until > 0 {
		add(`opened_at < ?`, f.Until)
	}
	rank, order := rankExpr, rankExpr+`, opened_at DESC, id DESC`
	if history {
		rank, order = "0", `opened_at DESC, id DESC` // (a constant in ORDER BY would mean a column number)
	}
	if f.Cursor != "" {
		c, err := decodeCursor(f.Cursor)
		if err != nil {
			return nil, err
		}
		add(`(`+rank+` > ? OR (`+rank+` = ? AND (opened_at < ? OR (opened_at = ? AND id < ?))))`, c.rank, c.rank, c.at, c.at, c.id)
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT `+alertCols+`, `+rank+` FROM alerts WHERE `+strings.Join(where, " AND ")+
		` ORDER BY `+order+` LIMIT ?`, append(args, limit+1)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := s.nowMs()
	p := &Page{Items: []*Alert{}}
	var ranks []int
	for rows.Next() {
		var r int
		a, err := scanAlert(rowWith{rows, &r}, now)
		if err != nil {
			return nil, err
		}
		p.Items = append(p.Items, a)
		ranks = append(ranks, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(p.Items) > limit {
		last := p.Items[limit-1]
		p.NextCursor = cursor{ranks[limit-1], last.OpenedAt, last.ID}.encode()
		p.Items = p.Items[:limit]
	}
	return p, s.decorate(ctx, p.Items)
}

// rowWith scans the alert columns plus extra trailing ones.
type rowWith struct {
	r     *sql.Rows
	extra *int
}

func (r rowWith) Scan(dest ...any) error { return r.r.Scan(append(dest, r.extra)...) }

// decorate adds notification summaries and maintenance references.
func (s *Service) decorate(ctx context.Context, as []*Alert) error {
	if len(as) == 0 {
		return nil
	}
	byID := map[string]*Alert{}
	var ph []string
	var args []any
	for _, a := range as {
		byID[a.ID] = a
		ph = append(ph, "?")
		args = append(args, a.ID)
	}
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT alert_id, status, count(*), max(attempts) FROM notify_deliveries
		WHERE alert_id IN (`+strings.Join(ph, ",")+`) GROUP BY alert_id, status`, args...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, st string
		var n, attempts int
		if err := rows.Scan(&id, &st, &n, &attempts); err != nil {
			rows.Close()
			return err
		}
		ns := &byID[id].Notification
		switch st {
		case "delivered":
			ns.Delivered += n
		case "queued", "sending":
			ns.Pending += n
			ns.Retrying = ns.Retrying || attempts > 0
		case "failed":
			ns.Failed += n
		case "cancelled":
			ns.Cancelled += n
		}
	}
	rows.Close()
	maint := map[string]*MaintenanceRef{}
	for _, a := range as {
		ns := &a.Notification
		switch {
		case ns.Pending > 0 && ns.Retrying:
			ns.Status = "failing"
		case ns.Pending > 0:
			ns.Status = "pending"
		case ns.Failed > 0 && ns.Delivered > 0:
			ns.Status = "partial"
		case ns.Failed > 0:
			ns.Status = "failed"
		case ns.Delivered > 0:
			ns.Status = "delivered"
		case a.Suppressed || a.EverSuppressed && ns.Cancelled > 0:
			ns.Status = "suppressed"
		default:
			ns.Status = "none"
		}
		if a.suppressedBy != "" {
			m, ok := maint[a.suppressedBy]
			if !ok {
				var r MaintenanceRef
				var ends sql.NullInt64
				if s.d.Store.Reader.QueryRowContext(ctx, `SELECT id, reason, ends_at FROM maintenance WHERE id = ?`, a.suppressedBy).Scan(&r.ID, &r.Reason, &ends) == nil {
					if ends.Valid {
						r.EndsAt = &ends.Int64
					}
					m = &r
				}
				maint[a.suppressedBy] = m
			}
			a.SuppressedBy = m
		}
	}
	return nil
}

// TimelineEntry is one line of an alert's timeline.
type TimelineEntry struct {
	TS    int64  `json:"ts"`
	Kind  string `json:"kind"`
	Text  string `json:"text"`
	Actor string `json:"actor,omitempty"`
}

// Detail is an alert with everything about it.
type Detail struct {
	Alert       *Alert          `json:"alert"`
	Timeline    []TimelineEntry `json:"timeline"`
	Deliveries  []*Delivery     `json:"deliveries"`
	Issue       *SourceIssue    `json:"issue,omitempty"` // nil once M11 pruned it
	Occurrences []Occurrence    `json:"occurrences"`     // other alerts for the same host, check and subject
}

// SourceIssue is the M11 health issue behind an alert.
type SourceIssue struct {
	ID         string   `json:"id"`
	State      string   `json:"state"`
	Severity   string   `json:"severity"`
	Title      string   `json:"title"`
	Detail     string   `json:"detail"`
	Value      *float64 `json:"value,omitempty"`
	StartedAt  int64    `json:"started_at"`
	UpdatedAt  int64    `json:"updated_at"`
	ResolvedAt *int64   `json:"resolved_at,omitempty"`
}

// Occurrence is an earlier or later alert for the same condition.
type Occurrence struct {
	ID         string `json:"id"`
	State      string `json:"state"`
	Severity   string `json:"severity"`
	OpenedAt   int64  `json:"opened_at"`
	ResolvedAt *int64 `json:"resolved_at,omitempty"`
}

// GetDetail loads an alert's details. withErrors includes delivery error
// texts (notifications.read).
func (s *Service) GetDetail(ctx context.Context, id string, withErrors bool) (*Detail, error) {
	a, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	d := &Detail{Alert: a, Timeline: []TimelineEntry{}, Occurrences: []Occurrence{}}
	r := s.d.Store.Reader
	rows, err := r.QueryContext(ctx, `SELECT ts, kind, text, actor FROM alert_events WHERE alert_id = ? ORDER BY ts, id`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var e TimelineEntry
		if err := rows.Scan(&e.TS, &e.Kind, &e.Text, &e.Actor); err != nil {
			rows.Close()
			return nil, err
		}
		d.Timeline = append(d.Timeline, e)
	}
	rows.Close()
	if d.Deliveries, err = s.listDeliveries(ctx, `d.alert_id = ?`, []any{id}, 100, withErrors); err != nil {
		return nil, err
	}
	var is SourceIssue
	var v sql.NullFloat64
	var res sql.NullInt64
	if r.QueryRowContext(ctx, `SELECT id, state, severity, title, detail, value, started_at, updated_at, resolved_at FROM mon_issues WHERE id = ?`, a.IssueID).
		Scan(&is.ID, &is.State, &is.Severity, &is.Title, &is.Detail, &v, &is.StartedAt, &is.UpdatedAt, &res) == nil {
		if v.Valid {
			is.Value = &v.Float64
		}
		if res.Valid {
			is.ResolvedAt = &res.Int64
		}
		d.Issue = &is
	}
	orows, err := r.QueryContext(ctx, `SELECT id, state, severity, opened_at, resolved_at FROM alerts WHERE host_id = ? AND check_id = ? AND subject = ? AND id != ?
		ORDER BY opened_at DESC LIMIT 10`, a.HostID, a.Check, a.Subject, a.ID)
	if err != nil {
		return nil, err
	}
	defer orows.Close()
	for orows.Next() {
		var o Occurrence
		var res sql.NullInt64
		if err := orows.Scan(&o.ID, &o.State, &o.Severity, &o.OpenedAt, &res); err != nil {
			return nil, err
		}
		if res.Valid {
			o.ResolvedAt = &res.Int64
		}
		d.Occurrences = append(d.Occurrences, o)
	}
	return d, orows.Err()
}

// Summary is the fleet's alert state at a glance (navigation badge,
// dashboard, fleet and host pages).
type Summary struct {
	Active       int                       `json:"active"`          // open and unacknowledged
	ActiveCrit   int                       `json:"active_critical"` // of those, critical
	Acknowledged int                       `json:"acknowledged"`
	Critical     int                       `json:"critical"` // open
	Warning      int                       `json:"warning"`  // open
	Suppressed   int                       `json:"suppressed"`
	Hosts        map[string]*HostCounts    `json:"hosts"`
	Maintenance  map[string]MaintenanceRef `json:"maintenance"` // hosts in maintenance
}

// HostCounts are one host's open alerts.
type HostCounts struct {
	Critical     int `json:"critical"`
	Warning      int `json:"warning"`
	Acknowledged int `json:"acknowledged"`
	Suppressed   int `json:"suppressed"`
}

// GetSummary counts open alerts.
func (s *Service) GetSummary(ctx context.Context) (*Summary, error) {
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT host_id, severity, state, suppressed, count(*) FROM alerts WHERE state IN ('active','acknowledged')
		GROUP BY host_id, severity, state, suppressed`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sum := &Summary{Hosts: map[string]*HostCounts{}}
	for rows.Next() {
		var h, sev, st string
		var sup, n int
		if err := rows.Scan(&h, &sev, &st, &sup, &n); err != nil {
			return nil, err
		}
		hc := sum.Hosts[h]
		if hc == nil {
			hc = &HostCounts{}
			sum.Hosts[h] = hc
		}
		if st == StateAcknowledged {
			sum.Acknowledged += n
			hc.Acknowledged += n
		} else {
			sum.Active += n
			if sev == "critical" {
				sum.ActiveCrit += n
			}
		}
		if sev == "critical" {
			sum.Critical += n
			hc.Critical += n
		} else {
			sum.Warning += n
			hc.Warning += n
		}
		if sup == 1 {
			sum.Suppressed += n
			hc.Suppressed += n
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sum.Maintenance, err = s.HostsInMaintenance(ctx)
	return sum, err
}

// EventFilter selects timeline events.
type EventFilter struct {
	HostID string
	Kind   string // prefix, e.g. "alert." or "maintenance."
	Limit  int
	Before int64 // id cursor
}

// Events lists the controller event timeline, newest first (bounded).
func (s *Service) Events(ctx context.Context, f EventFilter) ([]Event, error) {
	q, args := `SELECT id, ts, kind, severity, COALESCE(host_id, ''), object_type, object_id, title FROM events WHERE 1=1`, []any{}
	if f.HostID != "" {
		q += ` AND host_id = ?`
		args = append(args, f.HostID)
	}
	if f.Kind != "" {
		q += ` AND kind LIKE ?`
		args = append(args, strings.ReplaceAll(f.Kind, "%", "")+"%")
	}
	if f.Before > 0 {
		q += ` AND id < ?`
		args = append(args, f.Before)
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.d.Store.Reader.QueryContext(ctx, q+` ORDER BY id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.TS, &e.Kind, &e.Severity, &e.HostID, &e.ObjectType, &e.ObjectID, &e.Title); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Delivery is one notification in the outbox or its history.
type Delivery struct {
	ID            string    `json:"id"`
	AlertID       string    `json:"alert_id,omitempty"`
	HostName      string    `json:"host_name,omitempty"`
	AlertTitle    string    `json:"alert_title,omitempty"`
	DestinationID string    `json:"destination_id,omitempty"`
	Destination   string    `json:"destination"`
	DestKind      string    `json:"destination_kind"`
	Kind          string    `json:"kind"`
	Status        string    `json:"status"`
	Attempts      int       `json:"attempts"`
	NextAttemptAt *int64    `json:"next_attempt_at,omitempty"`
	CreatedAt     int64     `json:"created_at"`
	FinishedAt    *int64    `json:"finished_at,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
	LastCategory  string    `json:"last_category,omitempty"`
	StatusCode    *int      `json:"last_status_code,omitempty"`
	Ambiguous     bool      `json:"ambiguous"`
	AttemptLog    []Attempt `json:"attempt_log,omitempty"`
}

// Attempt is one try at a delivery.
type Attempt struct {
	N          int    `json:"n"`
	StartedAt  int64  `json:"started_at"`
	FinishedAt *int64 `json:"finished_at,omitempty"`
	Outcome    string `json:"outcome"`
	Category   string `json:"category,omitempty"`
	StatusCode *int   `json:"status_code,omitempty"`
	Error      string `json:"error,omitempty"`
}

// DeliveryFilter selects deliveries.
type DeliveryFilter struct {
	DestinationID string
	AlertID       string
	Status        string
	Limit         int
	Before        string // id cursor (ids sort by time)
}

// Deliveries pages through the delivery history, newest first.
func (s *Service) Deliveries(ctx context.Context, f DeliveryFilter) ([]*Delivery, string, error) {
	where, args := []string{"1=1"}, []any{}
	if f.DestinationID != "" {
		where, args = append(where, `d.destination_id = ?`), append(args, f.DestinationID)
	}
	if f.AlertID != "" {
		where, args = append(where, `d.alert_id = ?`), append(args, f.AlertID)
	}
	if f.Status != "" {
		switch f.Status {
		case "queued", "sending", "delivered", "failed", "cancelled":
		default:
			return nil, "", &ValidationError{"status must be queued, sending, delivered, failed or cancelled"}
		}
		where, args = append(where, `d.status = ?`), append(args, f.Status)
	}
	if f.Before != "" {
		where, args = append(where, `d.id < ?`), append(args, f.Before)
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	ds, err := s.listDeliveries(ctx, strings.Join(where, " AND "), args, limit+1, true)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(ds) > limit {
		ds = ds[:limit]
		next = ds[limit-1].ID
	}
	return ds, next, nil
}

func (s *Service) listDeliveries(ctx context.Context, where string, args []any, limit int, withErrors bool) ([]*Delivery, error) {
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT d.id, COALESCE(d.alert_id, ''), COALESCE(a.host_name, ''), COALESCE(a.title, ''),
		COALESCE(d.destination_id, ''), d.destination_name, d.destination_kind, d.kind, d.status, d.attempts, d.next_attempt_at, d.created_at, d.finished_at,
		d.last_error, d.last_category, d.last_status_code, d.ambiguous
		FROM notify_deliveries d LEFT JOIN alerts a ON a.id = d.alert_id WHERE `+where+` ORDER BY d.id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	out := []*Delivery{}
	for rows.Next() {
		var d Delivery
		var next, fin, code sql.NullInt64
		var amb int
		if err := rows.Scan(&d.ID, &d.AlertID, &d.HostName, &d.AlertTitle, &d.DestinationID, &d.Destination, &d.DestKind, &d.Kind, &d.Status,
			&d.Attempts, &next, &d.CreatedAt, &fin, &d.LastError, &d.LastCategory, &code, &amb); err != nil {
			rows.Close()
			return nil, err
		}
		if d.Status == "queued" && next.Valid {
			d.NextAttemptAt = &next.Int64
		}
		if fin.Valid {
			d.FinishedAt = &fin.Int64
		}
		if code.Valid {
			c := int(code.Int64)
			d.StatusCode = &c
		}
		d.Ambiguous = amb == 1
		if !withErrors {
			d.LastError = ""
		}
		out = append(out, &d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if withErrors {
		for _, d := range out {
			if d.AttemptLog, err = s.attempts(ctx, d.ID); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func (s *Service) attempts(ctx context.Context, id string) ([]Attempt, error) {
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT n, started_at, finished_at, outcome, category, status_code, error FROM notify_attempts
		WHERE delivery_id = ? ORDER BY n`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Attempt{}
	for rows.Next() {
		var a Attempt
		var fin, code sql.NullInt64
		if err := rows.Scan(&a.N, &a.StartedAt, &fin, &a.Outcome, &a.Category, &code, &a.Error); err != nil {
			return nil, err
		}
		if fin.Valid {
			a.FinishedAt = &fin.Int64
		}
		if code.Valid {
			c := int(code.Int64)
			a.StatusCode = &c
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
