package sched

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // schedules name IANA zones; never depend on the host's zone database

	"drool/internal/alerts"
	"drool/internal/audit"
	"drool/internal/engine"
	"drool/internal/ids"
	"drool/internal/model"
	"drool/internal/store"
	"drool/internal/store/db"
)

// The scheduler is one bounded loop over the database: every tick it asks
// for the schedules whose next occurrence is due (an indexed query), and
// handles each in its own short transaction. There is no goroutine per
// schedule and no in-memory queue: the schedule rows are the queue, and
// the (schedule, time) uniqueness of occurrences means a restart at any
// point can never create a second job for the same occurrence.

// Tuning tunes the scheduler.
type Tuning struct {
	Tick time.Duration // how often due work is looked for (default 15 s)
	// MisfireGrace: an occurrence handled later than this after its time
	// (DROOL was not running) is "missed" and follows the missed-run
	// policy (default 5 min).
	MisfireGrace time.Duration
	Batch        int // due schedules per query (default 100)
	MaxBatches   int // per tick (default 50)
	HistoryDays  int // occurrence retention (default 400)
}

func (c *Tuning) defaults() {
	if c.Tick <= 0 {
		c.Tick = 15 * time.Second
	}
	if c.MisfireGrace <= 0 {
		c.MisfireGrace = 5 * time.Minute
	}
	if c.Batch <= 0 {
		c.Batch = 100
	}
	if c.MaxBatches <= 0 {
		c.MaxBatches = 50
	}
	if c.HistoryDays <= 0 {
		c.HistoryDays = 400
	}
}

// Deps are the scheduler's collaborators.
type Deps struct {
	Store  *store.Store
	Engine *engine.Engine
	Alerts *alerts.Service // maintenance (nil: windows and maintenance checks unavailable)
	Audit  *audit.Writer
	Log    *slog.Logger
	Tuning Tuning
}

// Hooks let tests stop the scheduler at precise points.
type Hooks struct {
	// BeforeCommit runs inside a dispatch transaction; an error rolls the
	// whole occurrence back (a crash before commit).
	BeforeCommit func(scheduleID string) error
	// NoLaunch leaves created jobs unlaunched (a crash right after commit).
	NoLaunch bool
}

// Service is scheduling.
type Service struct {
	d   Deps
	cfg Tuning
	now func() time.Time
	log *slog.Logger

	tickMu    sync.Mutex
	lastPrune time.Time
	missed    map[string]int // per tick: schedule name → missed occurrences
	hooks     Hooks
}

// New creates the service.
func New(d Deps) *Service {
	d.Tuning.defaults()
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Service{d: d, cfg: d.Tuning, now: time.Now, log: d.Log}
}

// SetClock replaces the clock (tests).
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// SetHooks installs test hooks.
func (s *Service) SetHooks(h Hooks) { s.hooks = h }

func (s *Service) nowMs() int64 { return s.now().UnixMilli() }

// Run handles due work until ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(s.cfg.Tick)
	defer t.Stop()
	for {
		s.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick handles everything due now (Run calls it every Tick; tests call
// it with a fake clock).
func (s *Service) Tick(ctx context.Context) {
	s.tickMu.Lock()
	defer s.tickMu.Unlock()
	now := s.now()
	s.missed = map[string]int{}
	seen := map[string]bool{}
	for range s.cfg.MaxBatches {
		due, err := s.due(ctx, now)
		if err != nil {
			s.log.Warn("scheduler: due schedules", "err", err)
			break
		}
		fresh := 0
		for _, r := range due {
			if seen[r.ID] {
				continue
			}
			seen[r.ID] = true
			fresh++
			if err := s.process(ctx, r, now); err != nil && ctx.Err() == nil {
				s.log.Warn("scheduler: occurrence", "schedule", r.ID, "err", err)
			}
		}
		if len(due) < s.cfg.Batch || fresh == 0 {
			break
		}
	}
	s.queued(ctx)
	s.reportMissed(ctx)
	if now.Sub(s.lastPrune) > 6*time.Hour {
		s.lastPrune = now
		s.prune(ctx, now)
	}
}

func (s *Service) due(ctx context.Context, now time.Time) ([]*row, error) {
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT `+rowCols+` FROM schedules
		WHERE enabled = 1 AND archived_at IS NULL AND next_run_at <= ? ORDER BY next_run_at LIMIT ?`, now.UnixMilli(), s.cfg.Batch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*row
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func lateNote(t time.Time, loc *time.Location) string {
	return "started late: DROOL was not running at " + t.In(loc).Format("Mon 2 Jan 15:04 MST")
}

// process handles one due schedule: the occurrence at next_run_at, and
// any that passed while DROOL was not running (by the missed-run policy).
func (s *Service) process(ctx context.Context, r *row, now time.Time) error {
	c := r.Config
	loc, err := LoadZone(c.TimeZone)
	if err != nil {
		return s.attention(ctx, r, nil, "its time zone "+c.TimeZone+" is no longer known")
	}
	T := time.UnixMilli(*r.NextRunAt).In(loc)
	due := []time.Time{T}
	rest, more := c.Recurrence.Between(T, now, loc, 100000)
	due = append(due, rest...)
	var nextAt *int64
	if n, ok := c.Recurrence.Next(now, loc); ok {
		ms := n.UnixMilli()
		nextAt = &ms
	}
	grace := s.cfg.MisfireGrace
	last := due[len(due)-1]
	if r.Kind == ScheduleMaintenance {
		dur := time.Duration(c.DurationMinutes) * time.Minute
		var missed []time.Time
		var start *time.Time
		for i, t := range due {
			if !t.Add(dur).After(now) {
				missed = append(missed, t)
			} else if i == len(due)-1 {
				start = &due[i]
			}
		}
		if len(missed) > 0 {
			if err := s.recordMissed(ctx, r, missed, more, "the window passed while DROOL was not running; no maintenance was started afterwards"); err != nil {
				return err
			}
		}
		if start == nil {
			return s.advance(ctx, r, nextAt)
		}
		note := ""
		if now.Sub(*start) > grace {
			note = lateNote(*start, loc) + "; the window keeps its original end"
		}
		_, err := s.runWindow(ctx, r, *start, start.Add(dur), opts{note: note, advance: nextAt, setNext: true})
		return err
	}
	if !c.Recurrence.Recurring() {
		late := now.Sub(T)
		switch {
		case late <= grace:
			_, err := s.dispatchJob(ctx, r, T, opts{setNext: true})
			return err
		case c.LateLimitMinutes > 0 && late > time.Duration(c.LateLimitMinutes)*time.Minute:
			return s.record(ctx, r, T, OccExpired, fmt.Sprintf("DROOL was not running at %s, and the run is not started more than %d minutes late",
				T.Format("Mon 2 Jan 15:04 MST"), c.LateLimitMinutes), opts{setNext: true})
		default:
			_, err := s.dispatchJob(ctx, r, T, opts{note: lateNote(T, loc), setNext: true})
			return err
		}
	}
	missed := due
	var runAt *time.Time
	note := ""
	switch {
	case now.Sub(last) <= grace:
		runAt, missed = &due[len(due)-1], due[:len(due)-1]
	case c.MissedPolicy == MissedRunOnce:
		runAt, missed = &due[len(due)-1], due[:len(due)-1]
		note = lateNote(last, loc) + " (the schedule runs one missed occurrence when DROOL returns)"
	}
	if len(missed) > 0 {
		if err := s.recordMissed(ctx, r, missed, more, "DROOL was not running at the scheduled time; missed runs are skipped"); err != nil {
			return err
		}
	}
	if runAt == nil {
		return s.advance(ctx, r, nextAt)
	}
	_, err = s.dispatchJob(ctx, r, *runAt, opts{note: note, advance: nextAt, setNext: true})
	return err
}

type opts struct {
	manual  bool
	by      Actor  // manual runs: who
	note    string // why it started late, etc.
	occID   string // fill this queued occurrence
	advance *int64 // the schedule's next run, set in the same transaction
	setNext bool   // whether to set next_run_at (to advance, possibly NULL)
}

// errHandled: the occurrence already exists (another pass, or before a
// restart, handled it).
var errHandled = errors.New("occurrence already handled")

// advance moves a schedule to its next occurrence (NULL: none).
func (s *Service) advance(ctx context.Context, r *row, next *int64) error {
	_, err := s.d.Store.Writer.ExecContext(ctx, `UPDATE schedules SET next_run_at = ? WHERE id = ? AND next_run_at IS ?`, nullInt(next), r.ID, nullInt(r.NextRunAt))
	return err
}

func (s *Service) advanceTx(ctx context.Context, tx *sql.Tx, r *row, o opts) error {
	if !o.setNext {
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE schedules SET next_run_at = ? WHERE id = ? AND next_run_at IS ?`, nullInt(o.advance), r.ID, nullInt(r.NextRunAt))
	return err
}

// insertOcc inserts an occurrence; errHandled when one already exists
// for that time.
func (s *Service) insertOcc(ctx context.Context, tx *sql.Tx, r *row, at time.Time, id, status, reason string, o opts) error {
	triggered := ""
	if o.manual {
		triggered = o.by.Name
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO schedule_occurrences (id, schedule_id, schedule_revision, scheduled_for, manual, status, reason, triggered_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, id, r.ID, r.Revision, at.UnixMilli(), b2i(o.manual), status, reason, triggered, s.nowMs())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errHandled
	}
	return nil
}

// record stores an occurrence that doesn't run (skipped, expired, ...).
func (s *Service) record(ctx context.Context, r *row, at time.Time, status, reason string, o opts) error {
	err := s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
		if o.occID != "" {
			_, err := tx.ExecContext(ctx, `UPDATE schedule_occurrences SET status = ?, reason = ? WHERE id = ?`, status, reason, o.occID)
			return err
		}
		if err := s.insertOcc(ctx, tx, r, at, ids.New(ids.Occurrence), status, reason, o); err != nil {
			return err
		}
		return s.advanceTx(ctx, tx, r, o)
	})
	if errors.Is(err, errHandled) {
		if o.setNext {
			return s.advance(ctx, r, o.advance)
		}
		return nil
	}
	return err
}

// recordMissed stores the occurrences that passed while DROOL was not
// running as one row (bounded however long it was down).
func (s *Service) recordMissed(ctx context.Context, r *row, times []time.Time, more bool, why string) error {
	reason := why
	if more {
		reason += " (and more not counted)"
	}
	_, err := s.d.Store.Writer.ExecContext(ctx, `INSERT INTO schedule_occurrences (id, schedule_id, schedule_revision, scheduled_for, status, reason, missed_count, missed_until, created_at)
		VALUES (?, ?, ?, ?, 'missed', ?, ?, ?, ?) ON CONFLICT DO NOTHING`, ids.New(ids.Occurrence), r.ID, r.Revision, times[0].UnixMilli(), reason, len(times),
		times[len(times)-1].UnixMilli(), s.nowMs())
	if err == nil {
		s.missed[r.Name] += len(times)
	}
	return err
}

func (s *Service) reportMissed(ctx context.Context) {
	if len(s.missed) == 0 || s.d.Alerts == nil {
		return
	}
	var names []string
	n := 0
	for name, c := range s.missed {
		names = append(names, name)
		n += c
	}
	sort.Strings(names)
	list := strings.Join(names, ", ")
	if len(names) > 5 {
		list = strings.Join(names[:5], ", ") + fmt.Sprintf(" and %d more", len(names)-5)
	}
	s.d.Alerts.RecordEvent(ctx, alerts.Event{Kind: "schedule.missed", Severity: "warning", ObjectType: "schedule",
		Title: fmt.Sprintf("%d scheduled run(s) were missed while DROOL was not running: %s", n, list)})
}

// authorize checks that a schedule's owner may still run it.
func (s *Service) authorize(ctx context.Context, r *row, steps []model.Operation) string {
	if r.OwnerID == "" {
		return "its owner's account was deleted; an administrator can assign a new owner"
	}
	u, err := s.d.Store.R.GetUser(ctx, r.OwnerID)
	if err != nil {
		return "its owner's account was deleted; an administrator can assign a new owner"
	}
	if u.Disabled != 0 {
		return fmt.Sprintf("its owner's account (%s) is disabled", u.Username)
	}
	if p := MissingPermission(u.Role, RequiredPermissions(r.Kind, steps)); p != "" {
		return fmt.Sprintf("its owner (%s) no longer has the %s permission", u.Username, p)
	}
	return ""
}

// attention disables a schedule that can't run as configured, says why,
// and records it (event and audit). The occurrence is recorded as blocked
// when given.
func (s *Service) attention(ctx context.Context, r *row, at *time.Time, why string) error {
	now := s.nowMs()
	var emit func()
	err := s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
		if at != nil {
			if err := s.insertOcc(ctx, tx, r, *at, ids.New(ids.Occurrence), OccBlocked, why, opts{}); err != nil && !errors.Is(err, errHandled) {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE schedules SET enabled = 0, attention = ?, attention_at = ? WHERE id = ?`, "Disabled: "+why, now, r.ID); err != nil {
			return err
		}
		var err error
		emit, err = s.d.Audit.RecordTx(ctx, db.New(tx), audit.Event{Action: "schedule.blocked", Actor: &audit.System, Result: audit.Failure,
			TargetType: "schedule", TargetID: r.ID, Details: map[string]any{"name": r.Name, "reason": why, "owner_id": r.OwnerID}})
		return err
	})
	if err != nil {
		return err
	}
	emit()
	if s.d.Alerts != nil {
		s.d.Alerts.RecordEvent(ctx, alerts.Event{Kind: "schedule.disabled", Severity: "warning", ObjectType: "schedule", ObjectID: r.ID,
			Title: fmt.Sprintf("Schedule %q was disabled: %s", r.Name, why)})
	}
	s.log.Warn("schedule disabled", "schedule", r.ID, "reason", why)
	return nil
}

// activeJob returns the schedule's job that is still running, or "".
func (s *Service) activeJob(ctx context.Context, scheduleID string) string {
	var id string
	s.d.Store.Reader.QueryRowContext(ctx, `SELECT j.id FROM schedule_occurrences o JOIN jobs j ON j.id = o.job_id
		WHERE o.schedule_id = ? AND j.status IN ('pending','running','paused') LIMIT 1`, scheduleID).Scan(&id)
	return id
}

func (s *Service) hasQueued(ctx context.Context, scheduleID string) bool {
	var n int
	s.d.Store.Reader.QueryRowContext(ctx, `SELECT count(*) FROM schedule_occurrences WHERE schedule_id = ? AND status = 'queued'`, scheduleID).Scan(&n)
	return n > 0
}

// Result is what dispatching an occurrence produced.
type Result struct {
	OccurrenceID string
	JobID        string
	Status       string
}

// dispatchJob turns one occurrence of a job schedule into an ordinary job:
// checks (owner, overlap, window, maintenance), then the occurrence and
// the job in one transaction, then the launch.
func (s *Service) dispatchJob(ctx context.Context, r *row, at time.Time, o opts) (*Result, error) {
	c := r.Config
	steps, rev, err := s.steps(ctx, s.d.Store.Reader, c)
	if err != nil {
		why := "its workflow revision no longer exists"
		return nil, s.attention(ctx, r, &at, why)
	}
	runner := r.OwnerID
	if o.manual {
		runner = o.by.UserID
	} else if why := s.authorize(ctx, r, steps); why != "" {
		return &Result{Status: OccBlocked}, s.attention(ctx, r, &at, why)
	}
	if !o.manual && o.occID == "" {
		if job := s.activeJob(ctx, r.ID); job != "" {
			if c.OverlapPolicy == OverlapQueue && !s.hasQueued(ctx, r.ID) {
				return &Result{Status: OccQueued}, s.record(ctx, r, at, OccQueued, "the previous run (job "+job+") is still active; this run starts when it finishes", o)
			}
			reason := "the previous run (job " + job + ") is still active"
			if c.OverlapPolicy == OverlapQueue {
				reason += ", and a run is already queued"
			}
			return &Result{Status: OccSkipped}, s.record(ctx, r, at, OccSkipped, reason, o)
		}
	}
	// Maintenance: scheduled automation doesn't start on hosts in
	// maintenance (manual runs are deliberate and do). A schedule
	// associated with a recurring window runs only while that window is
	// active, and may run on the hosts it (and only it) covers.
	skip := map[string]string{}
	if !o.manual && s.d.Alerts != nil {
		if c.WindowID != "" {
			if m, err := s.d.Alerts.ActiveWindow(ctx, c.WindowID); err != nil {
				return nil, err
			} else if m == nil {
				var wname string
				s.d.Store.Reader.QueryRowContext(ctx, `SELECT name FROM schedules WHERE id = ?`, c.WindowID).Scan(&wname)
				return &Result{Status: OccSkipped}, s.record(ctx, r, at, OccSkipped, fmt.Sprintf("its maintenance window %q is not active", wname), o)
			}
		}
		hosts, _, err := engine.ResolveTargets(ctx, s.d.Store.R, c.Targets)
		if err == nil {
			ids := make([]string, len(hosts))
			for i, h := range hosts {
				ids[i] = h.ID
			}
			covers, err := s.d.Alerts.Covers(ctx, ids)
			if err != nil {
				return nil, err
			}
			for h, cs := range covers {
				for _, cv := range cs {
					if c.WindowID != "" && cv.ScheduleID == c.WindowID {
						continue // the associated window: approved
					}
					why := "maintenance active"
					if cv.Reason != "" {
						why += " (" + cv.Reason + ")"
					}
					skip[h] = why
					break
				}
			}
		}
	}
	name := r.Name
	spec := model.JobSpec{Name: name, Steps: steps, Targets: c.Targets, Strategy: c.Strategy}
	origin := engine.Origin{ScheduleID: r.ID}
	if rev != nil {
		origin.WorkflowID, origin.WorkflowRevision = c.Workflow.ID, rev.Revision
	}
	occID := o.occID
	if occID == "" {
		occID = ids.New(ids.Occurrence)
	}
	origin.OccurrenceID = occID
	var sub *engine.Submission
	var res Result
	var emit func()
	err = s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
		if o.occID == "" {
			if err := s.insertOcc(ctx, tx, r, at, occID, OccDispatched, o.note, o); err != nil {
				return err
			}
		}
		q := db.New(tx)
		var err error
		sub, err = s.d.Engine.Submit(ctx, q, spec, engine.SubmitOptions{UserID: runner, Origin: origin, Skip: skip})
		var ve *engine.ValidationError
		switch {
		case errors.Is(err, engine.ErrNothingToRun):
			res.Status = OccSkipped
			reason := fmt.Sprintf("all %d target host(s) are in maintenance", len(sub.Skipped))
			if len(sub.Skipped) > 0 {
				reason += ": " + sub.Skipped[0].Warning
			}
			sub = nil
			if _, err := tx.ExecContext(ctx, `UPDATE schedule_occurrences SET status = 'skipped', reason = ?, skipped_hosts = ? WHERE id = ?`, reason, len(skip), occID); err != nil {
				return err
			}
		case errors.As(err, &ve) && strings.Contains(ve.Msg, "match no hosts"):
			res.Status = OccSkipped
			sub = nil
			if _, err := tx.ExecContext(ctx, `UPDATE schedule_occurrences SET status = 'skipped', reason = 'no hosts match the targets now' WHERE id = ?`, occID); err != nil {
				return err
			}
		case errors.As(err, &ve):
			res.Status = OccFailed
			sub = nil
			if _, err := tx.ExecContext(ctx, `UPDATE schedule_occurrences SET status = 'failed', reason = ? WHERE id = ?`, ve.Msg, occID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE schedules SET attention = ?, attention_at = ? WHERE id = ?`, "The last run could not start: "+ve.Msg, s.nowMs(), r.ID); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			res.Status, res.JobID = OccDispatched, sub.Job.ID
			reason := o.note
			if sub.Plan.Note != "" {
				reason = strings.TrimPrefix(reason+"; "+sub.Plan.Note, "; ")
			}
			if _, err := tx.ExecContext(ctx, `UPDATE schedule_occurrences SET status = 'dispatched', job_id = ?, dispatched_at = ?, skipped_hosts = ?, reason = ? WHERE id = ?`,
				sub.Job.ID, s.nowMs(), len(sub.Skipped), reason, occID); err != nil {
				return err
			}
			actor := &audit.Actor{Type: audit.ActorSystem, ID: r.ID, Name: "schedule " + r.Name}
			if o.manual {
				actor = &audit.Actor{Type: audit.ActorUser, ID: o.by.UserID, Name: o.by.Name}
			}
			var owner string
			tx.QueryRowContext(ctx, `SELECT username FROM users WHERE id = ?`, r.OwnerID).Scan(&owner)
			stepTypes := make([]string, len(steps))
			for i, st := range steps {
				stepTypes[i] = st.Type
			}
			summary := fmt.Sprintf("system execution via schedule %q, owned by %s", r.Name, owner)
			if o.manual {
				summary = fmt.Sprintf("run now of schedule %q by %s", r.Name, o.by.Name)
			}
			emit, err = s.d.Audit.RecordTx(ctx, q, audit.Event{Action: "job.create", Actor: actor, TargetType: "job", TargetID: sub.Job.ID,
				Details: map[string]any{"summary": summary, "name": name, "steps": stepTypes, "hosts": len(sub.Hosts), "skipped": len(sub.Skipped), "via": "schedule",
					"schedule_id": r.ID, "schedule_name": r.Name, "owner_id": r.OwnerID, "owner": owner, "occurrence_id": occID,
					"scheduled_for": at.UTC().Format(time.RFC3339), "manual": o.manual, "workflow_id": origin.WorkflowID, "workflow_revision": origin.WorkflowRevision}})
			if err != nil {
				return err
			}
		}
		if err := s.advanceTx(ctx, tx, r, o); err != nil {
			return err
		}
		if s.hooks.BeforeCommit != nil {
			return s.hooks.BeforeCommit(r.ID)
		}
		return nil
	})
	if errors.Is(err, errHandled) {
		if o.setNext {
			return &Result{Status: "handled"}, s.advance(ctx, r, o.advance)
		}
		return &Result{Status: "handled"}, nil
	}
	if err != nil {
		return nil, err
	}
	res.OccurrenceID = occID
	if emit != nil {
		emit()
	}
	if sub != nil && !s.hooks.NoLaunch {
		s.d.Engine.Launch(sub.Job.ID)
	}
	return &res, nil
}

// runWindow starts a recurring window's maintenance (ordinary M12
// maintenance, ending at the window's end).
func (s *Service) runWindow(ctx context.Context, r *row, start, end time.Time, o opts) (*Result, error) {
	if s.d.Alerts == nil {
		return nil, errors.New("maintenance is not available")
	}
	c := r.Config
	if !o.manual {
		if why := s.authorize(ctx, r, nil); why != "" {
			return &Result{Status: OccBlocked}, s.attention(ctx, r, &start, why)
		}
	}
	if m, err := s.d.Alerts.ActiveWindow(ctx, r.ID); err != nil {
		return nil, err
	} else if m != nil {
		return &Result{Status: OccSkipped}, s.record(ctx, r, start, OccSkipped, "the previous window is still active", o)
	}
	occID := ids.New(ids.Occurrence)
	endMs := end.UnixMilli()
	reason := fmt.Sprintf("Recurring window %q", r.Name)
	if c.Reason != "" {
		reason += ": " + c.Reason
	}
	if len([]rune(reason)) > 200 {
		reason = string([]rune(reason)[:199]) + "…"
	}
	by := alerts.Actor{UserID: r.OwnerID, Name: fmt.Sprintf("recurring window %q", r.Name)}
	if o.manual {
		by = alerts.Actor{UserID: o.by.UserID, Name: o.by.Name}
	}
	var emit func()
	m, err := s.d.Alerts.StartMaintenance(ctx, alerts.MaintenanceInput{HostIDs: c.Targets.Hosts, GroupIDs: c.Targets.Groups, Reason: reason,
		Until: &endMs, ScheduleID: r.ID, OccurrenceID: occID}, by, func(tx *sql.Tx, m *alerts.Maintenance) error {
		if err := s.insertOcc(ctx, tx, r, start, occID, OccDispatched, o.note, o); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE schedule_occurrences SET maintenance_id = ?, dispatched_at = ? WHERE id = ?`, m.ID, s.nowMs(), occID); err != nil {
			return err
		}
		if err := s.advanceTx(ctx, tx, r, o); err != nil {
			return err
		}
		actor := &audit.Actor{Type: audit.ActorSystem, ID: r.ID, Name: "schedule " + r.Name}
		if o.manual {
			actor = &audit.Actor{Type: audit.ActorUser, ID: o.by.UserID, Name: o.by.Name}
		}
		var owner string
		tx.QueryRowContext(ctx, `SELECT username FROM users WHERE id = ?`, r.OwnerID).Scan(&owner)
		summary := fmt.Sprintf("system execution via schedule %q, owned by %s", r.Name, owner)
		if o.manual {
			summary = fmt.Sprintf("start now of schedule %q by %s", r.Name, o.by.Name)
		}
		var err error
		emit, err = s.d.Audit.RecordTx(ctx, db.New(tx), audit.Event{Action: "maintenance.start", Actor: actor, TargetType: "maintenance", TargetID: m.ID,
			Details: map[string]any{"summary": summary, "via": "schedule", "schedule_id": r.ID, "schedule_name": r.Name, "owner_id": r.OwnerID, "owner": owner, "occurrence_id": occID,
				"until": end.UTC().Format(time.RFC3339), "manual": o.manual}})
		if err != nil {
			return err
		}
		if s.hooks.BeforeCommit != nil {
			return s.hooks.BeforeCommit(r.ID)
		}
		return nil
	})
	var ve *alerts.ValidationError
	switch {
	case errors.Is(err, errHandled):
		if o.setNext {
			return &Result{Status: "handled"}, s.advance(ctx, r, o.advance)
		}
		return &Result{Status: "handled"}, nil
	case errors.As(err, &ve):
		if err := s.record(ctx, r, start, OccFailed, ve.Error(), o); err != nil {
			return nil, err
		}
		s.d.Store.Writer.ExecContext(ctx, `UPDATE schedules SET attention = ?, attention_at = ? WHERE id = ?`, "The last window could not start: "+ve.Error(), s.nowMs(), r.ID)
		return &Result{Status: OccFailed}, nil
	case err != nil:
		return nil, err
	}
	if emit != nil {
		emit()
	}
	return &Result{OccurrenceID: occID, Status: OccDispatched, JobID: m.ID}, nil
}

// queued starts queued runs whose previous run has finished.
func (s *Service) queued(ctx context.Context) {
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT id, schedule_id, scheduled_for FROM schedule_occurrences WHERE status = 'queued' LIMIT 200`)
	if err != nil {
		return
	}
	type q struct {
		id, schedule string
		at           int64
	}
	var qs []q
	for rows.Next() {
		var x q
		if rows.Scan(&x.id, &x.schedule, &x.at) == nil {
			qs = append(qs, x)
		}
	}
	rows.Close()
	for _, x := range qs {
		r, err := s.load(ctx, s.d.Store.Reader, x.schedule)
		if err != nil {
			continue
		}
		if !r.Enabled || r.Archived {
			s.record(ctx, r, time.UnixMilli(x.at), OccSkipped, "the schedule was disabled before it could run", opts{occID: x.id})
			continue
		}
		if s.activeJob(ctx, r.ID) != "" {
			continue
		}
		if _, err := s.dispatchJob(ctx, r, time.UnixMilli(x.at), opts{occID: x.id, note: "queued behind the previous run; started when it finished"}); err != nil {
			s.log.Warn("scheduler: queued run", "schedule", r.ID, "err", err)
		}
	}
}

// ErrBusy: the previous run is still active.
var ErrBusy = errors.New("the previous run is still active")

// RunNow runs a schedule once now, as the person asking (a deliberate
// action: hosts in maintenance are not skipped). It doesn't change the
// schedule's next run.
func (s *Service) RunNow(ctx context.Context, id string, by Actor) (*Result, error) {
	r, err := s.load(ctx, s.d.Store.Reader, id)
	if err != nil {
		return nil, err
	}
	if r.Archived {
		return nil, invalid("this schedule is archived")
	}
	now := s.now()
	o := opts{manual: true, by: by, note: "run now by " + by.Name}
	if r.Kind == ScheduleMaintenance {
		if m, _ := s.d.Alerts.ActiveWindow(ctx, r.ID); m != nil {
			return nil, ErrBusy
		}
		return s.runWindow(ctx, r, now, now.Add(time.Duration(r.Config.DurationMinutes)*time.Minute), o)
	}
	if s.activeJob(ctx, r.ID) != "" {
		return nil, ErrBusy
	}
	return s.dispatchJob(ctx, r, now, o)
}

// prune bounds occurrence history: older than the retention, and beyond
// the newest 1000 per schedule.
func (s *Service) prune(ctx context.Context, now time.Time) {
	cutoff := now.AddDate(0, 0, -s.cfg.HistoryDays).UnixMilli()
	for {
		res, err := s.d.Store.Writer.ExecContext(ctx, `DELETE FROM schedule_occurrences WHERE id IN (SELECT id FROM schedule_occurrences WHERE created_at < ? AND status != 'queued' LIMIT 500)`, cutoff)
		if err != nil {
			return
		}
		if n, _ := res.RowsAffected(); n < 500 {
			break
		}
	}
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT schedule_id FROM schedule_occurrences GROUP BY schedule_id HAVING count(*) > 1000`)
	if err != nil {
		return
	}
	var over []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			over = append(over, id)
		}
	}
	rows.Close()
	for _, id := range over {
		s.d.Store.Writer.ExecContext(ctx, `DELETE FROM schedule_occurrences WHERE id IN (SELECT id FROM schedule_occurrences WHERE schedule_id = ?
			ORDER BY scheduled_for DESC LIMIT -1 OFFSET 1000)`, id)
	}
}
