package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"time"

	"drool/internal/alerts"
	"drool/internal/audit"
	"drool/internal/auth"
	"drool/internal/monitor/monsvc"
	"drool/internal/notify"
	"drool/internal/store/db"
)

// Alerts, maintenance and notifications (M12, ARCHITECTURE-ALPHA §28).

func (s *Server) alertsEnabled(w http.ResponseWriter, r *http.Request) bool {
	if s.d.Alerts == nil {
		writeProblem(w, r, problem(http.StatusNotImplemented, "unavailable", "alerting is not configured"))
		return false
	}
	return true
}

// alertProblem maps the alert service's errors.
func (s *Server) alertProblem(r *http.Request, err error, what string) *Problem {
	var v *alerts.ValidationError
	switch {
	case errors.As(err, &v):
		return errValidation(v.Msg)
	case errors.Is(err, alerts.ErrNotFound):
		return errNotFound(what)
	case errors.Is(err, alerts.ErrAlreadyAcked):
		return errConflict("already_acknowledged", err.Error())
	case errors.Is(err, alerts.ErrNotActive):
		return errConflict("not_active", err.Error())
	case errors.Is(err, alerts.ErrNoteTooLong):
		return errValidation(err.Error())
	case errors.Is(err, alerts.ErrDefaultRoute):
		return errConflict("default_route", err.Error())
	case errors.Is(err, alerts.ErrNameTaken):
		return errConflict("name_taken", err.Error())
	}
	s.d.Logs.App.Error("alerting request failed", "path", r.URL.Path, "err", err)
	return errInternal
}

func actorFrom(ctx context.Context) alerts.Actor {
	p := principalFrom(ctx)
	if p == nil {
		return alerts.Actor{}
	}
	return alerts.Actor{UserID: p.UserID, Name: p.Username}
}

func (s *Server) auditTx(ctx context.Context, tx *sql.Tx, ev audit.Event, emit *func()) error {
	e, err := s.d.Audit.RecordTx(ctx, db.New(tx), ev)
	*emit = e
	return err
}

func qBool(r *http.Request, k string) (*bool, *Problem) {
	v := r.URL.Query().Get(k)
	if v == "" {
		return nil, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return nil, errValidation(k + " must be true or false")
	}
	return &b, nil
}

func qInt64(r *http.Request, k string) (int64, *Problem) {
	v := r.URL.Query().Get(k)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, errValidation(k + " must be a non-negative number")
	}
	return n, nil
}

// --- alerts ---

func (s *Server) handleListAlerts(w http.ResponseWriter, r *http.Request) {
	if !s.alertsEnabled(w, r) {
		return
	}
	q := r.URL.Query()
	f := alerts.Filter{View: q.Get("view"), Severity: q.Get("severity"), HostID: q.Get("host_id"), GroupID: q.Get("group_id"),
		Category: q.Get("category"), Check: q.Get("check"), Cursor: q.Get("cursor")}
	var p *Problem
	if f.Acknowledged, p = qBool(r, "acknowledged"); p != nil {
		writeProblem(w, r, p)
		return
	}
	if f.Suppressed, p = qBool(r, "suppressed"); p != nil {
		writeProblem(w, r, p)
		return
	}
	if f.Since, p = qInt64(r, "since"); p != nil {
		writeProblem(w, r, p)
		return
	}
	if f.Until, p = qInt64(r, "until"); p != nil {
		writeProblem(w, r, p)
		return
	}
	if f.Limit, p = qInt(r, "limit"); p != nil {
		writeProblem(w, r, p)
		return
	}
	if f.Category != "" && !slices.Contains(monsvc.CheckCategories, f.Category) {
		writeProblem(w, r, errValidation("unknown category"))
		return
	}
	if f.Check != "" && !monsvc.KnownCheck(f.Check) {
		writeProblem(w, r, errValidation("unknown check"))
		return
	}
	page, err := s.d.Alerts.List(r.Context(), f)
	if err != nil {
		writeProblem(w, r, s.alertProblem(r, err, "alert"))
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleAlertSummary(w http.ResponseWriter, r *http.Request) {
	if !s.alertsEnabled(w, r) {
		return
	}
	sum, err := s.d.Alerts.GetSummary(r.Context())
	if err != nil {
		writeProblem(w, r, s.alertProblem(r, err, "alert"))
		return
	}
	out := struct {
		*alerts.Summary
		Destinations *destinationSummary `json:"destinations,omitempty"`
	}{Summary: sum}
	if principalFrom(r.Context()).Can(auth.PermNotificationsRead) {
		ds, err := s.d.Alerts.Destinations(r.Context())
		if err == nil {
			out.Destinations = &destinationSummary{}
			for _, d := range ds {
				out.Destinations.Configured++
				if d.Health.Status == "failing" {
					out.Destinations.Failing++
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

type destinationSummary struct {
	Configured int `json:"configured"`
	Failing    int `json:"failing"`
}

func (s *Server) handleGetAlert(w http.ResponseWriter, r *http.Request) {
	if !s.alertsEnabled(w, r) {
		return
	}
	d, err := s.d.Alerts.GetDetail(r.Context(), r.PathValue("id"), principalFrom(r.Context()).Can(auth.PermNotificationsRead))
	if err != nil {
		writeProblem(w, r, s.alertProblem(r, err, "alert"))
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// quietOption is one health setting that would quiet an alert's condition
// for good, and whether the caller may apply it: the same rules as PUT
// /monitoring/health/overrides, which applies it (Needs names what's
// missing).
type quietOption struct {
	monsvc.QuietOption
	Allowed bool   `json:"allowed"`
	Needs   string `json:"needs,omitempty"`
}

// handleAlertIgnoreOptions lists the ways to quiet an open alert's
// condition for good (the Alerts page's "Ignore…", dogfooding correction
// batch): health settings at the narrowest scope that does it, never a
// manual close. Applying one is an ordinary override write.
func (s *Server) handleAlertIgnoreOptions(w http.ResponseWriter, r *http.Request) {
	if !s.alertsEnabled(w, r) || !s.monitorEnabled(w, r) {
		return
	}
	ctx := r.Context()
	a, err := s.d.Alerts.Get(ctx, r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, s.alertProblem(r, err, "alert"))
		return
	}
	out := struct {
		AlertID string        `json:"alert_id"`
		Options []quietOption `json:"options"`
		Closes  string        `json:"closes,omitempty"`
		Note    string        `json:"note,omitempty"`
	}{AlertID: a.ID, Options: []quietOption{}}
	if a.State == alerts.StateResolved {
		out.Note = "This alert is closed: there is nothing to quiet."
		writeJSON(w, http.StatusOK, out)
		return
	}
	s.d.Monitor.ApplyHealthConfig(ctx)
	opts, note := s.d.Monitor.QuietOptions(ctx, a.HostID, a.HostName, a.Check, a.Subject)
	p := principalFrom(ctx)
	for _, o := range opts {
		x := quietOption{QuietOption: o}
		switch {
		case !p.Can(auth.PermMonitoringManage):
			x.Needs = string(auth.PermMonitoringManage)
		case !p.Can(auth.PermMonitoringSettings) && s.d.Monitor.Weakens(ctx, o.Change.OverrideKey, o.Change.Fields):
			x.Needs = string(auth.PermMonitoringSettings)
		default:
			x.Allowed = true
		}
		out.Options = append(out.Options, x)
	}
	out.Note = note
	if len(opts) > 0 {
		out.Closes = "The alert then closes as cleared by configuration, not as a recovery. The setting stays until someone changes it."
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAcknowledgeAlert(w http.ResponseWriter, r *http.Request) {
	if !s.alertsEnabled(w, r) {
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	if r.ContentLength != 0 {
		if p := decodeJSON(w, r, &in); p != nil {
			writeProblem(w, r, p)
			return
		}
	}
	ctx := r.Context()
	id := r.PathValue("id")
	var emit func()
	a, err := s.d.Alerts.Acknowledge(ctx, id, actorFrom(ctx), in.Note, func(tx *sql.Tx) error {
		return s.auditTx(ctx, tx, audit.Event{Action: "alert.acknowledge", TargetType: "alert", TargetID: id,
			Details: map[string]any{"note": in.Note}}, &emit)
	})
	if err != nil {
		writeProblem(w, r, s.alertProblem(r, err, "alert"))
		return
	}
	emit()
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	if !s.alertsEnabled(w, r) {
		return
	}
	limit, p := qInt(r, "limit")
	if p != nil {
		writeProblem(w, r, p)
		return
	}
	before, p := qInt64(r, "before")
	if p != nil {
		writeProblem(w, r, p)
		return
	}
	evs, err := s.d.Alerts.Events(r.Context(), alerts.EventFilter{HostID: r.URL.Query().Get("host_id"), Kind: r.URL.Query().Get("kind"), Limit: limit, Before: before})
	if err != nil {
		writeProblem(w, r, s.alertProblem(r, err, "event"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": evs})
}

// --- maintenance ---

func (s *Server) handleListMaintenance(w http.ResponseWriter, r *http.Request) {
	if !s.alertsEnabled(w, r) {
		return
	}
	q := r.URL.Query()
	st := q.Get("state")
	if st != "" && st != "active" && st != "ended" && st != "all" {
		writeProblem(w, r, errValidation("state must be active, ended or all"))
		return
	}
	limit, p := qInt(r, "limit")
	if p != nil {
		writeProblem(w, r, p)
		return
	}
	before, p := qInt64(r, "before")
	if p != nil {
		writeProblem(w, r, p)
		return
	}
	ms, err := s.d.Alerts.ListMaintenance(r.Context(), alerts.MaintenanceFilter{State: st, HostID: q.Get("host_id"), Limit: limit, Before: before})
	if err != nil {
		writeProblem(w, r, s.alertProblem(r, err, "maintenance"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"maintenance": ms})
}

func (s *Server) handleGetMaintenance(w http.ResponseWriter, r *http.Request) {
	if !s.alertsEnabled(w, r) {
		return
	}
	m, err := s.d.Alerts.GetMaintenance(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, s.alertProblem(r, err, "maintenance"))
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) handleStartMaintenance(w http.ResponseWriter, r *http.Request) {
	if !s.alertsEnabled(w, r) {
		return
	}
	var in alerts.MaintenanceInput
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	var emit func()
	m, err := s.d.Alerts.StartMaintenance(ctx, in, actorFrom(ctx), func(tx *sql.Tx, m *alerts.Maintenance) error {
		return s.auditTx(ctx, tx, audit.Event{Action: "maintenance.start", TargetType: "maintenance", TargetID: m.ID,
			Details: map[string]any{"reason": m.Reason, "host_ids": in.HostIDs, "group_ids": in.GroupIDs, "ends_at": m.EndsAt}}, &emit)
	})
	if err != nil {
		writeProblem(w, r, s.alertProblem(r, err, "maintenance"))
		return
	}
	emit()
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) handleEndMaintenance(w http.ResponseWriter, r *http.Request) {
	if !s.alertsEnabled(w, r) {
		return
	}
	ctx := r.Context()
	var emit func()
	m, err := s.d.Alerts.EndMaintenance(ctx, r.PathValue("id"), actorFrom(ctx), func(tx *sql.Tx, m *alerts.Maintenance) error {
		return s.auditTx(ctx, tx, audit.Event{Action: "maintenance.end", TargetType: "maintenance", TargetID: m.ID,
			Details: map[string]any{"reason": m.Reason}}, &emit)
	})
	if err != nil {
		writeProblem(w, r, s.alertProblem(r, err, "maintenance"))
		return
	}
	emit()
	writeJSON(w, http.StatusOK, m)
}

// --- destinations ---

func (s *Server) handleListDestinations(w http.ResponseWriter, r *http.Request) {
	if !s.alertsEnabled(w, r) {
		return
	}
	ds, err := s.d.Alerts.Destinations(r.Context())
	if err != nil {
		writeProblem(w, r, s.alertProblem(r, err, "destination"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"destinations": ds})
}

func (s *Server) handleGetDestination(w http.ResponseWriter, r *http.Request) {
	if !s.alertsEnabled(w, r) {
		return
	}
	d, err := s.d.Alerts.Destination(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, s.alertProblem(r, err, "destination"))
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// destAudit describes a destination for the audit log: settings, never
// secret values.
func destAudit(d *alerts.Destination) map[string]any {
	if d == nil {
		return nil
	}
	return map[string]any{"name": d.Name, "kind": d.Kind, "enabled": d.Enabled, "config": d.Config, "notify_for": d.NotifyFor, "secrets_set": d.SecretsSet}
}

// browserOrigin is the address the UI is served from (for links in
// notifications), when the request came from a browser.
func browserOrigin(r *http.Request) string {
	o := r.Header.Get("Origin")
	if u, err := url.Parse(o); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
		return u.Scheme + "://" + u.Host
	}
	return ""
}

func (s *Server) handleCreateDestination(w http.ResponseWriter, r *http.Request) {
	if !s.alertsEnabled(w, r) {
		return
	}
	var in alerts.DestinationInput
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	var emit func()
	d, err := s.d.Alerts.CreateDestination(ctx, in, actorFrom(ctx), func(tx *sql.Tx, d *alerts.Destination) error {
		return s.auditTx(ctx, tx, audit.Event{Action: "notifications.destination.create", TargetType: "notification_destination", TargetID: d.ID,
			Details: map[string]any{"after": destAudit(d), "secrets_changed": in.Secrets.Changed()}}, &emit)
	})
	if err != nil {
		writeProblem(w, r, s.alertProblem(r, err, "destination"))
		return
	}
	emit()
	s.d.Alerts.SetLinkBaseIfUnset(ctx, browserOrigin(r))
	writeJSON(w, http.StatusCreated, d)
}

func (s *Server) handleUpdateDestination(w http.ResponseWriter, r *http.Request) {
	if !s.alertsEnabled(w, r) {
		return
	}
	var in alerts.DestinationInput
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	var emit func()
	d, err := s.d.Alerts.UpdateDestination(ctx, r.PathValue("id"), in, func(tx *sql.Tx, before, after *alerts.Destination) error {
		return s.auditTx(ctx, tx, audit.Event{Action: "notifications.destination.update", TargetType: "notification_destination", TargetID: after.ID,
			Details: map[string]any{"before": destAudit(before), "after": destAudit(after), "secrets_changed": in.Secrets.Changed()}}, &emit)
	})
	if err != nil {
		writeProblem(w, r, s.alertProblem(r, err, "destination"))
		return
	}
	emit()
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleDeleteDestination(w http.ResponseWriter, r *http.Request) {
	if !s.alertsEnabled(w, r) {
		return
	}
	ctx := r.Context()
	var emit func()
	err := s.d.Alerts.DeleteDestination(ctx, r.PathValue("id"), func(tx *sql.Tx, d *alerts.Destination) error {
		return s.auditTx(ctx, tx, audit.Event{Action: "notifications.destination.delete", TargetType: "notification_destination", TargetID: d.ID,
			Details: map[string]any{"before": destAudit(d)}}, &emit)
	})
	if err != nil {
		writeProblem(w, r, s.alertProblem(r, err, "destination"))
		return
	}
	emit()
	w.WriteHeader(http.StatusNoContent)
}

// testLimiter bounds test notifications per user (tests reach out to
// arbitrary addresses an administrator configured).
type testLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func (l *testLimiter) allow(user string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.hits == nil {
		l.hits = map[string][]time.Time{}
	}
	now := time.Now()
	var keep []time.Time
	for _, t := range l.hits[user] {
		if now.Sub(t) < time.Minute {
			keep = append(keep, t)
		}
	}
	if len(keep) >= 10 {
		l.hits[user] = keep
		return false
	}
	l.hits[user] = append(keep, now)
	return true
}

var notifyTests testLimiter

func (s *Server) handleTestDestination(w http.ResponseWriter, r *http.Request) {
	if !s.alertsEnabled(w, r) {
		return
	}
	ctx := r.Context()
	if !notifyTests.allow(principalFrom(ctx).UserID) {
		writeProblem(w, r, problem(http.StatusTooManyRequests, "rate_limited", "at most 10 test notifications a minute"))
		return
	}
	res, d, err := s.d.Alerts.Test(ctx, r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, s.alertProblem(r, err, "destination"))
		return
	}
	s.d.Audit.Record(ctx, audit.Event{Action: "notifications.destination.test", TargetType: "notification_destination", TargetID: d.ID,
		Result:  map[bool]audit.Result{true: audit.Success, false: audit.Failure}[res.OK()],
		Details: map[string]any{"name": d.Name, "kind": d.Kind, "outcome": res.Outcome, "category": res.Category, "message": res.Message}})
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleTestUnsavedDestination(w http.ResponseWriter, r *http.Request) {
	if !s.alertsEnabled(w, r) {
		return
	}
	var in struct {
		Kind    string               `json:"kind"`
		Config  notify.Config        `json:"config"`
		Secrets *alerts.SecretsInput `json:"secrets"`
	}
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	if !notifyTests.allow(principalFrom(ctx).UserID) {
		writeProblem(w, r, problem(http.StatusTooManyRequests, "rate_limited", "at most 10 test notifications a minute"))
		return
	}
	res := s.d.Alerts.TestUnsaved(ctx, in.Kind, in.Config, in.Secrets)
	s.d.Audit.Record(ctx, audit.Event{Action: "notifications.destination.test", TargetType: "notification_destination",
		Result:  map[bool]audit.Result{true: audit.Success, false: audit.Failure}[res.OK()],
		Details: map[string]any{"unsaved": true, "kind": in.Kind, "config": in.Config, "outcome": res.Outcome, "category": res.Category, "message": res.Message}})
	writeJSON(w, http.StatusOK, res)
}

// --- routes ---

func (s *Server) handleListRoutes(w http.ResponseWriter, r *http.Request) {
	if !s.alertsEnabled(w, r) {
		return
	}
	rs, err := s.d.Alerts.Routes(r.Context())
	if err != nil {
		writeProblem(w, r, s.alertProblem(r, err, "route"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"routes": rs, "categories": monsvc.CheckCategories, "checks": monsvc.AllChecks()})
}

func (s *Server) handleCreateRoute(w http.ResponseWriter, r *http.Request) {
	if !s.alertsEnabled(w, r) {
		return
	}
	var in alerts.RouteInput
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	var emit func()
	rt, err := s.d.Alerts.CreateRoute(ctx, in, func(tx *sql.Tx, rt *alerts.Route) error {
		return s.auditTx(ctx, tx, audit.Event{Action: "notifications.route.create", TargetType: "notification_route", TargetID: rt.ID,
			Details: map[string]any{"after": rt}}, &emit)
	})
	if err != nil {
		writeProblem(w, r, s.alertProblem(r, err, "route"))
		return
	}
	emit()
	writeJSON(w, http.StatusCreated, rt)
}

func (s *Server) handleUpdateRoute(w http.ResponseWriter, r *http.Request) {
	if !s.alertsEnabled(w, r) {
		return
	}
	var in alerts.RouteInput
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	var emit func()
	rt, err := s.d.Alerts.UpdateRoute(ctx, r.PathValue("id"), in, func(tx *sql.Tx, before, after *alerts.Route) error {
		return s.auditTx(ctx, tx, audit.Event{Action: "notifications.route.update", TargetType: "notification_route", TargetID: after.ID,
			Details: map[string]any{"before": before, "after": after}}, &emit)
	})
	if err != nil {
		writeProblem(w, r, s.alertProblem(r, err, "route"))
		return
	}
	emit()
	writeJSON(w, http.StatusOK, rt)
}

func (s *Server) handleDeleteRoute(w http.ResponseWriter, r *http.Request) {
	if !s.alertsEnabled(w, r) {
		return
	}
	ctx := r.Context()
	var emit func()
	err := s.d.Alerts.DeleteRoute(ctx, r.PathValue("id"), func(tx *sql.Tx, rt *alerts.Route) error {
		return s.auditTx(ctx, tx, audit.Event{Action: "notifications.route.delete", TargetType: "notification_route", TargetID: rt.ID,
			Details: map[string]any{"before": rt}}, &emit)
	})
	if err != nil {
		writeProblem(w, r, s.alertProblem(r, err, "route"))
		return
	}
	emit()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePreviewRoute(w http.ResponseWriter, r *http.Request) {
	if !s.alertsEnabled(w, r) {
		return
	}
	var in struct {
		HostID   string `json:"host_id"`
		Check    string `json:"check"`
		Severity string `json:"severity"`
	}
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	p, err := s.d.Alerts.Preview(r.Context(), in.HostID, in.Check, in.Severity)
	if err != nil {
		writeProblem(w, r, s.alertProblem(r, err, "route"))
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// --- deliveries and settings ---

func (s *Server) handleListDeliveries(w http.ResponseWriter, r *http.Request) {
	if !s.alertsEnabled(w, r) {
		return
	}
	q := r.URL.Query()
	limit, p := qInt(r, "limit")
	if p != nil {
		writeProblem(w, r, p)
		return
	}
	ds, next, err := s.d.Alerts.Deliveries(r.Context(), alerts.DeliveryFilter{DestinationID: q.Get("destination_id"), AlertID: q.Get("alert_id"),
		Status: q.Get("status"), Limit: limit, Before: q.Get("cursor")})
	if err != nil {
		writeProblem(w, r, s.alertProblem(r, err, "delivery"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": ds, "next_cursor": next})
}

type notificationSettingsView struct {
	Settings alerts.Settings `json:"settings"`
	Kinds    []string        `json:"kinds"`
}

func (s *Server) handleNotificationSettings(w http.ResponseWriter, r *http.Request) {
	if !s.alertsEnabled(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, notificationSettingsView{Settings: s.d.Alerts.Settings(r.Context()), Kinds: alerts.KindsList()})
}

func (s *Server) handleSetNotificationSettings(w http.ResponseWriter, r *http.Request) {
	if !s.alertsEnabled(w, r) {
		return
	}
	var in alerts.Settings
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	before := s.d.Alerts.Settings(ctx)
	var emit func()
	err := s.d.Alerts.SaveSettings(ctx, in, principalFrom(ctx).UserID, func(tx *sql.Tx) error {
		return s.auditTx(ctx, tx, audit.Event{Action: "notifications.settings.update", TargetType: "settings", TargetID: "alerts",
			Details: map[string]any{"before": before, "after": in}}, &emit)
	})
	if err != nil {
		writeProblem(w, r, s.alertProblem(r, err, "settings"))
		return
	}
	emit()
	writeJSON(w, http.StatusOK, notificationSettingsView{Settings: s.d.Alerts.Settings(ctx), Kinds: alerts.KindsList()})
}
