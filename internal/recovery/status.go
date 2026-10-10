package recovery

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"drool/internal/audit"
)

// ErrNoRestore: this controller was never restored (or its restore record
// isn't its own).
var ErrNoRestore = errors.New("this controller has no restore to reconcile")

// ErrState: the host isn't in a state the action applies to.
type ErrState struct{ Msg string }

func (e *ErrState) Error() string { return e.Msg }

// RestoreInfo describes the latest restore.
type RestoreInfo struct {
	ID              string     `json:"id"`
	RestoredAt      time.Time  `json:"restored_at"`
	BackupCreatedAt time.Time  `json:"backup_created_at"`
	BackupVersion   string     `json:"backup_version"`
	BackupHost      string     `json:"backup_host"`
	PreviousEpoch   string     `json:"previous_epoch"`
	Epoch           string     `json:"epoch"`
	RestoredBy      string     `json:"restored_by"`
	Status          string     `json:"status"` // reconciling | finished
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	FinishedBy      string     `json:"finished_by,omitempty"`
}

// HostRecord is one host's reconciliation.
type HostRecord struct {
	HostID        string        `json:"host_id"`
	HostName      string        `json:"host_name"`
	State         string        `json:"state"`
	Reason        string        `json:"reason"`
	Domains       []DomainCheck `json:"domains"`
	CheckedAt     *time.Time    `json:"checked_at,omitempty"`
	ReconciledAt  *time.Time    `json:"reconciled_at,omitempty"`
	DecidedBy     string        `json:"decided_by,omitempty"`
	Attempts      int64         `json:"attempts"`
	NextAttemptAt *time.Time    `json:"next_attempt_at,omitempty"`
	RestoreID     string        `json:"restore_id"`
}

// HeldItem is automation the restore held, and whether it still is.
type HeldItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Held bool   `json:"held"`
}

// UnknownOutcome counts what the restore closed without knowing how it ended.
type UnknownOutcome struct {
	Jobs                 int64 `json:"jobs"`
	JobHosts             int64 `json:"job_hosts"`
	FirewallTransactions int64 `json:"firewall_transactions"`
	Notifications        int64 `json:"notifications"`
	QueuedRuns           int64 `json:"queued_runs"`
}

// Status is the latest restore and its hosts.
type Status struct {
	Restore        *RestoreInfo   `json:"restore"`
	Counts         map[string]int `json:"counts"`
	Hosts          []HostRecord   `json:"hosts"`
	Schedules      []HeldItem     `json:"schedules"`
	Baselines      []HeldItem     `json:"baselines"`
	RolloutsPaused int64          `json:"agent_rollouts_paused"`
	UnknownOutcome UnknownOutcome `json:"unknown_outcome"`
}

type summary struct {
	JobsInterrupted   int64    `json:"jobs_interrupted"`
	HostsInterrupted  int64    `json:"job_hosts_interrupted"`
	FirewallClosed    int64    `json:"firewall_transactions_closed"`
	DeliveriesDropped int64    `json:"notifications_dropped"`
	SchedulesHeld     []string `json:"schedules_held"`
	OccurrencesDrop   int64    `json:"queued_runs_dropped"`
	AutoPaused        []string `json:"automatic_restore_paused"`
	RolloutsPaused    int64    `json:"agent_rollouts_paused"`
}

func msTime(v sql.NullInt64) *time.Time {
	if !v.Valid || v.Int64 == 0 {
		return nil
	}
	t := time.UnixMilli(v.Int64).UTC()
	return &t
}

// Status returns the latest restore (Restore nil: never restored).
func (s *Service) Status(ctx context.Context) (*Status, error) {
	out := &Status{Counts: map[string]int{}, Hosts: []HostRecord{}, Schedules: []HeldItem{}, Baselines: []HeldItem{}}
	r := s.activeRestore()
	if r == nil {
		return out, nil
	}
	var info RestoreInfo
	var restoredAt, backupAt int64
	var finishedAt sql.NullInt64
	var sumJSON string
	err := s.d.Store.Reader.QueryRowContext(ctx, `SELECT id, restored_at, backup_created_at, backup_version, backup_host, previous_epoch, epoch,
		restored_by, status, finished_at, finished_by, summary_json FROM controller_restores WHERE id = ?`, r.ID).Scan(&info.ID, &restoredAt, &backupAt,
		&info.BackupVersion, &info.BackupHost, &info.PreviousEpoch, &info.Epoch, &info.RestoredBy, &info.Status, &finishedAt, &info.FinishedBy, &sumJSON)
	if err != nil {
		return nil, err
	}
	info.RestoredAt, info.BackupCreatedAt, info.FinishedAt = time.UnixMilli(restoredAt).UTC(), time.UnixMilli(backupAt).UTC(), msTime(finishedAt)
	out.Restore = &info
	var sum summary
	json.Unmarshal([]byte(sumJSON), &sum)
	out.RolloutsPaused = sum.RolloutsPaused
	out.UnknownOutcome = UnknownOutcome{Jobs: sum.JobsInterrupted, JobHosts: sum.HostsInterrupted, FirewallTransactions: sum.FirewallClosed,
		Notifications: sum.DeliveriesDropped, QueuedRuns: sum.OccurrencesDrop}

	hosts, err := s.records(ctx, r.ID, "")
	if err != nil {
		return nil, err
	}
	for _, h := range hosts {
		out.Counts[h.State]++
	}
	out.Hosts = hosts
	for _, id := range sum.SchedulesHeld {
		var it HeldItem
		var enabled int64
		var attention string
		if err := s.d.Store.Reader.QueryRowContext(ctx, `SELECT id, name, enabled, attention FROM schedules WHERE id = ? AND archived_at IS NULL`, id).
			Scan(&it.ID, &it.Name, &enabled, &attention); err != nil {
			continue
		}
		it.Held = enabled == 0 && strings.HasPrefix(attention, "Held after a controller restore")
		out.Schedules = append(out.Schedules, it)
	}
	for _, id := range sum.AutoPaused {
		var it HeldItem
		var attention string
		if err := s.d.Store.Reader.QueryRowContext(ctx, `SELECT id, name, attention FROM ds_baselines WHERE id = ? AND archived_at IS NULL`, id).
			Scan(&it.ID, &it.Name, &attention); err != nil {
			continue
		}
		it.Held = strings.HasPrefix(attention, "Paused after a controller restore")
		out.Baselines = append(out.Baselines, it)
	}
	return out, nil
}

// records lists a restore's hosts (one host when hostID is set), those
// still needing something first.
func (s *Service) records(ctx context.Context, restoreID, hostID string) ([]HostRecord, error) {
	q := `SELECT r.host_id, coalesce(h.name, r.host_id), r.state, r.reason, r.domains_json, r.checked_at, r.reconciled_at, r.decided_by, r.attempts,
		r.next_attempt_at FROM host_reconciliation r LEFT JOIN hosts h ON h.id = r.host_id WHERE r.restore_id = ?`
	args := []any{restoreID}
	if hostID != "" {
		q += ` AND r.host_id = ?`
		args = append(args, hostID)
	}
	q += ` ORDER BY CASE r.state WHEN 'blocked' THEN 0 WHEN 'attention' THEN 1 WHEN 'ahead' THEN 2 WHEN 'incompatible' THEN 3
		WHEN 'unreachable' THEN 4 WHEN 'pending' THEN 5 ELSE 6 END, h.name LIMIT 5000`
	rows, err := s.d.Store.Reader.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HostRecord{}
	for rows.Next() {
		var h HostRecord
		var doms string
		var checked, reconciled sql.NullInt64
		var next int64
		if err := rows.Scan(&h.HostID, &h.HostName, &h.State, &h.Reason, &doms, &checked, &reconciled, &h.DecidedBy, &h.Attempts, &next); err != nil {
			return nil, err
		}
		h.RestoreID = restoreID
		h.Domains = []DomainCheck{}
		json.Unmarshal([]byte(doms), &h.Domains)
		h.CheckedAt, h.ReconciledAt = msTime(checked), msTime(reconciled)
		if (h.State == StatePending || h.State == StateUnreachable) && next > 0 {
			h.NextAttemptAt = msTime(sql.NullInt64{Int64: next, Valid: true})
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// HostStatus is a host's reconciliation in the latest restore (nil: none).
func (s *Service) HostStatus(ctx context.Context, hostID string) (*HostRecord, error) {
	r := s.activeRestore()
	if r == nil {
		return nil, nil
	}
	list, err := s.records(ctx, r.ID, hostID)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

const machineChanged = "its Shepherd's identity is now presented by another machine"

// Accept records an administrator's decision to accept a host's state as
// it is (state that is ahead of the controller, that disagrees with it,
// or a Shepherd identity that moved to another machine) and compares the
// host again. Nothing on the host changes: it moves to the new epoch with
// what it has, and DROOL's own configuration then shows where it differs.
func (s *Service) Accept(ctx context.Context, hostID string, actor audit.Actor) error {
	r := s.activeRestore()
	if r == nil {
		return ErrNoRestore
	}
	rec, err := s.loadRec(ctx, r.ID, hostID)
	if errors.Is(err, sql.ErrNoRows) {
		return &ErrState{"this host wasn't part of the restore"}
	}
	if err != nil {
		return err
	}
	switch {
	case rec.State == StateAhead, rec.State == StateAttention:
	case rec.State == StateBlocked && (strings.HasPrefix(rec.Reason, machineChanged) || strings.HasPrefix(rec.Reason, revokedElsewhere)):
	case rec.State == StateBlocked:
		return &ErrState{"blocked for safety: " + rec.Reason + ". Resolve that first, then check the host again"}
	default:
		return &ErrState{"there is nothing to accept: the host is " + rec.State}
	}
	if _, err := s.d.Store.Writer.ExecContext(ctx, `UPDATE host_reconciliation SET state = 'pending', reason = ?, decided_by = ?, next_attempt_at = 0
		WHERE restore_id = ? AND host_id = ?`, "accepted by "+actor.Name+"; comparing again", actor.Name, r.ID, hostID); err != nil {
		return err
	}
	if s.d.Audit != nil {
		s.d.Audit.Record(ctx, audit.Event{Action: "restore.accept", Actor: &actor, TargetType: "host", TargetID: hostID,
			Details: map[string]any{"restore_id": r.ID, "was": rec.State, "reason": rec.Reason}})
	}
	s.requeue(hostID)
	return nil
}

// Retry compares a host again now.
func (s *Service) Retry(ctx context.Context, hostID string) error {
	r := s.activeRestore()
	if r == nil {
		return ErrNoRestore
	}
	rec, err := s.loadRec(ctx, r.ID, hostID)
	if errors.Is(err, sql.ErrNoRows) {
		return &ErrState{"this host wasn't part of the restore"}
	}
	if err != nil {
		return err
	}
	if rec.State == StateReconciled || rec.State == StateRecovered {
		return &ErrState{"the host is already " + rec.State}
	}
	reason := rec.Reason
	if rec.State != StatePending && rec.State != StateUnreachable {
		reason = "checking again"
	}
	if _, err := s.d.Store.Writer.ExecContext(ctx, `UPDATE host_reconciliation SET state = CASE WHEN state = 'unreachable' THEN state ELSE 'pending' END,
		reason = ?, next_attempt_at = 0 WHERE restore_id = ? AND host_id = ?`, reason, r.ID, hostID); err != nil {
		return err
	}
	s.requeue(hostID)
	return nil
}

func (s *Service) requeue(hostID string) {
	s.mu.Lock()
	s.unsettled[hostID] = true
	started := s.started
	s.mu.Unlock()
	if started {
		s.enqueue(hostID)
	}
	if s.d.HostChanged != nil {
		s.d.HostChanged(hostID)
	}
}

// Finish closes the restore's reconciliation. Hosts not reconciled yet
// keep refusing commands from this controller until they are; their
// Shepherds are still compared whenever they connect.
func (s *Service) Finish(ctx context.Context, actor audit.Actor) error {
	r := s.activeRestore()
	if r == nil {
		return ErrNoRestore
	}
	res, err := s.d.Store.Writer.ExecContext(ctx, `UPDATE controller_restores SET status = 'finished', finished_at = ?, finished_by = ?
		WHERE id = ? AND status = 'reconciling'`, s.nowMs(), actor.Name, r.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return &ErrState{"the reconciliation is already finished"}
	}
	var open int
	s.d.Store.Reader.QueryRowContext(ctx, `SELECT count(*) FROM host_reconciliation WHERE restore_id = ? AND state NOT IN ('reconciled','recovered')`, r.ID).Scan(&open)
	s.mu.Lock()
	if s.active != nil && s.active.ID == r.ID {
		s.active.Status = "finished"
	}
	s.mu.Unlock()
	if s.d.Audit != nil {
		s.d.Audit.Record(ctx, audit.Event{Action: "restore.finish", Actor: &actor, TargetType: "controller",
			Details: map[string]any{"restore_id": r.ID, "hosts_not_reconciled": open}})
	}
	return nil
}

// Reconciling reports whether the latest restore is still being reconciled
// and how many hosts need an administrator (the header's indicator).
func (s *Service) Reconciling(ctx context.Context) (bool, int) {
	r := s.activeRestore()
	if r == nil {
		return false, 0
	}
	s.mu.Lock()
	open := r.Status == "reconciling"
	s.mu.Unlock()
	if !open {
		return false, 0
	}
	var n int
	s.d.Store.Reader.QueryRowContext(ctx, `SELECT count(*) FROM host_reconciliation WHERE restore_id = ? AND state IN ('ahead','attention','blocked','incompatible')`,
		r.ID).Scan(&n)
	return true, n
}
