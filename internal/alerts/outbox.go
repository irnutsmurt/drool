package alerts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"drool/internal/notify"
)

// The outbox. Rows are claimed atomically (the database has one writer)
// with a lease and a fencing number (attempts): a worker's result is
// recorded only if the row is still the one it claimed. A crash at any
// point leaves the row queued or leased; leased rows return to the queue
// when their lease expires (at once on start), with the interrupted
// attempt recorded and the delivery marked ambiguous, since the receiver
// may have got it.

type claimed struct {
	ID, AlertID, DestID, DestName, DestKind, Kind string
	Message                                       notify.Message
	Attempt                                       int
}

// rateBucket limits deliveries per destination per minute (the rest wait
// in the queue rather than failing): Discord and Slack throttle incoming
// webhooks to about one message every two seconds per channel, Telegram
// bots to 20 messages a minute in a group.
type rateBucket struct {
	tokens float64
	at     time.Time
	rate   float64 // per minute
}

var kindRate = map[string]float64{notify.KindDiscord: 30, notify.KindSlack: 30, notify.KindTelegram: 20, notify.KindEmail: 60, notify.KindWebhook: 120}

func (s *Service) rateFor(kind string) float64 {
	if s.cfg.RatePerMinute > 0 {
		return float64(s.cfg.RatePerMinute)
	}
	if r, ok := kindRate[kind]; ok {
		return r
	}
	return 30
}

// level is the bucket's tokens now (burst: half a minute's worth).
func (b *rateBucket) level(now time.Time) float64 {
	return min(b.rate/2+1, b.tokens+now.Sub(b.at).Seconds()*b.rate/60)
}

func (s *Service) busy() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	out := []string{} // "[]", never "null": NOT IN over a NULL matches nothing
	for id, n := range s.inflight {
		if n >= s.cfg.PerDestination {
			out = append(out, id)
		}
	}
	for id, b := range s.buckets {
		if b.level(now) < 1 {
			out = append(out, id)
		}
	}
	return out
}

func (s *Service) take(dest, kind string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	b := s.buckets[dest]
	if b == nil {
		r := s.rateFor(kind)
		b = &rateBucket{tokens: r/2 + 1, at: now, rate: r}
		s.buckets[dest] = b
	}
	b.tokens = b.level(now) - 1
	b.at = now
	s.inflight[dest]++
}

func (s *Service) release(dest string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inflight[dest]--; s.inflight[dest] <= 0 {
		delete(s.inflight, dest)
	}
}

// claim takes the next due delivery: per alert and destination in queue
// order, skipping destinations at their concurrency or rate limit.
func (s *Service) claim(ctx context.Context) (*claimed, error) {
	now := s.nowMs()
	busy := marshal(s.busy())
	var c claimed
	var msg string
	var dest sql.NullString
	err := s.d.Store.Writer.QueryRowContext(ctx, `UPDATE notify_deliveries SET status = 'sending', attempts = attempts + 1, lease_until = ?
		WHERE id = (SELECT d.id FROM notify_deliveries d WHERE d.status = 'queued' AND d.next_attempt_at <= ?
			AND (d.destination_id IS NULL OR d.destination_id NOT IN (SELECT value FROM json_each(?)))
			AND NOT EXISTS (SELECT 1 FROM notify_deliveries p WHERE p.alert_id = d.alert_id AND p.destination_id = d.destination_id
				AND p.rowid < d.rowid AND p.status IN ('queued','sending'))
			ORDER BY d.next_attempt_at, d.rowid LIMIT 1)
		RETURNING id, COALESCE(alert_id, ''), destination_id, destination_name, destination_kind, kind, message_json, attempts`,
		now+s.cfg.Lease.Milliseconds(), now, busy).Scan(&c.ID, &c.AlertID, &dest, &c.DestName, &c.DestKind, &c.Kind, &msg, &c.Attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.DestID = dest.String
	json.Unmarshal([]byte(msg), &c.Message)
	return &c, nil
}

func (s *Service) worker(ctx context.Context) {
	for ctx.Err() == nil {
		did, err := s.processOne(ctx)
		if err != nil && ctx.Err() == nil {
			s.log.Warn("notification outbox", "err", err)
		}
		if did {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-time.After(s.cfg.Poll):
		}
	}
}

// ProcessOutbox delivers everything due now, n deliveries at a time, and
// returns how many it processed (tests; workers do this continuously).
func (s *Service) ProcessOutbox(ctx context.Context, n int) int {
	total := 0
	for {
		var wg sync.WaitGroup
		var mu sync.Mutex
		round := 0
		for range max(n, 1) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if did, _ := s.processOne(ctx); did {
					mu.Lock()
					round++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		total += round
		if round == 0 {
			return total
		}
	}
}

// processOne claims and delivers one notification.
func (s *Service) processOne(ctx context.Context) (bool, error) {
	c, err := s.claim(ctx)
	if err != nil || c == nil {
		return false, err
	}
	if c.DestID != "" {
		s.take(c.DestID, c.DestKind)
		defer s.release(c.DestID)
	}
	s.deliver(ctx, c)
	return true, nil
}

// skip ends a claimed delivery without sending it.
func (s *Service) skip(ctx context.Context, c *claimed, status, reason string) {
	now := s.nowMs()
	s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE notify_deliveries SET status = ?, attempts = attempts - 1, finished_at = ?, lease_until = NULL, last_error = ?,
			message_json = '{}' WHERE id = ? AND status = 'sending' AND attempts = ?`, status, now, reason, c.ID, c.Attempt)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 || c.AlertID == "" {
			return nil
		}
		if c.Kind == notify.MsgOpening || c.Kind == notify.MsgEscalation || c.Kind == notify.MsgRecovery {
			s.timeline(ctx, tx, c.AlertID, now, "notify", fmt.Sprintf("%s: %s notification not sent — %s", c.DestName, c.Kind, reason), "")
		}
		return nil
	})
}

type destRow struct {
	enabled bool
	config  notify.Config
	secrets notify.Secrets
}

func (s *Service) loadDest(ctx context.Context, id string) (*destRow, error) {
	var en int
	var cfg string
	var ct, nonce []byte
	var ver sql.NullInt64
	err := s.d.Store.Reader.QueryRowContext(ctx, `SELECT enabled, config_json, secret_ciphertext, secret_nonce, secret_key_version FROM notify_destinations WHERE id = ?`,
		id).Scan(&en, &cfg, &ct, &nonce, &ver)
	if err != nil {
		return nil, err
	}
	d := &destRow{enabled: en == 1}
	json.Unmarshal([]byte(cfg), &d.config)
	if len(ct) > 0 {
		var kind string
		s.d.Store.Reader.QueryRowContext(ctx, `SELECT kind FROM notify_destinations WHERE id = ?`, id).Scan(&kind)
		pt, err := s.d.Box.Open(id, secretAAD(kind), ct, nonce, int(ver.Int64))
		if err != nil {
			return nil, ErrSecretsMismatch
		}
		if d.secrets, err = notify.UnmarshalSecrets(pt); err != nil {
			return nil, err
		}
	}
	return d, nil
}

func (s *Service) deliver(ctx context.Context, c *claimed) {
	if c.DestID == "" {
		s.skip(ctx, c, "cancelled", "the destination was deleted")
		return
	}
	d, err := s.loadDest(ctx, c.DestID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		s.skip(ctx, c, "cancelled", "the destination was deleted")
		return
	case errors.Is(err, ErrSecretsMismatch):
		s.record(ctx, c, notify.Result{Outcome: notify.OutcomePermanent, Category: notify.CategoryConfig,
			Message: "the destination's secrets can't be decrypted (master key changed); enter them again"}, s.nowMs())
		return
	case err != nil:
		s.log.Warn("load destination", "id", c.DestID, "err", err)
		s.requeue(ctx, c, time.Minute)
		return
	case !d.enabled:
		s.skip(ctx, c, "cancelled", "the destination is disabled")
		return
	}
	if c.AlertID != "" {
		if reason := s.stillWanted(ctx, c); reason != "" {
			s.skip(ctx, c, "cancelled", reason)
			return
		}
	}
	start := s.nowMs()
	s.d.Store.Writer.ExecContext(ctx, `INSERT OR REPLACE INTO notify_attempts (delivery_id, n, started_at, outcome) VALUES (?, ?, ?, 'interrupted')`,
		c.ID, c.Attempt, start)
	r := s.sender.Send(ctx, c.DestKind, d.config, d.secrets, c.Message, c.ID)
	if ctx.Err() != nil {
		return // shutting down: the lease expires and the attempt counts as interrupted
	}
	s.record(ctx, c, r, start)
}

// stillWanted re-checks the alert right before sending: suppression may
// have started, the alert may have been acknowledged (reminders) or the
// opening may have failed (recoveries).
func (s *Service) stillWanted(ctx context.Context, c *claimed) string {
	a, err := s.loadAlert(ctx, s.d.Store.Reader, `id = ?`, c.AlertID)
	if err != nil {
		return "the alert no longer exists"
	}
	if m, _ := s.covering(ctx, s.d.Store.Reader, a.HostID, s.nowMs()); m != nil {
		return "suppressed by maintenance"
	}
	switch c.Kind {
	case notify.MsgReminder:
		if a.State != StateActive {
			return "the alert is no longer unacknowledged and active"
		}
	case notify.MsgRecovery:
		var ok int
		s.d.Store.Reader.QueryRowContext(ctx, `SELECT count(*) FROM notify_deliveries WHERE alert_id = ? AND destination_id = ?
			AND kind IN ('opening','escalation') AND status = 'delivered'`, c.AlertID, c.DestID).Scan(&ok)
		if ok == 0 {
			return "the opening notification was never delivered"
		}
	}
	return ""
}

func (s *Service) requeue(ctx context.Context, c *claimed, after time.Duration) {
	s.d.Store.Writer.ExecContext(ctx, `UPDATE notify_deliveries SET status = 'queued', attempts = attempts - 1, lease_until = NULL, next_attempt_at = ?
		WHERE id = ? AND status = 'sending' AND attempts = ?`, s.nowMs()+after.Milliseconds(), c.ID, c.Attempt)
}

// Backoff is the delay before retry n (1-based): exponential from base,
// capped, with ±20 % jitter so a burst of failures doesn't retry in step.
func Backoff(n int, base, maxDelay time.Duration) time.Duration {
	d := base
	for i := 1; i < n && d < maxDelay; i++ {
		d *= 2
	}
	d = min(d, maxDelay)
	return time.Duration(float64(d) * (0.8 + 0.4*rand.Float64()))
}

// record stores an attempt's outcome (only if the row is still ours).
func (s *Service) record(ctx context.Context, c *claimed, r notify.Result, start int64) {
	now := s.nowMs()
	status, next := "", any(nil)
	switch {
	case r.OK():
		status = "delivered"
	case r.Outcome == notify.OutcomeTransient && c.Attempt < s.cfg.MaxAttempts:
		status = "queued"
		wait := max(Backoff(c.Attempt, s.cfg.BackoffBase, s.cfg.BackoffMax), r.RetryAfter)
		next = now + wait.Milliseconds()
	default:
		status = "failed"
	}
	var code any
	if r.StatusCode != 0 {
		code = r.StatusCode
	}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var res sql.Result
		var err error
		if status == "queued" {
			res, err = tx.ExecContext(ctx, `UPDATE notify_deliveries SET status = 'queued', next_attempt_at = ?, lease_until = NULL, last_error = ?,
				last_category = ?, last_status_code = ?, ambiguous = MAX(ambiguous, ?) WHERE id = ? AND status = 'sending' AND attempts = ?`,
				next, r.Message, r.Category, code, b2i(r.Ambiguous), c.ID, c.Attempt)
		} else {
			// A finished delivery doesn't need its message any more (the
			// alert has the content); dropping it keeps history small.
			res, err = tx.ExecContext(ctx, `UPDATE notify_deliveries SET status = ?, finished_at = ?, lease_until = NULL, last_error = ?,
				last_category = ?, last_status_code = ?, ambiguous = MAX(ambiguous, ?), message_json = '{}' WHERE id = ? AND status = 'sending' AND attempts = ?`,
				status, now, map[bool]string{true: "", false: r.Message}[r.OK()], r.Category, code, b2i(r.Ambiguous), c.ID, c.Attempt)
		}
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil // the lease expired and someone else has it: their result counts
		}
		outcome := r.Outcome
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO notify_attempts (delivery_id, n, started_at, finished_at, outcome, category, status_code, error)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, c.ID, c.Attempt, start, now, outcome, r.Category, code, map[bool]string{true: "", false: r.Message}[r.OK()]); err != nil {
			return err
		}
		if err := s.destinationHealth(ctx, tx, c, r, now); err != nil {
			return err
		}
		if c.AlertID == "" || status == "queued" {
			return nil
		}
		what := map[string]string{notify.MsgOpening: "notification", notify.MsgEscalation: "escalation", notify.MsgReminder: "reminder",
			notify.MsgRecovery: "closing notification"}[c.Kind]
		if status == "delivered" {
			if c.Kind != notify.MsgReminder || c.Attempt > 1 {
				text := c.DestName + ": " + what + " delivered"
				if c.Attempt > 1 {
					text += fmt.Sprintf(" (attempt %d)", c.Attempt)
				}
				s.timeline(ctx, tx, c.AlertID, now, "delivered", text, "")
			}
			return nil
		}
		s.timeline(ctx, tx, c.AlertID, now, "delivery_failed", fmt.Sprintf("%s: %s failed after %d attempt(s) — %s", c.DestName, what, c.Attempt, r.Message), "")
		if c.Kind == notify.MsgOpening || c.Kind == notify.MsgEscalation {
			// A recovery for an opening nobody received would only confuse.
			_, err := tx.ExecContext(ctx, `UPDATE notify_deliveries SET status = 'cancelled', finished_at = ?, last_error = 'the opening notification was never delivered'
				WHERE alert_id = ? AND destination_id = ? AND kind = 'recovery' AND status = 'queued'
				AND NOT EXISTS (SELECT 1 FROM notify_deliveries o WHERE o.alert_id = ? AND o.destination_id = ? AND o.kind IN ('opening','escalation') AND o.status IN ('delivered','queued','sending'))`,
				now, c.AlertID, c.DestID, c.AlertID, c.DestID)
			return err
		}
		return nil
	})
	if err != nil {
		s.log.Warn("record delivery", "id", c.ID, "err", err)
	}
}

// destinationHealth keeps the destination's summary. Repeated failure
// raises one "delivery failing" event (and one when it works again), not
// one per attempt.
func (s *Service) destinationHealth(ctx context.Context, tx *sql.Tx, c *claimed, r notify.Result, now int64) error {
	if c.DestID == "" {
		return nil
	}
	var failing sql.NullInt64
	var fails int
	if err := tx.QueryRowContext(ctx, `SELECT failing_since, consecutive_failures FROM notify_destinations WHERE id = ?`, c.DestID).Scan(&failing, &fails); err != nil {
		return nil // deleted meanwhile
	}
	if r.OK() {
		if _, err := tx.ExecContext(ctx, `UPDATE notify_destinations SET last_success_at = ?, consecutive_failures = 0, failing_since = NULL WHERE id = ?`, now, c.DestID); err != nil {
			return err
		}
		if failing.Valid {
			s.event(ctx, tx, Event{TS: now, Kind: "notifications.recovered", ObjectType: "destination", ObjectID: c.DestID,
				Title: "Notifications to " + c.DestName + " are being delivered again"})
		}
		return nil
	}
	fails++
	var since any
	if failing.Valid {
		since = failing.Int64
	} else if fails >= s.cfg.FailingAfter || r.Outcome == notify.OutcomePermanent {
		since = now
		s.event(ctx, tx, Event{TS: now, Kind: "notifications.failing", Severity: "warning", ObjectType: "destination", ObjectID: c.DestID,
			Title: "Notifications to " + c.DestName + " are failing: " + r.Message})
	}
	_, err := tx.ExecContext(ctx, `UPDATE notify_destinations SET last_failure_at = ?, last_error = ?, consecutive_failures = ?, failing_since = ? WHERE id = ?`,
		now, r.Message, fails, since, c.DestID)
	return err
}

// RecoverLeases returns deliveries whose worker is gone to the queue. At
// start every leased row is orphaned (one controller owns the outbox);
// later only expired leases are.
func (s *Service) RecoverLeases(ctx context.Context, all bool) {
	now := s.nowMs()
	cond := `status = 'sending' AND lease_until < ?`
	args := []any{now}
	if all {
		cond, args = `status = 'sending'`, nil
	}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id, attempts FROM notify_deliveries WHERE `+cond, args...)
		if err != nil {
			return err
		}
		type lost struct {
			id string
			n  int
		}
		var ls []lost
		for rows.Next() {
			var l lost
			if err := rows.Scan(&l.id, &l.n); err != nil {
				rows.Close()
				return err
			}
			ls = append(ls, l)
		}
		rows.Close()
		for _, l := range ls {
			if _, err := tx.ExecContext(ctx, `UPDATE notify_attempts SET finished_at = ?, outcome = 'interrupted',
				error = 'the controller stopped during this attempt; the receiver may or may not have it' WHERE delivery_id = ? AND n = ? AND finished_at IS NULL`,
				now, l.id, l.n); err != nil {
				return err
			}
			// An attempt that never started (no attempt row) wasn't sent.
			var started int
			tx.QueryRowContext(ctx, `SELECT count(*) FROM notify_attempts WHERE delivery_id = ? AND n = ?`, l.id, l.n).Scan(&started)
			if _, err := tx.ExecContext(ctx, `UPDATE notify_deliveries SET status = CASE WHEN attempts >= ? THEN 'failed' ELSE 'queued' END,
				finished_at = CASE WHEN attempts >= ? THEN ? END, lease_until = NULL, next_attempt_at = ?, ambiguous = MAX(ambiguous, ?),
				last_error = 'interrupted: the controller stopped during delivery', last_category = 'interrupted' WHERE id = ? AND status = 'sending'`,
				s.cfg.MaxAttempts, s.cfg.MaxAttempts, now, now, b2i(started > 0), l.id); err != nil {
				return err
			}
		}
		if len(ls) > 0 {
			s.log.Info("notifications returned to the queue after an interrupted delivery", "count", len(ls))
		}
		return nil
	})
	if err != nil {
		s.log.Warn("recover notification leases", "err", err)
	}
}

// --- reminders ---

// reminders re-notifies unacknowledged active alerts on routes that ask
// for it (off by default), at most MaxReminders times.
func (s *Service) reminders(ctx context.Context, now time.Time) {
	at := now.UnixMilli()
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT id FROM alerts WHERE next_reminder_at IS NOT NULL AND next_reminder_at <= ? AND state = 'active' LIMIT 500`, at)
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
			if a.State != StateActive || a.Suppressed || a.remindersSent >= s.cfg.MaxReminders {
				_, err := tx.ExecContext(ctx, `UPDATE alerts SET next_reminder_at = NULL WHERE id = ?`, id)
				return err
			}
			n := a.remindersSent + 1
			if _, err := tx.ExecContext(ctx, `UPDATE alerts SET reminders_sent = ? WHERE id = ?`, n, id); err != nil {
				return err
			}
			c, err := s.queue(ctx, tx, a, notify.MsgReminder, a.Severity, n, at)
			if err == nil && c > 0 {
				queued = true
				s.timeline(ctx, tx, id, at, "reminder", fmt.Sprintf("Reminder %d sent (still active and unacknowledged)", n), "")
			}
			if c == 0 {
				_, err = tx.ExecContext(ctx, `UPDATE alerts SET next_reminder_at = NULL WHERE id = ?`, id)
			}
			return err
		})
		if err != nil {
			s.log.Warn("alert reminder", "alert", id, "err", err)
		}
	}
	if queued {
		s.nudge()
	}
}

// --- reconciliation ---

// Reconcile brings alerts in line with M11's issues: an active actionable
// issue without an open alert gets one (alerting started after the issue,
// or a hook failure), and an open alert whose issue isn't active any more
// resolves. Normally there is nothing to do.
func (s *Service) Reconcile(ctx context.Context) {
	s.needReconcile.Store(false)
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT i.id, i.host_id, i.check_id, i.subject, i.severity, i.title, i.detail, i.value, i.observed_at, i.started_at
		FROM mon_issues i WHERE i.state = 'active' AND i.severity IN ('warning','critical')
		AND NOT EXISTS (SELECT 1 FROM alerts a WHERE a.issue_id = i.id AND a.state != 'resolved') LIMIT 1000`)
	if err != nil {
		s.log.Warn("reconcile alerts", "err", err)
		return
	}
	var missing []issueInfo
	for rows.Next() {
		var in issueInfo
		var v sql.NullFloat64
		var obs sql.NullInt64
		if rows.Scan(&in.IssueID, &in.HostID, &in.Check, &in.Subject, &in.Severity, &in.Title, &in.Detail, &v, &obs, &in.StartedAt) != nil {
			continue
		}
		if v.Valid {
			in.Value = &v.Float64
		}
		if obs.Valid {
			in.ObservedAt = &obs.Int64
		}
		missing = append(missing, in)
	}
	rows.Close()
	queued := false
	for _, in := range missing {
		err := s.inTx(ctx, func(tx *sql.Tx) error {
			var one int
			if tx.QueryRowContext(ctx, `SELECT 1 FROM mon_issues WHERE id = ? AND state = 'active'`, in.IssueID).Scan(&one) != nil {
				return nil // resolved meanwhile
			}
			if tx.QueryRowContext(ctx, `SELECT 1 FROM alerts WHERE issue_id = ? AND state != 'resolved'`, in.IssueID).Scan(&one) == nil {
				return nil
			}
			q, err := s.openAlert(ctx, tx, in, s.nowMs(), "the health issue was already active")
			queued = queued || q
			return err
		})
		if err != nil {
			s.log.Warn("reconcile alert", "issue", in.IssueID, "err", err)
		}
	}
	rows, err = s.d.Store.Reader.QueryContext(ctx, `SELECT a.id, COALESCE((SELECT i.resolved_at FROM mon_issues i WHERE i.id = a.issue_id), 0),
		COALESCE((SELECT i.resolution FROM mon_issues i WHERE i.id = a.issue_id), ''), COALESCE((SELECT i.resolution_note FROM mon_issues i WHERE i.id = a.issue_id), '')
		FROM alerts a WHERE a.state IN ('active','acknowledged') AND NOT EXISTS (SELECT 1 FROM mon_issues i WHERE i.id = a.issue_id AND i.state = 'active') LIMIT 1000`)
	if err != nil {
		return
	}
	type stale struct {
		id      string
		at      int64
		why, wn string
	}
	var ss []stale
	for rows.Next() {
		var st stale
		if rows.Scan(&st.id, &st.at, &st.why, &st.wn) == nil {
			ss = append(ss, st)
		}
	}
	rows.Close()
	for _, st := range ss {
		err := s.inTx(ctx, func(tx *sql.Tx) error {
			a, err := s.loadAlert(ctx, tx, `id = ? AND state != 'resolved'`, st.id)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			at := st.at
			if at == 0 {
				at = s.nowMs()
			}
			q, err := s.resolveAlert(ctx, tx, a, at, "the health issue is no longer active", st.why, st.wn)
			queued = queued || q
			return err
		})
		if err != nil {
			s.log.Warn("reconcile alert", "alert", st.id, "err", err)
		}
	}
	if queued {
		s.nudge()
	}
}

// --- retention ---

// PruneResult reports one prune.
type PruneResult struct {
	Alerts, Deliveries, Events, Maintenance int64
	Millis                                  int64
}

// Prune deletes resolved alerts, finished deliveries, events and ended
// maintenance older than their retention.
func (s *Service) Prune(ctx context.Context) PruneResult {
	start := time.Now()
	set := s.settings(ctx, s.d.Store.Reader)
	now := s.now()
	day := 24 * time.Hour
	alertCut := now.Add(-time.Duration(set.AlertRetentionDays) * day).UnixMilli()
	delCut := now.Add(-time.Duration(set.DeliveryRetentionDays) * day).UnixMilli()
	var r PruneResult
	// In batches: a large prune (after shortening retention) must never
	// hold the database's single writer for long.
	exec := func(table, cond string, arg int64) int64 {
		var total int64
		for ctx.Err() == nil {
			res, err := s.d.Store.Writer.ExecContext(ctx, `DELETE FROM `+table+` WHERE rowid IN (SELECT rowid FROM `+table+` WHERE `+cond+` LIMIT 500)`, arg)
			if err != nil {
				s.log.Warn("prune alerts", "table", table, "err", err)
				break
			}
			n, _ := res.RowsAffected()
			total += n
			if n < 500 {
				break
			}
		}
		return total
	}
	r.Deliveries = exec("notify_deliveries", `status IN ('delivered','failed','cancelled') AND finished_at < ?`, delCut)
	r.Alerts = exec("alerts", `state = 'resolved' AND resolved_at < ?`, alertCut)
	r.Events = exec("events", `ts < ?`, alertCut)
	r.Maintenance = exec("maintenance", `ended_at IS NOT NULL AND ended_at < ?`, alertCut)
	r.Millis = time.Since(start).Milliseconds()
	if r.Alerts+r.Deliveries+r.Events+r.Maintenance > 0 {
		s.log.Info("alert history pruned", "alerts", r.Alerts, "deliveries", r.Deliveries, "events", r.Events, "maintenance", r.Maintenance, "ms", r.Millis)
	}
	return r
}
