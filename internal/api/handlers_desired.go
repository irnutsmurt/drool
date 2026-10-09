package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"drool/internal/audit"
	"drool/internal/auth"
	"drool/internal/desired"
	"drool/internal/model"
)

// Desired state (M14): baselines of typed resources, their compliance on
// every host, and the three operator actions — Review difference
// (read-only), Accept current state (a host-specific declaration) and
// Restore managed state (ordinary jobs). The browser never resolves
// precedence: every effective value and its source come from here.

func (s *Server) desiredEnabled(w http.ResponseWriter, r *http.Request) bool {
	if s.d.Desired == nil {
		writeProblem(w, r, problem(http.StatusNotImplemented, "unavailable", "desired state is not configured"))
		return false
	}
	return true
}

func desiredActor(r *http.Request) desired.Actor {
	p := principalFrom(r.Context())
	if p == nil {
		return desired.Actor{}
	}
	return desired.Actor{UserID: p.UserID, Name: p.Username}
}

func (s *Server) desiredProblem(r *http.Request, err error, what string) *Problem {
	var ve *desired.ValidationError
	switch {
	case errors.As(err, &ve):
		return errValidation(ve.Msg)
	case errors.Is(err, desired.ErrNotFound):
		return errNotFound(what)
	case errors.Is(err, desired.ErrNameTaken):
		return errConflict("name_taken", "a baseline with that name already exists")
	case errors.Is(err, desired.ErrPlanChanged):
		return errConflict("plan_changed", err.Error())
	case errors.Is(err, desired.ErrNothingToRestore):
		return errConflict("nothing_to_restore", err.Error())
	}
	return s.internalError(r, err)
}

// --- fleet ---

type desiredSummaryJSON struct {
	*desired.Summary
	// Other domains with their own desired state and drift, linked rather
	// than duplicated: the firewall (its own designer and transactions)
	// and agents' state versions.
	Firewall *struct {
		Enforced int `json:"enforced"`
		Drift    int `json:"drift"`
	} `json:"firewall,omitempty"`
	Agents *struct {
		StateConflicts int `json:"state_conflicts"`
	} `json:"agents,omitempty"`
}

func (s *Server) handleDesiredSummary(w http.ResponseWriter, r *http.Request) {
	if !s.desiredEnabled(w, r) {
		return
	}
	ctx := r.Context()
	sum, err := s.d.Desired.Summary(ctx)
	if err != nil {
		writeProblem(w, r, s.internalError(r, err))
		return
	}
	out := desiredSummaryJSON{Summary: sum}
	p := principalFrom(ctx)
	if p.Can(auth.PermFirewallRead) {
		out.Firewall = &struct {
			Enforced int `json:"enforced"`
			Drift    int `json:"drift"`
		}{}
		s.d.Store.Reader.QueryRowContext(ctx, `SELECT COALESCE(sum(enforced), 0), COALESCE(sum(drift <> ''), 0) FROM fw_host_state`).
			Scan(&out.Firewall.Enforced, &out.Firewall.Drift)
	}
	if p.Can(auth.PermAgentsRead) {
		out.Agents = &struct {
			StateConflicts int `json:"state_conflicts"`
		}{}
		s.d.Store.Reader.QueryRowContext(ctx, `SELECT count(DISTINCT host_id) FROM state_versions WHERE status <> 'ok'`).Scan(&out.Agents.StateConflicts)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDesiredHosts(w http.ResponseWriter, r *http.Request) {
	if !s.desiredEnabled(w, r) {
		return
	}
	q := r.URL.Query()
	st := q.Get("status")
	switch st {
	case "", "attention", desired.StatusCompliant, desired.StatusDrift, desired.StatusConflict, desired.StatusUnknown,
		desired.StatusReconciling, desired.StatusFailed:
	default:
		writeProblem(w, r, errValidation("status must be attention, compliant, drift, conflict, unknown, reconciling or failed"))
		return
	}
	rows, err := s.d.Desired.Hosts(r.Context(), desired.HostFilter{Status: st, Query: q.Get("q"), BaselineID: q.Get("baseline")})
	if err != nil {
		writeProblem(w, r, s.internalError(r, err))
		return
	}
	writeJSON(w, http.StatusOK, listResponse[desired.HostRow]{Items: rows})
}

// desiredHostJSON is a host's desired state, with its firewall policy's
// state (managed by the Firewall: linked, never duplicated).
type desiredHostJSON struct {
	*desired.HostView
	Firewall *desiredFirewallJSON `json:"firewall,omitempty"`
}

type desiredFirewallJSON struct {
	Mode     string `json:"mode"`
	Enforced bool   `json:"enforced"`
	Drift    string `json:"drift,omitempty"`
}

func (s *Server) handleDesiredHost(w http.ResponseWriter, r *http.Request) {
	if !s.desiredEnabled(w, r) {
		return
	}
	ctx := r.Context()
	v, err := s.d.Desired.Host(ctx, r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, s.desiredProblem(r, err, "host"))
		return
	}
	out := desiredHostJSON{HostView: v}
	if principalFrom(ctx).Can(auth.PermFirewallRead) {
		fw := &desiredFirewallJSON{}
		var enforced int
		if err := s.d.Store.Reader.QueryRowContext(ctx, `SELECT COALESCE(c.mode, 'off'), COALESCE(st.enforced, 0), COALESCE(st.drift, '')
			FROM hosts h LEFT JOIN fw_host_config c ON c.host_id = h.id LEFT JOIN fw_host_state st ON st.host_id = h.id WHERE h.id = ?`, v.HostID).
			Scan(&fw.Mode, &enforced, &fw.Drift); err == nil {
			fw.Enforced = enforced == 1
			out.Firewall = fw
		}
	}
	writeJSON(w, http.StatusOK, out)
}

type desiredResourceJSON struct {
	Resource *desired.ResourceView  `json:"resource"`
	History  []desired.HistoryEntry `json:"history"`
}

func (s *Server) handleDesiredResource(w http.ResponseWriter, r *http.Request) {
	if !s.desiredEnabled(w, r) {
		return
	}
	v, h, err := s.d.Desired.Resource(r.Context(), r.PathValue("id"), r.URL.Query().Get("key"))
	if err != nil {
		writeProblem(w, r, s.desiredProblem(r, err, "resource"))
		return
	}
	writeJSON(w, http.StatusOK, desiredResourceJSON{Resource: v, History: h})
}

// handleDesiredReview is Review difference: read-only. Services and
// packages are compared from what DROOL observed; a managed file is
// compared on the host (hashes and metadata), with a text diff for small
// text files when the caller may read file contents (files.read, and
// files.elevate for files read as root).
func (s *Server) handleDesiredReview(w http.ResponseWriter, r *http.Request) {
	if !s.desiredEnabled(w, r) {
		return
	}
	ctx := r.Context()
	hostID, key := r.PathValue("id"), r.URL.Query().Get("key")
	v, _, err := s.d.Desired.Resource(ctx, hostID, key)
	if err != nil {
		writeProblem(w, r, s.desiredProblem(r, err, "resource"))
		return
	}
	out := map[string]any{"resource": v}
	if v.Kind == desired.KindFile && v.Desired != nil {
		p := principalFrom(ctx)
		content := p.Can(auth.PermFilesRead) && (!v.Desired.Elevated || p.Can(auth.PermFilesElevate))
		if content {
			if err := s.d.Audit.Record(ctx, audit.Event{Action: "desired.review", TargetType: "host", TargetID: hostID,
				Details: map[string]any{"path": v.Name, "elevated": v.Desired.Elevated}}); err != nil {
				writeProblem(w, r, errInternal)
				return
			}
		}
		fr, err := s.d.Desired.ReviewFile(ctx, hostID, key, content)
		if err != nil {
			var ve *desired.ValidationError
			if !errors.As(err, &ve) {
				writeProblem(w, r, s.internalError(r, err))
				return
			}
			out["file_error"] = ve.Msg
		} else {
			out["file"] = fr
		}
		out["content_allowed"] = content
	}
	writeJSON(w, http.StatusOK, out)
}

type desiredRefreshRequest struct {
	HostIDs []string `json:"host_ids"`
}

func (s *Server) handleDesiredRefresh(w http.ResponseWriter, r *http.Request) {
	if !s.desiredEnabled(w, r) {
		return
	}
	var req desiredRefreshRequest
	if id := r.PathValue("id"); id != "" {
		req.HostIDs = []string{id}
	} else if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	snap := s.d.Desired.Snapshot()
	if snap == nil {
		writeProblem(w, r, problem(http.StatusServiceUnavailable, "starting", "desired state is starting"))
		return
	}
	if len(req.HostIDs) == 0 {
		for id := range snap.Hosts {
			req.HostIDs = append(req.HostIDs, id)
		}
	}
	if len(req.HostIDs) > 1000 {
		writeProblem(w, r, errValidation("refresh at most 1000 hosts at once"))
		return
	}
	for _, id := range req.HostIDs {
		if _, ok := snap.Hosts[id]; !ok {
			writeProblem(w, r, errNotFound("host"))
			return
		}
	}
	// Checks read package databases and files on the hosts.
	if err := s.d.Audit.Record(r.Context(), audit.Event{Action: "desired.refresh", TargetType: "host",
		Details: map[string]any{"hosts": len(req.HostIDs)}}); err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	s.d.Desired.Refresh(req.HostIDs)
	writeJSON(w, http.StatusAccepted, map[string]any{"hosts": len(req.HostIDs)})
}

type desiredAcceptRequest struct {
	Keys []string `json:"keys"`
	Note string   `json:"note"`
}

func (s *Server) handleDesiredAccept(w http.ResponseWriter, r *http.Request) {
	if !s.desiredEnabled(w, r) {
		return
	}
	var req desiredAcceptRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	res, err := s.d.Desired.Accept(r.Context(), r.PathValue("id"), req.Keys, req.Note, desiredActor(r))
	if err != nil {
		writeProblem(w, r, s.desiredProblem(r, err, "host"))
		return
	}
	writeJSON(w, http.StatusOK, res)
}

type desiredOverrideRequest struct {
	Resource *desired.Resource `json:"resource"`
	Key      string            `json:"key"`
	Excluded bool              `json:"excluded"`
	Note     string            `json:"note"`
}

func (s *Server) handleDesiredSetOverride(w http.ResponseWriter, r *http.Request) {
	if !s.desiredEnabled(w, r) {
		return
	}
	var req desiredOverrideRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	hostID := r.PathValue("id")
	if err := s.d.Desired.SetOverride(r.Context(), hostID, req.Resource, req.Key, req.Excluded, req.Note, desiredActor(r)); err != nil {
		writeProblem(w, r, s.desiredProblem(r, err, "host"))
		return
	}
	s.handleDesiredHost(w, r)
}

func (s *Server) handleDesiredResetOverride(w http.ResponseWriter, r *http.Request) {
	if !s.desiredEnabled(w, r) {
		return
	}
	if err := s.d.Desired.ResetOverride(r.Context(), r.PathValue("id"), r.URL.Query().Get("key")); err != nil {
		writeProblem(w, r, s.desiredProblem(r, err, "host-specific setting"))
		return
	}
	s.handleDesiredHost(w, r)
}

type desiredRestoreRequest struct {
	desired.Selection
	desired.RestoreOptions
	// PlanHash: the preview's hash; the restore is refused when what
	// would be corrected changed since.
	PlanHash string `json:"plan_hash"`
}

// restorePerms are what restoring a plan needs beyond desired.restore:
// the corrective operations' own permissions.
func restorePerms(p *desired.Plan) []auth.Permission {
	perms := []auth.Permission{auth.PermJobsExecute}
	for _, n := range p.Needs {
		perms = append(perms, auth.Permission(n))
	}
	return perms
}

func (s *Server) handleDesiredRestorePreview(w http.ResponseWriter, r *http.Request) {
	if !s.desiredEnabled(w, r) {
		return
	}
	var req desiredRestoreRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	plan, err := s.d.Desired.PreviewRestore(r.Context(), req.Selection, req.RestoreOptions)
	if err != nil {
		writeProblem(w, r, s.desiredProblem(r, err, "host"))
		return
	}
	// Say up front which permissions the caller lacks for it.
	missing := []string{}
	pr := principalFrom(r.Context())
	for _, perm := range restorePerms(plan) {
		if !pr.Can(perm) {
			missing = append(missing, string(perm))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"plan": plan, "missing_permissions": missing})
}

func (s *Server) handleDesiredRestore(w http.ResponseWriter, r *http.Request) {
	if !s.desiredEnabled(w, r) {
		return
	}
	var req desiredRestoreRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	if strings.TrimSpace(req.PlanHash) == "" {
		writeProblem(w, r, errValidation("preview the correction first (plan_hash)"))
		return
	}
	ctx := r.Context()
	plan, err := s.d.Desired.PreviewRestoreNoCheck(ctx, req.Selection, req.RestoreOptions)
	if err != nil {
		writeProblem(w, r, s.desiredProblem(r, err, "host"))
		return
	}
	if !s.needs(w, r, restorePerms(plan), "restoring managed state") {
		return
	}
	res, err := s.d.Desired.Restore(ctx, req.Selection, req.RestoreOptions, req.PlanHash, desiredActor(r))
	if err != nil {
		writeProblem(w, r, s.desiredProblem(r, err, "host"))
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

func (s *Server) handleDesiredHistory(w http.ResponseWriter, r *http.Request) {
	if !s.desiredEnabled(w, r) {
		return
	}
	q := r.URL.Query()
	f := desired.HistoryFilter{HostID: q.Get("host"), Key: q.Get("key"), BaselineID: q.Get("baseline"), Kind: q.Get("kind")}
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			writeProblem(w, r, errValidation("before must be a history entry id"))
			return
		}
		f.Before = n
	}
	f.Limit = 100
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			writeProblem(w, r, errValidation("limit must be between 1 and 500"))
			return
		}
		f.Limit = n
	}
	hs, err := s.d.Desired.History(r.Context(), f)
	if err != nil {
		writeProblem(w, r, s.internalError(r, err))
		return
	}
	out := listResponse[desired.HistoryEntry]{Items: hs}
	if len(hs) == f.Limit {
		out.NextCursor = strconv.FormatInt(hs[len(hs)-1].ID, 10)
	}
	writeJSON(w, http.StatusOK, out)
}

// --- baselines ---

func (s *Server) handleListBaselines(w http.ResponseWriter, r *http.Request) {
	if !s.desiredEnabled(w, r) {
		return
	}
	vs, err := s.d.Desired.Baselines(r.Context(), r.URL.Query().Get("archived") == "true")
	if err != nil {
		writeProblem(w, r, s.internalError(r, err))
		return
	}
	if vs == nil {
		vs = []*desired.BaselineView{}
	}
	writeJSON(w, http.StatusOK, listResponse[*desired.BaselineView]{Items: vs})
}

func (s *Server) handleCreateBaseline(w http.ResponseWriter, r *http.Request) {
	if !s.desiredEnabled(w, r) {
		return
	}
	var in desired.BaselineInput
	if p := decodeJSONLimit(w, r, &in, 1<<20); p != nil {
		writeProblem(w, r, p)
		return
	}
	v, err := s.d.Desired.CreateBaseline(r.Context(), in, desiredActor(r))
	if err != nil {
		writeProblem(w, r, s.desiredProblem(r, err, "baseline"))
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

type baselinePreviewRequest struct {
	ID string `json:"id"`
	desired.BaselineInput
}

func (s *Server) handlePreviewBaseline(w http.ResponseWriter, r *http.Request) {
	if !s.desiredEnabled(w, r) {
		return
	}
	var req baselinePreviewRequest
	if p := decodeJSONLimit(w, r, &req, 1<<20); p != nil {
		writeProblem(w, r, p)
		return
	}
	imp, err := s.d.Desired.PreviewBaseline(r.Context(), req.ID, req.BaselineInput)
	if err != nil {
		writeProblem(w, r, s.desiredProblem(r, err, "baseline"))
		return
	}
	writeJSON(w, http.StatusOK, imp)
}

func (s *Server) handleGetBaseline(w http.ResponseWriter, r *http.Request) {
	if !s.desiredEnabled(w, r) {
		return
	}
	v, err := s.d.Desired.Baseline(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, s.desiredProblem(r, err, "baseline"))
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleUpdateBaseline(w http.ResponseWriter, r *http.Request) {
	if !s.desiredEnabled(w, r) {
		return
	}
	var in desired.BaselineInput
	if p := decodeJSONLimit(w, r, &in, 1<<20); p != nil {
		writeProblem(w, r, p)
		return
	}
	v, _, err := s.d.Desired.UpdateBaseline(r.Context(), r.PathValue("id"), in, desiredActor(r))
	if err != nil {
		writeProblem(w, r, s.desiredProblem(r, err, "baseline"))
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleArchiveBaseline(w http.ResponseWriter, r *http.Request) {
	if !s.desiredEnabled(w, r) {
		return
	}
	if err := s.d.Desired.ArchiveBaseline(r.Context(), r.PathValue("id")); err != nil {
		writeProblem(w, r, s.desiredProblem(r, err, "baseline"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleBaselineRevision(w http.ResponseWriter, r *http.Request) {
	if !s.desiredEnabled(w, r) {
		return
	}
	rev, err := strconv.Atoi(r.PathValue("rev"))
	if err != nil || rev < 1 {
		writeProblem(w, r, errNotFound("revision"))
		return
	}
	in, info, err := s.d.Desired.Revision(r.Context(), r.PathValue("id"), rev)
	if err != nil {
		writeProblem(w, r, s.desiredProblem(r, err, "revision"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revision": info, "baseline": in})
}

type baselineAutoRequest struct {
	Enabled  bool            `json:"enabled"`
	Strategy *model.Strategy `json:"strategy"`
}

// handleBaselineAuto turns automatic restore on or off. Whoever turns it
// on becomes its accountable owner, and must hold everything automatic
// corrections need.
func (s *Server) handleBaselineAuto(w http.ResponseWriter, r *http.Request) {
	if !s.desiredEnabled(w, r) {
		return
	}
	var req baselineAutoRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	// The corrections' own permissions (files) are checked against the
	// owner before every automatic job.
	if req.Enabled && !s.needs(w, r, []auth.Permission{auth.PermDesiredRestore, auth.PermJobsExecute}, "owning automatic restore") {
		return
	}
	v, err := s.d.Desired.SetAuto(r.Context(), r.PathValue("id"), req.Enabled, req.Strategy, desiredActor(r))
	if err != nil {
		writeProblem(w, r, s.desiredProblem(r, err, "baseline"))
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// --- group priorities ---

func (s *Server) handleDesiredGroups(w http.ResponseWriter, r *http.Request) {
	if !s.desiredEnabled(w, r) {
		return
	}
	gs, err := s.d.Desired.GroupPriorities(r.Context())
	if err != nil {
		writeProblem(w, r, s.internalError(r, err))
		return
	}
	writeJSON(w, http.StatusOK, listResponse[desired.GroupPriority]{Items: gs})
}

type groupPriorityRequest struct {
	Priority int `json:"priority"`
}

func (s *Server) handleSetDesiredPriority(w http.ResponseWriter, r *http.Request) {
	if !s.desiredEnabled(w, r) {
		return
	}
	var req groupPriorityRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	if err := s.d.Desired.SetGroupPriority(r.Context(), r.PathValue("id"), req.Priority); err != nil {
		writeProblem(w, r, s.desiredProblem(r, err, "group"))
		return
	}
	s.handleDesiredGroups(w, r)
}

func (s *Server) handlePreviewDesiredPriority(w http.ResponseWriter, r *http.Request) {
	if !s.desiredEnabled(w, r) {
		return
	}
	var req groupPriorityRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	imp, err := s.d.Desired.PreviewPriority(r.Context(), r.PathValue("id"), req.Priority)
	if err != nil {
		writeProblem(w, r, s.desiredProblem(r, err, "group"))
		return
	}
	writeJSON(w, http.StatusOK, imp)
}
