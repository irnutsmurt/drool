package api

import (
	"database/sql"
	"errors"
	"net/http"

	"drool/internal/audit"
	"drool/internal/auth"
	"drool/internal/monitor/monsvc"
	"drool/internal/store/db"
)

// Monitoring health overrides (M12.1): what M11 considers unhealthy, at
// global, group, host and subject scope. Not notification routing (M12).
//
// Permissions: reading needs monitoring.read. Global and group changes,
// and group priorities, need monitoring.settings. Host and subject changes
// need monitoring.manage when they make detection stricter or keep it as
// sensitive; a change that weakens detection anywhere (turns a check off,
// raises a threshold, lengthens a duration, clears sooner) needs
// monitoring.settings.

func (s *Server) healthProblem(r *http.Request, err error) *Problem {
	switch {
	case errors.Is(err, monsvc.ErrBadOverride):
		return errValidation(err.Error())
	case errors.Is(err, monsvc.ErrOverrideNotFound):
		return errNotFound("override")
	}
	return s.internalError(r, err)
}

func (s *Server) handleHealthChecks(w http.ResponseWriter, r *http.Request) {
	if !s.monitorEnabled(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"checks": monsvc.HealthChecks()})
}

func (s *Server) handleHealthGlobal(w http.ResponseWriter, r *http.Request) {
	if !s.monitorEnabled(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, s.d.Monitor.GlobalView(r.Context()))
}

func (s *Server) handleHealthGroups(w http.ResponseWriter, r *http.Request) {
	if !s.monitorEnabled(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": s.d.Monitor.GroupsHealth(r.Context())})
}

func (s *Server) handleHealthGroup(w http.ResponseWriter, r *http.Request) {
	if !s.monitorEnabled(w, r) {
		return
	}
	v, err := s.d.Monitor.GroupView(r.Context(), r.PathValue("id"))
	if errors.Is(err, monsvc.ErrOverrideNotFound) {
		writeProblem(w, r, errNotFound("group"))
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleHostHealth(w http.ResponseWriter, r *http.Request) {
	id, ok := s.monitorHost(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	if c := q.Get("check"); c != "" {
		if _, ok := monsvc.Spec(c); !ok {
			writeProblem(w, r, errValidation("unknown check"))
			return
		}
	}
	v, err := s.d.Monitor.HostView(r.Context(), id, q.Get("check"), q.Get("subject"))
	if errors.Is(err, monsvc.ErrOverrideNotFound) {
		writeProblem(w, r, errNotFound("subject"))
		return
	}
	if err != nil {
		writeProblem(w, r, s.internalError(r, err))
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleListOverrides(w http.ResponseWriter, r *http.Request) {
	if !s.monitorEnabled(w, r) {
		return
	}
	q := r.URL.Query()
	switch q.Get("scope") {
	case "", monsvc.ScopeGlobal, monsvc.ScopeGroup, monsvc.ScopeHost:
	default:
		writeProblem(w, r, errValidation("scope must be global, group or host"))
		return
	}
	items := s.d.Monitor.Overrides(r.Context(), q.Get("scope"), q.Get("group_id"), q.Get("host_id"), q.Get("check"))
	if len(items) > 1000 {
		items = items[:1000]
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

type overrideInput struct {
	monsvc.OverrideKey
	SubjectLabel string        `json:"subject_label"`
	Fields       monsvc.Fields `json:"fields"`
}

// checkScope checks the target exists and the caller may change it.
func (s *Server) checkScope(w http.ResponseWriter, r *http.Request, k monsvc.OverrideKey, fields monsvc.Fields) bool {
	ctx := r.Context()
	p := principalFrom(ctx)
	switch k.Scope {
	case monsvc.ScopeGroup:
		if _, err := s.d.Store.R.GetGroup(ctx, k.GroupID); err != nil {
			writeProblem(w, r, errNotFound("group"))
			return false
		}
		fallthrough
	case monsvc.ScopeGlobal:
		if !p.Can(auth.PermMonitoringSettings) {
			writeProblem(w, r, errForbidden("fleet-wide and group health settings require monitoring.settings"))
			return false
		}
	case monsvc.ScopeHost:
		if _, err := s.d.Store.R.GetHost(ctx, k.HostID); err != nil {
			writeProblem(w, r, errNotFound("host"))
			return false
		}
		if !p.Can(auth.PermMonitoringSettings) && s.d.Monitor.Weakens(ctx, k, fields) {
			writeProblem(w, r, errForbidden("this change makes health detection less sensitive (a check off, a higher threshold, a longer duration or a sooner clear), which requires monitoring.settings"))
			return false
		}
	}
	return true
}

func overrideAction(before, after *monsvc.Override) string {
	off := func(o *monsvc.Override) bool {
		if o == nil {
			return false
		}
		v, ok := o.Fields[monsvc.FieldEnabled]
		return ok && v == 0
	}
	switch {
	case off(after) && !off(before):
		return "monitoring.health_check.disable"
	case before == nil:
		return "monitoring.health_override.create"
	case after == nil:
		return "monitoring.health_override.reset"
	}
	return "monitoring.health_override.update"
}

func overrideDetails(k monsvc.OverrideKey, label string, before, after *monsvc.Override) map[string]any {
	var b, a monsvc.Fields
	if before != nil {
		b = before.Fields
		if label == "" {
			label = before.SubjectLabel
		}
	}
	if after != nil {
		a = after.Fields
	}
	d := map[string]any{"scope": k.Scope, "check": k.Check, "changed": monsvc.Diff(b, a)}
	if k.GroupID != "" {
		d["group_id"] = k.GroupID
	}
	if k.HostID != "" {
		d["host_id"] = k.HostID
	}
	if k.Subject != "" {
		d["subject"] = k.Subject
		d["subject_label"] = label
	}
	return d
}

func overrideTarget(k monsvc.OverrideKey) (string, string) {
	switch k.Scope {
	case monsvc.ScopeGroup:
		return "group", k.GroupID
	case monsvc.ScopeHost:
		return "host", k.HostID
	}
	return "settings", "monitoring"
}

func (s *Server) handlePutOverride(w http.ResponseWriter, r *http.Request) {
	if !s.monitorEnabled(w, r) {
		return
	}
	var in overrideInput
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	if in.Fields == nil {
		in.Fields = monsvc.Fields{}
	}
	if err := in.OverrideKey.Validate(); err != nil {
		writeProblem(w, r, errValidation(err.Error()))
		return
	}
	ctx := r.Context()
	s.d.Monitor.ApplyHealthConfig(ctx) // judge against the latest configuration
	if !s.checkScope(w, r, in.OverrideKey, in.Fields) {
		return
	}
	_, after, err := s.putOverride(r, in.OverrideKey, in.Fields, in.SubjectLabel, nil)
	if err != nil {
		writeProblem(w, r, s.healthProblem(r, err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"override": after, "removed": after == nil})
}

// putOverride stores one override and audits it in the same transaction
// (extra details go with the audit record), then judges every host against
// the new configuration. The caller has checked the scope.
func (s *Server) putOverride(r *http.Request, k monsvc.OverrideKey, fields monsvc.Fields, label string, extra map[string]any) (before, after *monsvc.Override, err error) {
	ctx := r.Context()
	var emit func()
	err = s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
		var err error
		before, after, err = s.d.Monitor.PutOverrideTx(ctx, tx, k, fields, label, principalFrom(ctx).UserID, nowMs())
		if err != nil || before == nil && after == nil {
			return err
		}
		tt, tid := overrideTarget(k)
		d := overrideDetails(k, label, before, after)
		for x, v := range extra {
			d[x] = v
		}
		emit, err = s.d.Audit.RecordTx(ctx, db.New(tx), audit.Event{Action: overrideAction(before, after), TargetType: tt, TargetID: tid, Details: d})
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	if emit != nil {
		emit()
	}
	s.d.Monitor.ApplyHealthConfig(ctx)
	return before, after, nil
}

// handleMonitorService ticks or unticks Monitor for one service on a host
// (dogfooding correction batch). It changes the host's own expected state
// for the unit as monsvc.MonitorPlan works out: unticking a service a pack
// or every-host setting monitors makes an exception for this host
// ("ignored") instead of revealing the inherited setting again. The same
// permissions and audit as any host override: an exception that stops
// inherited monitoring weakens detection, and needs monitoring.settings.
func (s *Server) handleMonitorService(w http.ResponseWriter, r *http.Request) {
	id, ok := s.monitorHost(w, r)
	if !ok {
		return
	}
	var in struct {
		Monitor *bool `json:"monitor"`
	}
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	if in.Monitor == nil {
		writeProblem(w, r, errValidation("monitor must be true or false"))
		return
	}
	unit := r.PathValue("unit")
	ctx := r.Context()
	s.d.Monitor.ApplyHealthConfig(ctx)
	k, fields, noop := s.d.Monitor.MonitorPlan(ctx, id, unit, *in.Monitor)
	if err := k.Validate(); err != nil {
		writeProblem(w, r, errValidation(err.Error()))
		return
	}
	var after *monsvc.Override
	if !noop {
		if !s.checkScope(w, r, k, fields) {
			return
		}
		var err error
		if _, after, err = s.putOverride(r, k, fields, unit, map[string]any{"monitor": *in.Monitor}); err != nil {
			writeProblem(w, r, s.healthProblem(r, err))
			return
		}
	}
	out := map[string]any{"changed": !noop, "override": after}
	if row, found := s.d.Monitor.ServiceRowOf(ctx, id, unit); found {
		out["unit"] = row
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteOverride(w http.ResponseWriter, r *http.Request) {
	if !s.monitorEnabled(w, r) {
		return
	}
	ctx := r.Context()
	s.d.Monitor.ApplyHealthConfig(ctx)
	o, err := s.d.Monitor.OverrideByID(ctx, r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, errNotFound("override"))
		return
	}
	k := monsvc.OverrideKey{Scope: o.Scope, GroupID: o.GroupID, HostID: o.HostID, Check: o.Check, Subject: o.Subject}
	if !s.checkScope(w, r, k, monsvc.Fields{}) {
		return
	}
	var emit func()
	err = s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
		before, err := s.d.Monitor.DeleteOverrideTx(ctx, tx, o.ID)
		if err != nil {
			return err
		}
		tt, tid := overrideTarget(k)
		emit, err = s.d.Audit.RecordTx(ctx, db.New(tx), audit.Event{Action: "monitoring.health_override.reset", TargetType: tt, TargetID: tid,
			Details: overrideDetails(k, "", &before, nil)})
		return err
	})
	if err != nil {
		writeProblem(w, r, s.healthProblem(r, err))
		return
	}
	emit()
	s.d.Monitor.ApplyHealthConfig(ctx)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSetGroupPriority(w http.ResponseWriter, r *http.Request) {
	if !s.monitorEnabled(w, r) {
		return
	}
	var in struct {
		Priority *int `json:"priority"`
	}
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	if in.Priority == nil {
		writeProblem(w, r, errValidation("priority is required"))
		return
	}
	ctx := r.Context()
	g, err := s.d.Store.R.GetGroup(ctx, r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, errNotFound("group"))
		return
	}
	s.d.Monitor.ApplyHealthConfig(ctx)
	var emit func()
	err = s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
		before, err := s.d.Monitor.SetGroupPriorityTx(ctx, tx, g.ID, *in.Priority, principalFrom(ctx).UserID, nowMs())
		if err != nil {
			return err
		}
		emit, err = s.d.Audit.RecordTx(ctx, db.New(tx), audit.Event{Action: "monitoring.health_group.priority", TargetType: "group", TargetID: g.ID,
			Details: map[string]any{"name": g.Name, "before": before, "after": *in.Priority}})
		return err
	})
	if err != nil {
		writeProblem(w, r, s.healthProblem(r, err))
		return
	}
	emit()
	s.d.Monitor.ApplyHealthConfig(ctx)
	v, _ := s.d.Monitor.GroupView(ctx, g.ID)
	writeJSON(w, http.StatusOK, v)
}

type previewInput struct {
	monsvc.OverrideKey
	Fields monsvc.Fields `json:"fields"`
	// Priority previews a group priority change instead.
	Priority *int `json:"priority,omitempty"`
}

func (s *Server) handleHealthPreview(w http.ResponseWriter, r *http.Request) {
	if !s.monitorEnabled(w, r) {
		return
	}
	var in previewInput
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	if in.Fields == nil {
		in.Fields = monsvc.Fields{}
	}
	if in.Priority != nil {
		in.Scope = monsvc.ScopeGroup
	}
	pv, err := s.d.Monitor.PreviewChange(r.Context(), in.OverrideKey, in.Fields, in.Priority)
	if err != nil {
		writeProblem(w, r, s.healthProblem(r, err))
		return
	}
	writeJSON(w, http.StatusOK, pv)
}
