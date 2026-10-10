package backup

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"drool/internal/ids"
)

// What a restore sets aside before the restored controller starts (DESIGN
// §10.3, ARCHITECTURE-ALPHA §33.1). The backup describes the world as it
// was when it was taken; the world moved on. So the restored controller
// gets a new epoch (nothing of the old one can change a host until that
// host was compared and moved), and nothing the backup records as in
// progress is resumed or repeated: it is closed as unknown, or held until
// an administrator resumes it.

// RestoreSummary is what the restore set aside, kept with the restore.
type RestoreSummary struct {
	RestoreID         string   `json:"restore_id"`
	PreviousEpoch     string   `json:"previous_epoch"`
	Epoch             string   `json:"epoch"`
	JobsInterrupted   int64    `json:"jobs_interrupted"`
	HostsInterrupted  int64    `json:"job_hosts_interrupted"`
	FirewallClosed    int64    `json:"firewall_transactions_closed"`
	DeliveriesDropped int64    `json:"notifications_dropped"`
	SchedulesHeld     []string `json:"schedules_held"`
	OccurrencesDrop   int64    `json:"queued_runs_dropped"`
	AutoPaused        []string `json:"automatic_restore_paused"`
	RolloutsPaused    int64    `json:"agent_rollouts_paused"`
	Hosts             int64    `json:"hosts"`
}

func newEpoch() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// PrepareRestored rewrites a restored database (one transaction, before
// the controller starts on it) so the controller starts in
// reconciliation. Restore calls it on the staged database.
func PrepareRestored(ctx context.Context, tx *sql.Tx, m *Manifest, actor string, now time.Time) (*RestoreSummary, error) {
	ms := now.UnixMilli()
	when := m.CreatedAt.UTC().Format("2 Jan 2006 15:04 MST")
	sum := &RestoreSummary{RestoreID: ids.New(ids.Restore), SchedulesHeld: []string{}, AutoPaused: []string{}}

	// A new epoch.
	var prev sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT value_json FROM settings WHERE key = 'controller_epoch'`).Scan(&prev); err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if prev.Valid {
		json.Unmarshal([]byte(prev.String), &sum.PreviousEpoch)
	}
	ep, err := newEpoch()
	if err != nil {
		return nil, err
	}
	sum.Epoch = ep
	v, _ := json.Marshal(ep)
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value_json, updated_at) VALUES ('controller_epoch', ?, ?)
		ON CONFLICT (key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`, string(v), ms); err != nil {
		return nil, err
	}

	exec := func(n *int64, q string, args ...any) error {
		r, err := tx.ExecContext(ctx, q, args...)
		if err != nil {
			return err
		}
		if n != nil {
			*n, _ = r.RowsAffected()
		}
		return nil
	}
	// Jobs in progress when the backup was taken: what happened next is
	// not known here. Never resumed, never re-run automatically.
	if err := exec(&sum.JobsInterrupted, `UPDATE jobs SET status = 'interrupted', finished_at = ? WHERE status IN ('pending','running','paused')`, ms); err != nil {
		return nil, err
	}
	if err := exec(&sum.HostsInterrupted, `UPDATE job_hosts SET state = 'interrupted', finished_at = ?, error_class = 'InterruptedError',
		error_message = ? WHERE state NOT IN ('succeeded','failed','timed_out','cancelled','skipped','interrupted')`,
		ms, "unfinished when the backup was taken ("+when+"); this controller was restored from that backup, so whether and how it ran afterwards is not known. It is not run again automatically"); err != nil {
		return nil, err
	}
	if err := exec(nil, `UPDATE job_steps SET state = 'interrupted', finished_at = ? WHERE state = 'running'`, ms); err != nil {
		return nil, err
	}
	// Firewall transactions open in the backup: the host's own timer
	// settled them long ago, one way or the other. Closed without claiming
	// an outcome; the host's committed state is compared in reconciliation.
	if err := exec(&sum.FirewallClosed, `UPDATE fw_transactions SET closed_reason = 'restore',
		state = CASE WHEN state IN ('preparing','pending') THEN 'unknown' ELSE state END,
		detail = trim(detail || ' — open when the backup was taken (' || ? || '); the controller was restored, so its outcome on the host is not known here'),
		finished_at = coalesce(finished_at, ?), updated_at = ?
		WHERE state IN ('preparing','pending','unknown') AND closed_reason = ''`, when, ms, ms); err != nil {
		return nil, err
	}
	// Notifications not yet sent: the previous controller may have sent
	// them after the backup; they describe alerts that are evaluated again
	// anyway. Not sent from here.
	if err := exec(&sum.DeliveriesDropped, `UPDATE notify_deliveries SET status = 'cancelled', finished_at = ?, ambiguous = 1,
		last_error = 'not sent: the controller was restored from a backup taken ' || ? || '; it may already have been sent then'
		WHERE status IN ('queued','sending')`, ms, when); err != nil {
		return nil, err
	}
	// Schedules that run jobs are held: the backup's schedules may be out
	// of date (one deleted afterwards would run again), and runs between
	// the backup and now may already have happened. An administrator
	// resumes them; re-enabling counts from then, never replays the gap.
	rows, err := tx.QueryContext(ctx, `SELECT id, revision FROM schedules WHERE kind = 'job' AND enabled = 1 AND archived_at IS NULL`)
	if err != nil {
		return nil, err
	}
	type sched struct {
		id  string
		rev int64
	}
	var held []sched
	for rows.Next() {
		var s sched
		if err := rows.Scan(&s.id, &s.rev); err != nil {
			rows.Close()
			return nil, err
		}
		held = append(held, s)
	}
	rows.Close()
	for _, s := range held {
		if err := exec(nil, `UPDATE schedules SET enabled = 0, attention = ?, attention_at = ? WHERE id = ?`,
			"Held after a controller restore (backup of "+when+"): its runs since then may already have happened, and the schedule may have changed. Check it, then enable it again.", ms, s.id); err != nil {
			return nil, err
		}
		// One marker for the gap: runs due between the backup and the
		// restore are not known here (missed_count 0: not counted).
		if err := exec(nil, `INSERT INTO schedule_occurrences (id, schedule_id, schedule_revision, scheduled_for, status, reason, missed_count, missed_until, created_at)
			VALUES (?, ?, ?, ?, 'missed', ?, 0, ?, ?) ON CONFLICT DO NOTHING`, ids.New(ids.Occurrence), s.id, s.rev, m.CreatedAt.UnixMilli(),
			"the controller was restored from a backup taken "+when+": runs between then and the restore may have happened on the previous controller; none is repeated", ms, ms); err != nil {
			return nil, err
		}
		sum.SchedulesHeld = append(sum.SchedulesHeld, s.id)
	}
	if err := exec(&sum.OccurrencesDrop, `UPDATE schedule_occurrences SET status = 'skipped',
		reason = 'not run: queued when the backup was taken; the controller was restored from it' WHERE status = 'queued'`); err != nil {
		return nil, err
	}
	// Automatic restore acts on the backup's baselines, which may be older
	// than what hosts were moved to since: paused until reviewed.
	rows, err = tx.QueryContext(ctx, `SELECT id FROM ds_baselines WHERE auto_enabled = 1 AND archived_at IS NULL AND attention = ''`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		sum.AutoPaused = append(sum.AutoPaused, id)
	}
	rows.Close()
	if err := exec(nil, `UPDATE ds_baselines SET attention = ?, attention_at = ? WHERE auto_enabled = 1 AND archived_at IS NULL AND attention = ''`,
		"Paused after a controller restore (backup of "+when+"): this baseline may be older than what its hosts were changed to since. Review the drift, then turn automatic restore on again.", ms); err != nil {
		return nil, err
	}
	if err := exec(&sum.RolloutsPaused, `UPDATE agent_rollouts SET status = 'paused', paused_reason = 'paused after a controller restore; resume when the Shepherds are reconciled', updated_at = ?
		WHERE status = 'running'`, ms); err != nil {
		return nil, err
	}
	// An earlier reconciliation the backup was taken in is superseded.
	if err := exec(nil, `UPDATE controller_restores SET status = 'finished', finished_at = ?, finished_by = 'superseded by a later restore' WHERE status = 'reconciling'`, ms); err != nil {
		return nil, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM hosts`).Scan(&sum.Hosts); err != nil {
		return nil, err
	}
	js, _ := json.Marshal(sum)
	if err := exec(nil, `INSERT INTO controller_restores (id, restored_at, backup_created_at, backup_version, backup_host, previous_epoch, epoch,
		restored_by, status, summary_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'reconciling', ?)`,
		sum.RestoreID, ms, m.CreatedAt.UnixMilli(), m.DroolVersion, m.Hostname, sum.PreviousEpoch, ep, actor, string(js)); err != nil {
		return nil, fmt.Errorf("record the restore: %w", err)
	}
	if err := exec(nil, `INSERT INTO host_reconciliation (restore_id, host_id, state) SELECT ?, id, 'pending' FROM hosts`, sum.RestoreID); err != nil {
		return nil, err
	}
	return sum, nil
}
