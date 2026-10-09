package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"drool/internal/agentsvc"
	"drool/internal/audit"
	"drool/internal/auth"
	"drool/internal/monitor/collect"
	"drool/internal/monitor/monsvc"
	"drool/internal/store/db"
)

// System monitoring (M11, ARCHITECTURE-ALPHA §27): current state, bounded
// history, services, storage health, sensors, health issues, settings,
// monitoring helpers and bulk SSH → agent conversion.

func (s *Server) monitorEnabled(w http.ResponseWriter, r *http.Request) bool {
	if s.d.Monitor == nil {
		writeProblem(w, r, problem(http.StatusNotImplemented, "unavailable", "monitoring is not configured"))
		return false
	}
	return true
}

// monitorHost checks the host exists.
func (s *Server) monitorHost(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !s.monitorEnabled(w, r) {
		return "", false
	}
	id := r.PathValue("id")
	if _, err := s.d.Store.R.GetHost(r.Context(), id); err != nil {
		writeProblem(w, r, errNotFound("host"))
		return "", false
	}
	return id, true
}

func qInt(r *http.Request, k string) (int, *Problem) {
	v := r.URL.Query().Get(k)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, errValidation(k + " must be a non-negative number")
	}
	return n, nil
}

func (s *Server) handleMonitoringSummary(w http.ResponseWriter, r *http.Request) {
	if !s.monitorEnabled(w, r) {
		return
	}
	q := r.URL.Query()
	limit, p := qInt(r, "limit")
	if p != nil {
		writeProblem(w, r, p)
		return
	}
	off, p := qInt(r, "offset")
	if p != nil {
		writeProblem(w, r, p)
		return
	}
	cat := q.Get("category")
	if cat != "" && cat != "all" && !contains(monsvc.FleetCategories, cat) {
		writeProblem(w, r, errValidation("unknown category"))
		return
	}
	switch q.Get("sort") {
	case "", "attention", "name", "cpu", "memory", "disk", "temperature":
	default:
		writeProblem(w, r, errValidation("sort must be attention, name, cpu, memory, disk or temperature"))
		return
	}
	sum, err := s.d.Monitor.Fleet(r.Context(), monsvc.FleetFilter{Category: cat, Query: q.Get("q"), Sort: q.Get("sort"), Limit: limit, Offset: off})
	if err != nil {
		writeProblem(w, r, s.internalError(r, err))
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

// internalError logs the cause and returns the generic problem.
func (s *Server) internalError(r *http.Request, err error) *Problem {
	s.d.Logs.App.Error("monitoring request failed", "path", r.URL.Path, "err", err)
	return errInternal
}

func nowMs() int64 { return time.Now().UnixMilli() }

func contextWithoutCancel(r *http.Request) context.Context { return context.WithoutCancel(r.Context()) }

func contains(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}

func (s *Server) handleMonitoringIssues(w http.ResponseWriter, r *http.Request) {
	if !s.monitorEnabled(w, r) {
		return
	}
	q := r.URL.Query()
	limit, p := qInt(r, "limit")
	if p != nil {
		writeProblem(w, r, p)
		return
	}
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	st := q.Get("state")
	if st != "" && st != "active" && st != "resolved" && st != "all" {
		writeProblem(w, r, errValidation("state must be active, resolved or all"))
		return
	}
	is, err := s.d.Monitor.Issues(r.Context(), monsvc.IssueFilter{HostID: q.Get("host_id"), State: st, Severity: q.Get("severity"),
		Check: q.Get("check"), Limit: limit, Before: before})
	if err != nil {
		writeProblem(w, r, s.internalError(r, err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"issues": is})
}

type monitoringSettingsView struct {
	Settings  monsvc.Settings    `json:"settings"`
	Checks    []string           `json:"checks"`
	Helpers   []string           `json:"helpers"`
	LastPrune monsvc.PruneResult `json:"last_prune"`
}

func (s *Server) monitoringSettingsView(r *http.Request) monitoringSettingsView {
	return monitoringSettingsView{Settings: s.d.Monitor.Settings(r.Context()), Checks: monsvc.AllChecks(), Helpers: collect.Helpers,
		LastPrune: s.d.Monitor.LastPrune()}
}

func (s *Server) handleMonitoringSettings(w http.ResponseWriter, r *http.Request) {
	if !s.monitorEnabled(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, s.monitoringSettingsView(r))
}

func (s *Server) handleSetMonitoringSettings(w http.ResponseWriter, r *http.Request) {
	if !s.monitorEnabled(w, r) {
		return
	}
	var in monsvc.Settings
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	if in.Profile == monsvc.ProfileCustom {
		writeProblem(w, r, errValidation("health thresholds are configured as overrides (PUT /monitoring/health/overrides), not as a custom profile"))
		return
	}
	in.Profile, in.Custom = "", nil
	if err := in.Validate(); err != nil {
		writeProblem(w, r, errValidation(err.Error()))
		return
	}
	ctx := r.Context()
	before := s.d.Monitor.Settings(ctx)
	err := s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		if err := monsvc.SaveSettingsTx(ctx, q, in, principalFrom(ctx).UserID, nowMs()); err != nil {
			return audit.Event{}, err
		}
		return audit.Event{Action: "monitoring.settings.update", TargetType: "settings", TargetID: "monitoring",
			Details: map[string]any{"before": before, "after": in}}, nil
	})
	if err != nil {
		writeProblem(w, r, errValidation(err.Error()))
		return
	}
	s.d.Monitor.PushAll(ctx)
	writeJSON(w, http.StatusOK, s.monitoringSettingsView(r))
}

// --- host ---

func (s *Server) handleHostMonitoring(w http.ResponseWriter, r *http.Request) {
	id, ok := s.monitorHost(w, r)
	if !ok {
		return
	}
	o, err := s.d.Monitor.Overview(r.Context(), id)
	if err != nil {
		writeProblem(w, r, s.internalError(r, err))
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *Server) handleHostMonitoringHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := s.monitorHost(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	rng := q.Get("range")
	if rng == "" {
		rng = "1h"
	}
	var sel []string
	for _, v := range q["series"] {
		for _, x := range strings.Split(v, ",") {
			if x = strings.TrimSpace(x); x != "" {
				sel = append(sel, x)
			}
		}
	}
	if len(sel) == 0 {
		sel = []string{"cpu", "mem"}
	}
	if len(sel) > monsvc.MaxHistorySeries {
		writeProblem(w, r, errValidation("at most 32 series selectors"))
		return
	}
	h, err := s.d.Monitor.HistoryFor(r.Context(), id, rng, sel)
	if errors.Is(err, monsvc.ErrBadRange) {
		writeProblem(w, r, errValidation(err.Error()))
		return
	}
	if err != nil {
		writeProblem(w, r, s.internalError(r, err))
		return
	}
	writeJSON(w, http.StatusOK, h)
}

func (s *Server) handleHostServices(w http.ResponseWriter, r *http.Request) {
	id, ok := s.monitorHost(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := q.Get("filter")
	if f != "" && !contains(monsvc.ServiceFilters, f) {
		writeProblem(w, r, errValidation("filter must be one of "+strings.Join(monsvc.ServiceFilters, ", ")))
		return
	}
	v, err := s.d.Monitor.ServiceList(r.Context(), id, f, q.Get("all") == "true")
	if err != nil {
		writeProblem(w, r, s.internalError(r, err))
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleHostServiceHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := s.monitorHost(w, r)
	if !ok {
		return
	}
	limit, p := qInt(r, "limit")
	if p != nil {
		writeProblem(w, r, p)
		return
	}
	ev, err := s.d.Monitor.ServiceHistory(r.Context(), id, r.PathValue("unit"), limit)
	if err != nil {
		writeProblem(w, r, s.internalError(r, err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": ev})
}

func (s *Server) handleHostServiceEvents(w http.ResponseWriter, r *http.Request) {
	id, ok := s.monitorHost(w, r)
	if !ok {
		return
	}
	limit, p := qInt(r, "limit")
	if p != nil {
		writeProblem(w, r, p)
		return
	}
	ev, err := s.d.Monitor.ServiceHistory(r.Context(), id, "", limit)
	if err != nil {
		writeProblem(w, r, s.internalError(r, err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": ev})
}

func (s *Server) handleHostStorage(w http.ResponseWriter, r *http.Request) {
	id, ok := s.monitorHost(w, r)
	if !ok {
		return
	}
	v, err := s.d.Monitor.Storage(r.Context(), id)
	if err != nil {
		writeProblem(w, r, s.internalError(r, err))
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleHostHardware(w http.ResponseWriter, r *http.Request) {
	id, ok := s.monitorHost(w, r)
	if !ok {
		return
	}
	o, err := s.d.Monitor.Overview(r.Context(), id)
	if err != nil {
		writeProblem(w, r, s.internalError(r, err))
		return
	}
	out := map[string]any{"sensors": []any{}, "gpus": []any{}, "gpu_note": ""}
	if o.State != nil {
		out["sensors"], out["gpus"], out["gpu_note"] = o.State.Sensors, o.State.GPUs, o.State.GPUNote
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleHostMonitoringEvents(w http.ResponseWriter, r *http.Request) {
	id, ok := s.monitorHost(w, r)
	if !ok {
		return
	}
	limit, p := qInt(r, "limit")
	if p != nil {
		writeProblem(w, r, p)
		return
	}
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	ev, err := s.d.Monitor.Events(r.Context(), id, r.URL.Query().Get("kind"), limit, before)
	if err != nil {
		writeProblem(w, r, s.internalError(r, err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": ev})
}

type hostMonitoringSettingsResult struct {
	Settings monsvc.HostSettings `json:"settings"`
	Warning  string              `json:"warning,omitempty"`
}

func (s *Server) handleSetHostMonitoringSettings(w http.ResponseWriter, r *http.Request) {
	id, ok := s.monitorHost(w, r)
	if !ok {
		return
	}
	var body struct {
		monsvc.HostSettings
		// M11's per-host disabled checks are health overrides since M12.1;
		// an empty list is still accepted from older clients.
		DisabledChecks []string `json:"disabled_checks"`
	}
	if p := decodeJSON(w, r, &body); p != nil {
		writeProblem(w, r, p)
		return
	}
	if len(body.DisabledChecks) > 0 {
		writeProblem(w, r, errValidation("turn checks off with health overrides (PUT /monitoring/health/overrides, enabled: false), not disabled_checks"))
		return
	}
	in := body.HostSettings
	if in.WatchedServices == nil {
		in.WatchedServices = []string{}
	}
	if in.ProbeMounts == nil {
		in.ProbeMounts = []string{}
	}
	warning, err := in.Validate()
	if err == nil {
		// The combined agent config too (the fleet's buffer must fit this
		// host's budget).
		err = s.d.Monitor.Settings(r.Context()).AgentConfig(in).Validate()
	}
	if err != nil {
		writeProblem(w, r, errValidation(err.Error()))
		return
	}
	ctx := r.Context()
	before := s.d.Monitor.HostSettings(ctx, id)
	// The agent's resource budget is an administrator's decision.
	if !reflect.DeepEqual(before.Limits, in.Limits) && !principalFrom(ctx).Can(auth.PermMonitoringSettings) {
		writeProblem(w, r, errForbidden("changing an agent's resource budget requires monitoring.settings"))
		return
	}
	var emit func()
	err = s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
		if err := monsvc.SaveHostSettingsTx(ctx, tx, id, in, principalFrom(ctx).UserID, nowMs()); err != nil {
			return err
		}
		var err error
		emit, err = s.d.Audit.RecordTx(ctx, db.New(tx), audit.Event{Action: "monitoring.host_settings.update", TargetType: "host", TargetID: id,
			Details: map[string]any{"before": before, "after": in}})
		return err
	})
	if err != nil {
		writeProblem(w, r, s.internalError(r, err))
		return
	}
	emit()
	s.d.Monitor.PushConfig(ctx, id)
	s.d.Monitor.ApplyHealthConfig(ctx) // watched services changed: judge them now
	writeJSON(w, http.StatusOK, hostMonitoringSettingsResult{Settings: in, Warning: warning})
}

func (s *Server) handleRefreshHostMonitoring(w http.ResponseWriter, r *http.Request) {
	id, ok := s.monitorHost(w, r)
	if !ok {
		return
	}
	sent := s.d.Monitor.RequestRefresh(r.Context(), id, "monitor_state", "services", "smart")
	if !sent && s.d.Poller != nil {
		s.d.Monitor.ForceStorage(id)
		s.d.Poller.RunNow(id)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"agent": sent})
}

func (s *Server) handleHostDiagnostics(w http.ResponseWriter, r *http.Request) {
	id, ok := s.monitorHost(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodPost {
		if !s.d.Monitor.RequestRefresh(r.Context(), id, "diagnostics") {
			writeProblem(w, r, errConflict("no_agent", "diagnostics come from a connected agent"))
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"requested": true})
		return
	}
	b := s.d.Monitor.Diagnostics(id)
	if b == nil {
		writeJSON(w, http.StatusOK, map[string]any{"entries": []any{}})
		return
	}
	var entries json.RawMessage = b
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// --- helpers ---

type helpersRequest struct {
	HostIDs  []string `json:"host_ids"`
	Packages []string `json:"packages"`
}

func (s *Server) handleHostHelpers(w http.ResponseWriter, r *http.Request) {
	id, ok := s.monitorHost(w, r)
	if !ok {
		return
	}
	pv, err := s.d.Monitor.PreviewHelpers(r.Context(), []string{id}, nil)
	if err != nil {
		writeProblem(w, r, errValidation(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, pv)
}

func (s *Server) handlePreviewHelpers(w http.ResponseWriter, r *http.Request) {
	if !s.monitorEnabled(w, r) {
		return
	}
	var in helpersRequest
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	if len(in.HostIDs) == 0 {
		writeProblem(w, r, errValidation("select at least one host"))
		return
	}
	pv, err := s.d.Monitor.PreviewHelpers(r.Context(), in.HostIDs, in.Packages)
	if err != nil {
		writeProblem(w, r, errValidation(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, pv)
}

func (s *Server) handleInstallHelpers(w http.ResponseWriter, r *http.Request) {
	if !s.monitorEnabled(w, r) {
		return
	}
	var in helpersRequest
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	if len(in.HostIDs) == 0 {
		writeProblem(w, r, errValidation("select at least one host"))
		return
	}
	scope := "selection"
	if len(in.HostIDs) == 1 {
		scope = "host"
	}
	res, err := s.d.Monitor.InstallHelpers(r.Context(), in.HostIDs, in.Packages, scope, principalFrom(r.Context()).UserID)
	if err != nil {
		writeProblem(w, r, errValidation(err.Error()))
		return
	}
	writeJSON(w, http.StatusAccepted, res)
}

func (s *Server) handleListApprovals(w http.ResponseWriter, r *http.Request) {
	if !s.monitorEnabled(w, r) {
		return
	}
	a, err := s.d.Monitor.Approvals(r.Context())
	if err != nil {
		writeProblem(w, r, s.internalError(r, err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"approvals": a, "allowlist": collect.Helpers})
}

func (s *Server) handleAddApproval(w http.ResponseWriter, r *http.Request) {
	if !s.monitorEnabled(w, r) {
		return
	}
	var in monsvc.Approval
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	if in.Scope == "group" {
		if _, err := s.d.Store.R.GetGroup(ctx, in.GroupID); err != nil {
			writeProblem(w, r, errValidation("unknown group"))
			return
		}
	}
	var id string
	var emit func()
	err := s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = s.d.Monitor.AddApprovalTx(ctx, tx, in, principalFrom(ctx).UserID)
		if err != nil {
			return err
		}
		emit, err = s.d.Audit.RecordTx(ctx, db.New(tx), audit.Event{Action: "monitoring.helpers.approve", TargetType: "approval", TargetID: id,
			Details: map[string]any{"scope": in.Scope, "group_id": in.GroupID, "package": in.Package}})
		return err
	})
	if err != nil {
		writeProblem(w, r, errValidation(err.Error()))
		return
	}
	emit()
	go s.d.Monitor.AutoInstallHelpers(contextWithoutCancel(r))
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (s *Server) handleDeleteApproval(w http.ResponseWriter, r *http.Request) {
	if !s.monitorEnabled(w, r) {
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	var emit func()
	n := int64(0)
	err := s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM mon_helper_approvals WHERE id = ?`, id)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		if n == 0 {
			return nil
		}
		emit, err = s.d.Audit.RecordTx(ctx, db.New(tx), audit.Event{Action: "monitoring.helpers.unapprove", TargetType: "approval", TargetID: id})
		return err
	})
	if err != nil {
		writeProblem(w, r, s.internalError(r, err))
		return
	}
	if n == 0 {
		writeProblem(w, r, errNotFound("approval"))
		return
	}
	emit()
	w.WriteHeader(http.StatusNoContent)
}

// --- SSH → agent conversion ---

type conversionRequest struct {
	HostIDs     []string `json:"host_ids"`
	Preset      string   `json:"preset"`
	Canary      int      `json:"canary"`
	BatchSize   int      `json:"batch_size"`
	MaxFailures int      `json:"max_failures"`
}

func (s *Server) handlePreviewConversion(w http.ResponseWriter, r *http.Request) {
	if !s.agentsEnabled(w, r) {
		return
	}
	var in conversionRequest
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	p, err := s.d.Agents.PreviewConversion(r.Context(), in.HostIDs)
	if err != nil {
		writeProblem(w, r, errValidation(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleStartConversion(w http.ResponseWriter, r *http.Request) {
	if !s.agentsEnabled(w, r) {
		return
	}
	var in conversionRequest
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	if in.Preset == "" {
		in.Preset = "managed"
	}
	id, p, err := s.d.Agents.StartConversion(r.Context(), agentsvc.ConversionOptions{HostIDs: in.HostIDs, Preset: in.Preset, Canary: in.Canary,
		BatchSize: in.BatchSize, MaxFailures: in.MaxFailures, UserID: principalFrom(r.Context()).UserID})
	if err != nil {
		writeProblem(w, r, errValidation(err.Error()))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"rollout_id": id, "preview": p})
}
