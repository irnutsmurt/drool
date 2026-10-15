package monsvc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"drool/internal/monitor"
	"drool/internal/monitor/agentmon"
	"drool/internal/monitor/services"
	"drool/internal/monitor/storage"
)

// --- host settings ---

// HostSettings returns a host's monitoring settings. WatchedServices are
// the units this host's own settings expect to run (expected state is a
// health setting since the final After-M17 batch; the list stays for API
// clients).
func (s *Service) HostSettings(ctx context.Context, hostID string) HostSettings {
	h := HostSettings{WatchedServices: []string{}, ProbeMounts: []string{}, ProbeS: agentmon.DefaultProbeS}
	var j string
	if err := s.d.Store.Reader.QueryRowContext(ctx, `SELECT settings_json FROM mon_host_settings WHERE host_id = ?`, hostID).Scan(&j); err == nil {
		json.Unmarshal([]byte(j), &h)
	}
	h.WatchedServices = s.ownExpectedRunning(s.eval.current(ctx), hostID)
	return h
}

// ownExpectedRunning lists the units a host's own settings expect to run.
func (s *Service) ownExpectedRunning(p *policy, hostID string) []string {
	out := []string{}
	for k, f := range p.subj[hostID][CheckServiceExpected] {
		if f.has(FieldExpected) && f[FieldExpected] == ExpectRunning {
			out = append(out, strings.TrimPrefix(k, "unit:"))
		}
	}
	sort.Strings(out)
	return out
}

// SetWatchedTx makes want the units a host's own settings expect to run,
// for clients of watched_services: units added become expected running on
// the host, units taken out lose that setting (whatever else is set for
// them stays). It returns whether anything changed.
func (s *Service) SetWatchedTx(ctx context.Context, tx *sql.Tx, hostID string, want []string, by string, now int64) (bool, error) {
	have := map[string]bool{}
	for _, u := range s.ownExpectedRunning(s.eval.peek(ctx), hostID) {
		have[u] = true
	}
	wanted := map[string]bool{}
	for _, u := range want {
		wanted[u] = true
	}
	changed := false
	for u := range wanted {
		if have[u] {
			continue
		}
		k := OverrideKey{Scope: ScopeHost, HostID: hostID, Check: CheckServiceExpected, Subject: "unit:" + u}
		f := Fields{}
		if o, ok := findTx(ctx, tx, k); ok {
			f = o.Fields.clone()
		}
		f[FieldExpected] = ExpectRunning
		if _, _, err := s.PutOverrideTx(ctx, tx, k, f, u, by, now); err != nil {
			return changed, err
		}
		changed = true
	}
	for u := range have {
		if wanted[u] {
			continue
		}
		k := OverrideKey{Scope: ScopeHost, HostID: hostID, Check: CheckServiceExpected, Subject: "unit:" + u}
		o, ok := findTx(ctx, tx, k)
		if !ok {
			continue
		}
		f := o.Fields.clone()
		delete(f, FieldExpected)
		if _, _, err := s.PutOverrideTx(ctx, tx, k, f, "", by, now); err != nil {
			return changed, err
		}
		changed = true
	}
	return changed, nil
}

// SaveHostSettingsTx stores a host's settings (the caller audits; expected
// states go through SetWatchedTx).
func SaveHostSettingsTx(ctx context.Context, tx *sql.Tx, hostID string, h HostSettings, by string, now int64) error {
	h.WatchedServices = nil
	b, _ := json.Marshal(h)
	var byv any
	if by != "" {
		byv = by
	}
	if _, err := tx.ExecContext(ctx, `UPDATE mon_health_gen SET gen = gen + 1 WHERE id = 1`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO mon_host_settings (host_id, settings_json, updated_by, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(host_id) DO UPDATE SET settings_json = excluded.settings_json, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
		hostID, string(b), byv, now)
	return err
}

// --- services ---

// ServiceRow is one unit as stored.
type ServiceRow struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Load        string `json:"load"`
	Active      string `json:"active"`
	Sub         string `json:"sub"`
	FileState   string `json:"file_state"`
	Since       int64  `json:"since,omitempty"`
	Restarts    *int   `json:"restarts,omitempty"`
	Result      string `json:"result,omitempty"`
	Transient   bool   `json:"transient"`
	Known       bool   `json:"known"`
	ChangedAt   int64  `json:"changed_at"`
	// Derived for the UI.
	State   string `json:"state"`   // running | stopped | failed | unknown
	Notable bool   `json:"notable"` // in the default view
	Watched bool   `json:"watched"` // expected to run (the old "watched")
	// Expected is the unit's expected state ("running", "stopped",
	// "ignored"; "" automatic), and ExpectedSource where it comes from.
	Expected       string          `json:"expected"`
	ExpectedSource *ExpectedSource `json:"expected_source,omitempty"`
	// Health says whether its state is a health problem now (problem |
	// expected | ignored | ok | unknown) and HealthNote why it alerts or
	// doesn't.
	Health     string `json:"health"`
	HealthNote string `json:"health_note,omitempty"`
	// Actions DROOL offers for it now (services.Actions), and why others
	// aren't.
	Actions    []string `json:"actions"`
	ActionNote string   `json:"action_note,omitempty"`
}

// ExpectedSource is where a unit's expected state comes from.
type ExpectedSource struct {
	Kind  string `json:"kind"`  // subject (this host) | group | global | unset
	Label string `json:"label"` // "set on this host", "from pack media", …
	// OverrideID is this host's own setting for the unit, if any (what
	// "Use the inherited setting" deletes).
	OverrideID string `json:"override_id,omitempty"`
}

// Services returns a host's units.
func (s *Service) Services(ctx context.Context, hostID string) ([]ServiceRow, error) {
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT unit, description, load_state, active_state, sub_state, file_state, since, restarts, result, transient, known, changed_at
		FROM mon_services WHERE host_id = ? ORDER BY unit`, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ServiceRow
	for rows.Next() {
		var r ServiceRow
		var restarts sql.NullInt64
		var tr, known int
		if err := rows.Scan(&r.Name, &r.Description, &r.Load, &r.Active, &r.Sub, &r.FileState, &r.Since, &restarts, &r.Result, &tr, &known, &r.ChangedAt); err != nil {
			return nil, err
		}
		if restarts.Valid {
			n := int(restarts.Int64)
			r.Restarts = &n
		}
		r.Transient, r.Known = tr == 1, known == 1
		u := services.Unit{Name: r.Name, Active: r.Active, Sub: r.Sub, FileState: r.FileState, Load: r.Load, Transient: r.Transient}
		r.State, r.Notable = services.State(u), services.Notable(u)
		if !r.Known {
			r.State = "unknown"
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Service filters.
var ServiceFilters = []string{"all", "running", "stopped", "failed", "enabled", "disabled"}

// ServicesView is the service inventory page.
type ServicesView struct {
	Supported bool           `json:"supported"`
	Note      string         `json:"note,omitempty"`
	UpdatedAt int64          `json:"updated_at"`
	Source    string         `json:"source"`
	Counts    map[string]int `json:"counts"`
	Hidden    int            `json:"hidden"` // units outside the default view
	Units     []ServiceRow   `json:"units"`
}

// ServiceList filters a host's services. allUnits includes transient,
// static and other units the default view hides.
func (s *Service) ServiceList(ctx context.Context, hostID, filter string, allUnits bool) (*ServicesView, error) {
	units, err := s.Services(ctx, hostID)
	if err != nil {
		return nil, err
	}
	v := &ServicesView{Counts: map[string]int{}, Units: []ServiceRow{}}
	var note string
	var sup int
	var at int64
	var src string
	s.d.Store.Reader.QueryRowContext(ctx, `SELECT services_note, services_supported, services_at, source FROM mon_current WHERE host_id = ?`, hostID).Scan(&note, &sup, &at, &src)
	v.Note, v.Supported, v.UpdatedAt, v.Source = note, sup == 1, at, src
	pol := s.eval.current(ctx)
	for _, u := range units {
		s.judgeService(pol, hostID, &u)
		if !allUnits && !u.Notable && u.Expected == "" {
			v.Hidden++
			continue
		}
		enabled := strings.HasPrefix(u.FileState, "enabled") || u.FileState == "alias" || u.FileState == "indirect" || u.FileState == "linked"
		v.Counts["all"]++
		v.Counts[u.State]++
		if enabled {
			v.Counts["enabled"]++
		}
		if u.FileState == "disabled" {
			v.Counts["disabled"]++
		}
		keep := false
		switch filter {
		case "", "all":
			keep = true
		case "running", "stopped", "failed":
			keep = u.State == filter
		case "enabled":
			keep = enabled
		case "disabled":
			keep = u.FileState == "disabled"
		}
		if keep {
			v.Units = append(v.Units, u)
		}
	}
	return v, nil
}

// judgeService fills a row's expected state, whether it alerts and why,
// and the actions that make sense for it: the same rules as the evaluator
// (health.go, services), in words.
func (s *Service) judgeService(pol *policy, hostID string, u *ServiceRow) {
	exp, src := pol.expected(hostID, u.Name)
	u.Expected, u.Watched = ExpectName(exp), exp == ExpectRunning
	from := ExpectSource(src)
	if exp != ExpectAuto || src.Conflict != nil {
		es := &ExpectedSource{Kind: src.Source.Kind, Label: from}
		if src.Source.Kind == "subject" {
			if o, ok := (OverrideKey{Scope: ScopeHost, HostID: hostID, Check: CheckServiceExpected, Subject: "unit:" + u.Name}).find(pol); ok {
				es.OverrideID = o.ID
			}
		}
		u.ExpectedSource = es
	}
	su := services.Unit{Name: u.Name, Active: u.Active, Sub: u.Sub, FileState: u.FileState, Load: u.Load, Transient: u.Transient}
	u.Actions, u.ActionNote = services.Actions(su)
	on := func(check string) bool { return pol.rule(hostID, check, unitCtx(u.Name), nil).on }
	switch {
	case u.State == "unknown":
		u.Health = "unknown"
	case exp == ExpectIgnored:
		u.Health, u.HealthNote = "ignored", "Ignored ("+from+"): its state is shown, never a health issue or alert."
	case exp == ExpectRunning && u.State == "running":
		u.Health, u.HealthNote = "ok", "Expected to run ("+from+")."
	case exp == ExpectRunning && u.State == "failed":
		u.Health, u.HealthNote = alerting(on(CheckServiceFailed), "Failed, though expected to run ("+from+"): a critical health issue.")
	case exp == ExpectRunning:
		u.Health, u.HealthNote = alerting(on(CheckServiceStopped), "Not running, though expected to run ("+from+"): a health issue once that lasts.")
	case exp == ExpectStopped && u.State == "running":
		u.Health, u.HealthNote = alerting(on(CheckServiceUnexpected), "Running, though expected to be stopped ("+from+"): a health issue once that lasts.")
	case exp == ExpectStopped:
		u.Health, u.HealthNote = "expected", "Not alerting: expected to be stopped ("+from+")."
	case u.State == "failed" && u.Notable:
		u.Health, u.HealthNote = alerting(on(CheckServiceFailed), "Failed: a health issue. If it's meant to stay off, set its expected state to Stopped.")
	case src.Conflict != nil:
		u.Health, u.HealthNote = "ok", "Packs disagree about its expected state, so DROOL judges it automatically: set it on this host, or give one pack a higher priority."
	default:
		u.Health = "ok"
	}
}

// alerting is "problem" with the note, or "ok" when the check is off.
func alerting(on bool, note string) (string, string) {
	if !on {
		return "ok", "Its health check is turned off, so it doesn't alert."
	}
	return "problem", note
}

// ServiceEvent is one recorded transition.
type ServiceEvent struct {
	Unit   string `json:"unit"`
	TS     int64  `json:"ts"`
	From   string `json:"from"`
	To     string `json:"to"`
	Result string `json:"result,omitempty"`
}

// ServiceHistory returns a unit's transitions, newest first (unit ""
// returns the host's).
func (s *Service) ServiceHistory(ctx context.Context, hostID, unit string, limit int) ([]ServiceEvent, error) {
	limit = clampInt(limit, 1, 500, 100)
	q := `SELECT unit, ts, from_state, to_state, result FROM mon_service_events WHERE host_id = ?`
	args := []any{hostID}
	if unit != "" {
		q += ` AND unit = ?`
		args = append(args, unit)
	}
	q += ` ORDER BY ts DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.d.Store.Reader.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ServiceEvent{}
	for rows.Next() {
		var e ServiceEvent
		if err := rows.Scan(&e.Unit, &e.TS, &e.From, &e.To, &e.Result); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func clampInt(v, lo, hi, def int) int {
	if v <= 0 {
		return def
	}
	return max(lo, min(hi, v))
}

// --- issues ---

// Issue is a health issue.
type Issue struct {
	ID         string   `json:"id"`
	HostID     string   `json:"host_id"`
	HostName   string   `json:"host_name,omitempty"`
	Check      string   `json:"check"`
	Subject    string   `json:"subject"`
	Severity   string   `json:"severity"`
	State      string   `json:"state"`
	Title      string   `json:"title"`
	Detail     string   `json:"detail,omitempty"`
	Value      *float64 `json:"value,omitempty"`
	StartedAt  int64    `json:"started_at"`
	UpdatedAt  int64    `json:"updated_at"`
	ResolvedAt *int64   `json:"resolved_at,omitempty"`
	// Resolution: recovered | disabled | reconfigured | gone (M12.1).
	Resolution     string `json:"resolution,omitempty"`
	ResolutionNote string `json:"resolution_note,omitempty"`
}

// IssueFilter selects issues.
type IssueFilter struct {
	HostID   string
	State    string // active | resolved | all
	Severity string
	Check    string
	Limit    int
	Before   int64 // started_at cursor
}

// Issues lists issues, newest first (bounded).
func (s *Service) Issues(ctx context.Context, f IssueFilter) ([]Issue, error) {
	q := `SELECT i.id, i.host_id, h.name, i.check_id, i.subject, i.severity, i.state, i.title, i.detail, i.value, i.started_at, i.updated_at, i.resolved_at,
		COALESCE(i.resolution, ''), i.resolution_note
		FROM mon_issues i JOIN hosts h ON h.id = i.host_id WHERE 1=1`
	var args []any
	if f.HostID != "" {
		q += ` AND i.host_id = ?`
		args = append(args, f.HostID)
	}
	switch f.State {
	case "", "active":
		q += ` AND i.state = 'active'`
	case "resolved":
		q += ` AND i.state = 'resolved'`
	}
	if f.Severity != "" {
		q += ` AND i.severity = ?`
		args = append(args, f.Severity)
	}
	if f.Check != "" {
		q += ` AND i.check_id = ?`
		args = append(args, f.Check)
	}
	if f.Before > 0 {
		q += ` AND i.started_at < ?`
		args = append(args, f.Before)
	}
	q += ` ORDER BY CASE i.severity WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END, i.started_at DESC LIMIT ?`
	args = append(args, clampInt(f.Limit, 1, 500, 200))
	rows, err := s.d.Store.Reader.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Issue{}
	for rows.Next() {
		var i Issue
		var v sql.NullFloat64
		var r sql.NullInt64
		if err := rows.Scan(&i.ID, &i.HostID, &i.HostName, &i.Check, &i.Subject, &i.Severity, &i.State, &i.Title, &i.Detail, &v, &i.StartedAt, &i.UpdatedAt, &r,
			&i.Resolution, &i.ResolutionNote); err != nil {
			return nil, err
		}
		if v.Valid {
			i.Value = &v.Float64
		}
		if r.Valid {
			i.ResolvedAt = &r.Int64
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// --- host overview ---

// Tile is one headline of the host view. Value is nil when unknown.
type Tile struct {
	Key    string   `json:"key"`
	Label  string   `json:"label"`
	Value  *float64 `json:"value,omitempty"`
	Unit   string   `json:"unit,omitempty"`
	Text   string   `json:"text"`
	Status string   `json:"status"` // ok | warning | critical | unknown | info
	Detail string   `json:"detail,omitempty"`
}

// HostOverview is a host's monitoring page.
type HostOverview struct {
	HostID    string                          `json:"host_id"`
	HasData   bool                            `json:"has_data"`
	Source    string                          `json:"source"`
	Stale     bool                            `json:"stale"`
	UpdatedAt int64                           `json:"updated_at"`
	Tiles     []Tile                          `json:"tiles"`
	State     *monitor.State                  `json:"state,omitempty"`
	Issues    []Issue                         `json:"issues"`
	Probes    map[string]agentmon.ProbeResult `json:"probes"`
	Settings  HostSettings                    `json:"settings"`
	Helpers   []HelperStatus                  `json:"helpers"`
	Gaps      []monitor.Gap                   `json:"gaps"`
	Agent     *AgentView                      `json:"agent,omitempty"`
}

// AgentView is the agent's effective budget as the UI shows it.
type AgentView struct {
	Limits     agentmon.EffectiveLimits `json:"limits"`     // what DROOL asks for
	Overridden bool                     `json:"overridden"` // a per-host budget is set
	Monitor1   bool                     `json:"monitor_1"`  // the agent supports M11 monitoring
	Connected  bool                     `json:"connected"`
}

func worstStatus(a, b string) string {
	rank := map[string]int{"ok": 0, "info": 1, "unknown": 2, "warning": 3, "critical": 4}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// Overview builds the host page.
func (s *Service) Overview(ctx context.Context, hostID string) (*HostOverview, error) {
	o := &HostOverview{HostID: hostID, Issues: []Issue{}, Probes: map[string]agentmon.ProbeResult{}, Helpers: []HelperStatus{}, Gaps: []monitor.Gap{}}
	o.Settings = s.HostSettings(ctx, hostID)
	set := s.Settings(ctx)
	s.mu.Lock()
	c := s.cur[hostID]
	var st *monitor.State
	var sto *storage.Report
	if c != nil {
		st, sto = c.state, c.storage
		o.Source, o.UpdatedAt = c.source, c.at.UnixMilli()
		for k, v := range c.probes {
			o.Probes[k] = v
		}
	}
	s.mu.Unlock()
	issues, err := s.Issues(ctx, IssueFilter{HostID: hostID, Limit: 200})
	if err != nil {
		return nil, err
	}
	o.Issues = issues
	o.Helpers = s.helperStatus(ctx, hostID)
	if s.d.Agents != nil {
		av := &AgentView{Limits: set.AgentConfig(o.Settings).Limits.Effective(), Overridden: o.Settings.Limits != nil}
		if fs, ok := s.d.Agents.SessionFeatures(hostID); ok {
			av.Connected, av.Monitor1 = true, hasFeature(fs, "monitor-1")
		}
		if av.Connected || o.Source == "agent" {
			o.Agent = av
		}
	}
	o.Gaps, _ = s.gaps(ctx, hostID, s.now().Add(-31*24*time.Hour).Unix())
	if st == nil || st.At == 0 {
		o.Tiles = []Tile{}
		return o, nil
	}
	o.HasData, o.State = true, st
	o.Stale = s.now().Sub(time.UnixMilli(o.UpdatedAt)) > staleAfter(s.eval.current(ctx), hostID, o.Source, set.SSHPollS)
	sevOf := func(checks ...string) string {
		st := "ok"
		for _, i := range issues {
			for _, c := range checks {
				if i.Check == c {
					switch i.Severity {
					case "critical":
						st = worstStatus(st, "critical")
					case "warning":
						st = worstStatus(st, "warning")
					}
				}
			}
		}
		return st
	}
	pct := func(key, label string, v *float64, checks ...string) Tile {
		t := Tile{Key: key, Label: label, Unit: "%", Status: sevOf(checks...)}
		if v == nil {
			t.Text, t.Status = "Unknown", "unknown"
			return t
		}
		x := math.Round(*v)
		t.Value, t.Text = &x, fmt.Sprintf("%.0f%%", x)
		return t
	}
	var cpu, mem *float64
	if st.CPU != nil {
		cpu = st.CPU.Percent
	}
	if st.Memory != nil {
		m := st.Memory.UsedPercent
		mem = &m
	}
	o.Tiles = append(o.Tiles, pct("cpu", "CPU", cpu, CheckCPU), pct("memory", "Memory", mem, CheckMemory))
	// Storage: the fullest filesystem.
	var full *monitor.FS
	for i := range st.Filesystems {
		f := &st.Filesystems[i]
		if f.TotalBytes > 0 && (full == nil || f.UsedPercent > full.UsedPercent) {
			full = f
		}
	}
	if full != nil {
		t := pct("storage", "Storage", &full.UsedPercent, CheckFilesystem, CheckInodes, CheckFSReadOnly)
		t.Detail = "Fullest: " + full.Mount
		o.Tiles = append(o.Tiles, t)
	} else {
		o.Tiles = append(o.Tiles, Tile{Key: "storage", Label: "Storage", Text: "Not reported", Status: "unknown",
			Detail: "No local filesystems are visible (in a container, its own storage is an overlay)."})
	}
	// Temperature: the hottest CPU sensor, else the hottest sensor.
	var hot *monitor.Sensor
	for i := range st.Sensors {
		sn := &st.Sensors[i]
		if hot == nil || (sn.Kind == "cpu") && hot.Kind != "cpu" || (sn.Kind == "cpu") == (hot.Kind == "cpu") && sn.Celsius > hot.Celsius {
			hot = sn
		}
	}
	if hot != nil {
		v := math.Round(hot.Celsius)
		o.Tiles = append(o.Tiles, Tile{Key: "temperature", Label: "Temperature", Value: &v, Unit: "°C", Text: fmt.Sprintf("%.0f °C", v),
			Status: sevOf(CheckTemperature), Detail: sensorName(*hot)})
	} else {
		o.Tiles = append(o.Tiles, Tile{Key: "temperature", Label: "Temperature", Text: "No sensors", Status: "unknown",
			Detail: "This host exposes no temperature sensors (normal for VMs and containers)."})
	}
	// Network: physical interfaces up, error counters.
	up, phys := 0, 0
	for _, in := range st.Interfaces {
		if in.Kind == "physical" {
			phys++
			if in.State == "up" {
				up++
			}
		}
	}
	nt := Tile{Key: "network", Label: "Network", Status: sevOf(CheckInterfaceDown)}
	switch {
	case nt.Status != "ok":
		nt.Text = "Interface down"
	case phys == 0:
		nt.Text = "Healthy"
		nt.Detail = "No physical interfaces (virtual host)"
	default:
		nt.Text = "Healthy"
		nt.Detail = fmt.Sprintf("%d of %d physical interfaces up", up, phys)
	}
	o.Tiles = append(o.Tiles, nt)
	// Services.
	units, _ := s.Services(ctx, hostID)
	notable, failed := 0, 0
	pol := s.eval.current(ctx)
	for _, u := range units {
		if u.Notable || pol.isWatched(hostID, u.Name) {
			notable++
			if u.State == "failed" {
				failed++
			}
		}
	}
	svc := Tile{Key: "services", Label: "Services", Status: sevOf(CheckServiceFailed, CheckServiceStopped)}
	switch {
	case len(units) == 0:
		svc.Text, svc.Status = "No inventory", "unknown"
		var note string
		s.d.Store.Reader.QueryRowContext(ctx, `SELECT services_note FROM mon_current WHERE host_id = ?`, hostID).Scan(&note)
		svc.Detail = note
	default:
		svc.Text = fmt.Sprintf("%d/%d healthy", notable-failed, notable)
		if failed > 0 && svc.Status == "ok" {
			svc.Status = "warning"
		}
	}
	o.Tiles = append(o.Tiles, svc)
	// Storage devices: the worst judged state.
	dt := Tile{Key: "devices", Label: "Storage devices", Text: "No data yet", Status: "unknown"}
	if sto != nil && len(sto.Devices) == 0 && sto.Note != "" {
		dt.Text, dt.Detail = "Not applicable", sto.Note
	}
	if sto != nil && len(sto.Devices) > 0 {
		order := map[string]int{storage.StateFailing: 0, storage.StateWarning: 1, storage.StateUnknown: 2, storage.StateNoIndicators: 3, storage.StateOK: 4}
		worst := storage.StateOK
		for _, d := range sto.Devices {
			if order[d.Health.State] < order[worst] {
				worst = d.Health.State
			}
		}
		switch worst {
		case storage.StateFailing:
			dt.Text, dt.Status = "Failing", "critical"
		case storage.StateWarning:
			dt.Text, dt.Status = "Warning", "warning"
		case storage.StateUnknown:
			dt.Text, dt.Status = "Health unknown", "unknown"
			dt.Detail = "At least one device's health can't be read (see Storage)."
		case storage.StateNoIndicators:
			dt.Text, dt.Status = "No failure indicators", "ok"
			dt.Detail = "Limited telemetry: no failure indicators detected."
		default:
			dt.Text, dt.Status = "Healthy", "ok"
		}
		dt.Status = worstStatus(dt.Status, sevOf(CheckStorageHealth, CheckMediaErrors, CheckKernelStorage, CheckProbe, CheckDeviceReadOnly))
		if dt.Status == "critical" && worst != storage.StateFailing {
			dt.Text = "Problem detected"
		} else if dt.Status == "warning" && worst != storage.StateWarning {
			dt.Text = "Warning"
		}
	}
	o.Tiles = append(o.Tiles, dt)
	// Active issues.
	it := Tile{Key: "issues", Label: "Active health issues", Text: "None", Status: "ok"}
	if n := len(issues); n > 0 {
		it.Text = fmt.Sprintf("%d", n)
		for _, i := range issues {
			it.Status = worstStatus(it.Status, map[string]string{"critical": "critical", "warning": "warning", "info": "info"}[i.Severity])
		}
	}
	o.Tiles = append(o.Tiles, it)
	return o, nil
}

func (s *Service) gaps(ctx context.Context, hostID string, since int64) ([]monitor.Gap, error) {
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT from_ts, to_ts, reason FROM mon_gaps WHERE host_id = ? AND to_ts >= ? ORDER BY from_ts LIMIT 200`, hostID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []monitor.Gap{}
	for rows.Next() {
		var g monitor.Gap
		rows.Scan(&g.From, &g.To, &g.Reason)
		out = append(out, g)
	}
	return out, rows.Err()
}

// StorageView is a host's storage health page.
type StorageView struct {
	UpdatedAt int64                           `json:"updated_at"`
	Smart     bool                            `json:"smart_read"`
	Devices   []storage.Device                `json:"devices"`
	Probes    map[string]agentmon.ProbeResult `json:"probes"`
	Events    []Event                         `json:"events"` // recent kernel storage evidence
	Source    string                          `json:"source"`
	Note      string                          `json:"note,omitempty"`
}

// Storage returns a host's devices with their health.
func (s *Service) Storage(ctx context.Context, hostID string) (*StorageView, error) {
	v := &StorageView{Devices: []storage.Device{}, Probes: map[string]agentmon.ProbeResult{}}
	s.mu.Lock()
	if c := s.cur[hostID]; c != nil {
		v.Source = c.source
		if c.storage != nil {
			v.UpdatedAt, v.Smart, v.Note = c.storage.At, c.storage.Smart, c.storage.Note
			v.Devices = append(v.Devices, c.storage.Devices...)
		}
		for k, p := range c.probes {
			v.Probes[k] = p
		}
	}
	s.mu.Unlock()
	ev, err := s.Events(ctx, hostID, "kernel", 50, 0)
	if err != nil {
		return nil, err
	}
	v.Events = ev
	return v, nil
}

// Event is a monitoring event.
type Event struct {
	ID       int64           `json:"id"`
	TS       int64           `json:"ts"`
	Kind     string          `json:"kind"`
	Subject  string          `json:"subject,omitempty"`
	Severity string          `json:"severity"`
	Message  string          `json:"message"`
	Data     json.RawMessage `json:"data,omitempty"`
}

// Events lists a host's events, newest first (bounded).
func (s *Service) Events(ctx context.Context, hostID, kind string, limit int, before int64) ([]Event, error) {
	q := `SELECT id, ts, kind, subject, severity, message, data_json FROM mon_events WHERE host_id = ?`
	args := []any{hostID}
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	if before > 0 {
		q += ` AND id < ?`
		args = append(args, before)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, clampInt(limit, 1, 500, 100))
	rows, err := s.d.Store.Reader.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var d string
		if err := rows.Scan(&e.ID, &e.TS, &e.Kind, &e.Subject, &e.Severity, &e.Message, &d); err != nil {
			return nil, err
		}
		if d != "" && json.Valid([]byte(d)) {
			e.Data = json.RawMessage(d)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// --- history ---

// Ranges for history.
var Ranges = map[string]struct {
	Dur  time.Duration
	Tier int
}{
	"1h": {time.Hour, TierRaw}, "6h": {6 * time.Hour, TierRaw}, "24h": {24 * time.Hour, Tier5m},
	"7d": {7 * 24 * time.Hour, Tier1h}, "30d": {30 * 24 * time.Hour, Tier1h},
}

// Series is one metric over time: [t, avg, max] triples. Missing
// windows are simply absent (gaps), never zeros.
type Series struct {
	Key    string       `json:"key"`
	Points [][3]float64 `json:"points"`
}

// History is a host's metric history for one range.
type History struct {
	Range     string        `json:"range"`
	From      int64         `json:"from"`
	To        int64         `json:"to"`
	StepS     int64         `json:"step_s"`
	Series    []Series      `json:"series"`
	Available []string      `json:"available"` // every key in the range (for pickers)
	Gaps      []monitor.Gap `json:"gaps"`
	Truncated bool          `json:"truncated"`
}

// History limits.
const (
	MaxHistorySeries = 32
	MaxHistoryPoints = 2000
)

// ErrBadRange: an unknown range.
var ErrBadRange = errors.New("range must be 1h, 6h, 24h, 7d or 30d")

// HistoryFor reads one host's series for a range. selectors are exact
// keys or prefixes ending in ':' ("fs:" = every filesystem).
func (s *Service) HistoryFor(ctx context.Context, hostID, rng string, selectors []string) (*History, error) {
	r, ok := Ranges[rng]
	if !ok {
		return nil, ErrBadRange
	}
	now := s.now()
	from := now.Add(-r.Dur).Unix()
	h := &History{Range: rng, From: from, To: now.Unix(), Series: []Series{}, Available: []string{}}
	tier := r.Tier
	// SSH hosts and young hosts may not have rolled-up tiers yet: fall
	// back to the finest tier that covers the range.
	pts, err := s.readTier(ctx, hostID, tier, from, now.Unix()+1)
	if err != nil {
		return nil, err
	}
	if tier > TierRaw && len(pts) < 2 {
		for t := tier - 1; t >= TierRaw && len(pts) < 2; t-- {
			pts, err = s.readTier(ctx, hostID, t, from, now.Unix()+1)
			if err != nil {
				return nil, err
			}
			tier = t
		}
	}
	if len(pts) > MaxHistoryPoints {
		pts, h.Truncated = pts[len(pts)-MaxHistoryPoints:], true
	}
	if len(pts) > 0 {
		h.StepS = pts[len(pts)-1].span
	}
	keys := map[string]bool{}
	for _, p := range pts {
		for k := range p.v {
			keys[k] = true
		}
	}
	for k := range keys {
		h.Available = append(h.Available, k)
	}
	sort.Strings(h.Available)
	want := func(k string) bool {
		for _, sel := range selectors {
			if sel == k || strings.HasSuffix(sel, ":") && strings.HasPrefix(k, sel) {
				return true
			}
		}
		return false
	}
	var sel []string
	for _, k := range h.Available {
		if want(k) {
			sel = append(sel, k)
		}
	}
	if len(sel) > MaxHistorySeries {
		sel, h.Truncated = sel[:MaxHistorySeries], true
	}
	for _, k := range sel {
		se := Series{Key: k, Points: [][3]float64{}}
		for _, p := range pts {
			if v, ok := p.v[k]; ok {
				se.Points = append(se.Points, [3]float64{float64(p.ts + p.span), round2(v[0]), round2(v[1])})
			}
		}
		h.Series = append(h.Series, se)
	}
	h.Gaps, _ = s.gaps(ctx, hostID, from)
	return h, nil
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
