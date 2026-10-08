package monsvc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"drool/internal/audit"
	"drool/internal/engine"
	"drool/internal/ids"
	"drool/internal/model"
	"drool/internal/monitor/collect"
	"drool/internal/store/db"
)

// Monitoring helpers (smartmontools, nvme-cli, lm-sensors) are installed
// only with approval, through the ordinary package job engine: the same
// simulation, locking, capability checks, audit and job history as any
// package install. DROOL records that it asked; it never uninstalls them.
// GPU drivers are never installed.

// HelperStatus is one helper on one host.
type HelperStatus struct {
	Package   string `json:"package"`
	Installed bool   `json:"installed"`
	Useful    bool   `json:"useful"`
	Purpose   string `json:"purpose"`
	// Requested: DROOL asked for the installation (status of that job).
	Requested string `json:"requested,omitempty"` // requested | installed | failed
	JobID     string `json:"job_id,omitempty"`
	Approved  string `json:"approved,omitempty"` // global | group: a standing approval covers it
}

func (s *Service) helperStatus(ctx context.Context, hostID string) []HelperStatus {
	s.mu.Lock()
	c := s.cur[hostID]
	var out []HelperStatus
	if c != nil && c.state != nil {
		for _, h := range c.state.Helpers {
			out = append(out, HelperStatus{Package: h.Package, Installed: h.Installed, Useful: h.Useful, Purpose: h.Purpose})
		}
	}
	s.mu.Unlock()
	req := s.requests(ctx, hostID)
	approved := s.approvalsFor(ctx, hostID)
	for i := range out {
		if r, ok := req[out[i].Package]; ok {
			out[i].Requested, out[i].JobID = r.status, r.job
		}
		out[i].Approved = approved[out[i].Package]
	}
	if out == nil {
		out = []HelperStatus{}
	}
	return out
}

type helperReq struct{ status, job string }

func (s *Service) requests(ctx context.Context, hostID string) map[string]helperReq {
	out := map[string]helperReq{}
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT package, status, COALESCE(job_id, '') FROM mon_helper_installs WHERE host_id = ?`, hostID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		var r helperReq
		rows.Scan(&p, &r.status, &r.job)
		out[p] = r
	}
	return out
}

// HelperPlanHost is one host in an installation preview.
type HelperPlanHost struct {
	HostID   string   `json:"host_id"`
	Name     string   `json:"name"`
	Helpers  []string `json:"helpers"`  // what will be installed (DROOL names)
	Packages []string `json:"packages"` // the package manager's names
	Manager  string   `json:"manager,omitempty"`
	Skip     string   `json:"skip,omitempty"` // why nothing will happen on this host
}

// HelperPreview is what an installation would do.
type HelperPreview struct {
	Hosts    int              `json:"hosts"`
	Counts   map[string]int   `json:"counts"` // helper → hosts that need it
	Plan     []HelperPlanHost `json:"plan"`
	Installs int              `json:"installs"` // hosts that would get a job
}

func validHelper(p string) bool {
	for _, h := range collect.Helpers {
		if h == p {
			return true
		}
	}
	return false
}

// PreviewHelpers works out which helpers each host needs (only those
// that would add capability, optionally restricted to only).
func (s *Service) PreviewHelpers(ctx context.Context, hostIDs []string, only []string) (*HelperPreview, error) {
	if len(hostIDs) > 500 {
		return nil, errors.New("at most 500 hosts at once")
	}
	for _, p := range only {
		if !validHelper(p) {
			return nil, fmt.Errorf("%q is not a monitoring helper DROOL installs (allowed: %s)", p, strings.Join(collect.Helpers, ", "))
		}
	}
	pv := &HelperPreview{Counts: map[string]int{}, Plan: []HelperPlanHost{}}
	for _, id := range hostIDs {
		h, err := s.d.Store.R.GetHost(ctx, id)
		if err != nil {
			continue
		}
		pv.Hosts++
		ph := HelperPlanHost{HostID: id, Name: h.Name, Helpers: []string{}, Packages: []string{}}
		f, ferr := s.d.Store.R.GetHostFacts(ctx, id)
		if ferr == nil {
			ph.Manager = f.PkgManager
		}
		for _, st := range s.helperStatus(ctx, id) {
			if !st.Useful || st.Installed || (len(only) > 0 && !containsStr(only, st.Package)) {
				continue
			}
			pv.Counts[st.Package]++
			ph.Helpers = append(ph.Helpers, st.Package)
			ph.Packages = append(ph.Packages, collect.PackageName(st.Package, ph.Manager))
		}
		switch {
		case len(ph.Helpers) == 0:
			ph.Skip = "Nothing to install: the useful helpers are present (or none would help on this host)."
		case ph.Manager != "apt" && ph.Manager != "dnf" && ph.Manager != "yum":
			ph.Skip = "No supported package manager (apt or dnf) is known for this host."
		default:
			pv.Installs++
		}
		pv.Plan = append(pv.Plan, ph)
	}
	return pv, nil
}

func containsStr(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}

// InstallResult is what an installation started.
type InstallResult struct {
	Jobs    []string       `json:"jobs"`
	Preview *HelperPreview `json:"preview"`
}

// InstallHelpers starts package jobs for the hosts that need helpers:
// one job per distinct package list (hosts needing the same packages
// share a job). scope records how it was approved.
func (s *Service) InstallHelpers(ctx context.Context, hostIDs, only []string, scope, userID string) (*InstallResult, error) {
	if s.d.Engine == nil {
		return nil, errors.New("the job engine is not available")
	}
	pv, err := s.PreviewHelpers(ctx, hostIDs, only)
	if err != nil {
		return nil, err
	}
	groups := map[string][]HelperPlanHost{}
	for _, p := range pv.Plan {
		if p.Skip != "" {
			continue
		}
		pk := append([]string(nil), p.Packages...)
		sort.Strings(pk)
		groups[strings.Join(pk, " ")] = append(groups[strings.Join(pk, " ")], p)
	}
	res := &InstallResult{Jobs: []string{}, Preview: pv}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		hosts := groups[k]
		var ids []string
		for _, h := range hosts {
			ids = append(ids, h.HostID)
		}
		spec := model.JobSpec{Name: "Install monitoring helpers: " + k,
			Steps:   []model.Operation{{Type: model.OpPackage, Package: &model.PackageOp{Action: "install", Names: strings.Fields(k)}}},
			Targets: model.TargetSpec{Hosts: ids}, Strategy: model.Strategy{Concurrency: 5}}
		var sub *engine.Submission
		err := s.d.Store.InTx(ctx, func(q *db.Queries) error {
			var err error
			sub, err = s.d.Engine.SubmitTx(ctx, q, spec, userID, "", "")
			if err != nil {
				return err
			}
			_, err = s.d.Audit.RecordTx(ctx, q, audit.Event{Action: "monitoring.helpers.install", TargetType: "job", TargetID: sub.Job.ID,
				Details: map[string]any{"packages": strings.Fields(k), "hosts": ids, "scope": scope}})
			return err
		})
		if err != nil {
			return res, err
		}
		if err := s.recordRequests(ctx, hosts, sub.Job.ID, scope, userID); err != nil {
			s.log.Warn("record helper installation", "err", err)
		}
		s.d.Engine.Launch(sub.Job.ID)
		res.Jobs = append(res.Jobs, sub.Job.ID)
	}
	return res, nil
}

func (s *Service) recordRequests(ctx context.Context, hosts []HelperPlanHost, jobID, scope, userID string) error {
	now := s.now().UnixMilli()
	return s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
		for _, h := range hosts {
			for _, helper := range h.Helpers {
				var by any
				if userID != "" {
					by = userID
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO mon_helper_installs (host_id, package, job_id, status, scope, requested_by, requested_at)
					VALUES (?, ?, ?, 'requested', ?, ?, ?) ON CONFLICT(host_id, package) DO UPDATE SET job_id = excluded.job_id,
					status = 'requested', scope = excluded.scope, requested_by = excluded.requested_by, requested_at = excluded.requested_at, detail = ''`,
					h.HostID, helper, jobID, scope, by, now); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// --- standing approvals ---

// Approval is a standing approval to install an allowlisted helper.
type Approval struct {
	ID        string `json:"id"`
	Scope     string `json:"scope"` // global | group
	GroupID   string `json:"group_id,omitempty"`
	GroupName string `json:"group_name,omitempty"`
	Package   string `json:"package"`
	CreatedBy string `json:"created_by,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

// Approvals lists standing approvals.
func (s *Service) Approvals(ctx context.Context) ([]Approval, error) {
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT a.id, a.scope, COALESCE(a.group_id, ''), COALESCE(g.name, ''), a.package, COALESCE(a.created_by, ''), a.created_at
		FROM mon_helper_approvals a LEFT JOIN groups g ON g.id = a.group_id ORDER BY a.created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Approval{}
	for rows.Next() {
		var a Approval
		if err := rows.Scan(&a.ID, &a.Scope, &a.GroupID, &a.GroupName, &a.Package, &a.CreatedBy, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AddApprovalTx records a standing approval (the caller audits).
func (s *Service) AddApprovalTx(ctx context.Context, tx *sql.Tx, a Approval, userID string) (string, error) {
	if !validHelper(a.Package) {
		return "", fmt.Errorf("%q is not on the monitoring helper allowlist (%s)", a.Package, strings.Join(collect.Helpers, ", "))
	}
	var group any
	switch a.Scope {
	case "global":
	case "group":
		if a.GroupID == "" {
			return "", errors.New("choose a group")
		}
		group = a.GroupID
	default:
		return "", errors.New("scope must be global or group")
	}
	id := ids.New(ids.MonApproval)
	var by any
	if userID != "" {
		by = userID
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO mon_helper_approvals (id, scope, group_id, package, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, a.Scope, group, a.Package, by, s.now().UnixMilli())
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return "", errors.New("that approval already exists")
	}
	return id, err
}

// approvalsFor maps helper → the approval scope covering a host.
func (s *Service) approvalsFor(ctx context.Context, hostID string) map[string]string {
	out := map[string]string{}
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT a.package, a.scope FROM mon_helper_approvals a
		WHERE a.scope = 'global' OR (a.scope = 'group' AND a.group_id IN (SELECT group_id FROM group_members WHERE host_id = ?))`, hostID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var p, sc string
		rows.Scan(&p, &sc)
		if out[p] != "global" {
			out[p] = sc
		}
	}
	return out
}

// approverFor returns who approved a helper for a host (the job's owner).
func (s *Service) approverFor(ctx context.Context, hostID, pkg string) (string, string) {
	var by, scope string
	s.d.Store.Reader.QueryRowContext(ctx, `SELECT COALESCE(a.created_by, ''), a.scope FROM mon_helper_approvals a
		WHERE a.package = ? AND (a.scope = 'global' OR (a.scope = 'group' AND a.group_id IN (SELECT group_id FROM group_members WHERE host_id = ?)))
		ORDER BY CASE a.scope WHEN 'group' THEN 0 ELSE 1 END LIMIT 1`, pkg, hostID).Scan(&by, &scope)
	return by, scope
}

// AutoInstallHelpers installs approved helpers where they'd help and
// nothing was requested yet, and tracks earlier requests to completion.
// A failed installation is not retried automatically: the host keeps its
// reduced capability and the UI says why.
func (s *Service) AutoInstallHelpers(ctx context.Context) {
	s.trackRequests(ctx)
	s.mu.Lock()
	type want struct{ host, pkg string }
	var wants []want
	for id, c := range s.cur {
		if c.state == nil {
			continue
		}
		for _, h := range c.state.Helpers {
			if h.Useful && !h.Installed {
				wants = append(wants, want{id, h.Package})
			}
		}
	}
	s.mu.Unlock()
	byApprover := map[string]map[string][]string{} // user → host → packages
	scopes := map[string]string{}
	for _, w := range wants {
		if _, asked := s.requests(ctx, w.host)[w.pkg]; asked {
			continue
		}
		by, scope := s.approverFor(ctx, w.host, w.pkg)
		if scope == "" || by == "" {
			continue
		}
		if byApprover[by] == nil {
			byApprover[by] = map[string][]string{}
		}
		byApprover[by][w.host] = append(byApprover[by][w.host], w.pkg)
		scopes[by] = scope
	}
	for user, hosts := range byApprover {
		for host, pkgs := range hosts {
			if _, err := s.InstallHelpers(ctx, []string{host}, pkgs, scopes[user], user); err != nil {
				s.log.Warn("automatic monitoring helper installation", "host", host, "err", err)
			}
		}
	}
}

// trackRequests updates requested installations from their jobs and asks
// the host for fresh storage data once a job finished.
func (s *Service) trackRequests(ctx context.Context) {
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT i.host_id, i.package, i.job_id, COALESCE(jh.state, ''), COALESCE(j.status, '')
		FROM mon_helper_installs i LEFT JOIN jobs j ON j.id = i.job_id LEFT JOIN job_hosts jh ON jh.job_id = i.job_id AND jh.host_id = i.host_id
		WHERE i.status = 'requested'`)
	if err != nil {
		return
	}
	type upd struct{ host, pkg, status, detail string }
	var ups []upd
	for rows.Next() {
		var h, p, job, hs, js string
		rows.Scan(&h, &p, &job, &hs, &js)
		switch {
		case hs == "succeeded":
			ups = append(ups, upd{h, p, "installed", ""})
		case hs == "failed" || hs == "timed_out" || hs == "cancelled" || hs == "skipped" || hs == "interrupted" || js == "cancelled" || js == "interrupted":
			ups = append(ups, upd{h, p, "failed", "the installation job did not succeed on this host (" + hs + ")"})
		}
	}
	rows.Close()
	for _, u := range ups {
		s.d.Store.Writer.ExecContext(ctx, `UPDATE mon_helper_installs SET status = ?, detail = ? WHERE host_id = ? AND package = ?`, u.status, u.detail, u.host, u.pkg)
		if u.status == "installed" {
			s.RequestRefresh(ctx, u.host, "smart", "monitor_state")
		}
	}
}

// OnJobFinished lets the controller refresh capability right after a
// helper job (optional; trackRequests also catches it).
func (s *Service) OnJobFinished(ctx context.Context) { s.trackRequests(ctx) }
