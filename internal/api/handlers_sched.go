package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"drool/internal/audit"
	"drool/internal/auth"
	"drool/internal/engine"
	"drool/internal/model"
	"drool/internal/sched"
)

// Scheduling, fleet automation and workflows (M13). Schedules decide when
// (and with which rollout) ordinary jobs are created; workflows are
// reusable, revisioned job specifications. Every run is a normal job.

func (s *Server) schedEnabled(w http.ResponseWriter, r *http.Request) bool {
	if s.d.Sched == nil {
		writeProblem(w, r, problem(http.StatusNotImplemented, "unavailable", "scheduling is not configured"))
		return false
	}
	return true
}

func schedActor(r *http.Request) sched.Actor {
	p := principalFrom(r.Context())
	if p == nil {
		return sched.Actor{}
	}
	return sched.Actor{UserID: p.UserID, Name: p.Username}
}

func (s *Server) schedProblem(r *http.Request, err error, what string) *Problem {
	var ve *sched.ValidationError
	switch {
	case errors.As(err, &ve):
		return errValidation(ve.Msg)
	case errors.Is(err, sched.ErrNotFound):
		return errNotFound(what)
	case errors.Is(err, sched.ErrNameTaken):
		return errConflict("name_taken", "a "+what+" with that name already exists")
	case errors.Is(err, sched.ErrBusy):
		return errConflict("busy", "the previous run is still active; cancel it or wait for it to finish")
	}
	return s.internalError(r, err)
}

// needs refuses unless the caller holds every permission.
func (s *Server) needs(w http.ResponseWriter, r *http.Request, perms []auth.Permission, why string) bool {
	p := principalFrom(r.Context())
	for _, perm := range perms {
		if !p.Can(perm) {
			_ = s.d.Audit.Record(r.Context(), audit.Event{Action: "authz.denied", Result: audit.Denied,
				Details: map[string]any{"permission": string(perm), "method": r.Method, "path": r.URL.Path}})
			writeProblem(w, r, errForbidden(why+" requires "+string(perm)))
			return false
		}
	}
	return true
}

// --- workflows ---

func (s *Server) handleListWorkflows(w http.ResponseWriter, r *http.Request) {
	if !s.schedEnabled(w, r) {
		return
	}
	ws, err := s.d.Sched.ListWorkflows(r.Context(), r.URL.Query().Get("archived") == "true")
	if err != nil {
		writeProblem(w, r, s.internalError(r, err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": ws})
}

func (s *Server) handleWorkflowTemplates(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"items": sched.Templates()})
}

func (s *Server) handleCreateWorkflow(w http.ResponseWriter, r *http.Request) {
	if !s.schedEnabled(w, r) {
		return
	}
	var in sched.WorkflowInput
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	var emit func()
	wf, err := s.d.Sched.CreateWorkflow(ctx, in, schedActor(r), func(tx *sql.Tx, wf *sched.Workflow) error {
		return s.auditTx(ctx, tx, audit.Event{Action: "workflow.create", TargetType: "workflow", TargetID: wf.ID,
			Details: map[string]any{"name": wf.Name, "revision": 1, "steps": stepTypes(in.Steps)}}, &emit)
	})
	if err != nil {
		writeProblem(w, r, s.schedProblem(r, err, "workflow"))
		return
	}
	emit()
	writeJSON(w, http.StatusCreated, wf)
}

func stepTypes(steps []model.Operation) []string {
	out := make([]string, len(steps))
	for i, st := range steps {
		out[i] = st.Type
	}
	return out
}

func (s *Server) handleGetWorkflow(w http.ResponseWriter, r *http.Request) {
	if !s.schedEnabled(w, r) {
		return
	}
	wf, err := s.d.Sched.GetWorkflow(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, s.schedProblem(r, err, "workflow"))
		return
	}
	writeJSON(w, http.StatusOK, wf)
}

func (s *Server) handleUpdateWorkflow(w http.ResponseWriter, r *http.Request) {
	if !s.schedEnabled(w, r) {
		return
	}
	var in sched.WorkflowInput
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	emit := func() {}
	wf, changed, err := s.d.Sched.UpdateWorkflow(ctx, r.PathValue("id"), in, schedActor(r), func(tx *sql.Tx, before, after *sched.Revision) error {
		return s.auditTx(ctx, tx, audit.Event{Action: "workflow.update", TargetType: "workflow", TargetID: r.PathValue("id"),
			Details: map[string]any{"name": after.Name, "revision": after.Revision, "previous_revision": before.Revision,
				"steps_before": stepTypes(before.Steps), "steps_after": stepTypes(after.Steps)}}, &emit)
	})
	if err != nil {
		writeProblem(w, r, s.schedProblem(r, err, "workflow"))
		return
	}
	emit()
	writeJSON(w, http.StatusOK, map[string]any{"workflow": wf, "changed": changed})
}

func (s *Server) handleArchiveWorkflow(w http.ResponseWriter, r *http.Request) {
	if !s.schedEnabled(w, r) {
		return
	}
	ctx := r.Context()
	emit := func() {}
	err := s.d.Sched.ArchiveWorkflow(ctx, r.PathValue("id"), func(tx *sql.Tx) error {
		return s.auditTx(ctx, tx, audit.Event{Action: "workflow.archive", TargetType: "workflow", TargetID: r.PathValue("id")}, &emit)
	})
	if err != nil {
		writeProblem(w, r, s.schedProblem(r, err, "workflow"))
		return
	}
	emit()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleWorkflowRevision(w http.ResponseWriter, r *http.Request) {
	if !s.schedEnabled(w, r) {
		return
	}
	rev, err := strconv.Atoi(r.PathValue("rev"))
	if err != nil {
		writeProblem(w, r, errNotFound("revision"))
		return
	}
	out, err := s.d.Sched.WorkflowRevision(r.Context(), r.PathValue("id"), rev)
	if err != nil {
		writeProblem(w, r, s.schedProblem(r, err, "revision"))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type workflowRunInput struct {
	Revision int              `json:"revision"` // 0: latest
	Targets  model.TargetSpec `json:"targets"`
	Strategy *model.Strategy  `json:"strategy"` // nil: the workflow's default rollout
	Name     string           `json:"name"`
}

// handleRunWorkflow runs a workflow revision now as an ordinary job.
func (s *Server) handleRunWorkflow(w http.ResponseWriter, r *http.Request) {
	if !s.schedEnabled(w, r) {
		return
	}
	var in workflowRunInput
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	wf, err := s.d.Sched.GetWorkflow(ctx, r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, s.schedProblem(r, err, "workflow"))
		return
	}
	rev := in.Revision
	if rev == 0 {
		rev = wf.LatestRevision
	}
	rv, err := s.d.Sched.WorkflowRevision(ctx, wf.ID, rev)
	if err != nil {
		writeProblem(w, r, s.schedProblem(r, err, "revision"))
		return
	}
	strategy := rv.Strategy
	if in.Strategy != nil {
		strategy = *in.Strategy
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = rv.Name
	}
	spec := model.JobSpec{Name: name, Steps: rv.Steps, Targets: in.Targets, Strategy: strategy}
	if !s.checkFileSteps(w, r, spec, false) {
		return
	}
	s.submitJobWith(w, r, spec, engine.SubmitOptions{Origin: engine.Origin{WorkflowID: wf.ID, WorkflowRevision: rev}})
}

// --- schedules ---

func (s *Server) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	if !s.schedEnabled(w, r) {
		return
	}
	q := r.URL.Query()
	f := sched.ScheduleFilter{Kind: q.Get("kind"), Archived: q.Get("archived") == "true"}
	switch f.Kind {
	case "", sched.ScheduleJob, sched.ScheduleMaintenance:
	default:
		writeProblem(w, r, errValidation(`kind must be "job" or "maintenance"`))
		return
	}
	if q.Get("owner") == "me" {
		f.OwnerID = principalFrom(r.Context()).UserID
	}
	out, err := s.d.Sched.ListSchedules(r.Context(), f)
	if err != nil {
		writeProblem(w, r, s.internalError(r, err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// stepsOf returns what a schedule input runs (to check permissions).
func (s *Server) stepsOf(r *http.Request, in sched.ScheduleInput) ([]model.Operation, *Problem) {
	if in.Kind == sched.ScheduleMaintenance {
		return nil, nil
	}
	if in.Workflow != nil {
		rev := in.Workflow.Revision
		if rev == 0 {
			wf, err := s.d.Sched.GetWorkflow(r.Context(), in.Workflow.ID)
			if err != nil {
				return nil, errValidation("workflow " + in.Workflow.ID + " not found")
			}
			rev = wf.LatestRevision
		}
		rv, err := s.d.Sched.WorkflowRevision(r.Context(), in.Workflow.ID, rev)
		if err != nil {
			return nil, errValidation("that workflow revision doesn't exist")
		}
		return rv.Steps, nil
	}
	return in.Steps, nil
}

// canRun checks the caller holds what running the schedule needs.
func (s *Server) canRun(w http.ResponseWriter, r *http.Request, in sched.ScheduleInput) bool {
	steps, p := s.stepsOf(r, in)
	if p != nil {
		writeProblem(w, r, p)
		return false
	}
	why := "scheduling this"
	if in.Kind == sched.ScheduleMaintenance {
		why = "scheduling maintenance"
	}
	return s.needs(w, r, sched.RequiredPermissions(in.Kind, steps), why)
}

func scheduleDetails(sc *sched.Schedule) map[string]any {
	d := map[string]any{"name": sc.Name, "kind": sc.Kind, "when": sc.When, "revision": sc.Revision, "enabled": sc.Enabled,
		"targets": sc.Config.Targets}
	if sc.Owner != nil {
		d["owner_id"], d["owner"] = sc.Owner.ID, sc.Owner.Name
	}
	if sc.Config.Workflow != nil {
		d["workflow_id"], d["workflow_revision"] = sc.Config.Workflow.ID, sc.Config.Workflow.Revision
	} else if sc.Kind == sched.ScheduleJob {
		d["steps"] = stepTypes(sc.Config.Steps)
	}
	if sc.Kind == sched.ScheduleMaintenance {
		d["duration_minutes"] = sc.Config.DurationMinutes
	}
	if sc.Config.WindowID != "" {
		d["window_id"] = sc.Config.WindowID
	}
	return d
}

func (s *Server) handleCreateSchedule(w http.ResponseWriter, r *http.Request) {
	if !s.schedEnabled(w, r) {
		return
	}
	var in sched.ScheduleInput
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	if in.Kind == "" {
		in.Kind = sched.ScheduleJob
	}
	if !s.canRun(w, r, in) {
		return
	}
	if in.Kind == sched.ScheduleJob && !s.checkFileSteps(w, r, model.JobSpec{Steps: in.Steps}, false) {
		return
	}
	ctx := r.Context()
	emit := func() {}
	sc, err := s.d.Sched.CreateSchedule(ctx, in, schedActor(r), func(tx *sql.Tx, sc *sched.Schedule) error {
		return s.auditTx(ctx, tx, audit.Event{Action: "schedule.create", TargetType: "schedule", TargetID: sc.ID, Details: scheduleDetails(sc)}, &emit)
	})
	if err != nil {
		writeProblem(w, r, s.schedProblem(r, err, "schedule"))
		return
	}
	emit()
	writeJSON(w, http.StatusCreated, sc)
}

// mine loads a schedule the caller may change: their own, or any with
// schedules.admin.
func (s *Server) mine(w http.ResponseWriter, r *http.Request) (*sched.Schedule, bool) {
	sc, err := s.d.Sched.GetSchedule(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, s.schedProblem(r, err, "schedule"))
		return nil, false
	}
	p := principalFrom(r.Context())
	if (sc.Owner == nil || sc.Owner.ID != p.UserID) && !p.Can(auth.PermSchedulesAdmin) {
		writeProblem(w, r, errForbidden("only its owner (or someone with schedules.admin) may change this schedule"))
		return nil, false
	}
	return sc, true
}

func (s *Server) handleGetSchedule(w http.ResponseWriter, r *http.Request) {
	if !s.schedEnabled(w, r) {
		return
	}
	sc, err := s.d.Sched.GetSchedule(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, s.schedProblem(r, err, "schedule"))
		return
	}
	writeJSON(w, http.StatusOK, sc)
}

func (s *Server) handleUpdateSchedule(w http.ResponseWriter, r *http.Request) {
	if !s.schedEnabled(w, r) {
		return
	}
	cur, ok := s.mine(w, r)
	if !ok {
		return
	}
	var in sched.ScheduleInput
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	in.Kind = cur.Kind
	if !s.canRun(w, r, in) {
		return
	}
	if in.Kind == sched.ScheduleJob && !s.checkFileSteps(w, r, model.JobSpec{Steps: in.Steps}, false) {
		return
	}
	ctx := r.Context()
	p := principalFrom(ctx)
	// Whoever reviews and changes what runs owns it from then on: a
	// schedule never runs someone else's edits with its old owner's rights.
	transfer := cur.Owner == nil || cur.Owner.ID != p.UserID
	emit := func() {}
	sc, err := s.d.Sched.UpdateSchedule(ctx, cur.ID, in, schedActor(r), transfer, func(tx *sql.Tx, before, after *sched.Schedule) error {
		d := scheduleDetails(after)
		d["previous_revision"] = before.Revision
		if transfer {
			d["ownership"] = "transferred to the editor"
			if before.Owner != nil {
				d["previous_owner"] = before.Owner.Name
			}
		}
		if before.Config.Workflow != nil && after.Config.Workflow != nil && before.Config.Workflow.Revision != after.Config.Workflow.Revision {
			d["workflow_revision_before"] = before.Config.Workflow.Revision
		}
		return s.auditTx(ctx, tx, audit.Event{Action: "schedule.update", TargetType: "schedule", TargetID: cur.ID, Details: d}, &emit)
	})
	if err != nil {
		writeProblem(w, r, s.schedProblem(r, err, "schedule"))
		return
	}
	emit()
	writeJSON(w, http.StatusOK, sc)
}

func (s *Server) handleEnableSchedule(w http.ResponseWriter, r *http.Request) {
	if !s.schedEnabled(w, r) {
		return
	}
	cur, ok := s.mine(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	// The owner must (still) be able to run it.
	if cur.Owner == nil {
		writeProblem(w, r, errValidation("this schedule has no owner; an administrator must assign one first"))
		return
	}
	in := sched.ScheduleInput{Kind: cur.Kind, Config: cur.Config}
	steps, prob := s.stepsOf(r, in)
	if prob != nil {
		writeProblem(w, r, prob)
		return
	}
	u, err := s.d.Store.R.GetUser(ctx, cur.Owner.ID)
	if err != nil || u.Disabled != 0 {
		writeProblem(w, r, errValidation("the owner's account is disabled; an administrator can assign a new owner"))
		return
	}
	if p := sched.MissingPermission(u.Role, sched.RequiredPermissions(cur.Kind, steps)); p != "" {
		writeProblem(w, r, errValidation("the owner ("+u.Username+") doesn't have the "+string(p)+" permission this schedule needs"))
		return
	}
	s.setEnabled(w, r, cur.ID, true)
}

func (s *Server) handleDisableSchedule(w http.ResponseWriter, r *http.Request) {
	if !s.schedEnabled(w, r) {
		return
	}
	s.setEnabled(w, r, r.PathValue("id"), false)
}

func (s *Server) setEnabled(w http.ResponseWriter, r *http.Request, id string, on bool) {
	ctx := r.Context()
	emit := func() {}
	action := "schedule.disable"
	if on {
		action = "schedule.enable"
	}
	sc, err := s.d.Sched.SetEnabled(ctx, id, on, func(tx *sql.Tx, sc *sched.Schedule) error {
		d := map[string]any{"name": sc.Name}
		if !on && sc.ActiveJobID != "" {
			d["running_job_continues"] = sc.ActiveJobID
		}
		return s.auditTx(ctx, tx, audit.Event{Action: action, TargetType: "schedule", TargetID: id, Details: d}, &emit)
	})
	if err != nil {
		writeProblem(w, r, s.schedProblem(r, err, "schedule"))
		return
	}
	emit()
	writeJSON(w, http.StatusOK, sc)
}

func (s *Server) handleArchiveSchedule(w http.ResponseWriter, r *http.Request) {
	if !s.schedEnabled(w, r) {
		return
	}
	cur, ok := s.mine(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	emit := func() {}
	err := s.d.Sched.ArchiveSchedule(ctx, cur.ID, func(tx *sql.Tx, sc *sched.Schedule) error {
		return s.auditTx(ctx, tx, audit.Event{Action: "schedule.archive", TargetType: "schedule", TargetID: cur.ID,
			Details: map[string]any{"name": sc.Name}}, &emit)
	})
	if err != nil {
		writeProblem(w, r, s.schedProblem(r, err, "schedule"))
		return
	}
	emit()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRunScheduleNow(w http.ResponseWriter, r *http.Request) {
	if !s.schedEnabled(w, r) {
		return
	}
	ctx := r.Context()
	sc, err := s.d.Sched.GetSchedule(ctx, r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, s.schedProblem(r, err, "schedule"))
		return
	}
	// A manual run is the caller's own action, with the caller's rights.
	if !s.canRun(w, r, sched.ScheduleInput{Kind: sc.Kind, Config: sc.Config}) {
		return
	}
	res, err := s.d.Sched.RunNow(ctx, sc.ID, schedActor(r))
	if err != nil {
		writeProblem(w, r, s.schedProblem(r, err, "schedule"))
		return
	}
	_ = s.d.Audit.Record(ctx, audit.Event{Action: "schedule.run_now", TargetType: "schedule", TargetID: sc.ID,
		Details: map[string]any{"name": sc.Name, "occurrence_id": res.OccurrenceID, "result": res.Status}})
	out := map[string]any{"occurrence_id": res.OccurrenceID, "status": res.Status}
	if sc.Kind == sched.ScheduleJob {
		out["job_id"] = res.JobID
	} else {
		out["maintenance_id"] = res.JobID
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleReassignSchedule(w http.ResponseWriter, r *http.Request) {
	if !s.schedEnabled(w, r) {
		return
	}
	var in struct {
		UserID string `json:"user_id"`
	}
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	emit := func() {}
	sc, err := s.d.Sched.Reassign(ctx, r.PathValue("id"), in.UserID, func(tx *sql.Tx, before, after *sched.Schedule) error {
		d := map[string]any{"name": after.Name, "owner_id": in.UserID}
		if after.Owner != nil {
			d["owner"] = after.Owner.Name
		}
		if before.Owner != nil {
			d["previous_owner"] = before.Owner.Name
		}
		return s.auditTx(ctx, tx, audit.Event{Action: "schedule.reassign", TargetType: "schedule", TargetID: after.ID, Details: d}, &emit)
	})
	if err != nil {
		writeProblem(w, r, s.schedProblem(r, err, "schedule"))
		return
	}
	emit()
	writeJSON(w, http.StatusOK, sc)
}

func (s *Server) handleScheduleOccurrences(w http.ResponseWriter, r *http.Request) {
	if !s.schedEnabled(w, r) {
		return
	}
	q := r.URL.Query()
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit < 0 || limit > 200 {
		writeProblem(w, r, errValidation("limit must be 1–200"))
		return
	}
	if _, err := s.d.Sched.GetSchedule(r.Context(), r.PathValue("id")); err != nil {
		writeProblem(w, r, s.schedProblem(r, err, "schedule"))
		return
	}
	out, err := s.d.Sched.Occurrences(r.Context(), r.PathValue("id"), before, q.Get("before_id"), limit)
	if err != nil {
		writeProblem(w, r, s.internalError(r, err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handlePreviewSchedule(w http.ResponseWriter, r *http.Request) {
	if !s.schedEnabled(w, r) {
		return
	}
	var in sched.ScheduleInput
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	if in.Kind == "" {
		in.Kind = sched.ScheduleJob
	}
	pv, err := s.d.Sched.PreviewSchedule(r.Context(), in)
	if err != nil {
		var ve *engine.ValidationError
		if errors.As(err, &ve) {
			writeProblem(w, r, errValidation(ve.Msg))
			return
		}
		writeProblem(w, r, s.schedProblem(r, err, "schedule"))
		return
	}
	writeJSON(w, http.StatusOK, pv)
}

func (s *Server) handleNextRuns(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Recurrence sched.Recurrence `json:"recurrence"`
		TimeZone   string           `json:"time_zone"`
		Count      int              `json:"count"`
	}
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	if err := in.Recurrence.Normalize(); err != nil {
		writeProblem(w, r, errValidation(err.Error()))
		return
	}
	runs, err := sched.NextRuns(in.Recurrence, in.TimeZone, time.Now(), in.Count)
	if err != nil {
		writeProblem(w, r, s.schedProblem(r, err, "schedule"))
		return
	}
	loc, _ := sched.LoadZone(in.TimeZone)
	writeJSON(w, http.StatusOK, map[string]any{"when": in.Recurrence.Describe(loc), "runs": runs})
}
