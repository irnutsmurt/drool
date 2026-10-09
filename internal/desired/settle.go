package desired

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"drool/internal/alerts"
	"drool/internal/audit"
	"drool/internal/engine"
	"drool/internal/store/db"
)

// After a corrective job finishes, every host it touched is checked again
// (fresh service data, packages and files observed after the job ended),
// and only what that check proves is recorded: "restored" when the host
// now matches and the step for it succeeded; "restore failed" when it
// still differs (a successful command is not enough); unknown when the
// host can't be checked. Interrupted jobs are settled the same way and
// never re-run.

func (s *Service) activeReconciles(ctx context.Context) ([]string, error) {
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT id FROM ds_reconciles WHERE status = 'active' ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// settle starts settling finished corrections in the background (one
// goroutine per correction being settled, a handful at most).
func (s *Service) settle(ctx context.Context) {
	s.mu.Lock()
	ids := make([]string, 0, len(s.post))
	for id := range s.post {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	sort.Strings(ids)
	running := 0
	s.settling.Range(func(_, _ any) bool { running++; return true })
	for _, id := range ids {
		if running >= 2 {
			return
		}
		if _, busy := s.settling.LoadOrStore(id, true); busy {
			continue
		}
		s.mu.Lock()
		delete(s.post, id)
		s.mu.Unlock()
		running++
		go func(id string) {
			defer s.settling.Delete(id)
			if err := s.settleOne(ctx, id); err != nil {
				s.log.Error("desired state: settle correction", "reconcile", id, "err", err)
				s.mu.Lock()
				s.post[id] = true // try again later
				s.mu.Unlock()
			}
		}(id)
	}
}

// SettleNow settles every finished correction synchronously (tests).
func (s *Service) SettleNow(ctx context.Context) error {
	ids, err := s.activeReconciles(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.settleOne(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

type reconcileItem struct {
	HostID   string `json:"host_id"`
	HostName string `json:"host_name"`
	Key      string `json:"key"`
	Step     int    `json:"step"`
}

func (s *Service) settleOne(ctx context.Context, id string) error {
	var kind, jobID, itemsJSON, baselineID string
	var by sql.NullString
	err := s.d.Store.Reader.QueryRowContext(ctx, `SELECT kind, job_id, items_json, baseline_id, requested_by FROM ds_reconciles WHERE id = ? AND status = 'active'`, id).
		Scan(&kind, &jobID, &itemsJSON, &baselineID, &by)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	j, err := s.d.Store.R.GetJob(ctx, jobID)
	if err == sql.ErrNoRows {
		return s.finishReconcile(ctx, id, nil)
	}
	if err != nil {
		return err
	}
	switch j.Status {
	case engine.JobPending, engine.JobRunning, engine.JobPaused:
		return nil // not finished: settled when it is
	}
	var items []reconcileItem
	if err := json.Unmarshal([]byte(itemsJSON), &items); err != nil {
		return err
	}
	hosts, err := s.d.Store.R.ListJobHosts(ctx, jobID)
	if err != nil {
		return err
	}
	type hostRun struct {
		state, errMsg string
		steps         map[int]string // step → state
	}
	runs := map[string]*hostRun{}
	for _, h := range hosts {
		hr := &hostRun{state: h.State, errMsg: h.ErrorMessage, steps: map[int]string{}}
		steps, err := s.d.Store.R.ListJobSteps(ctx, db.ListJobStepsParams{JobID: jobID, HostID: h.HostID})
		if err != nil {
			return err
		}
		for _, st := range steps {
			hr.steps[int(st.StepIndex)] = st.State
		}
		runs[h.HostID] = hr
	}
	actorID, actorName := by.String, ""
	if by.Valid {
		if u, err := s.d.Store.R.GetUser(ctx, by.String); err == nil {
			actorName = u.Username
		}
	}
	// Only observations made from now on (after the job ended) count.
	since := s.nowMs()
	byHost := map[string]map[string]itemOutcome{}
	for _, it := range items {
		hr := runs[it.HostID]
		o := itemOutcome{}
		step := ""
		if hr != nil {
			step = hr.steps[it.Step]
		}
		switch {
		case hr == nil:
			o.message = "the host was not part of the job"
		case step == "succeeded":
			o.ran = true
		case hr.state == "interrupted" || step == "interrupted" || j.Status == engine.JobInterrupted:
			o.message = "the controller restarted during the correction; its outcome on the host is unknown and it is not re-run automatically"
		case step != "":
			o.message = "the correction failed: " + hr.errMsg
		case hr.state == engine.HostCancelled:
			o.message = "the job was cancelled before this correction ran"
		case hr.state == engine.HostSkipped:
			o.message = "skipped: " + strings.TrimPrefix(hr.errMsg, "skipped: ")
		case hr.errMsg != "":
			o.message = "not run: an earlier correction on this host failed: " + hr.errMsg
		default:
			o.message = "the correction didn't run on this host"
		}
		if byHost[it.HostID] == nil {
			byHost[it.HostID] = map[string]itemOutcome{}
		}
		byHost[it.HostID][it.Key] = o
	}
	hostIDs := make([]string, 0, len(byHost))
	for h := range byHost {
		hostIDs = append(hostIDs, h)
	}
	sort.Strings(hostIDs)
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for _, hid := range hostIDs {
		wg.Add(1)
		sem <- struct{}{}
		go func(hid string) {
			defer func() { <-sem; wg.Done() }()
			cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
			res := s.runCheck(cctx, hid, true)
			cancel()
			out := &jobOutcome{reconcileID: id, jobID: jobID, kind: kind, actorID: actorID, actorName: actorName, items: byHost[hid]}
			if err := s.evaluate(ctx, hid, evalInput{check: &res, since: since, outcome: out}); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		}(hid)
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	return s.finishReconcile(ctx, id, items)
}

// finishReconcile marks a correction done and clears any resource still
// pointing at it (e.g. one no longer managed).
func (s *Service) finishReconcile(ctx context.Context, id string, _ []reconcileItem) error {
	now := s.nowMs()
	_, err := s.d.Store.Writer.ExecContext(ctx, `UPDATE ds_reconciles SET status = 'done', finished_at = ? WHERE id = ?`, now, id)
	if err != nil {
		return err
	}
	// Anything still marked with this correction is unknown until checked
	// again (never assumed restored).
	rows, err := s.d.Store.Writer.QueryContext(ctx, `UPDATE ds_resource_state SET status = 'unknown', last_known = 'drift',
		note = 'the correction finished; waiting for the next check', reconcile_id = NULL, job_id = NULL, since = ?
		WHERE reconcile_id = ? AND status = 'reconciling' RETURNING host_id`, now, id)
	if err != nil {
		return err
	}
	var hosts []string
	for rows.Next() {
		var h string
		if rows.Scan(&h) == nil {
			hosts = append(hosts, h)
		}
	}
	rows.Close()
	if len(hosts) > 0 {
		s.markDirty(hosts...)
	}
	return rows.Err()
}

// OnJobPaused is told when a job paused (canary failure, failure
// threshold). A paused automatic restore pauses its baseline's automatic
// restore until an administrator resumes it: the rollout's own pause is
// a decision for a person.
func (s *Service) OnJobPaused(jobID, kind, reason string) {
	if kind != engine.PauseCanary && kind != engine.PauseThreshold {
		return
	}
	ctx := context.Background()
	var rid, rkind, baselineID string
	if err := s.d.Store.Reader.QueryRowContext(ctx, `SELECT c.id, c.kind, c.baseline_id FROM ds_reconciles c JOIN jobs j ON j.reconcile_id = c.id
		WHERE j.id = ?`, jobID).Scan(&rid, &rkind, &baselineID); err != nil || rkind != "auto" || baselineID == "" {
		return
	}
	why := "the automatic restore paused its rollout: " + reason
	if err := s.pauseAuto(ctx, baselineID, why); err != nil {
		s.log.Error("desired state: pause automatic restore", "baseline", baselineID, "err", err)
	}
}

// pauseAuto marks a baseline's automatic restore as needing attention
// (once), audits it and records one event.
func (s *Service) pauseAuto(ctx context.Context, baselineID, why string) error {
	now := s.nowMs()
	var name string
	var emit func()
	changed := false
	err := s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
		var attention string
		if err := tx.QueryRowContext(ctx, `SELECT name, attention FROM ds_baselines WHERE id = ?`, baselineID).Scan(&name, &attention); err != nil {
			return err
		}
		if attention != "" {
			return nil
		}
		changed = true
		if _, err := tx.ExecContext(ctx, `UPDATE ds_baselines SET attention = ?, attention_at = ? WHERE id = ?`, why, now, baselineID); err != nil {
			return err
		}
		if s.d.Audit != nil {
			sys := audit.System
			e, err := s.d.Audit.RecordTx(ctx, db.New(tx), audit.Event{Action: "desired.auto_paused", Actor: &sys, TargetType: "baseline", TargetID: baselineID,
				Details: map[string]any{"name": name, "reason": why}})
			if err != nil {
				return err
			}
			emit = e
		}
		return nil
	})
	if err != nil || !changed {
		return err
	}
	if emit != nil {
		emit()
	}
	s.recordEvent(ctx, alerts.Event{Kind: "desired.auto_paused", Severity: "warning", ObjectType: "baseline", ObjectID: baselineID,
		Title: fmt.Sprintf("Automatic restore paused for baseline %s: %s", name, why)})
	s.markAllDirty()
	return nil
}

func (s *Service) markAllDirty() {
	s.mu.Lock()
	for id := range s.eff {
		s.dirty[id] = true
	}
	s.mu.Unlock()
	s.poke()
}

func (s *Service) recordEvent(ctx context.Context, e alerts.Event) {
	if s.d.Alerts == nil {
		return
	}
	if err := s.d.Alerts.RecordEvent(ctx, e); err != nil {
		s.log.Warn("desired state: record event", "err", err)
	}
}

// flushEvents writes the step's transitions to the event timeline, a few
// at a time: one event per host when few hosts drifted, one for all of
// them otherwise. Unchanged conditions never produce events.
func (s *Service) flushEvents(ctx context.Context) {
	b := s.events
	b.mu.Lock()
	drift, conflicts, paused := b.drift, b.conflicts, b.paused
	b.drift, b.conflicts, b.paused = nil, nil, nil
	b.mu.Unlock()
	emit := func(m map[string][]string, what, kind string) {
		if len(m) == 0 {
			return
		}
		hosts := make([]string, 0, len(m))
		for h := range m {
			hosts = append(hosts, h)
		}
		sort.Strings(hosts)
		if len(hosts) <= 3 {
			for _, h := range hosts {
				keys := m[h]
				sort.Strings(keys)
				names := make([]string, 0, len(keys))
				for _, k := range keys[:min(len(keys), 3)] {
					names = append(names, Describe(k))
				}
				more := ""
				if len(keys) > 3 {
					more = fmt.Sprintf(" and %d more", len(keys)-3)
				}
				s.recordEvent(ctx, alerts.Event{Kind: kind, Severity: "warning", ObjectType: "desired_host",
					ObjectID: s.hostIDByName(h), HostID: s.hostIDByName(h),
					Title: fmt.Sprintf("Desired state: %s on %s (%s%s)", what, h, strings.Join(names, ", "), more)})
			}
			return
		}
		s.recordEvent(ctx, alerts.Event{Kind: kind, Severity: "warning", ObjectType: "desired",
			Title: fmt.Sprintf("Desired state: %s on %d hosts (%s, …)", what, len(hosts), strings.Join(hosts[:3], ", "))})
	}
	emit(drift, "drift detected", "desired.drift")
	emit(conflicts, "conflict", "desired.conflict")
	for _, t := range paused {
		s.recordEvent(ctx, alerts.Event{Kind: "desired.auto_paused", Severity: "warning", ObjectType: "desired", Title: t})
	}
}

func (s *Service) hostIDByName(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snap != nil {
		for id, n := range s.snap.Hosts {
			if n == name {
				return id
			}
		}
	}
	return ""
}

// staleSweep re-evaluates hosts whose observations may have become too
// old to trust (they turn unknown) or whose state is unknown (fresh data
// may have arrived without a change).
func (s *Service) staleSweep(ctx context.Context) {
	now := s.nowMs()
	checkCut := now - (2*s.cfg.CheckEvery + 5*time.Minute).Milliseconds()
	_, agentWin, sshWin := s.monWindows(ctx)
	sshCut, graceCut, currentCut := now-sshWin.Milliseconds(), now-agentWin.Milliseconds(), now-agentCurrent.Milliseconds()
	// Service data goes stale by source: SSH after its poll window; an
	// agent's after agentCurrent, or after a short grace once the agent
	// is no longer connected (as observeService judges it).
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT h.host_id FROM ds_host_state h LEFT JOIN mon_current m ON m.host_id = h.host_id
		LEFT JOIN agent_identities a ON a.host_id = h.host_id AND a.status = 'active'
		WHERE h.n_unknown > 0 OR (h.packages_at IS NOT NULL AND h.packages_at < ?) OR (h.files_at IS NOT NULL AND h.files_at < ?)
		   OR (m.services_at IS NOT NULL AND (
		        (m.source = 'ssh' AND m.services_at < ?)
		     OR (m.source <> 'ssh' AND (m.services_at < ?
		         OR (COALESCE(a.connected, 0) = 0 AND m.services_at < ? AND COALESCE(a.last_seen_at, 0) < ?)))))`,
		checkCut, checkCut, sshCut, currentCut, graceCut, graceCut)
	if err != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	if len(ids) > 0 {
		s.markDirty(ids...)
	}
}

// prune bounds history: by age, and per resource.
func (s *Service) prune(ctx context.Context) {
	cut := s.nowMs() - int64(s.cfg.HistoryDays)*24*3600*1000
	if _, err := s.d.Store.Writer.ExecContext(ctx, `DELETE FROM ds_history WHERE ts < ?`, cut); err != nil {
		s.log.Warn("desired state: prune history", "err", err)
	}
	if _, err := s.d.Store.Writer.ExecContext(ctx, `DELETE FROM ds_history WHERE id IN (SELECT id FROM (
		SELECT id, ROW_NUMBER() OVER (PARTITION BY host_id, key ORDER BY id DESC) AS rn FROM ds_history) WHERE rn > ?)`, s.cfg.HistoryPerResource); err != nil {
		s.log.Warn("desired state: prune history", "err", err)
	}
	if _, err := s.d.Store.Writer.ExecContext(ctx, `DELETE FROM ds_reconciles WHERE status = 'done' AND finished_at < ?`, cut); err != nil {
		s.log.Warn("desired state: prune corrections", "err", err)
	}
}
