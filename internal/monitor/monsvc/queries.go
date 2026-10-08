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

// HostSettings returns a host's monitoring settings.
func (s *Service) HostSettings(ctx context.Context, hostID string) HostSettings {
	h := HostSettings{WatchedServices: []string{}, ProbeMounts: []string{}, DisabledChecks: []string{}, ProbeS: agentmon.DefaultProbeS}
	var j string
	if err := s.d.Store.Reader.QueryRowContext(ctx, `SELECT settings_json FROM mon_host_settings WHERE host_id = ?`, hostID).Scan(&j); err == nil {
		json.Unmarshal([]byte(j), &h)
	}
	return h
}

// SaveHostSettingsTx stores a host's settings (the caller audits).
func SaveHostSettingsTx(ctx context.Context, tx *sql.Tx, hostID string, h HostSettings, by string, now int64) error {
	b, _ := json.Marshal(h)
	var byv any
	if by != "" {
		byv = by
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
	Watched bool   `json:"watched"`
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
	hs := s.HostSettings(ctx, hostID)
	for _, u := range units {
		u.Watched = hs.Watched(u.Name)
		if !allUnits && !u.Notable && !u.Watched {
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
	q := `SELECT i.id, i.host_id, h.name, i.check_id, i.subject, i.severity, i.state, i.title, i.detail, i.value, i.started_at, i.updated_at, i.resolved_at
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
		if err := rows.Scan(&i.ID, &i.HostID, &i.HostName, &i.Check, &i.Subject, &i.Severity, &i.State, &i.Title, &i.Detail, &v, &i.StartedAt, &i.UpdatedAt, &r); err != nil {
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
	interval := time.Minute
	if o.Source == "ssh" {
		interval = time.Duration(set.SSHPollS) * time.Second
	}
	o.Stale = s.now().Sub(time.UnixMilli(o.UpdatedAt)) > time.Duration(set.Thresholds().StaleIntervals)*interval+time.Minute
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
	for _, u := range units {
		if u.Notable || o.Settings.Watched(u.Name) {
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
