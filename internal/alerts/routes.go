package alerts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"drool/internal/ids"
	"drool/internal/monitor/monsvc"
	"drool/internal/notify"
)

// DefaultRouteID is the route every installation starts with: Warning and
// Critical alerts, with recoveries, to the destinations chosen for it.
const DefaultRouteID = "nrt_default"

// Reminder bounds (minutes). Reminders are off unless a route sets them.
const (
	MinReminderMinutes = 60
	MaxReminderMinutes = 7 * 24 * 60
)

// Route selects destinations for alerts. Empty filters match everything;
// filters combine with AND, values within one with OR. Hosts and groups
// form one filter (the host is listed, or is in a listed group), as do
// categories and checks.
type Route struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	IsDefault       bool     `json:"is_default"`
	Enabled         bool     `json:"enabled"`
	Severities      []string `json:"severities"`
	HostIDs         []string `json:"host_ids"`
	GroupIDs        []string `json:"group_ids"`
	Categories      []string `json:"categories"`
	Checks          []string `json:"checks"`
	Recovery        bool     `json:"recovery"`
	ReminderMinutes int      `json:"reminder_minutes"`
	DestinationIDs  []string `json:"destination_ids"`
	CreatedAt       int64    `json:"created_at"`
	UpdatedAt       int64    `json:"updated_at"`
}

// target is a destination chosen for an alert.
type target struct {
	ID, Name, Kind string
	Recovery       bool
	Reminder       int // minutes, 0: none
}

func (r *Route) matches(sev, hostID string, hostGroups []string, category, check string) bool {
	if !r.Enabled {
		return false
	}
	if len(r.Severities) > 0 && !slices.Contains(r.Severities, sev) {
		return false
	}
	if len(r.HostIDs)+len(r.GroupIDs) > 0 {
		in := slices.Contains(r.HostIDs, hostID)
		for _, g := range hostGroups {
			in = in || slices.Contains(r.GroupIDs, g)
		}
		if !in {
			return false
		}
	}
	if len(r.Categories)+len(r.Checks) > 0 && !slices.Contains(r.Categories, category) && !slices.Contains(r.Checks, check) {
		return false
	}
	return true
}

const routeCols = `id, name, is_default, enabled, severities_json, host_ids_json, group_ids_json, categories_json, checks_json, recovery, reminder_minutes, created_at, updated_at`

func scanRoute(r scanner) (*Route, error) {
	var rt Route
	var def, en, rec int
	var sev, hosts, groups, cats, checks string
	if err := r.Scan(&rt.ID, &rt.Name, &def, &en, &sev, &hosts, &groups, &cats, &checks, &rec, &rt.ReminderMinutes, &rt.CreatedAt, &rt.UpdatedAt); err != nil {
		return nil, err
	}
	rt.IsDefault, rt.Enabled, rt.Recovery = def == 1, en == 1, rec == 1
	for _, x := range []struct {
		s string
		v *[]string
	}{{sev, &rt.Severities}, {hosts, &rt.HostIDs}, {groups, &rt.GroupIDs}, {cats, &rt.Categories}, {checks, &rt.Checks}} {
		json.Unmarshal([]byte(x.s), x.v)
		if *x.v == nil {
			*x.v = []string{}
		}
	}
	return &rt, nil
}

func (s *Service) loadRoutes(ctx context.Context, qq q) ([]*Route, error) {
	rows, err := qq.QueryContext(ctx, `SELECT `+routeCols+` FROM notify_routes ORDER BY is_default DESC, name, id`)
	if err != nil {
		return nil, err
	}
	var out []*Route
	byID := map[string]*Route{}
	for rows.Next() {
		r, err := scanRoute(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		r.DestinationIDs = []string{}
		out = append(out, r)
		byID[r.ID] = r
	}
	rows.Close()
	rows, err = qq.QueryContext(ctx, `SELECT route_id, destination_id FROM notify_route_destinations ORDER BY destination_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var rid, did string
		if err := rows.Scan(&rid, &did); err != nil {
			return nil, err
		}
		if r := byID[rid]; r != nil {
			r.DestinationIDs = append(r.DestinationIDs, did)
		}
	}
	return out, rows.Err()
}

func hostGroups(ctx context.Context, qq q, hostID string) ([]string, error) {
	rows, err := qq.QueryContext(ctx, `SELECT group_id FROM group_members WHERE host_id = ?`, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// targets resolves which enabled destinations an alert goes to. Every
// matching route contributes; a destination chosen by several routes is
// notified once, with recoveries if any of them wants recoveries and the
// shortest reminder interval among them. Order is by destination name.
func (s *Service) targets(ctx context.Context, qq q, a *Alert, sev string) ([]target, error) {
	routes, err := s.loadRoutes(ctx, qq)
	if err != nil {
		return nil, err
	}
	groups, err := hostGroups(ctx, qq, a.HostID)
	if err != nil {
		return nil, err
	}
	chosen := map[string]*target{}
	for _, r := range routes {
		if !r.matches(sev, a.HostID, groups, a.Category, a.Check) {
			continue
		}
		for _, d := range r.DestinationIDs {
			t := chosen[d]
			if t == nil {
				t = &target{ID: d}
				chosen[d] = t
			}
			t.Recovery = t.Recovery || r.Recovery
			if r.ReminderMinutes > 0 && (t.Reminder == 0 || r.ReminderMinutes < t.Reminder) {
				t.Reminder = r.ReminderMinutes
			}
		}
	}
	if len(chosen) == 0 {
		return nil, nil
	}
	rows, err := qq.QueryContext(ctx, `SELECT id, name, kind FROM notify_destinations WHERE enabled = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []target
	for rows.Next() {
		var id, name, kind string
		if err := rows.Scan(&id, &name, &kind); err != nil {
			return nil, err
		}
		if t := chosen[id]; t != nil {
			t.Name, t.Kind = name, kind
			out = append(out, *t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, rows.Err()
}

// queue puts the notifications for one alert event into the outbox and
// returns how many. Queueing is idempotent per (alert, destination, kind,
// reminder number); a cancelled one (suppressed by maintenance) is
// re-queued. The timeline names only the destinations queued now: one that
// already had it (an opening sent before maintenance) isn't sent it again.
func (s *Service) queue(ctx context.Context, tx *sql.Tx, a *Alert, kind, sev string, reminder int, at int64) (int, error) {
	ts, err := s.targets(ctx, tx, a, sev)
	if err != nil {
		return 0, err
	}
	if kind == notify.MsgReminder {
		var withRem []target
		for _, t := range ts {
			if t.Reminder > 0 {
				withRem = append(withRem, t)
			}
		}
		ts = withRem
	}
	if len(ts) == 0 {
		if kind == notify.MsgOpening {
			s.timeline(ctx, tx, a.ID, at, "notify", "No notification destination matches this alert", "")
		}
		return 0, nil
	}
	msg := s.message(ctx, tx, a, kind, reminder)
	minRem := 0
	var names []string
	for _, t := range ts {
		queued, err := s.insertDelivery(ctx, tx, a.ID, t, kind, reminder, msg, at)
		if err != nil {
			return 0, err
		}
		if queued {
			names = append(names, t.Name)
		}
		if t.Reminder > 0 && (minRem == 0 || t.Reminder < minRem) {
			minRem = t.Reminder
		}
	}
	if kind == notify.MsgOpening || kind == notify.MsgReminder {
		var next any
		if minRem > 0 && a.State == StateActive && reminder < s.cfg.MaxReminders {
			next = at + int64(minRem)*60_000
		}
		if _, err := tx.ExecContext(ctx, `UPDATE alerts SET next_reminder_at = ? WHERE id = ?`, next, a.ID); err != nil {
			return 0, err
		}
	}
	if kind != notify.MsgReminder && len(names) > 0 {
		label := map[string]string{notify.MsgOpening: "Notifying", notify.MsgEscalation: "Notifying about the escalation"}[kind]
		if label != "" {
			s.timeline(ctx, tx, a.ID, at, "notify", label+": "+strings.Join(names, ", "), "")
		}
	}
	return len(names), nil
}

// insertDelivery queues one notification, and says whether it did: not
// when that destination already has it (sent, sending or failed).
func (s *Service) insertDelivery(ctx context.Context, tx *sql.Tx, alertID string, t target, kind string, reminder int, msg notify.Message, at int64) (bool, error) {
	key := deliveryKey(alertID, t.ID, kind)
	switch kind {
	case notify.MsgReminder:
		key += fmt.Sprintf("|%d", reminder)
	case notify.MsgEscalation:
		key += "|" + msg.Severity
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO notify_deliveries (id, dedupe_key, alert_id, destination_id, destination_name, destination_kind, kind,
		recovery_wanted, message_json, status, attempts, next_attempt_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'queued', 0, ?, ?)
		ON CONFLICT (dedupe_key) DO UPDATE SET status = 'queued', attempts = 0, next_attempt_at = excluded.next_attempt_at,
			message_json = excluded.message_json, recovery_wanted = excluded.recovery_wanted, last_error = '', last_category = '',
			last_status_code = NULL, finished_at = NULL, ambiguous = 0, created_at = excluded.created_at
		WHERE notify_deliveries.status = 'cancelled'`,
		ids.New(ids.NotifyDelivery), key, alertID, t.ID, t.Name, t.Kind, kind, b2i(t.Recovery), marshal(msg), at, at)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func deliveryKey(alertID, destID, kind string) string {
	return fmt.Sprintf("%s|%s|%s", alertID, destID, kind)
}

// unsentOpening says how many of the destinations an alert goes to now
// would be sent its opening (those that never had it, or whose opening
// maintenance cancelled), and how many it goes to.
func (s *Service) unsentOpening(ctx context.Context, qq q, a *Alert) (unsent, targets int, err error) {
	ts, err := s.targets(ctx, qq, a, a.Severity)
	if err != nil {
		return 0, 0, err
	}
	n := 0
	for _, t := range ts {
		var st string
		err := qq.QueryRowContext(ctx, `SELECT status FROM notify_deliveries WHERE dedupe_key = ?`, deliveryKey(a.ID, t.ID, notify.MsgOpening)).Scan(&st)
		if errors.Is(err, sql.ErrNoRows) || err == nil && st == "cancelled" {
			n++
		} else if err != nil {
			return 0, 0, err
		}
	}
	return n, len(ts), nil
}

// --- route management ---

// RouteInput creates or changes a route.
type RouteInput struct {
	Name            *string   `json:"name"`
	Enabled         *bool     `json:"enabled"`
	Severities      *[]string `json:"severities"`
	HostIDs         *[]string `json:"host_ids"`
	GroupIDs        *[]string `json:"group_ids"`
	Categories      *[]string `json:"categories"`
	Checks          *[]string `json:"checks"`
	Recovery        *bool     `json:"recovery"`
	ReminderMinutes *int      `json:"reminder_minutes"`
	DestinationIDs  *[]string `json:"destination_ids"`
}

const maxFilterValues = 200

// validateRoute checks a route. Host, group and destination ids must exist,
// except ids the route already had (a deleted host stays in its filter and
// matches nothing: dropping it would silently widen the route to every
// host).
func (s *Service) validateRoute(ctx context.Context, qq q, r *Route, prev *Route) error {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" || len(r.Name) > 80 {
		return &ValidationError{"the route needs a name (at most 80 characters)"}
	}
	for _, v := range r.Severities {
		if !actionable(v) {
			return &ValidationError{"severities are warning and critical"}
		}
	}
	for _, v := range r.Categories {
		if !slices.Contains(monsvc.CheckCategories, v) {
			return &ValidationError{"unknown category " + v}
		}
	}
	for _, v := range r.Checks {
		if !monsvc.KnownCheck(v) {
			return &ValidationError{"unknown check " + v}
		}
	}
	if r.ReminderMinutes != 0 && (r.ReminderMinutes < MinReminderMinutes || r.ReminderMinutes > MaxReminderMinutes) {
		return &ValidationError{fmt.Sprintf("reminders are off (0) or every %d minutes to 7 days", MinReminderMinutes)}
	}
	for _, list := range [][]string{r.Severities, r.HostIDs, r.GroupIDs, r.Categories, r.Checks, r.DestinationIDs} {
		if len(list) > maxFilterValues {
			return &ValidationError{fmt.Sprintf("at most %d values per filter", maxFilterValues)}
		}
	}
	var had []string
	if prev != nil {
		had = append(append(slices.Clone(prev.HostIDs), prev.GroupIDs...), prev.DestinationIDs...)
	}
	for _, x := range []struct {
		table, what string
		ids         []string
	}{{"hosts", "host", r.HostIDs}, {"groups", "group", r.GroupIDs}, {"notify_destinations", "destination", r.DestinationIDs}} {
		for _, id := range x.ids {
			if slices.Contains(had, id) {
				continue
			}
			var one int
			if err := qq.QueryRowContext(ctx, `SELECT 1 FROM `+x.table+` WHERE id = ?`, id).Scan(&one); err != nil {
				return &ValidationError{"unknown " + x.what + " " + id}
			}
		}
	}
	return nil
}

// ValidationError is a bad request.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func dedupe(v []string) []string {
	out := []string{}
	for _, x := range v {
		if x = strings.TrimSpace(x); x != "" && !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

func (in RouteInput) apply(r *Route) {
	set := func(dst *[]string, src *[]string) {
		if src != nil {
			*dst = dedupe(*src)
		}
	}
	if in.Name != nil {
		r.Name = *in.Name
	}
	if in.Enabled != nil {
		r.Enabled = *in.Enabled
	}
	set(&r.Severities, in.Severities)
	set(&r.HostIDs, in.HostIDs)
	set(&r.GroupIDs, in.GroupIDs)
	set(&r.Categories, in.Categories)
	set(&r.Checks, in.Checks)
	set(&r.DestinationIDs, in.DestinationIDs)
	if in.Recovery != nil {
		r.Recovery = *in.Recovery
	}
	if in.ReminderMinutes != nil {
		r.ReminderMinutes = *in.ReminderMinutes
	}
}

// Routes lists every route (default first).
func (s *Service) Routes(ctx context.Context) ([]*Route, error) {
	return s.loadRoutes(ctx, s.d.Store.Reader)
}

func (s *Service) route(ctx context.Context, qq q, id string) (*Route, error) {
	rs, err := s.loadRoutes(ctx, qq)
	if err != nil {
		return nil, err
	}
	for _, r := range rs {
		if r.ID == id {
			return r, nil
		}
	}
	return nil, ErrNotFound
}

func (s *Service) saveRoute(ctx context.Context, tx *sql.Tx, r *Route) error {
	j := func(v []string) string { return marshal(v) }
	if _, err := tx.ExecContext(ctx, `INSERT INTO notify_routes (id, name, is_default, enabled, severities_json, host_ids_json, group_ids_json,
		categories_json, checks_json, recovery, reminder_minutes, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET name = excluded.name, enabled = excluded.enabled, severities_json = excluded.severities_json,
		host_ids_json = excluded.host_ids_json, group_ids_json = excluded.group_ids_json, categories_json = excluded.categories_json,
		checks_json = excluded.checks_json, recovery = excluded.recovery, reminder_minutes = excluded.reminder_minutes, updated_at = excluded.updated_at`,
		r.ID, r.Name, b2i(r.IsDefault), b2i(r.Enabled), j(r.Severities), j(r.HostIDs), j(r.GroupIDs), j(r.Categories), j(r.Checks),
		b2i(r.Recovery), r.ReminderMinutes, r.CreatedAt, r.UpdatedAt); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM notify_route_destinations WHERE route_id = ?`, r.ID); err != nil {
		return err
	}
	for _, d := range r.DestinationIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO notify_route_destinations (route_id, destination_id) VALUES (?, ?)`, r.ID, d); err != nil {
			return err
		}
	}
	return nil
}

// CreateRoute adds a route; onTx runs in the transaction (auditing).
func (s *Service) CreateRoute(ctx context.Context, in RouteInput, onTx func(tx *sql.Tx, r *Route) error) (*Route, error) {
	now := s.nowMs()
	r := &Route{ID: ids.New(ids.NotifyRoute), Enabled: true, Recovery: true, Severities: []string{}, HostIDs: []string{}, GroupIDs: []string{},
		Categories: []string{}, Checks: []string{}, DestinationIDs: []string{}, CreatedAt: now, UpdatedAt: now}
	in.apply(r)
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var n int
		tx.QueryRowContext(ctx, `SELECT count(*) FROM notify_routes`).Scan(&n)
		if n >= 100 {
			return &ValidationError{"at most 100 routes"}
		}
		if err := s.validateRoute(ctx, tx, r, nil); err != nil {
			return err
		}
		if err := s.saveRoute(ctx, tx, r); err != nil {
			return err
		}
		return onTx(tx, r)
	})
	return r, err
}

// UpdateRoute changes a route; onTx gets the route before and after.
func (s *Service) UpdateRoute(ctx context.Context, id string, in RouteInput, onTx func(tx *sql.Tx, before, after *Route) error) (*Route, error) {
	var out *Route
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		before, err := s.route(ctx, tx, id)
		if err != nil {
			return err
		}
		r := *before
		r.Severities, r.HostIDs, r.GroupIDs = slices.Clone(before.Severities), slices.Clone(before.HostIDs), slices.Clone(before.GroupIDs)
		r.Categories, r.Checks, r.DestinationIDs = slices.Clone(before.Categories), slices.Clone(before.Checks), slices.Clone(before.DestinationIDs)
		in.apply(&r)
		r.UpdatedAt = s.nowMs()
		if err := s.validateRoute(ctx, tx, &r, before); err != nil {
			return err
		}
		if err := s.saveRoute(ctx, tx, &r); err != nil {
			return err
		}
		out = &r
		return onTx(tx, before, &r)
	})
	return out, err
}

// DeleteRoute removes a route (never the default one).
func (s *Service) DeleteRoute(ctx context.Context, id string, onTx func(tx *sql.Tx, r *Route) error) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		r, err := s.route(ctx, tx, id)
		if err != nil {
			return err
		}
		if r.IsDefault {
			return ErrDefaultRoute
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM notify_routes WHERE id = ?`, id); err != nil {
			return err
		}
		return onTx(tx, r)
	})
}

// RoutePreview says which destinations an alert would go to.
type RoutePreview struct {
	Destinations []PreviewTarget `json:"destinations"`
}

// PreviewTarget is one destination in a preview.
type PreviewTarget struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Recovery bool   `json:"recovery"`
	Reminder int    `json:"reminder_minutes"`
}

// Preview resolves routing for a hypothetical alert.
func (s *Service) Preview(ctx context.Context, hostID, check, severity string) (*RoutePreview, error) {
	if !actionable(severity) {
		return nil, &ValidationError{"severity must be warning or critical"}
	}
	if !monsvc.KnownCheck(check) {
		return nil, &ValidationError{"unknown check"}
	}
	var one int
	if err := s.d.Store.Reader.QueryRowContext(ctx, `SELECT 1 FROM hosts WHERE id = ?`, hostID).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, &ValidationError{"unknown host"}
		}
		return nil, err
	}
	a := &Alert{HostID: hostID, Check: check, Category: monsvc.CheckCategory(check), Severity: severity}
	ts, err := s.targets(ctx, s.d.Store.Reader, a, severity)
	if err != nil {
		return nil, err
	}
	out := &RoutePreview{Destinations: []PreviewTarget{}}
	for _, t := range ts {
		out.Destinations = append(out.Destinations, PreviewTarget{t.ID, t.Name, t.Kind, t.Recovery, t.Reminder})
	}
	return out, nil
}
