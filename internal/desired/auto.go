package desired

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"drool/internal/auth"
	"drool/internal/model"
)

// Automatic restore (off by default) runs inside the same loop as
// everything else, every AutoEvery:
//
//   - only baselines an administrator switched on, and only the resources
//     whose effective value comes from such a baseline (host-specific
//     settings are never restored automatically);
//   - only drift that was just checked again, never conflicts or unknown;
//   - not on hosts in maintenance, nor within MaintenanceGrace after it;
//   - at most one corrective job per baseline at a time and maxAutoJobs
//     across the fleet, each an ordinary job with the baseline's rollout
//     (canary and batches for several hosts, a failure threshold);
//   - as the baseline's owner, whose account and permissions are checked
//     before every job; a lost permission pauses automatic restore;
//   - with bounded retries per resource (autoBackoff, autoMaxAttempts)
//     and a pause when a resource keeps drifting back (redriftLimit).
const maxAutoJobs = 3

// autoBaseline is a baseline with automatic restore on.
type autoBaseline struct {
	ID, Name string
	OwnerID  string
	Strategy *model.Strategy
}

func (s *Service) autoBaselines(ctx context.Context) ([]autoBaseline, error) {
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT id, name, COALESCE(owner_id, ''), auto_strategy_json FROM ds_baselines
		WHERE archived_at IS NULL AND auto_enabled = 1 AND attention = '' ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []autoBaseline
	for rows.Next() {
		var b autoBaseline
		var sj string
		if err := rows.Scan(&b.ID, &b.Name, &b.OwnerID, &sj); err != nil {
			return nil, err
		}
		var st model.Strategy
		if sj != "" && sj != "{}" && json.Unmarshal([]byte(sj), &st) == nil {
			b.Strategy = &st
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// AutoPermissions are what an automatic-restore owner needs for a plan.
func AutoPermissions(needs []string) []auth.Permission {
	perms := []auth.Permission{auth.PermDesiredAuto, auth.PermDesiredRestore, auth.PermJobsExecute}
	for _, n := range needs {
		perms = append(perms, auth.Permission(n))
	}
	return perms
}

// ownerProblem says why a baseline's owner can't run automatic restore
// now ("" when they can).
func (s *Service) ownerProblem(ctx context.Context, ownerID string, perms []auth.Permission) (string, Actor) {
	if ownerID == "" {
		return "its owner's account was deleted; an administrator can assign a new owner", Actor{}
	}
	u, err := s.d.Store.R.GetUser(ctx, ownerID)
	if err != nil {
		return "its owner's account was deleted; an administrator can assign a new owner", Actor{}
	}
	if u.Disabled != 0 {
		return fmt.Sprintf("its owner's account (%s) is disabled", u.Username), Actor{}
	}
	for _, p := range perms {
		if !auth.RoleHas(u.Role, p) {
			return fmt.Sprintf("its owner (%s) no longer has the %s permission", u.Username, p), Actor{}
		}
	}
	return "", Actor{UserID: u.ID, Name: u.Username}
}

// activeAutoJobs counts automatic corrections still running, per baseline.
func (s *Service) activeAutoJobs(ctx context.Context) (map[string]int, int, error) {
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT c.baseline_id FROM ds_reconciles c JOIN jobs j ON j.id = c.job_id
		WHERE c.status = 'active' AND c.kind = 'auto' AND j.status IN ('pending','running','paused')`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	by := map[string]int{}
	n := 0
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			return nil, 0, err
		}
		by[b]++
		n++
	}
	return by, n, rows.Err()
}

// recentlyInMaintenance lists hosts whose maintenance ended less than
// grace ago (automatic restore waits a little after maintenance).
func (s *Service) recentlyInMaintenance(ctx context.Context, grace time.Duration) map[string]bool {
	now := s.nowMs()
	since := now - grace.Milliseconds()
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT t.target_id FROM maintenance m JOIN maintenance_targets t ON t.maintenance_id = m.id
		WHERE t.kind = 'host' AND COALESCE(m.ended_at, m.ends_at) BETWEEN ? AND ?
		UNION
		SELECT gm.host_id FROM maintenance m JOIN maintenance_targets t ON t.maintenance_id = m.id JOIN group_members gm ON gm.group_id = t.target_id
		WHERE t.kind = 'group' AND COALESCE(m.ended_at, m.ends_at) BETWEEN ? AND ?`, since, now, since, now)
	out := map[string]bool{}
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var h string
		if rows.Scan(&h) == nil {
			out[h] = true
		}
	}
	return out
}

// autoRestore runs one pass of automatic restore.
func (s *Service) autoRestore(ctx context.Context) {
	bls, err := s.autoBaselines(ctx)
	if err != nil || len(bls) == 0 {
		return
	}
	perBaseline, total, err := s.activeAutoJobs(ctx)
	if err != nil {
		return
	}
	var maint map[string]bool
	inMaint := map[string]bool{}
	if s.d.Alerts != nil {
		if m, err := s.d.Alerts.HostsInMaintenance(ctx); err == nil {
			for h := range m {
				inMaint[h] = true
			}
		}
		maint = s.recentlyInMaintenance(ctx, s.cfg.MaintenanceGrace)
	}
	for _, b := range bls {
		if total >= maxAutoJobs {
			return
		}
		if perBaseline[b.ID] > 0 {
			continue // one automatic correction per baseline at a time
		}
		hosts := s.autoCandidates(ctx, b.ID, inMaint, maint)
		if len(hosts) == 0 {
			continue
		}
		// Check those hosts again now: only drift that is still there is
		// corrected.
		s.checkHosts(ctx, hosts, true, 0)
		p, err := s.plan(ctx, Selection{HostIDs: hosts, BaselineID: b.ID}, RestoreOptions{Strategy: b.Strategy}, planCtx{auto: b.ID, now: s.nowMs()})
		if err != nil {
			s.log.Warn("desired state: plan automatic restore", "baseline", b.Name, "err", err)
			continue
		}
		// Only resources this baseline decides and restores automatically.
		p = s.onlyAuto(p, b.ID)
		if len(p.Items) == 0 {
			continue
		}
		why, owner := s.ownerProblem(ctx, b.OwnerID, AutoPermissions(p.Needs))
		if why != "" {
			if err := s.pauseAuto(ctx, b.ID, "automatic restore can't run: "+why); err != nil {
				s.log.Error("desired state: pause automatic restore", "err", err)
			}
			continue
		}
		jobs, err := s.submit(ctx, p, "auto", b.ID, owner)
		if err != nil {
			if !errors.Is(err, ErrPlanChanged) {
				s.log.Warn("desired state: automatic restore", "baseline", b.Name, "err", err)
			}
			continue
		}
		total += len(jobs)
	}
}

// autoCandidates lists hosts with drift that this baseline restores
// automatically and that may be corrected now.
func (s *Service) autoCandidates(ctx context.Context, baselineID string, inMaint, recent map[string]bool) []string {
	now := s.nowMs()
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT host_id, key FROM ds_resource_state
		WHERE status IN ('drift','failed') AND auto_paused = '' AND (auto_next_at IS NULL OR auto_next_at <= ?)`, now)
	if err != nil {
		return nil
	}
	defer rows.Close()
	s.mu.Lock()
	eff := s.eff
	s.mu.Unlock()
	set := map[string]bool{}
	for rows.Next() {
		var hid, key string
		if rows.Scan(&hid, &key) != nil {
			continue
		}
		if inMaint[hid] || recent[hid] {
			continue
		}
		// Who restores it is decided by the current configuration.
		e, ok := eff[hid].Resources[key]
		if !ok || !e.Source.Auto || e.Source.AutoBaseline == nil || e.Source.AutoBaseline.ID != baselineID || e.Source.Level == LevelHost {
			continue
		}
		set[hid] = true
	}
	out := make([]string, 0, len(set))
	for h := range set {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// onlyAuto keeps the plan's items whose value this baseline restores
// automatically (the plan selected by baseline; a value several
// baselines agree on is restored by the first automatic one).
func (s *Service) onlyAuto(p *Plan, baselineID string) *Plan {
	s.mu.Lock()
	eff := s.eff
	snap := s.snap
	s.mu.Unlock()
	keep := map[string]map[string]bool{}
	n := 0
	for _, it := range p.Items {
		e, ok := eff[it.HostID].Resources[it.Key]
		if !ok || !e.Source.Auto || e.Source.AutoBaseline == nil || e.Source.AutoBaseline.ID != baselineID || e.Source.Level == LevelHost {
			continue
		}
		if keep[it.HostID] == nil {
			keep[it.HostID] = map[string]bool{}
		}
		keep[it.HostID][it.Key] = true
		n++
	}
	if n == len(p.Items) {
		return p
	}
	sel := Selection{BaselineID: baselineID}
	for h := range keep {
		sel.HostIDs = append(sel.HostIDs, h)
	}
	out := &Plan{Items: []PlanItem{}, Excluded: p.Excluded, Jobs: []PlanJob{}, Warnings: []PlanWarning{}, Removals: []RemovalCheck{},
		Options: p.Options, At: p.At, needs: map[string]bool{}, byHost: map[string][]PlanItem{}}
	for _, it := range p.Items {
		if keep[it.HostID][it.Key] {
			out.Items = append(out.Items, it)
			out.byHost[it.HostID] = append(out.byHost[it.HostID], it)
			if it.Kind == KindFile {
				out.needs[string(auth.PermFilesDeploy)] = true
				if it.Desired.Elevated {
					out.needs[string(auth.PermFilesElevate)] = true
				}
			}
		}
	}
	for k := range out.needs {
		out.Needs = append(out.Needs, k)
	}
	sort.Strings(out.Needs)
	out.Hosts = len(out.byHost)
	if len(out.Items) == 0 || snap == nil {
		return out
	}
	if err := s.buildJobs(out, snap, p.Options, planCtx{auto: baselineID}); err != nil {
		return &Plan{}
	}
	out.Hash = planHash(out)
	return out
}

// SetAuto turns a baseline's automatic restore on or off and sets its
// rollout. Turning it on makes the caller the accountable owner (their
// permissions are checked before every automatic job) and clears a
// pause.
func (s *Service) SetAuto(ctx context.Context, baselineID string, on bool, strategy *model.Strategy, by Actor) (*BaselineView, error) {
	sj := "{}"
	if strategy != nil {
		st := *strategy
		if err := model.NormalizeStrategy(&st); err != nil {
			return nil, &ValidationError{Msg: err.Error()}
		}
		sj = mustJSON(st)
	}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var archived sql.NullInt64
		var was int
		if err := tx.QueryRowContext(ctx, `SELECT archived_at, auto_enabled FROM ds_baselines WHERE id = ?`, baselineID).Scan(&archived, &was); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if archived.Valid {
			return &ValidationError{Msg: "the baseline is archived"}
		}
		now := s.nowMs()
		if on {
			if _, err := tx.ExecContext(ctx, `UPDATE ds_baselines SET auto_enabled = 1, auto_strategy_json = ?, owner_id = ?, attention = '', attention_at = NULL,
				updated_at = ? WHERE id = ?`, sj, by.UserID, now, baselineID); err != nil {
				return err
			}
			// A fresh start for its resources: earlier pauses and retry
			// counts were about the earlier attempts.
			s.mu.Lock()
			eff := s.eff
			s.mu.Unlock()
			for hid, hs := range eff {
				for key, e := range hs.Resources {
					if !hasBaseline(e.Source.Baselines, baselineID) {
						continue
					}
					if _, err := tx.ExecContext(ctx, `UPDATE ds_resource_state SET auto_paused = '', auto_attempts = 0, auto_next_at = NULL,
						redrifts = 0, redrift_since = NULL WHERE host_id = ? AND key = ? AND (auto_paused <> '' OR auto_attempts > 0 OR redrifts > 0)`,
						hid, key); err != nil {
						return err
					}
				}
			}
		} else if _, err := tx.ExecContext(ctx, `UPDATE ds_baselines SET auto_enabled = 0, auto_strategy_json = ?, attention = '', attention_at = NULL, updated_at = ?
			WHERE id = ?`, sj, now, baselineID); err != nil {
			return err
		}
		return s.auditTx(ctx, tx, "desired.auto", "baseline", baselineID, map[string]any{"enabled": on, "strategy": json.RawMessage(sj), "owner": by.Name, "was_enabled": was == 1})
	})
	if err != nil {
		return nil, err
	}
	s.markAllDirty()
	if err := s.reload(ctx, true); err != nil {
		return nil, err
	}
	return s.Baseline(ctx, baselineID)
}
