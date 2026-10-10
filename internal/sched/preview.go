package sched

import (
	"context"
	"fmt"
	"time"

	"drool/internal/engine"
	"drool/internal/model"
)

// NextRun is one upcoming occurrence.
type NextRun struct {
	At    int64  `json:"at"`    // unix ms
	Local string `json:"local"` // in the schedule's zone
	Note  string `json:"note,omitempty"`
}

// NextRuns lists the next count occurrences after now.
func NextRuns(r Recurrence, zone string, after time.Time, count int) ([]NextRun, error) {
	if err := r.Normalize(); err != nil {
		return nil, &ValidationError{Msg: err.Error()}
	}
	loc, err := LoadZone(zone)
	if err != nil {
		return nil, &ValidationError{Msg: err.Error()}
	}
	if count <= 0 || count > 20 {
		count = 5
	}
	out := []NextRun{}
	t := after
	for range count {
		n, ok := r.Next(t, loc)
		if !ok {
			break
		}
		out = append(out, NextRun{At: n.UnixMilli(), Local: n.In(loc).Format("Mon 2 Jan 2006 15:04 MST"), Note: r.Shifted(n, loc)})
		t = n
	}
	return out, nil
}

// PreviewHost is a target as it would run now.
type PreviewHost struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Warning     string `json:"warning,omitempty"`
	Maintenance string `json:"maintenance,omitempty"` // in maintenance now (would be skipped by a scheduled run)
	Wave        int    `json:"wave"`                  // -1: not part of the run
}

// PreviewWave is one wave of the rollout.
type PreviewWave struct {
	Index  int      `json:"index"`
	Canary bool     `json:"canary"`
	Hosts  []string `json:"hosts"` // names
}

// PreviewWindow is a schedule's relationship to its maintenance window.
type PreviewWindow struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	NextStart *int64 `json:"next_start,omitempty"`
	NextEnd   *int64 `json:"next_end,omitempty"`
	// Inside: the schedule's next run falls inside the window's next
	// occurrence.
	Inside bool   `json:"inside"`
	Note   string `json:"note"`
}

// Preview is what a schedule would do, before it is saved.
type Preview struct {
	Kind        string         `json:"kind"`
	When        string         `json:"when"`
	TimeZone    string         `json:"time_zone"`
	NextRuns    []NextRun      `json:"next_runs"`
	Targets     []PreviewHost  `json:"targets"`
	Waves       []PreviewWave  `json:"waves"`
	Concurrency int            `json:"concurrency"`
	Threshold   *int           `json:"failure_threshold,omitempty"`
	Steps       []string       `json:"steps"`
	Reboot      string         `json:"reboot"`
	Verify      []string       `json:"verification"`
	Window      *PreviewWindow `json:"window,omitempty"`
	Duration    string         `json:"duration,omitempty"` // maintenance windows
	Policies    []string       `json:"policies"`
	Notes       []string       `json:"notes"`
	Warnings    []string       `json:"warnings"`
}

// rebootText says how a job's steps reboot hosts.
func rebootText(steps []model.Operation) string {
	for i, st := range steps {
		switch st.Type {
		case model.OpReboot:
			return fmt.Sprintf("Every host reboots (step %d); DROOL waits for each to come back", i+1)
		case model.OpSystemUpdate:
			switch st.SystemUpdate.Reboot {
			case "always":
				return fmt.Sprintf("Every host reboots after the update (step %d); DROOL waits for each to come back", i+1)
			case "if_required":
				return fmt.Sprintf("Hosts reboot only if the update requires it (step %d); DROOL waits for each to come back", i+1)
			}
		}
	}
	return "No reboots"
}

// PreviewSchedule shows what a schedule would do, without saving it.
func (s *Service) PreviewSchedule(ctx context.Context, in ScheduleInput) (*Preview, error) {
	if in.Name == "" {
		in.Name = "preview"
	}
	if err := s.normalize(ctx, &in, nil); err != nil {
		return nil, err
	}
	c := in.Config
	loc, _ := LoadZone(c.TimeZone)
	p := &Preview{Kind: in.Kind, When: c.Recurrence.Describe(loc), TimeZone: c.TimeZone, Targets: []PreviewHost{}, Waves: []PreviewWave{},
		Steps: []string{}, Verify: []string{}, Policies: []string{}, Notes: []string{}, Warnings: []string{}}
	p.NextRuns, _ = NextRuns(c.Recurrence, c.TimeZone, s.now(), 5)
	if len(p.NextRuns) == 0 {
		p.Warnings = append(p.Warnings, "That time has already passed: the schedule would never run.")
	}
	hosts, warnings, err := engine.ResolveTargets(ctx, s.d.Store.R, c.Targets)
	if err != nil {
		return nil, err
	}
	p.Warnings = append(p.Warnings, warnings...)
	covers := map[string]string{}
	if s.d.Alerts != nil {
		ids := make([]string, len(hosts))
		for i, h := range hosts {
			ids[i] = h.ID
		}
		cs, _ := s.d.Alerts.Covers(ctx, ids)
		for h, list := range cs {
			for _, cv := range list {
				if c.WindowID != "" && cv.ScheduleID == c.WindowID {
					continue
				}
				covers[h] = cv.Reason
				if covers[h] == "" {
					covers[h] = "maintenance"
				}
				break
			}
		}
	}
	p.Notes = append(p.Notes, "Targets are resolved again at every run: hosts added to a pack later take part, and each run's job keeps the hosts it started with.")
	if in.Kind == ScheduleMaintenance {
		for _, h := range hosts {
			p.Targets = append(p.Targets, PreviewHost{ID: h.ID, Name: h.Name, Status: h.Status})
		}
		p.Duration = (time.Duration(c.DurationMinutes) * time.Minute).String()
		p.Notes = append(p.Notes, "Each window is ordinary maintenance: notifications are paused for these hosts; monitoring, alerts and every safety mechanism carry on. Group membership is checked live while it lasts.",
			"If DROOL isn't running when a window starts, it starts the window when it returns (if the window hasn't ended); a window that passed entirely is recorded as missed, never started late.")
		return p, nil
	}
	steps, _, err := s.steps(ctx, s.d.Store.Reader, c)
	if err != nil {
		return nil, err
	}
	p.Steps = stepSummaries(steps)
	p.Reboot = rebootText(steps)
	for _, st := range steps {
		if model.IsLocal(st.Type) && st.Type != model.OpWait {
			p.Verify = append(p.Verify, st.Summary())
		}
	}
	p.Concurrency, p.Threshold = c.Strategy.Concurrency, c.Strategy.FailureThreshold
	var eligible []engine.TargetHost
	for _, h := range hosts {
		if covers[h.ID] == "" {
			eligible = append(eligible, h)
		}
	}
	if err := engine.AnnotateSkips(ctx, s.d.Store.R, eligible, steps); err != nil {
		return nil, err
	}
	ids := make([]string, len(eligible))
	for i, h := range eligible {
		ids[i] = h.ID
	}
	plan := model.PlanWaves(ids, c.Strategy)
	wave := map[string]int{}
	for w, members := range plan.Waves {
		pw := PreviewWave{Index: w, Canary: plan.Canary && w == 0}
		for _, i := range members {
			pw.Hosts = append(pw.Hosts, eligible[i].Name)
			wave[eligible[i].ID] = w
		}
		p.Waves = append(p.Waves, pw)
	}
	if plan.Note != "" {
		p.Warnings = append(p.Warnings, plan.Note)
	}
	byID := map[string]engine.TargetHost{}
	for _, h := range eligible {
		byID[h.ID] = h
	}
	for _, h := range hosts {
		ph := PreviewHost{ID: h.ID, Name: h.Name, Status: h.Status, Warning: h.Warning, Wave: -1}
		if e, ok := byID[h.ID]; ok {
			ph.Warning, ph.Wave = e.Warning, wave[h.ID]
		} else {
			ph.Maintenance = covers[h.ID]
		}
		p.Targets = append(p.Targets, ph)
	}
	if len(covers) > 0 {
		p.Notes = append(p.Notes, fmt.Sprintf("%d host(s) are in maintenance now; a scheduled run skips hosts under maintenance it isn't associated with (Run now doesn't).", len(covers)))
	}
	if c.Recurrence.Recurring() {
		if c.MissedPolicy == MissedRunOnce {
			p.Policies = append(p.Policies, "If DROOL wasn't running at a scheduled time, it runs the most recent missed occurrence once when it returns (never a backlog).")
		} else {
			p.Policies = append(p.Policies, "If DROOL wasn't running at a scheduled time, that run is skipped and recorded as missed; the schedule continues with the next one.")
		}
	} else if c.LateLimitMinutes > 0 {
		p.Policies = append(p.Policies, fmt.Sprintf("If DROOL wasn't running at the time, it starts when DROOL returns, unless that is more than %d minutes late.", c.LateLimitMinutes))
	} else {
		p.Policies = append(p.Policies, "If DROOL wasn't running at the time, it starts when DROOL returns.")
	}
	if c.OverlapPolicy == OverlapQueue {
		p.Policies = append(p.Policies, "If the previous run is still going, one run waits and starts when it finishes; further runs are skipped.")
	} else {
		p.Policies = append(p.Policies, "If the previous run is still going, the new run is skipped.")
	}
	for _, st := range steps {
		if st.Type == model.OpSystemUpdate || st.Type == model.OpPackage {
			p.Notes = append(p.Notes, "Package changes depend on the repositories at run time: a simulation today can't promise exactly what next week's run installs.")
			break
		}
	}
	if c.WindowID != "" {
		p.Window = s.windowPreview(ctx, c, p.NextRuns)
	}
	if len(steps) > 0 && len(p.Waves) == 0 && len(hosts) > 0 {
		p.Warnings = append(p.Warnings, "Every target is in maintenance now: a run now would be skipped.")
	}
	return p, nil
}

func (s *Service) windowPreview(ctx context.Context, c Config, next []NextRun) *PreviewWindow {
	w, err := s.load(ctx, s.d.Store.Reader, c.WindowID)
	if err != nil {
		return nil
	}
	pw := &PreviewWindow{ID: w.ID, Name: w.Name}
	loc, err := LoadZone(w.Config.TimeZone)
	if err != nil {
		return pw
	}
	dur := time.Duration(w.Config.DurationMinutes) * time.Minute
	if len(next) == 0 {
		pw.Note = "The schedule has no future run."
		return pw
	}
	run := time.UnixMilli(next[0].At)
	// The window occurrence that would contain the run: the last start at
	// or before it.
	start, ok := w.Config.Recurrence.Next(run.Add(-dur), loc)
	if ok {
		ss, ee := start.UnixMilli(), start.Add(dur).UnixMilli()
		pw.NextStart, pw.NextEnd = &ss, &ee
		pw.Inside = !run.Before(start) && run.Before(start.Add(dur))
	}
	if pw.Inside {
		pw.Note = fmt.Sprintf("The next run (%s) falls inside window %q; it may run on the hosts that window has in maintenance.", next[0].Local, w.Name)
	} else {
		pw.Note = fmt.Sprintf("The next run (%s) is outside window %q's next occurrence: a run when the window isn't active is skipped.", next[0].Local, w.Name)
	}
	return pw
}
