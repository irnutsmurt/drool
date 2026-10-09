package desired

import (
	"context"
	"database/sql"
	"encoding/json"
)

// Statuses of a managed resource.
const (
	StatusCompliant   = "compliant"
	StatusDrift       = "drift"
	StatusConflict    = "conflict"
	StatusUnknown     = "unknown"
	StatusReconciling = "reconciling"
	StatusFailed      = "failed"
	// StatusNone: a host with nothing managed.
	StatusNone = "none"
)

// hostStatus is a host's overall status: the most actionable of its
// resources' statuses.
func hostStatus(c counts) string {
	switch {
	case c.total == 0:
		return StatusNone
	case c.failed > 0:
		return StatusFailed
	case c.conflict > 0:
		return StatusConflict
	case c.drift > 0:
		return StatusDrift
	case c.reconciling > 0:
		return StatusReconciling
	case c.unknown > 0:
		return StatusUnknown
	}
	return StatusCompliant
}

type counts struct {
	total, compliant, drift, conflict, unknown, reconciling, failed, autoPaused int
}

func (c *counts) add(status string, autoPaused bool) {
	c.total++
	switch status {
	case StatusCompliant:
		c.compliant++
	case StatusDrift:
		c.drift++
	case StatusConflict:
		c.conflict++
	case StatusUnknown:
		c.unknown++
	case StatusReconciling:
		c.reconciling++
	case StatusFailed:
		c.failed++
	}
	if autoPaused {
		c.autoPaused++
	}
}

// resRow is a ds_resource_state row.
type resRow struct {
	Key, Kind, Status         string
	Desired, Source, Observed string
	ObservedAt                sql.NullInt64
	Diffs, Sig, LastKnown     string
	Note                      string
	Since                     int64
	ReconcileID, JobID        sql.NullString
	LastJobID                 string
	AutoAttempts              int
	AutoNextAt                sql.NullInt64
	AutoPaused                string
	RestoredAt                sql.NullInt64
	Redrifts                  int
	RedriftSince              sql.NullInt64
	exists                    bool
}

const resCols = `key, kind, status, desired_json, source_json, observed_json, observed_at, diffs_json, sig, last_known, note, since,
	reconcile_id, job_id, last_job_id, auto_attempts, auto_next_at, auto_paused, restored_at, redrifts, redrift_since`

func scanRes(sc interface{ Scan(...any) error }) (resRow, error) {
	var r resRow
	err := sc.Scan(&r.Key, &r.Kind, &r.Status, &r.Desired, &r.Source, &r.Observed, &r.ObservedAt, &r.Diffs, &r.Sig, &r.LastKnown,
		&r.Note, &r.Since, &r.ReconcileID, &r.JobID, &r.LastJobID, &r.AutoAttempts, &r.AutoNextAt, &r.AutoPaused, &r.RestoredAt,
		&r.Redrifts, &r.RedriftSince)
	r.exists = err == nil
	return r, err
}

func loadRows(ctx context.Context, q queryer, hostID string) (map[string]*resRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+resCols+` FROM ds_resource_state WHERE host_id = ?`, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*resRow{}
	for rows.Next() {
		r, err := scanRes(rows)
		if err != nil {
			return nil, err
		}
		out[r.Key] = &r
	}
	return out, rows.Err()
}

func saveRow(ctx context.Context, tx execer, hostID string, r *resRow) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO ds_resource_state (host_id, `+resCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(host_id, key) DO UPDATE SET kind = excluded.kind, status = excluded.status, desired_json = excluded.desired_json,
		source_json = excluded.source_json, observed_json = excluded.observed_json, observed_at = excluded.observed_at,
		diffs_json = excluded.diffs_json, sig = excluded.sig, last_known = excluded.last_known, note = excluded.note, since = excluded.since,
		reconcile_id = excluded.reconcile_id, job_id = excluded.job_id, last_job_id = excluded.last_job_id,
		auto_attempts = excluded.auto_attempts, auto_next_at = excluded.auto_next_at, auto_paused = excluded.auto_paused,
		restored_at = excluded.restored_at, redrifts = excluded.redrifts, redrift_since = excluded.redrift_since`,
		hostID, r.Key, r.Kind, r.Status, r.Desired, r.Source, r.Observed, r.ObservedAt, r.Diffs, r.Sig, r.LastKnown, r.Note, r.Since,
		r.ReconcileID, r.JobID, r.LastJobID, r.AutoAttempts, r.AutoNextAt, r.AutoPaused, r.RestoredAt, r.Redrifts, r.RedriftSince)
	return err
}

// hostRow is a ds_host_state row.
type hostRow struct {
	EffHash      string
	Status       string
	C            counts
	PackagesAt   sql.NullInt64
	PackagesNote string
	FilesAt      sql.NullInt64
	FilesNote    string
	UnknownSince sql.NullInt64
	ChangedAt    int64
	exists       bool
}

func loadHost(ctx context.Context, q queryer, hostID string) (hostRow, error) {
	var h hostRow
	err := q.QueryRowContext(ctx, `SELECT eff_hash, status, n_total, n_compliant, n_drift, n_conflict, n_unknown, n_reconciling, n_failed,
		n_auto_paused, packages_at, packages_note, files_at, files_note, unknown_since, changed_at FROM ds_host_state WHERE host_id = ?`, hostID).
		Scan(&h.EffHash, &h.Status, &h.C.total, &h.C.compliant, &h.C.drift, &h.C.conflict, &h.C.unknown, &h.C.reconciling, &h.C.failed,
			&h.C.autoPaused, &h.PackagesAt, &h.PackagesNote, &h.FilesAt, &h.FilesNote, &h.UnknownSince, &h.ChangedAt)
	if err == sql.ErrNoRows {
		return hostRow{Status: StatusNone}, nil
	}
	h.exists = err == nil
	return h, err
}

func saveHost(ctx context.Context, tx execer, hostID string, h hostRow) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO ds_host_state (host_id, eff_hash, status, n_total, n_compliant, n_drift, n_conflict, n_unknown,
		n_reconciling, n_failed, n_auto_paused, packages_at, packages_note, files_at, files_note, unknown_since, changed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(host_id) DO UPDATE SET eff_hash = excluded.eff_hash, status = excluded.status, n_total = excluded.n_total,
		n_compliant = excluded.n_compliant, n_drift = excluded.n_drift, n_conflict = excluded.n_conflict, n_unknown = excluded.n_unknown,
		n_reconciling = excluded.n_reconciling, n_failed = excluded.n_failed, n_auto_paused = excluded.n_auto_paused,
		packages_at = excluded.packages_at, packages_note = excluded.packages_note, files_at = excluded.files_at,
		files_note = excluded.files_note, unknown_since = excluded.unknown_since, changed_at = excluded.changed_at`,
		hostID, h.EffHash, h.Status, h.C.total, h.C.compliant, h.C.drift, h.C.conflict, h.C.unknown, h.C.reconciling, h.C.failed,
		h.C.autoPaused, h.PackagesAt, h.PackagesNote, h.FilesAt, h.FilesNote, h.UnknownSince, h.ChangedAt)
	return err
}

// historyEntry is a ds_history row to write.
type historyEntry struct {
	HostID, HostName, Key, Kind string
	Status, PrevStatus          string
	Resolution, Detail          string
	Desired, Observed, Diffs    string
	BaselineID                  string
	BaselineRevision            int
	SourceLabel                 string
	JobID                       string
	ActorID, ActorName          string
}

func writeHistory(ctx context.Context, tx execer, ts int64, e historyEntry) error {
	if e.Diffs == "" {
		e.Diffs = "[]"
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO ds_history (ts, host_id, host_name, key, kind, status, prev_status, resolution, detail,
		desired_json, observed_json, diffs_json, baseline_id, baseline_revision, source_label, job_id, actor_id, actor_name)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ts, e.HostID, e.HostName, e.Key, e.Kind, e.Status, e.PrevStatus, e.Resolution, e.Detail, e.Desired, e.Observed, e.Diffs,
		e.BaselineID, e.BaselineRevision, e.SourceLabel, e.JobID, e.ActorID, e.ActorName)
	return err
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func nullInt(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }
