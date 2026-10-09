package sched

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Occurrence statuses.
const (
	OccQueued     = "queued"     // waiting for the previous run (queue one)
	OccDispatched = "dispatched" // a job was created (or a maintenance started)
	OccSkipped    = "skipped"    // intentionally not run (overlap, maintenance, window not active, no hosts)
	OccMissed     = "missed"     // DROOL was not running at the time
	OccExpired    = "expired"    // a one-time run that would have started too late
	OccBlocked    = "blocked"    // the owner may no longer run it
	OccFailed     = "failed"     // it could not be started (a target or workflow is gone)
)

// Occurrence is one due time of a schedule and what happened.
type Occurrence struct {
	ID               string         `json:"id"`
	ScheduleID       string         `json:"schedule_id"`
	ScheduleRevision int            `json:"schedule_revision"`
	ScheduledFor     int64          `json:"scheduled_for"`
	Manual           bool           `json:"manual"`
	Status           string         `json:"status"`
	Reason           string         `json:"reason,omitempty"`
	MissedCount      int            `json:"missed_count,omitempty"`
	MissedUntil      *int64         `json:"missed_until,omitempty"`
	DispatchedAt     *int64         `json:"dispatched_at,omitempty"`
	JobID            string         `json:"job_id,omitempty"`
	JobStatus        string         `json:"job_status,omitempty"`
	JobCounts        map[string]int `json:"job_counts,omitempty"`
	JobPauseReason   string         `json:"job_pause_reason,omitempty"`
	MaintenanceID    string         `json:"maintenance_id,omitempty"`
	TriggeredBy      string         `json:"triggered_by,omitempty"`
	SkippedHosts     int            `json:"skipped_hosts"`
	CreatedAt        int64          `json:"created_at"`
	// Outcome says what happened, in words.
	Outcome string `json:"outcome"`
}

// Occurrences lists a schedule's history, newest first. before pages by
// scheduled time.
func (s *Service) Occurrences(ctx context.Context, scheduleID string, before int64, beforeID string, limit int) ([]*Occurrence, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return s.occurrences(ctx, s.d.Store.Reader, scheduleID, before, beforeID, limit)
}

func (s *Service) occurrences(ctx context.Context, q queryer, scheduleID string, before int64, beforeID string, limit int) ([]*Occurrence, error) {
	where, args := "o.schedule_id = ?", []any{scheduleID}
	if before > 0 {
		where += " AND (o.scheduled_for < ? OR (o.scheduled_for = ? AND o.id < ?))"
		args = append(args, before, before, beforeID)
	}
	rows, err := q.QueryContext(ctx, `SELECT o.id, o.schedule_id, o.schedule_revision, o.scheduled_for, o.manual, o.status, o.reason, o.missed_count, o.missed_until,
		o.dispatched_at, COALESCE(o.job_id, ''), COALESCE(j.status, ''), COALESCE(j.pause_reason, ''), COALESCE(o.maintenance_id, ''), o.triggered_by,
		o.skipped_hosts, o.created_at
		FROM schedule_occurrences o LEFT JOIN jobs j ON j.id = o.job_id
		WHERE `+where+` ORDER BY o.scheduled_for DESC, o.id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	var out []*Occurrence
	for rows.Next() {
		var o Occurrence
		var manual int
		var until, disp sql.NullInt64
		if err := rows.Scan(&o.ID, &o.ScheduleID, &o.ScheduleRevision, &o.ScheduledFor, &manual, &o.Status, &o.Reason, &o.MissedCount, &until,
			&disp, &o.JobID, &o.JobStatus, &o.JobPauseReason, &o.MaintenanceID, &o.TriggeredBy, &o.SkippedHosts, &o.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		o.Manual = manual == 1
		if until.Valid {
			o.MissedUntil = &until.Int64
		}
		if disp.Valid {
			o.DispatchedAt = &disp.Int64
		}
		out = append(out, &o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, o := range out {
		if o.JobID != "" {
			o.JobCounts = jobCounts(ctx, q, o.JobID)
		}
		o.Outcome = outcome(o)
	}
	if out == nil {
		out = []*Occurrence{}
	}
	return out, nil
}

func jobCounts(ctx context.Context, q queryer, jobID string) map[string]int {
	rows, err := q.QueryContext(ctx, `SELECT state, count(*) FROM job_hosts WHERE job_id = ? GROUP BY state`, jobID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var st string
		var n int
		if rows.Scan(&st, &n) == nil {
			out[st] = n
		}
	}
	return out
}

var stateWords = map[string]string{"succeeded": "succeeded", "failed": "failed", "timed_out": "timed out", "cancelled": "cancelled",
	"skipped": "skipped", "interrupted": "interrupted", "queued": "not started", "running": "running"}

func countsText(c map[string]int) string {
	var keys []string
	for k := range c {
		keys = append(keys, k)
	}
	order := map[string]int{"succeeded": 0, "failed": 1, "timed_out": 2, "skipped": 3, "cancelled": 4, "interrupted": 5}
	sort.Slice(keys, func(i, j int) bool {
		a, aok := order[keys[i]]
		b, bok := order[keys[j]]
		if !aok {
			a = 9
		}
		if !bok {
			b = 9
		}
		return a < b || a == b && keys[i] < keys[j]
	})
	var parts []string
	for _, k := range keys {
		w := stateWords[k]
		if w == "" {
			w = strings.ReplaceAll(k, "_", " ")
		}
		parts = append(parts, fmt.Sprintf("%d %s", c[k], w))
	}
	return strings.Join(parts, ", ")
}

func outcome(o *Occurrence) string {
	switch o.Status {
	case OccDispatched:
		if o.MaintenanceID != "" {
			return "Maintenance started"
		}
		switch o.JobStatus {
		case "":
			return "Ran (the job has since been removed by job retention)"
		case "pending", "running":
			return "Running: " + countsText(o.JobCounts)
		case "paused":
			return "Paused: " + o.JobPauseReason
		case "completed":
			c := o.JobCounts
			if c["failed"]+c["timed_out"]+c["interrupted"] > 0 {
				return "Completed with failures: " + countsText(c)
			}
			return "Completed: " + countsText(c)
		case "cancelled":
			return "Cancelled: " + countsText(o.JobCounts)
		case "interrupted":
			return "Interrupted: the controller restarted during the run (not re-run automatically)"
		}
		return o.JobStatus
	case OccQueued:
		return "Queued: waiting for the previous run to finish"
	case OccSkipped:
		return "Skipped: " + o.Reason
	case OccMissed:
		if o.MissedCount > 1 && o.MissedUntil != nil {
			return fmt.Sprintf("Missed %d runs while DROOL was not running (until %s)", o.MissedCount, time.UnixMilli(*o.MissedUntil).UTC().Format("2006-01-02 15:04 UTC"))
		}
		return "Missed: DROOL was not running at the scheduled time"
	case OccExpired:
		return "Not started: " + o.Reason
	case OccBlocked:
		return "Blocked: " + o.Reason
	case OccFailed:
		return "Could not start: " + o.Reason
	}
	return o.Status
}
