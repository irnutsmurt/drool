package alerts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"drool/internal/ids"
	"drool/internal/notify"
)

// Maintenance suppresses notifications for hosts, nothing else: health
// collection and evaluation continue, issues and alerts stay visible
// (marked "suppressed by maintenance"), and no safety mechanism
// (firewall rollback, agent recovery, host-key checks, capabilities) is
// touched. Recurring windows (M13 schedules of kind "maintenance") start
// and end ordinary maintenance like this.
type Maintenance struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
	// ScheduleID: the recurring window that started it ("" when a person
	// did).
	ScheduleID  string              `json:"schedule_id,omitempty"`
	CreatedBy   string              `json:"created_by"`
	StartedAt   int64               `json:"started_at"`
	EndsAt      *int64              `json:"ends_at,omitempty"` // nil: until ended manually
	EndedAt     *int64              `json:"ended_at,omitempty"`
	EndedBy     string              `json:"ended_by,omitempty"`
	Active      bool                `json:"active"`
	Targets     []MaintenanceTarget `json:"targets"`
	HostCount   int                 `json:"host_count"`  // hosts covered now
	Suppressing int                 `json:"suppressing"` // open alerts it suppresses
}

// MaintenanceTarget is a host or group in maintenance.
type MaintenanceTarget struct {
	Kind string `json:"kind"` // host | group
	ID   string `json:"id"`
	Name string `json:"name"`
}

// MaintenanceInput starts maintenance.
type MaintenanceInput struct {
	HostIDs         []string `json:"host_ids"`
	GroupIDs        []string `json:"group_ids"`
	Reason          string   `json:"reason"`
	DurationMinutes int      `json:"duration_minutes"` // or
	Until           *int64   `json:"until"`            // unix ms, or
	Indefinite      bool     `json:"indefinite"`       // until ended manually
	// A recurring window's occurrence (M13; not settable over the API).
	ScheduleID   string `json:"-"`
	OccurrenceID string `json:"-"`
}

// MaxMaintenance bounds a timed window (longer: "until ended manually").
const MaxMaintenance = 30 * 24 * time.Hour

// covering returns the active maintenance covering a host (the earliest
// started), or nil.
func (s *Service) covering(ctx context.Context, qq q, hostID string, at int64) (*MaintenanceRef, error) {
	var r MaintenanceRef
	var ends sql.NullInt64
	err := qq.QueryRowContext(ctx, `SELECT m.id, m.reason, m.ends_at FROM maintenance m JOIN maintenance_targets t ON t.maintenance_id = m.id
		WHERE m.ended_at IS NULL AND m.started_at <= ? AND (m.ends_at IS NULL OR m.ends_at > ?)
		AND ((t.kind = 'host' AND t.target_id = ?) OR (t.kind = 'group' AND t.target_id IN (SELECT group_id FROM group_members WHERE host_id = ?)))
		ORDER BY m.started_at, m.id LIMIT 1`, at, at, hostID, hostID).Scan(&r.ID, &r.Reason, &ends)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if ends.Valid {
		r.EndsAt = &ends.Int64
	}
	return &r, nil
}

// StartMaintenance begins maintenance now. Open alerts on the covered
// hosts become suppressed and their unsent notifications are cancelled.
func (s *Service) StartMaintenance(ctx context.Context, in MaintenanceInput, by Actor, onTx func(tx *sql.Tx, m *Maintenance) error) (*Maintenance, error) {
	now := s.now()
	in.Reason = strings.TrimSpace(in.Reason)
	if len([]rune(in.Reason)) > 200 {
		return nil, &ValidationError{"the reason must be at most 200 characters"}
	}
	in.HostIDs, in.GroupIDs = dedupe(in.HostIDs), dedupe(in.GroupIDs)
	if len(in.HostIDs)+len(in.GroupIDs) == 0 {
		return nil, &ValidationError{"choose at least one host or group"}
	}
	if len(in.HostIDs)+len(in.GroupIDs) > 500 {
		return nil, &ValidationError{"at most 500 hosts and groups"}
	}
	var ends *int64
	n := 0
	if in.DurationMinutes != 0 {
		n++
		if in.DurationMinutes < 1 || time.Duration(in.DurationMinutes)*time.Minute > MaxMaintenance {
			return nil, &ValidationError{"the duration must be 1 minute to 30 days (or until ended manually)"}
		}
		e := now.Add(time.Duration(in.DurationMinutes) * time.Minute).UnixMilli()
		ends = &e
	}
	if in.Until != nil {
		n++
		u := time.UnixMilli(*in.Until)
		if !u.After(now) || u.Sub(now) > MaxMaintenance {
			return nil, &ValidationError{"the end must be in the future and at most 30 days away (or until ended manually)"}
		}
		ends = in.Until
	}
	if in.Indefinite {
		n++
	}
	if n != 1 {
		return nil, &ValidationError{"choose one of: a duration, an end time, or until ended manually"}
	}
	m := &Maintenance{ID: ids.New(ids.Maintenance), Reason: in.Reason, CreatedBy: by.Name, StartedAt: now.UnixMilli(), EndsAt: ends, Active: true,
		ScheduleID: in.ScheduleID}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var uid any
		if by.UserID != "" {
			uid = by.UserID
		}
		var sch, occ any
		if in.ScheduleID != "" {
			sch, occ = in.ScheduleID, in.OccurrenceID
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO maintenance (id, reason, created_by, created_by_name, started_at, ends_at, schedule_id, occurrence_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			m.ID, m.Reason, uid, by.Name, m.StartedAt, m.EndsAt, sch, occ); err != nil {
			return err
		}
		for _, x := range []struct {
			kind, table string
			ids         []string
		}{{"host", "hosts", in.HostIDs}, {"group", "groups", in.GroupIDs}} {
			for _, id := range x.ids {
				var name string
				if err := tx.QueryRowContext(ctx, `SELECT name FROM `+x.table+` WHERE id = ?`, id).Scan(&name); err != nil {
					return &ValidationError{"unknown " + x.kind + " " + id}
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO maintenance_targets (maintenance_id, kind, target_id, name) VALUES (?, ?, ?, ?)`,
					m.ID, x.kind, id, name); err != nil {
					return err
				}
				m.Targets = append(m.Targets, MaintenanceTarget{x.kind, id, name})
			}
		}
		s.event(ctx, tx, Event{TS: m.StartedAt, Kind: "maintenance.started", ObjectType: "maintenance", ObjectID: m.ID,
			Title: "Maintenance started by " + by.Name + " for " + targetsText(m.Targets) + untilText(m.EndsAt) + reasonSuffix(m.Reason)})
		if err := s.applySuppression(ctx, tx, m.StartedAt); err != nil {
			return err
		}
		return onTx(tx, m)
	})
	if err != nil {
		return nil, err
	}
	return s.GetMaintenance(ctx, m.ID)
}

func targetsText(ts []MaintenanceTarget) string {
	var names []string
	for _, t := range ts {
		n := t.Name
		if t.Kind == "group" {
			n = "pack " + n
		}
		names = append(names, n)
	}
	if len(names) > 5 {
		return strings.Join(names[:5], ", ") + fmt.Sprintf(" and %d more", len(names)-5)
	}
	return strings.Join(names, ", ")
}

func untilText(ends *int64) string {
	if ends == nil {
		return " until ended manually"
	}
	return " until " + time.UnixMilli(*ends).Format("2006-01-02 15:04 MST")
}

// EndMaintenance ends maintenance now.
func (s *Service) EndMaintenance(ctx context.Context, id string, by Actor, onTx func(tx *sql.Tx, m *Maintenance) error) (*Maintenance, error) {
	now := s.nowMs()
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		m, err := s.maintenance(ctx, tx, id, now)
		if err != nil {
			return err
		}
		if !m.Active {
			return &ValidationError{"this maintenance has already ended"}
		}
		if err := s.endTx(ctx, tx, m, now, by.Name); err != nil {
			return err
		}
		return onTx(tx, m)
	})
	if err != nil {
		return nil, err
	}
	return s.GetMaintenance(ctx, id)
}

func (s *Service) endTx(ctx context.Context, tx *sql.Tx, m *Maintenance, at int64, by string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE maintenance SET ended_at = ?, ended_by_name = ? WHERE id = ? AND ended_at IS NULL`, at, by, m.ID); err != nil {
		return err
	}
	how := "ended by " + by
	if by == "" {
		how = "expired"
	}
	s.event(ctx, tx, Event{TS: at, Kind: "maintenance.ended", ObjectType: "maintenance", ObjectID: m.ID,
		Title: "Maintenance " + how + " for " + targetsText(m.Targets) + reasonSuffix(m.Reason)})
	return s.applySuppression(ctx, tx, at)
}

// expireMaintenance ends windows whose time is up.
func (s *Service) expireMaintenance(ctx context.Context, now time.Time) {
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT id, ends_at FROM maintenance WHERE ended_at IS NULL AND ends_at IS NOT NULL AND ends_at <= ?`, now.UnixMilli())
	if err != nil {
		return
	}
	type due struct {
		id string
		at int64
	}
	var ds []due
	for rows.Next() {
		var d due
		if rows.Scan(&d.id, &d.at) == nil {
			ds = append(ds, d)
		}
	}
	rows.Close()
	for _, d := range ds {
		err := s.inTx(ctx, func(tx *sql.Tx) error {
			m, err := s.maintenance(ctx, tx, d.id, d.at)
			if err != nil {
				return err
			}
			return s.endTx(ctx, tx, m, d.at, "")
		})
		if err != nil {
			s.log.Warn("expire maintenance", "id", d.id, "err", err)
		}
	}
}

// syncSuppression re-applies suppression (group membership may change
// while maintenance runs).
func (s *Service) syncSuppression(ctx context.Context, now time.Time) {
	if err := s.inTx(ctx, func(tx *sql.Tx) error { return s.applySuppression(ctx, tx, now.UnixMilli()) }); err != nil {
		s.log.Warn("maintenance suppression", "err", err)
	}
}

// applySuppression brings every open alert's suppression in line with the
// maintenance in force at `at`. Newly suppressed alerts lose their unsent
// notifications; alerts leaving maintenance while still active get one
// opening notification after a grace period (the condition may clear as
// the work finishes).
func (s *Service) applySuppression(ctx context.Context, tx *sql.Tx, at int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, host_id, suppressed, COALESCE(suppressed_by, ''), state FROM alerts WHERE state IN ('active','acknowledged')`)
	if err != nil {
		return err
	}
	type open struct {
		id, host, by, state string
		sup                 bool
	}
	var os []open
	for rows.Next() {
		var o open
		var sup int
		if err := rows.Scan(&o.id, &o.host, &sup, &o.by, &o.state); err != nil {
			rows.Close()
			return err
		}
		o.sup = sup == 1
		os = append(os, o)
	}
	rows.Close()
	if len(os) == 0 {
		return nil
	}
	cover, err := s.coverage(ctx, tx, at)
	if err != nil {
		return err
	}
	for _, o := range os {
		var m *MaintenanceRef
		if r, ok := cover[o.host]; ok {
			m = &r
		}
		switch {
		case m != nil && !o.sup:
			if _, err := tx.ExecContext(ctx, `UPDATE alerts SET suppressed = 1, suppressed_by = ?, ever_suppressed = 1, notify_after = NULL, updated_at = ? WHERE id = ?`,
				m.ID, at, o.id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE notify_deliveries SET status = 'cancelled', finished_at = ?, last_error = 'suppressed by maintenance'
				WHERE alert_id = ? AND status = 'queued'`, at, o.id); err != nil {
				return err
			}
			s.timeline(ctx, tx, o.id, at, "suppressed", "Maintenance started"+reasonSuffix(m.Reason)+": notifications suppressed", "")
		case m != nil && o.by != m.ID:
			if _, err := tx.ExecContext(ctx, `UPDATE alerts SET suppressed_by = ? WHERE id = ?`, m.ID, o.id); err != nil {
				return err
			}
		case m == nil && o.sup:
			var notifyAfter any
			text := "Maintenance ended; the alert is still active"
			if o.state == StateActive {
				// Still due after the grace (the opening checks again then),
				// but said as it is: an alert notified before the
				// maintenance isn't notified again.
				notifyAfter = at + s.cfg.Grace.Milliseconds()
				a, err := s.loadAlert(ctx, tx, `id = ?`, o.id)
				if err != nil {
					return err
				}
				unsent, targets, err := s.unsentOpening(ctx, tx, a)
				if err != nil {
					return err
				}
				switch {
				case unsent > 0:
					text += " — notifying in " + notify.Duration(s.cfg.Grace) + " unless it recovers"
				case targets > 0:
					text += " (already notified before the maintenance: no new message)"
				}
			} else {
				text += " (acknowledged: no notification)"
			}
			if _, err := tx.ExecContext(ctx, `UPDATE alerts SET suppressed = 0, suppressed_by = NULL, notify_after = ?, updated_at = ? WHERE id = ?`,
				notifyAfter, at, o.id); err != nil {
				return err
			}
			s.timeline(ctx, tx, o.id, at, "unsuppressed", text, "")
		}
	}
	return nil
}

// dueOpenings sends the one opening notification for alerts still active
// after their maintenance ended (and its grace period).
func (s *Service) dueOpenings(ctx context.Context, now time.Time) {
	at := now.UnixMilli()
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT id FROM alerts WHERE notify_after IS NOT NULL AND notify_after <= ?`, at)
	if err != nil {
		return
	}
	var due []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			due = append(due, id)
		}
	}
	rows.Close()
	queued := false
	for _, id := range due {
		err := s.inTx(ctx, func(tx *sql.Tx) error {
			a, err := s.loadAlert(ctx, tx, `id = ?`, id)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE alerts SET notify_after = NULL WHERE id = ?`, id); err != nil {
				return err
			}
			if a.State != StateActive || a.Suppressed {
				return nil
			}
			n, err := s.queue(ctx, tx, a, notify.MsgOpening, a.Severity, 0, at)
			queued = queued || n > 0
			return err
		})
		if err != nil {
			s.log.Warn("notify after maintenance", "alert", id, "err", err)
		}
	}
	if queued {
		s.nudge()
	}
}

// --- queries ---

func (s *Service) maintenance(ctx context.Context, qq q, id string, at int64) (*Maintenance, error) {
	ms, err := s.listMaintenance(ctx, qq, `m.id = ?`, []any{id}, at, 1)
	if err != nil {
		return nil, err
	}
	if len(ms) == 0 {
		return nil, ErrNotFound
	}
	return ms[0], nil
}

// GetMaintenance loads one maintenance.
func (s *Service) GetMaintenance(ctx context.Context, id string) (*Maintenance, error) {
	return s.maintenance(ctx, s.d.Store.Reader, id, s.nowMs())
}

// MaintenanceFilter selects maintenance.
type MaintenanceFilter struct {
	State  string // active | ended | all
	HostID string
	Limit  int
	Before int64 // started_at cursor
}

// ListMaintenance lists maintenance, newest first (bounded).
func (s *Service) ListMaintenance(ctx context.Context, f MaintenanceFilter) ([]*Maintenance, error) {
	now := s.nowMs()
	where, args := "1=1", []any{}
	switch f.State {
	case "", "active":
		where += ` AND m.ended_at IS NULL AND (m.ends_at IS NULL OR m.ends_at > ?)`
		args = append(args, now)
	case "ended":
		where += ` AND (m.ended_at IS NOT NULL OR m.ends_at <= ?)`
		args = append(args, now)
	}
	if f.HostID != "" {
		where += ` AND m.id IN (SELECT maintenance_id FROM maintenance_targets WHERE (kind = 'host' AND target_id = ?)
			OR (kind = 'group' AND target_id IN (SELECT group_id FROM group_members WHERE host_id = ?)))`
		args = append(args, f.HostID, f.HostID)
	}
	if f.Before > 0 {
		where += ` AND m.started_at < ?`
		args = append(args, f.Before)
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return s.listMaintenance(ctx, s.d.Store.Reader, where, args, now, limit)
}

func (s *Service) listMaintenance(ctx context.Context, qq q, where string, args []any, now int64, limit int) ([]*Maintenance, error) {
	rows, err := qq.QueryContext(ctx, `SELECT m.id, m.reason, m.created_by_name, m.started_at, m.ends_at, m.ended_at, m.ended_by_name,
		(SELECT count(*) FROM alerts a WHERE a.suppressed_by = m.id AND a.state IN ('active','acknowledged')), COALESCE(m.schedule_id, '')
		FROM maintenance m WHERE `+where+` ORDER BY m.started_at DESC, m.id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	out := []*Maintenance{}
	for rows.Next() {
		var m Maintenance
		var ends, ended sql.NullInt64
		if err := rows.Scan(&m.ID, &m.Reason, &m.CreatedBy, &m.StartedAt, &ends, &ended, &m.EndedBy, &m.Suppressing, &m.ScheduleID); err != nil {
			rows.Close()
			return nil, err
		}
		if ends.Valid {
			m.EndsAt = &ends.Int64
		}
		if ended.Valid {
			m.EndedAt = &ended.Int64
		} else if m.EndsAt != nil && *m.EndsAt <= now {
			m.EndedAt = m.EndsAt // expired; the next tick records it
		}
		m.Active = m.EndedAt == nil
		m.Targets = []MaintenanceTarget{}
		out = append(out, &m)
	}
	rows.Close()
	for _, m := range out {
		trows, err := qq.QueryContext(ctx, `SELECT kind, target_id, name FROM maintenance_targets WHERE maintenance_id = ? ORDER BY kind DESC, name`, m.ID)
		if err != nil {
			return nil, err
		}
		for trows.Next() {
			var t MaintenanceTarget
			if err := trows.Scan(&t.Kind, &t.ID, &t.Name); err != nil {
				trows.Close()
				return nil, err
			}
			m.Targets = append(m.Targets, t)
		}
		trows.Close()
		qq.QueryRowContext(ctx, `SELECT count(DISTINCT h) FROM (
			SELECT target_id AS h FROM maintenance_targets WHERE maintenance_id = ? AND kind = 'host' AND target_id IN (SELECT id FROM hosts)
			UNION SELECT gm.host_id FROM maintenance_targets t JOIN group_members gm ON gm.group_id = t.target_id WHERE t.maintenance_id = ? AND t.kind = 'group')`,
			m.ID, m.ID).Scan(&m.HostCount)
	}
	return out, nil
}

// Cover is one active maintenance covering a host.
type Cover struct {
	MaintenanceID string
	Reason        string
	ScheduleID    string // the recurring window that started it, if any
}

// Covers lists, for each of hosts, every active maintenance covering it
// now (by host or live group membership).
func (s *Service) Covers(ctx context.Context, hostIDs []string) (map[string][]Cover, error) {
	out := map[string][]Cover{}
	if len(hostIDs) == 0 {
		return out, nil
	}
	want := map[string]bool{}
	for _, h := range hostIDs {
		want[h] = true
	}
	now := s.nowMs()
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT h, id, reason, sch FROM (
		SELECT t.target_id AS h, m.id, m.reason, COALESCE(m.schedule_id, '') AS sch, m.started_at FROM maintenance m JOIN maintenance_targets t ON t.maintenance_id = m.id
			WHERE t.kind = 'host' AND m.ended_at IS NULL AND m.started_at <= ? AND (m.ends_at IS NULL OR m.ends_at > ?)
		UNION
		SELECT gm.host_id, m.id, m.reason, COALESCE(m.schedule_id, ''), m.started_at FROM maintenance m JOIN maintenance_targets t ON t.maintenance_id = m.id
			JOIN group_members gm ON gm.group_id = t.target_id WHERE t.kind = 'group' AND m.ended_at IS NULL AND m.started_at <= ? AND (m.ends_at IS NULL OR m.ends_at > ?))
		ORDER BY started_at, id`, now, now, now, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var h string
		var c Cover
		if err := rows.Scan(&h, &c.MaintenanceID, &c.Reason, &c.ScheduleID); err != nil {
			return nil, err
		}
		if want[h] {
			out[h] = append(out[h], c)
		}
	}
	return out, rows.Err()
}

// ActiveWindow returns the active maintenance a recurring window started,
// or nil.
func (s *Service) ActiveWindow(ctx context.Context, scheduleID string) (*Maintenance, error) {
	now := s.nowMs()
	ms, err := s.listMaintenance(ctx, s.d.Store.Reader, `m.schedule_id = ? AND m.ended_at IS NULL AND m.started_at <= ? AND (m.ends_at IS NULL OR m.ends_at > ?)`,
		[]any{scheduleID, now, now}, now, 1)
	if err != nil || len(ms) == 0 {
		return nil, err
	}
	return ms[0], nil
}

// RecordEvent adds an entry to the controller's event timeline (M13's
// automation events).
func (s *Service) RecordEvent(ctx context.Context, e Event) error {
	if e.TS == 0 {
		e.TS = s.nowMs()
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		s.event(ctx, tx, e)
		return nil
	})
}

// HostsInMaintenance maps every host covered by active maintenance to it.
func (s *Service) HostsInMaintenance(ctx context.Context) (map[string]MaintenanceRef, error) {
	return s.coverage(ctx, s.d.Store.Reader, s.nowMs())
}

// coverage maps each host in maintenance at `now` to the earliest-started
// window covering it (the same choice as covering).
func (s *Service) coverage(ctx context.Context, qq q, now int64) (map[string]MaintenanceRef, error) {
	rows, err := qq.QueryContext(ctx, `SELECT h, id, reason, ends_at FROM (
		SELECT t.target_id AS h, m.id, m.reason, m.ends_at, m.started_at FROM maintenance m JOIN maintenance_targets t ON t.maintenance_id = m.id
			WHERE t.kind = 'host' AND m.ended_at IS NULL AND m.started_at <= ? AND (m.ends_at IS NULL OR m.ends_at > ?)
		UNION ALL
		SELECT gm.host_id, m.id, m.reason, m.ends_at, m.started_at FROM maintenance m JOIN maintenance_targets t ON t.maintenance_id = m.id
			JOIN group_members gm ON gm.group_id = t.target_id WHERE t.kind = 'group' AND m.ended_at IS NULL AND m.started_at <= ? AND (m.ends_at IS NULL OR m.ends_at > ?)
	) ORDER BY started_at DESC, id DESC`, now, now, now, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]MaintenanceRef{}
	for rows.Next() {
		var h string
		var r MaintenanceRef
		var ends sql.NullInt64
		if err := rows.Scan(&h, &r.ID, &r.Reason, &ends); err != nil {
			return nil, err
		}
		if ends.Valid {
			r.EndsAt = &ends.Int64
		}
		out[h] = r // the earliest-started wins (rows are newest first)
	}
	return out, rows.Err()
}
