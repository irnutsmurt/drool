// Package alerts is DROOL's alert lifecycle, maintenance and notification
// delivery (M12, ARCHITECTURE-ALPHA §28).
//
// M11's health evaluator decides what is wrong (thresholds, durations,
// hysteresis). This package only consumes its issue transitions: an issue
// that becomes active with Warning or Critical severity opens an alert
// occurrence, a severity rise escalates it, and the issue's recovery
// resolves it. Operators acknowledge alerts; they can never resolve one,
// because only the source condition can recover.
//
// Every change is written in the transaction that records the issue
// transition, together with the notifications it needs (the durable
// outbox). Workers deliver them with bounded retries, so a controller
// crash never loses an alert, at the cost of an occasional duplicate
// (delivery is at least once).
package alerts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"drool/internal/audit"
	"drool/internal/ids"
	"drool/internal/monitor/monsvc"
	"drool/internal/notify"
	"drool/internal/secrets"
	"drool/internal/store"
)

// Alert states. Suppression by maintenance is orthogonal.
const (
	StateActive       = "active"
	StateAcknowledged = "acknowledged"
	StateResolved     = "resolved"
)

// Timings and bounds. Tests shorten them through Config.
type Config struct {
	Workers        int           // concurrent deliveries (default 4)
	PerDestination int           // concurrent deliveries per destination (default 2)
	RatePerMinute  int           // deliveries per destination per minute (default 30)
	MaxAttempts    int           // per delivery (default 8)
	BackoffBase    time.Duration // first retry delay (default 30 s), doubling
	BackoffMax     time.Duration // (default 30 min)
	Lease          time.Duration // a claimed delivery returns to the queue after this (default 2 min)
	Grace          time.Duration // after maintenance ends, before notifying still-active alerts (default 2 min)
	Poll           time.Duration // idle workers look for work this often (default 2 s)
	Tick           time.Duration // maintenance expiry, reminders, reconciliation (default 15 s)
	FailingAfter   int           // consecutive failed attempts before a destination counts as failing (default 3)
	MaxReminders   int           // per alert (default 12)
	TimelineMax    int           // entries per alert (default 200)
	DisableWorkers bool          // tests drive delivery with ProcessOutbox
}

func (c *Config) defaults() {
	def := func(v *int, d int) {
		if *v <= 0 {
			*v = d
		}
	}
	defd := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&c.Workers, 4)
	def(&c.PerDestination, 2)
	def(&c.MaxAttempts, 8)
	def(&c.FailingAfter, 3)
	def(&c.MaxReminders, 12)
	def(&c.TimelineMax, 200)
	defd(&c.BackoffBase, 30*time.Second)
	defd(&c.BackoffMax, 30*time.Minute)
	defd(&c.Lease, 2*time.Minute)
	defd(&c.Grace, 2*time.Minute)
	defd(&c.Poll, 2*time.Second)
	defd(&c.Tick, 15*time.Second)
}

// Deps are the service's collaborators.
type Deps struct {
	Store  *store.Store
	Box    *secrets.Box
	Audit  *audit.Writer
	Log    *slog.Logger
	Sender *notify.Sender // nil: a default sender
	Config Config
}

// Service is alerting.
type Service struct {
	d      Deps
	cfg    Config
	log    *slog.Logger
	sender *notify.Sender
	now    func() time.Time

	wake          chan struct{}
	needReconcile atomic.Bool

	mu       sync.Mutex
	inflight map[string]int         // destination → deliveries being sent
	buckets  map[string]*rateBucket // destination → rate limit
	lastTick time.Time
	pruned   time.Time
}

// New creates the service.
func New(d Deps) *Service {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Sender == nil {
		d.Sender = &notify.Sender{}
	}
	cfg := d.Config
	cfg.defaults()
	return &Service{d: d, cfg: cfg, log: d.Log, sender: d.Sender, now: time.Now, wake: make(chan struct{}, 1),
		inflight: map[string]int{}, buckets: map[string]*rateBucket{}}
}

// SetClock replaces the clock (tests).
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// Attach installs the issue hook on system monitoring.
func (s *Service) Attach(m *monsvc.Service) { m.SetIssueHook(s.OnIssue) }

func (s *Service) nowMs() int64 { return s.now().UnixMilli() }

// nudge wakes a worker (after the transaction that queued work commits).
func (s *Service) nudge() {
	time.AfterFunc(50*time.Millisecond, func() {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	})
}

// Run starts the workers and the periodic work until ctx ends.
func (s *Service) Run(ctx context.Context) {
	s.RecoverLeases(ctx, true)
	s.Reconcile(ctx)
	if !s.cfg.DisableWorkers {
		for range s.cfg.Workers {
			go s.worker(ctx)
		}
	}
	t := time.NewTicker(s.cfg.Tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Tick(ctx)
		}
	}
}

// Tick runs the periodic work once: maintenance expiry and suppression,
// notifications due after maintenance, reminders, expired leases,
// reconciliation with M11's issues and (hourly) pruning.
func (s *Service) Tick(ctx context.Context) {
	now := s.now()
	s.expireMaintenance(ctx, now)
	s.syncSuppression(ctx, now)
	s.dueOpenings(ctx, now)
	s.reminders(ctx, now)
	s.RecoverLeases(ctx, false)
	s.mu.Lock()
	reconcile := now.Sub(s.lastTick) >= time.Minute || s.needReconcile.Load()
	if reconcile {
		s.lastTick = now
	}
	prune := now.Sub(s.pruned) >= time.Hour
	if prune {
		s.pruned = now
	}
	s.mu.Unlock()
	if reconcile {
		s.Reconcile(ctx)
	}
	if prune {
		s.Prune(ctx)
	}
}

// q is what both *sql.Tx and *sql.DB offer.
type q interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *Service) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	return s.d.Store.InRawTx(ctx, fn)
}

// --- the issue hook ---

// OnIssue applies an issue transition inside M11's transaction. Alerting
// must never stop monitoring from recording an issue, so a failure here
// rolls back only alerting's part (a savepoint) and the periodic
// reconciliation repairs it.
func (s *Service) OnIssue(ctx context.Context, tx *sql.Tx, t monsvc.IssueTransition) error {
	if _, err := tx.ExecContext(ctx, `SAVEPOINT alerting`); err != nil {
		return err
	}
	queued, err := s.onIssue(ctx, tx, t)
	if err != nil {
		if _, rerr := tx.ExecContext(ctx, `ROLLBACK TO alerting`); rerr != nil {
			return rerr
		}
		s.log.Error("alert transition failed; it will be reconciled", "issue", t.IssueID, "kind", t.Kind, "err", err)
		s.needReconcile.Store(true)
	}
	if _, err := tx.ExecContext(ctx, `RELEASE alerting`); err != nil {
		return err
	}
	if queued {
		s.nudge()
	}
	return nil
}

func actionable(sev string) bool { return sev == "warning" || sev == "critical" }

func sevRank(sev string) int {
	switch sev {
	case "critical":
		return 2
	case "warning":
		return 1
	}
	return 0
}

type issueInfo struct {
	IssueID, HostID, Check, Subject, Severity, Title, Detail string
	Value                                                    *float64
	ObservedAt                                               *int64
	StartedAt                                                int64
}

func (s *Service) onIssue(ctx context.Context, tx *sql.Tx, t monsvc.IssueTransition) (bool, error) {
	info := issueInfo{IssueID: t.IssueID, HostID: t.HostID, Check: t.Check, Subject: t.Subject, Severity: t.Severity, Title: t.Title,
		Detail: t.Detail, Value: t.Value, ObservedAt: t.ObservedAt, StartedAt: t.StartedAt}
	switch t.Kind {
	case "opened":
		if !actionable(t.Severity) {
			return false, nil // informational: visible in monitoring, never an alert
		}
		return s.openAlert(ctx, tx, info, t.At, "")
	case "changed":
		a, err := s.openAlertForIssue(ctx, tx, t.IssueID)
		if errors.Is(err, sql.ErrNoRows) {
			if !actionable(t.Severity) {
				return false, nil
			}
			return s.openAlert(ctx, tx, info, t.At, "")
		}
		if err != nil {
			return false, err
		}
		return s.changeAlert(ctx, tx, a, info, t.At)
	case "resolved":
		a, err := s.openAlertForIssue(ctx, tx, t.IssueID)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return s.resolveAlert(ctx, tx, a, t.At, "", t.Resolution, t.ResolutionNote)
	}
	return false, nil
}

// Alert is an alert occurrence.
type Alert struct {
	ID             string            `json:"id"`
	HostID         string            `json:"host_id"`
	HostName       string            `json:"host_name"`
	IssueID        string            `json:"issue_id"`
	Check          string            `json:"check"`
	Subject        string            `json:"subject"`
	Category       string            `json:"category"`
	Severity       string            `json:"severity"`
	PeakSeverity   string            `json:"peak_severity"`
	State          string            `json:"state"`
	Title          string            `json:"title"`
	Detail         string            `json:"detail"`
	Value          *float64          `json:"value,omitempty"`
	ValueText      string            `json:"value_text,omitempty"`
	ObservedAt     *int64            `json:"observed_at,omitempty"`
	IssueStartedAt int64             `json:"issue_started_at"`
	OpenedAt       int64             `json:"opened_at"`
	Acknowledgment *Acknowledgment   `json:"acknowledgement,omitempty"`
	ResolvedAt     *int64            `json:"resolved_at,omitempty"`
	DurationMs     int64             `json:"duration_ms"`
	Suppressed     bool              `json:"suppressed"`
	SuppressedBy   *MaintenanceRef   `json:"suppressed_by,omitempty"`
	EverSuppressed bool              `json:"ever_suppressed"`
	NotifyAfter    *int64            `json:"notify_after,omitempty"`
	Notification   NotificationState `json:"notification"`
	// Resolution: why a resolved alert closed. "recovered" is the only
	// recovery; disabled, reconfigured and gone are configuration or
	// inventory changes (M12.1).
	Resolution     string `json:"resolution,omitempty"`
	ResolutionNote string `json:"resolution_note,omitempty"`

	suppressedBy  string
	nextReminder  *int64
	remindersSent int
}

// Acknowledgment records who acknowledged an alert.
type Acknowledgment struct {
	By   string `json:"by"`
	At   int64  `json:"at"`
	Note string `json:"note,omitempty"`
}

// MaintenanceRef names the maintenance suppressing an alert.
type MaintenanceRef struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
	EndsAt *int64 `json:"ends_at,omitempty"`
}

// NotificationState summarises an alert's deliveries.
type NotificationState struct {
	Status    string `json:"status"` // none | suppressed | pending | delivered | partial | failing | failed
	Delivered int    `json:"delivered"`
	Pending   int    `json:"pending"`
	Failed    int    `json:"failed"`
	Cancelled int    `json:"cancelled"`
	Retrying  bool   `json:"retrying"`
}

const alertCols = `id, host_id, host_name, issue_id, check_id, subject, category, severity, peak_severity, state, title, detail, value,
	observed_at, issue_started_at, opened_at, acknowledged_at, ack_by_name, ack_note, resolved_at, suppressed, suppressed_by,
	ever_suppressed, notify_after, next_reminder_at, reminders_sent, COALESCE(resolution, ''), resolution_note`

type scanner interface{ Scan(dest ...any) error }

func scanAlert(r scanner, now int64) (*Alert, error) {
	var a Alert
	var value sql.NullFloat64
	var observed, ackAt, resolved, notifyAfter, nextRem sql.NullInt64
	var ackBy, ackNote string
	var supBy sql.NullString
	var sup, ever int
	if err := r.Scan(&a.ID, &a.HostID, &a.HostName, &a.IssueID, &a.Check, &a.Subject, &a.Category, &a.Severity, &a.PeakSeverity,
		&a.State, &a.Title, &a.Detail, &value, &observed, &a.IssueStartedAt, &a.OpenedAt, &ackAt, &ackBy, &ackNote, &resolved,
		&sup, &supBy, &ever, &notifyAfter, &nextRem, &a.remindersSent, &a.Resolution, &a.ResolutionNote); err != nil {
		return nil, err
	}
	if value.Valid {
		a.Value = &value.Float64
		a.ValueText = valueText(a.Check, value.Float64)
	}
	if observed.Valid {
		a.ObservedAt = &observed.Int64
	}
	if ackAt.Valid {
		a.Acknowledgment = &Acknowledgment{By: ackBy, At: ackAt.Int64, Note: ackNote}
	}
	if resolved.Valid {
		a.ResolvedAt = &resolved.Int64
	}
	if notifyAfter.Valid {
		a.NotifyAfter = &notifyAfter.Int64
	}
	if nextRem.Valid {
		a.nextReminder = &nextRem.Int64
	}
	a.Suppressed, a.EverSuppressed, a.suppressedBy = sup == 1, ever == 1, supBy.String
	end := now
	if a.ResolvedAt != nil {
		end = *a.ResolvedAt
	}
	a.DurationMs = max(0, end-a.start())
	return &a, nil
}

// start is when the outage began: when the condition was first observed
// if M11 knows, otherwise when the issue became active.
func (a *Alert) start() int64 {
	if a.ObservedAt != nil {
		return *a.ObservedAt
	}
	return a.IssueStartedAt
}

func (s *Service) loadAlert(ctx context.Context, qq q, where string, args ...any) (*Alert, error) {
	return scanAlert(qq.QueryRowContext(ctx, `SELECT `+alertCols+` FROM alerts WHERE `+where, args...), s.nowMs())
}

func (s *Service) openAlertForIssue(ctx context.Context, qq q, issueID string) (*Alert, error) {
	return s.loadAlert(ctx, qq, `issue_id = ? AND state != 'resolved'`, issueID)
}

// valueText formats an issue's value with its unit.
func valueText(check string, v float64) string {
	switch check {
	case monsvc.CheckCPU, monsvc.CheckMemory, monsvc.CheckFilesystem, monsvc.CheckInodes:
		return fmt.Sprintf("%.0f%%", v)
	case monsvc.CheckTemperature:
		return fmt.Sprintf("%.0f °C", v)
	}
	return ""
}

func sevLabel(sev string) string {
	if sev == "" {
		return ""
	}
	return strings.ToUpper(sev[:1]) + sev[1:]
}

// openAlert creates an occurrence for an active issue.
func (s *Service) openAlert(ctx context.Context, tx *sql.Tx, in issueInfo, at int64, note string) (bool, error) {
	var hostName string
	if err := tx.QueryRowContext(ctx, `SELECT name FROM hosts WHERE id = ?`, in.HostID).Scan(&hostName); err != nil {
		return false, fmt.Errorf("host: %w", err)
	}
	m, err := s.covering(ctx, tx, in.HostID, at)
	if err != nil {
		return false, err
	}
	a := &Alert{ID: ids.New(ids.Alert), HostID: in.HostID, HostName: hostName, IssueID: in.IssueID, Check: in.Check, Subject: in.Subject,
		Category: monsvc.CheckCategory(in.Check), Severity: in.Severity, PeakSeverity: in.Severity, State: StateActive, Title: in.Title,
		Detail: in.Detail, Value: in.Value, ObservedAt: in.ObservedAt, IssueStartedAt: in.StartedAt, OpenedAt: at}
	if m != nil {
		a.Suppressed, a.EverSuppressed, a.suppressedBy = true, true, m.ID
	}
	var supBy any
	if m != nil {
		supBy = m.ID
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO alerts (id, host_id, host_name, issue_id, check_id, subject, category, severity, peak_severity,
		state, title, detail, value, observed_at, issue_started_at, opened_at, suppressed, suppressed_by, ever_suppressed, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.HostID, a.HostName, a.IssueID, a.Check, a.Subject, a.Category, a.Severity, a.PeakSeverity, a.Title, a.Detail, a.Value,
		a.ObservedAt, a.IssueStartedAt, a.OpenedAt, b2i(a.Suppressed), supBy, b2i(a.EverSuppressed), at); err != nil {
		return false, err
	}
	if a.ObservedAt != nil {
		s.timeline(ctx, tx, a.ID, *a.ObservedAt, "observed", "Condition first observed", "")
	}
	issueText := "Monitoring raised the issue"
	if a.ObservedAt != nil {
		issueText = "Condition lasted long enough; health issue active"
	}
	s.timeline(ctx, tx, a.ID, a.IssueStartedAt, "issue", issueText, "")
	open := "Alert opened (" + sevLabel(a.Severity) + ")"
	if note != "" {
		open += " — " + note
	}
	s.timeline(ctx, tx, a.ID, at, "opened", open, "")
	evKind := "alert.opened"
	if a.Severity == "critical" {
		evKind = "alert.critical"
	}
	s.event(ctx, tx, Event{TS: at, Kind: evKind, Severity: a.Severity, HostID: a.HostID, ObjectType: "alert", ObjectID: a.ID,
		Title: sevLabel(a.Severity) + ": " + a.HostName + " — " + a.Title})
	if m != nil {
		s.timeline(ctx, tx, a.ID, at, "suppressed", "Suppressed by maintenance"+reasonSuffix(m.Reason)+": no notification is sent", "")
		return false, nil
	}
	n, err := s.queue(ctx, tx, a, notify.MsgOpening, a.Severity, 0, at)
	return n > 0, err
}

func reasonSuffix(r string) string {
	if r == "" {
		return ""
	}
	return " (" + r + ")"
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// changeAlert follows a severity or title change of the source issue. Only
// a rise above the alert's peak severity notifies (once per level), so a
// value hovering around the critical threshold can't flood anyone.
func (s *Service) changeAlert(ctx context.Context, tx *sql.Tx, a *Alert, in issueInfo, at int64) (bool, error) {
	sev := a.Severity
	if actionable(in.Severity) {
		sev = in.Severity
	}
	escalate := sevRank(sev) > sevRank(a.PeakSeverity)
	peak := a.PeakSeverity
	if escalate {
		peak = sev
	}
	if _, err := tx.ExecContext(ctx, `UPDATE alerts SET severity = ?, peak_severity = ?, title = ?, detail = ?, value = ?, updated_at = ? WHERE id = ?`,
		sev, peak, in.Title, in.Detail, in.Value, at, a.ID); err != nil {
		return false, err
	}
	if !escalate {
		return false, nil
	}
	a.Severity, a.PeakSeverity, a.Title, a.Detail, a.Value = sev, peak, in.Title, in.Detail, in.Value
	s.timeline(ctx, tx, a.ID, at, "escalated", "Escalated to "+sevLabel(sev)+": "+in.Title, "")
	s.event(ctx, tx, Event{TS: at, Kind: "alert.critical", Severity: sev, HostID: a.HostID, ObjectType: "alert", ObjectID: a.ID,
		Title: sevLabel(sev) + " (escalated): " + a.HostName + " — " + in.Title})
	if a.Suppressed {
		return false, nil
	}
	n, err := s.queue(ctx, tx, a, notify.MsgEscalation, sev, 0, at)
	return n > 0, err
}

// Resolutions that aren't recoveries (M12.1; see monsvc.Res*).
func clearedBy(why string) bool { return why == "disabled" || why == "reconfigured" || why == "gone" }

// resolveAlert records the end of the source issue. A recovery goes to
// the destinations that were sent the opening (or escalation) and whose
// routes want recoveries, unless the host is in maintenance. An issue that
// ended because of configuration (its check turned off, its threshold
// changed) or because its subject disappeared closes the alert truthfully:
// the timeline and the closing notification say "cleared", never
// "recovered".
func (s *Service) resolveAlert(ctx context.Context, tx *sql.Tx, a *Alert, at int64, note, why, whyNote string) (bool, error) {
	if why == "" {
		why = "recovered"
	}
	cleared := clearedBy(why)
	if _, err := tx.ExecContext(ctx, `UPDATE alerts SET state = 'resolved', resolved_at = ?, notify_after = NULL, next_reminder_at = NULL, updated_at = ?,
		resolution = ?, resolution_note = ? WHERE id = ? AND state != 'resolved'`, at, at, why, whyNote, a.ID); err != nil {
		return false, err
	}
	a.State, a.ResolvedAt, a.Resolution, a.ResolutionNote = StateResolved, &at, why, whyNote
	dur := time.Duration(max(0, at-a.start())) * time.Millisecond
	msgKind := notify.MsgRecovery
	if cleared {
		msgKind = notify.MsgCleared
		text := "Cleared by configuration"
		if why == "gone" {
			text = "Closed: no longer reported by the host"
		}
		if whyNote != "" {
			text += " — " + whyNote
		}
		s.timeline(ctx, tx, a.ID, at, "cleared", text, "")
		s.timeline(ctx, tx, a.ID, at, "resolved", "Alert closed after "+notify.Duration(dur)+" (not a recovery)", "")
		s.event(ctx, tx, Event{TS: at, Kind: "alert.cleared", Severity: "info", HostID: a.HostID, ObjectType: "alert", ObjectID: a.ID,
			Title: "Cleared (" + why + "): " + a.HostName + " — " + a.Title})
	} else {
		rec := "Condition recovered"
		if note != "" {
			rec += " — " + note
		}
		s.timeline(ctx, tx, a.ID, at, "recovered", rec, "")
		s.timeline(ctx, tx, a.ID, at, "resolved", "Alert resolved after "+notify.Duration(dur), "")
		s.event(ctx, tx, Event{TS: at, Kind: "alert.recovered", Severity: "info", HostID: a.HostID, ObjectType: "alert", ObjectID: a.ID,
			Title: "Recovered: " + a.HostName + " — " + a.Title + " (" + notify.Duration(dur) + ")"})
	}
	// Reminders and escalations that haven't gone out are moot now.
	if _, err := tx.ExecContext(ctx, `UPDATE notify_deliveries SET status = 'cancelled', finished_at = ?, last_error = 'the alert closed first'
		WHERE alert_id = ? AND status = 'queued' AND kind = 'reminder'`, at, a.ID); err != nil {
		return false, err
	}
	m, err := s.covering(ctx, tx, a.HostID, at)
	if err != nil {
		return false, err
	}
	if m != nil {
		res, err := tx.ExecContext(ctx, `UPDATE notify_deliveries SET status = 'cancelled', finished_at = ?, last_error = 'suppressed by maintenance'
			WHERE alert_id = ? AND status = 'queued'`, at, a.ID)
		if err != nil {
			return false, err
		}
		var had int
		tx.QueryRowContext(ctx, `SELECT count(*) FROM notify_deliveries WHERE alert_id = ? AND status = 'delivered'`, a.ID).Scan(&had)
		if n, _ := res.RowsAffected(); n > 0 || had > 0 {
			s.timeline(ctx, tx, a.ID, at, "suppressed", "Closing notification suppressed by maintenance"+reasonSuffix(m.Reason), "")
		}
		return false, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT d.destination_id, d.destination_name, d.destination_kind FROM notify_deliveries d
		WHERE d.alert_id = ? AND d.kind IN ('opening','escalation') AND d.status NOT IN ('cancelled','failed') AND d.recovery_wanted = 1 AND d.destination_id IS NOT NULL`, a.ID)
	if err != nil {
		return false, err
	}
	var targets []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.ID, &t.Name, &t.Kind); err != nil {
			rows.Close()
			return false, err
		}
		t.Recovery = true
		targets = append(targets, t)
	}
	rows.Close()
	msg := s.message(ctx, tx, a, msgKind, 0)
	for _, t := range targets {
		if err := s.insertDelivery(ctx, tx, a.ID, t, notify.MsgRecovery, 0, msg, at); err != nil {
			return false, err
		}
	}
	return len(targets) > 0, nil
}

// --- timeline and events ---

func (s *Service) timeline(ctx context.Context, qq q, alertID string, ts int64, kind, text, actor string) {
	var n int
	qq.QueryRowContext(ctx, `SELECT count(*) FROM alert_events WHERE alert_id = ?`, alertID).Scan(&n)
	if n >= s.cfg.TimelineMax && kind != "resolved" && kind != "recovered" && kind != "cleared" {
		return // bounded; the lifecycle's end is always kept
	}
	if len(text) > 500 {
		text = text[:500] + "…"
	}
	qq.ExecContext(ctx, `INSERT INTO alert_events (alert_id, ts, kind, text, actor) VALUES (?, ?, ?, ?, ?)`, alertID, ts, kind, text, actor)
}

// Event is one entry of the controller's event timeline.
type Event struct {
	ID         int64  `json:"id"`
	TS         int64  `json:"ts"`
	Kind       string `json:"kind"`
	Severity   string `json:"severity"`
	HostID     string `json:"host_id,omitempty"`
	ObjectType string `json:"object_type,omitempty"`
	ObjectID   string `json:"object_id,omitempty"`
	Title      string `json:"title"`
}

func (s *Service) event(ctx context.Context, qq q, e Event) {
	var host any
	if e.HostID != "" {
		host = e.HostID
	}
	if e.Severity == "" {
		e.Severity = "info"
	}
	if len(e.Title) > 300 {
		e.Title = e.Title[:300] + "…"
	}
	if _, err := qq.ExecContext(ctx, `INSERT INTO events (ts, kind, severity, host_id, object_type, object_id, title) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.TS, e.Kind, e.Severity, host, e.ObjectType, e.ObjectID, e.Title); err != nil {
		s.log.Warn("record event", "kind", e.Kind, "err", err)
	}
}

// --- acknowledgement ---

// Errors.
var (
	ErrNotFound        = errors.New("not found")
	ErrNotActive       = errors.New("only an active alert can be acknowledged")
	ErrAlreadyAcked    = errors.New("the alert is already acknowledged")
	ErrNoteTooLong     = errors.New("the note must be at most 500 characters")
	ErrDefaultRoute    = errors.New("the default route can't be deleted; disable it or change its destinations instead")
	ErrNameTaken       = errors.New("a destination with this name already exists")
	ErrSecretsMismatch = errors.New("stored destination secrets can't be decrypted with this master key")
)

// Actor is who performs an action.
type Actor struct {
	UserID, Name string
}

// Acknowledge marks an active alert as acknowledged. It stops reminders
// and changes nothing about the source issue: the alert stays open until
// the condition recovers.
func (s *Service) Acknowledge(ctx context.Context, id string, by Actor, note string, onTx func(tx *sql.Tx) error) (*Alert, error) {
	note = strings.TrimSpace(note)
	if len([]rune(note)) > 500 {
		return nil, ErrNoteTooLong
	}
	now := s.nowMs()
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		a, err := s.loadAlert(ctx, tx, `id = ?`, id)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		switch a.State {
		case StateAcknowledged:
			return ErrAlreadyAcked
		case StateResolved:
			return ErrNotActive
		}
		var uid any
		if by.UserID != "" {
			uid = by.UserID
		}
		if _, err := tx.ExecContext(ctx, `UPDATE alerts SET state = 'acknowledged', acknowledged_at = ?, ack_by = ?, ack_by_name = ?, ack_note = ?,
			next_reminder_at = NULL, updated_at = ? WHERE id = ?`, now, uid, by.Name, note, now, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE notify_deliveries SET status = 'cancelled', finished_at = ?, last_error = 'acknowledged'
			WHERE alert_id = ? AND status = 'queued' AND kind = 'reminder'`, now, id); err != nil {
			return err
		}
		text := "Acknowledged by " + by.Name
		if note != "" {
			text += ": " + note
		}
		s.timeline(ctx, tx, id, now, "acknowledged", text, by.Name)
		s.event(ctx, tx, Event{TS: now, Kind: "alert.acknowledged", HostID: a.HostID, ObjectType: "alert", ObjectID: id,
			Title: "Acknowledged by " + by.Name + ": " + a.HostName + " — " + a.Title})
		if onTx != nil {
			return onTx(tx)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// Get loads one alert with its notification summary.
func (s *Service) Get(ctx context.Context, id string) (*Alert, error) {
	a, err := s.loadAlert(ctx, s.d.Store.Reader, `id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := s.decorate(ctx, []*Alert{a}); err != nil {
		return nil, err
	}
	return a, nil
}

// --- notification content ---

func (s *Service) message(ctx context.Context, qq q, a *Alert, kind string, reminder int) notify.Message {
	set := s.settings(ctx, qq)
	m := notify.Message{Kind: kind, EventID: a.ID + ":" + kind, AlertID: a.ID, Severity: a.Severity, HostID: a.HostID, Host: a.HostName,
		Title: a.Title, Detail: a.Detail, Check: a.Check, Category: a.Category, Subject: a.Subject, Value: a.Value,
		OpenedAt: time.UnixMilli(a.OpenedAt).UTC(), Reminder: reminder, Fingerprint: a.HostID + "/" + a.Check + "/" + a.Subject}
	if kind == notify.MsgReminder {
		m.EventID = fmt.Sprintf("%s:reminder:%d", a.ID, reminder)
	}
	if kind == notify.MsgEscalation {
		m.EventID = a.ID + ":escalation:" + a.Severity
	}
	if a.Value != nil {
		m.ValueText = valueText(a.Check, *a.Value)
	}
	if a.ObservedAt != nil {
		t := time.UnixMilli(*a.ObservedAt).UTC()
		m.ObservedAt = &t
	} else {
		t := time.UnixMilli(a.IssueStartedAt).UTC()
		m.ObservedAt = &t
	}
	if a.ResolvedAt != nil {
		t := time.UnixMilli(*a.ResolvedAt).UTC()
		m.ResolvedAt = &t
		d := max(0, *a.ResolvedAt-a.start()) / 1000
		m.DurationS = &d
	}
	if kind == notify.MsgCleared {
		m.EventID = a.ID + ":" + notify.MsgRecovery // one closing event per alert, whatever its reason
		m.Resolution, m.ResolutionNote = a.Resolution, a.ResolutionNote
	}
	if a.Acknowledgment != nil {
		m.AckBy, m.AckNote = a.Acknowledgment.By, a.Acknowledgment.Note
		t := time.UnixMilli(a.Acknowledgment.At).UTC()
		m.AckAt = &t
	}
	if set.LinkBaseURL != "" {
		m.URL = strings.TrimRight(set.LinkBaseURL, "/") + "/alerts/" + a.ID
	}
	m.TimeZone = set.TimeZone
	return m
}

func marshal(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
