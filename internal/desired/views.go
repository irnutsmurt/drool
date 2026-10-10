package desired

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"

	"drool/internal/filedeploy"
	"drool/internal/files"
	"drool/internal/model"
)

// Counts are status counts.
type Counts struct {
	Total       int `json:"total"`
	Compliant   int `json:"compliant"`
	Drift       int `json:"drift"`
	Conflict    int `json:"conflict"`
	Unknown     int `json:"unknown"`
	Reconciling int `json:"reconciling"`
	Failed      int `json:"failed"`
	AutoPaused  int `json:"auto_paused"`
}

func toCounts(c counts) Counts {
	return Counts{Total: c.total, Compliant: c.compliant, Drift: c.drift, Conflict: c.conflict, Unknown: c.unknown,
		Reconciling: c.reconciling, Failed: c.failed, AutoPaused: c.autoPaused}
}

// PausedBaseline is a baseline whose automatic restore needs attention.
type PausedBaseline struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Attention string `json:"attention"`
	At        *int64 `json:"at,omitempty"`
}

// Summary is the fleet's desired state at a glance.
type Summary struct {
	// Hosts by overall status; Unmanaged: hosts nothing applies to.
	Hosts     Counts `json:"hosts"`
	Unmanaged int    `json:"unmanaged"`
	// Resources by status (one per host and resource).
	Resources Counts `json:"resources"`
	Baselines int    `json:"baselines"`
	// AutoBaselines: baselines restoring automatically now (switched on
	// and not paused); AutoPaused: those paused and needing attention.
	AutoBaselines int              `json:"auto_baselines"`
	AutoPaused    []PausedBaseline `json:"auto_paused"`
	// Corrections in progress.
	Reconciling int   `json:"reconciling"`
	ChangedAt   int64 `json:"changed_at"`
}

// Summary computes the fleet summary.
func (s *Service) Summary(ctx context.Context) (*Summary, error) {
	s.fresh(ctx)
	out := &Summary{AutoPaused: []PausedBaseline{}}
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT status, n_total, n_compliant, n_drift, n_conflict, n_unknown, n_reconciling, n_failed,
		n_auto_paused, changed_at FROM ds_host_state`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var st string
		var c counts
		var changed int64
		if err := rows.Scan(&st, &c.total, &c.compliant, &c.drift, &c.conflict, &c.unknown, &c.reconciling, &c.failed, &c.autoPaused, &changed); err != nil {
			rows.Close()
			return nil, err
		}
		if c.total == 0 {
			continue
		}
		out.Hosts.Total++
		switch st {
		case StatusCompliant:
			out.Hosts.Compliant++
		case StatusDrift:
			out.Hosts.Drift++
		case StatusConflict:
			out.Hosts.Conflict++
		case StatusUnknown:
			out.Hosts.Unknown++
		case StatusReconciling:
			out.Hosts.Reconciling++
		case StatusFailed:
			out.Hosts.Failed++
		}
		if c.autoPaused > 0 {
			out.Hosts.AutoPaused++
		}
		r := &out.Resources
		r.Total += c.total
		r.Compliant += c.compliant
		r.Drift += c.drift
		r.Conflict += c.conflict
		r.Unknown += c.unknown
		r.Reconciling += c.reconciling
		r.Failed += c.failed
		r.AutoPaused += c.autoPaused
		out.ChangedAt = max(out.ChangedAt, changed)
	}
	rows.Close()
	snap := s.Snapshot()
	if snap != nil {
		out.Unmanaged = len(snap.Hosts) - out.Hosts.Total
		if out.Unmanaged < 0 {
			out.Unmanaged = 0
		}
	}
	if err := s.d.Store.Reader.QueryRowContext(ctx, `SELECT count(*), COALESCE(sum(auto_enabled = 1 AND attention = ''), 0) FROM ds_baselines WHERE archived_at IS NULL`).
		Scan(&out.Baselines, &out.AutoBaselines); err != nil {
		return nil, err
	}
	prow, err := s.d.Store.Reader.QueryContext(ctx, `SELECT id, name, attention, attention_at FROM ds_baselines WHERE archived_at IS NULL AND attention <> ''
		ORDER BY attention_at DESC LIMIT 20`)
	if err != nil {
		return nil, err
	}
	for prow.Next() {
		var p PausedBaseline
		var at sql.NullInt64
		if err := prow.Scan(&p.ID, &p.Name, &p.Attention, &at); err != nil {
			prow.Close()
			return nil, err
		}
		if at.Valid {
			p.At = &at.Int64
		}
		out.AutoPaused = append(out.AutoPaused, p)
	}
	prow.Close()
	s.d.Store.Reader.QueryRowContext(ctx, `SELECT count(*) FROM ds_reconciles WHERE status = 'active'`).Scan(&out.Reconciling)
	return out, nil
}

// HostRow is a host in the fleet list.
type HostRow struct {
	HostID     string        `json:"host_id"`
	Name       string        `json:"name"`
	Status     string        `json:"status"`
	Counts     Counts        `json:"counts"`
	Baselines  []BaselineRef `json:"baselines"`
	Problems   []string      `json:"problems"` // the first few resources that differ, in words
	Unknown    string        `json:"unknown,omitempty"`
	CheckedAt  *int64        `json:"checked_at,omitempty"`
	ChangedAt  int64         `json:"changed_at"`
	InMaint    bool          `json:"in_maintenance"`
	Reconciles int           `json:"reconciles"`
}

// HostFilter narrows the host list.
type HostFilter struct {
	Status     string // compliant | drift | conflict | unknown | reconciling | failed | attention (anything not compliant)
	Query      string
	BaselineID string
	Limit      int
}

// Hosts lists managed hosts with their status.
func (s *Service) Hosts(ctx context.Context, f HostFilter) ([]HostRow, error) {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 1000
	}
	s.fresh(ctx)
	s.mu.Lock()
	snap, eff := s.snap, s.eff
	s.mu.Unlock()
	if snap == nil {
		return []HostRow{}, nil
	}
	maint := map[string]bool{}
	if s.d.Alerts != nil {
		if m, err := s.d.Alerts.HostsInMaintenance(ctx); err == nil {
			for h := range m {
				maint[h] = true
			}
		}
	}
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT host_id, status, n_total, n_compliant, n_drift, n_conflict, n_unknown, n_reconciling,
		n_failed, n_auto_paused, packages_at, files_at, changed_at FROM ds_host_state`)
	if err != nil {
		return nil, err
	}
	byHost := map[string]*HostRow{}
	for rows.Next() {
		var hr HostRow
		var c counts
		var pa, fa sql.NullInt64
		if err := rows.Scan(&hr.HostID, &hr.Status, &c.total, &c.compliant, &c.drift, &c.conflict, &c.unknown, &c.reconciling, &c.failed,
			&c.autoPaused, &pa, &fa, &hr.ChangedAt); err != nil {
			rows.Close()
			return nil, err
		}
		hr.Counts = toCounts(c)
		for _, t := range []sql.NullInt64{pa, fa} {
			if t.Valid && (hr.CheckedAt == nil || t.Int64 < *hr.CheckedAt) {
				v := t.Int64
				hr.CheckedAt = &v
			}
		}
		byHost[hr.HostID] = &hr
	}
	rows.Close()
	// What differs, in words (a few per host). Listed for one baseline, a
	// host's status, counts and problems are those of the resources that
	// baseline decides there (not the host's other baselines').
	byBaseline := map[string]*counts{}
	if f.BaselineID != "" {
		for id := range byHost {
			byBaseline[id] = &counts{}
		}
	}
	prob, err := s.d.Store.Reader.QueryContext(ctx, `SELECT host_id, key, status, note, auto_paused FROM ds_resource_state
		WHERE status <> 'compliant' OR ? <> '' ORDER BY host_id, key`, f.BaselineID)
	if err != nil {
		return nil, err
	}
	for prob.Next() {
		var hid, key, st, note, paused string
		if err := prob.Scan(&hid, &key, &st, &note, &paused); err != nil {
			prob.Close()
			return nil, err
		}
		hr := byHost[hid]
		if hr == nil {
			continue
		}
		if f.BaselineID != "" {
			if !hasBaseline(eff[hid].Resources[key].Source.Baselines, f.BaselineID) {
				continue
			}
			byBaseline[hid].add(st, paused != "")
			if st == StatusCompliant {
				continue
			}
		}
		if st == StatusUnknown && hr.Unknown == "" {
			hr.Unknown = note
		}
		if st == StatusReconciling {
			hr.Reconciles++
		}
		if len(hr.Problems) < 3 && st != StatusUnknown && st != StatusReconciling {
			hr.Problems = append(hr.Problems, Describe(key)+" ("+statusWord(st)+")")
		}
	}
	prob.Close()
	for id, c := range byBaseline {
		byHost[id].Status, byHost[id].Counts = hostStatus(*c), toCounts(*c)
	}
	q := strings.ToLower(strings.TrimSpace(f.Query))
	var out []HostRow
	for id, name := range snap.Hosts {
		hr := byHost[id]
		if hr == nil {
			continue // nothing managed
		}
		hr.Name = name
		hr.InMaint = maint[id]
		hs := eff[id]
		seen := map[string]bool{}
		for _, e := range hs.Resources {
			for _, b := range e.Source.Baselines {
				if !seen[b.ID] {
					seen[b.ID] = true
					hr.Baselines = append(hr.Baselines, b)
				}
			}
		}
		sort.Slice(hr.Baselines, func(i, j int) bool { return hr.Baselines[i].Name < hr.Baselines[j].Name })
		if hr.Baselines == nil {
			hr.Baselines = []BaselineRef{}
		}
		if hr.Problems == nil {
			hr.Problems = []string{}
		}
		if q != "" && !strings.Contains(strings.ToLower(name), q) {
			continue
		}
		if f.BaselineID != "" && !seen[f.BaselineID] {
			continue
		}
		switch f.Status {
		case "":
		case "attention":
			if hr.Status == StatusCompliant {
				continue
			}
		default:
			if hr.Status != f.Status {
				continue
			}
		}
		out = append(out, *hr)
	}
	rank := map[string]int{StatusFailed: 0, StatusConflict: 1, StatusDrift: 2, StatusReconciling: 3, StatusUnknown: 4, StatusCompliant: 5}
	sort.Slice(out, func(i, j int) bool {
		if rank[out[i].Status] != rank[out[j].Status] {
			return rank[out[i].Status] < rank[out[j].Status]
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	if len(out) > f.Limit {
		out = out[:f.Limit]
	}
	if out == nil {
		out = []HostRow{}
	}
	return out, nil
}

func statusWord(st string) string {
	switch st {
	case StatusDrift:
		return "drift"
	case StatusFailed:
		return "restore failed"
	case StatusConflict:
		return "conflict"
	}
	return st
}

// ResourceView is one managed resource on one host.
type ResourceView struct {
	Key        string       `json:"key"`
	Kind       string       `json:"kind"`
	Name       string       `json:"name"`
	Status     string       `json:"status"`
	Since      int64        `json:"since"`
	Desired    *Resource    `json:"desired,omitempty"`
	Want       string       `json:"want"`
	Current    string       `json:"current"`
	Observed   *Observation `json:"observed,omitempty"`
	ObservedAt *int64       `json:"observed_at,omitempty"`
	Diffs      []Diff       `json:"diffs"`
	// Note: why it is unknown, why a restore failed, why it can't be restored.
	Note   string `json:"note,omitempty"`
	Source Source `json:"source"`
	// Restorable: Restore managed state can correct it now; Blocker says
	// why not otherwise.
	Restorable bool   `json:"restorable"`
	Blocker    string `json:"blocker,omitempty"`
	// Acceptable: Accept current state can make what it is now desired.
	Acceptable bool    `json:"acceptable"`
	JobID      string  `json:"job_id,omitempty"`      // a correction in progress
	LastJobID  string  `json:"last_job_id,omitempty"` // the last correction
	Auto       AutoRes `json:"auto"`
}

// AutoRes is a resource's automatic restore.
type AutoRes struct {
	Enabled  bool         `json:"enabled"`
	Baseline *BaselineRef `json:"baseline,omitempty"`
	Paused   string       `json:"paused,omitempty"`
	Attempts int          `json:"attempts"`
	NextAt   *int64       `json:"next_at,omitempty"`
}

// ExcludedView is a resource a host-specific setting leaves unmanaged.
type ExcludedView struct {
	Key    string `json:"key"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Source Source `json:"source"`
}

// HostView is a host's effective desired state, provenance and drift.
type HostView struct {
	HostID       string         `json:"host_id"`
	Name         string         `json:"name"`
	Status       string         `json:"status"`
	Counts       Counts         `json:"counts"`
	Resources    []ResourceView `json:"resources"`
	Excluded     []ExcludedView `json:"excluded"`
	Groups       []GroupRef     `json:"groups"`
	Baselines    []BaselineUse  `json:"baselines"`
	PackagesAt   *int64         `json:"packages_at,omitempty"`
	PackagesNote string         `json:"packages_note,omitempty"`
	FilesAt      *int64         `json:"files_at,omitempty"`
	FilesNote    string         `json:"files_note,omitempty"`
	ServicesAt   *int64         `json:"services_at,omitempty"`
	UnknownSince *int64         `json:"unknown_since,omitempty"`
	Maintenance  string         `json:"maintenance,omitempty"` // reason, when in maintenance
	Path         PathInfo       `json:"path"`
	// CheckEvery: how often packages and files are checked (seconds).
	CheckEvery int `json:"check_every_s"`
}

// BaselineUse is a baseline that applies to a host, and how.
type BaselineUse struct {
	BaselineRef
	Via string `json:"via"` // "everywhere", "pack Media Servers", "this host"
}

// Host returns a host's view.
func (s *Service) Host(ctx context.Context, hostID string) (*HostView, error) {
	s.fresh(ctx)
	s.mu.Lock()
	snap := s.snap
	hs, ok := s.eff[hostID]
	s.mu.Unlock()
	if snap == nil {
		return nil, errors.New("desired state is starting")
	}
	name, known := snap.Hosts[hostID]
	if !known {
		return nil, ErrNotFound
	}
	v := &HostView{HostID: hostID, Name: name, Status: StatusNone, Resources: []ResourceView{}, Excluded: []ExcludedView{},
		Groups: []GroupRef{}, Baselines: []BaselineUse{}, CheckEvery: int(s.cfg.CheckEvery.Seconds())}
	h, err := loadHost(ctx, s.d.Store.Reader, hostID)
	if err != nil {
		return nil, err
	}
	if h.exists {
		v.Status, v.Counts = h.Status, toCounts(h.C)
		v.PackagesAt, v.FilesAt = nullPtr(h.PackagesAt), nullPtr(h.FilesAt)
		v.PackagesNote, v.FilesNote, v.UnknownSince = h.PackagesNote, h.FilesNote, nullPtr(h.UnknownSince)
	}
	if at := s.servicesStamp(ctx, hostID); at > 0 {
		v.ServicesAt = &at
	}
	for _, gid := range snap.Members[hostID] {
		if g, ok := snap.Groups[gid]; ok {
			v.Groups = append(v.Groups, GroupRef{ID: gid, Name: g.Name, Priority: g.Priority})
		}
	}
	sort.Slice(v.Groups, func(i, j int) bool {
		if v.Groups[i].Priority != v.Groups[j].Priority {
			return v.Groups[i].Priority > v.Groups[j].Priority
		}
		return v.Groups[i].Name < v.Groups[j].Name
	})
	v.Baselines = baselinesFor(snap, hostID)
	if s.d.Alerts != nil {
		if m, err := s.d.Alerts.HostsInMaintenance(ctx); err == nil {
			if ref, ok := m[hostID]; ok {
				v.Maintenance = ref.Reason
				if v.Maintenance == "" {
					v.Maintenance = "maintenance"
				}
			}
		}
	}
	if dbh, err := s.d.Store.R.GetHost(ctx, hostID); err == nil && s.d.Path != nil {
		v.Path = s.d.Path(ctx, dbh)
	}
	if !ok {
		return v, nil
	}
	rows, err := loadRows(ctx, s.d.Store.Reader, hostID)
	if err != nil {
		return nil, err
	}
	for k, e := range hs.Resources {
		v.Resources = append(v.Resources, resourceView(k, e, rows[k]))
	}
	rank := map[string]int{StatusFailed: 0, StatusConflict: 1, StatusDrift: 2, StatusReconciling: 3, StatusUnknown: 4, StatusCompliant: 5}
	sort.Slice(v.Resources, func(i, j int) bool {
		a, b := v.Resources[i], v.Resources[j]
		if rank[a.Status] != rank[b.Status] {
			return rank[a.Status] < rank[b.Status]
		}
		return a.Key < b.Key
	})
	for k, src := range hs.Excluded {
		v.Excluded = append(v.Excluded, ExcludedView{Key: k, Name: KeyName(k), Kind: KeyKind(k), Source: src})
	}
	sort.Slice(v.Excluded, func(i, j int) bool { return v.Excluded[i].Key < v.Excluded[j].Key })
	return v, nil
}

// baselinesFor lists the baselines that apply to a host and through what.
func baselinesFor(snap *Snapshot, hostID string) []BaselineUse {
	var out []BaselineUse
	add := func(id, via string) {
		if b, ok := snap.Baselines[id]; ok {
			out = append(out, BaselineUse{BaselineRef: BaselineRef{ID: b.ID, Name: b.Name, Revision: b.Revision}, Via: via})
		}
	}
	for _, id := range snap.ByHost[hostID] {
		add(id, "this host")
	}
	for _, gid := range snap.Members[hostID] {
		for _, id := range snap.ByGroup[gid] {
			add(id, "pack "+snap.Groups[gid].Name)
		}
	}
	for _, id := range snap.Global {
		add(id, "everywhere")
	}
	if out == nil {
		out = []BaselineUse{}
	}
	return out
}

func nullPtr(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

func resourceView(key string, e Effective, r *resRow) ResourceView {
	v := ResourceView{Key: key, Kind: e.Kind, Name: KeyName(key), Status: StatusUnknown, Desired: e.Resource, Source: e.Source,
		Diffs: []Diff{}, Note: "not checked yet"}
	if e.Resource != nil {
		v.Want = e.Resource.Want()
	} else {
		var ws []string
		for _, c := range e.Source.Competing {
			if !slices.Contains(ws, c.Want) {
				ws = append(ws, c.Want)
			}
		}
		v.Want = strings.Join(ws, " or ") + " (conflict)"
		v.Status = StatusConflict
	}
	v.Auto = AutoRes{Enabled: e.Source.Auto && e.Source.Level != LevelHost, Baseline: e.Source.AutoBaseline}
	if r == nil {
		return v
	}
	v.Status, v.Since, v.Note = r.Status, r.Since, r.Note
	v.ObservedAt = nullPtr(r.ObservedAt)
	_ = json.Unmarshal([]byte(r.Diffs), &v.Diffs)
	if v.Diffs == nil {
		v.Diffs = []Diff{}
	}
	if r.Observed != "" {
		var o Observation
		if json.Unmarshal([]byte(r.Observed), &o) == nil {
			v.Observed = &o
			v.Current = CurrentText(e.Kind, o)
		}
	}
	if r.JobID.Valid {
		v.JobID = r.JobID.String
	}
	v.LastJobID = r.LastJobID
	v.Auto.Paused, v.Auto.Attempts, v.Auto.NextAt = r.AutoPaused, r.AutoAttempts, nullPtr(r.AutoNextAt)
	switch r.Status {
	case StatusDrift, StatusFailed:
		if e.Resource != nil && v.Observed != nil {
			j := Compare(*e.Resource, *v.Observed)
			v.Restorable, v.Blocker = j.Blocker == "", j.Blocker
		}
		v.Acceptable = true
	case StatusConflict:
		v.Blocker = "a conflict can't be restored until it is resolved"
		v.Acceptable = v.Observed != nil && v.Observed.Error == ""
	case StatusUnknown:
		v.Blocker = "DROOL can't currently verify it"
	}
	if v.Observed != nil && v.Observed.File != nil && v.Observed.File.Exists && v.Observed.File.Type != "" && v.Observed.File.Type != string(files.KindFile) {
		v.Acceptable = false
	}
	return v
}

// Resource returns one resource on a host with its recent history.
func (s *Service) Resource(ctx context.Context, hostID, key string) (*ResourceView, []HistoryEntry, error) {
	s.fresh(ctx)
	s.mu.Lock()
	hs, ok := s.eff[hostID]
	s.mu.Unlock()
	if !ok {
		return nil, nil, ErrNotFound
	}
	e, ok := hs.Resources[key]
	if !ok {
		return nil, nil, ErrNotFound
	}
	var r *resRow
	row, err := scanRes(s.d.Store.Reader.QueryRowContext(ctx, `SELECT `+resCols+` FROM ds_resource_state WHERE host_id = ? AND key = ?`, hostID, key))
	if err == nil {
		r = &row
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, err
	}
	v := resourceView(key, e, r)
	hist, err := s.History(ctx, HistoryFilter{HostID: hostID, Key: key, Limit: 50})
	if err != nil {
		return nil, nil, err
	}
	return &v, hist, nil
}

// HistoryEntry is one recorded transition.
type HistoryEntry struct {
	ID          int64        `json:"id"`
	TS          int64        `json:"ts"`
	HostID      string       `json:"host_id"`
	HostName    string       `json:"host_name"`
	Key         string       `json:"key,omitempty"`
	Resource    string       `json:"resource,omitempty"`
	Kind        string       `json:"kind"`
	Status      string       `json:"status,omitempty"`
	PrevStatus  string       `json:"prev_status,omitempty"`
	Resolution  string       `json:"resolution,omitempty"`
	Detail      string       `json:"detail"`
	Desired     *Resource    `json:"desired,omitempty"`
	Diffs       []Diff       `json:"diffs"`
	Baseline    *BaselineRef `json:"baseline,omitempty"`
	SourceLabel string       `json:"source_label,omitempty"`
	JobID       string       `json:"job_id,omitempty"`
	Actor       string       `json:"actor,omitempty"`
}

// HistoryFilter narrows history.
type HistoryFilter struct {
	HostID, Key, BaselineID, Kind string
	Before                        int64 // id
	Limit                         int
}

// History lists transitions, newest first.
func (s *Service) History(ctx context.Context, f HistoryFilter) ([]HistoryEntry, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	where := []string{"1 = 1"}
	var args []any
	if f.HostID != "" {
		where, args = append(where, "host_id = ?"), append(args, f.HostID)
	}
	if f.Key != "" {
		where, args = append(where, "key = ?"), append(args, f.Key)
	}
	if f.BaselineID != "" {
		where, args = append(where, "baseline_id = ?"), append(args, f.BaselineID)
	}
	if f.Kind != "" {
		where, args = append(where, "kind = ?"), append(args, f.Kind)
	}
	if f.Before > 0 {
		where, args = append(where, "id < ?"), append(args, f.Before)
	}
	args = append(args, f.Limit)
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT h.id, h.ts, h.host_id, h.host_name, h.key, h.kind, h.status, h.prev_status, h.resolution,
		h.detail, h.desired_json, h.diffs_json, h.baseline_id, h.baseline_revision, COALESCE(b.name, ''), h.source_label, h.job_id, h.actor_name
		FROM ds_history h LEFT JOIN ds_baselines b ON b.id = h.baseline_id WHERE `+strings.Join(where, " AND ")+` ORDER BY h.id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HistoryEntry{}
	for rows.Next() {
		var e HistoryEntry
		var dj, fj, bid, bname string
		var brev int
		if err := rows.Scan(&e.ID, &e.TS, &e.HostID, &e.HostName, &e.Key, &e.Kind, &e.Status, &e.PrevStatus, &e.Resolution, &e.Detail,
			&dj, &fj, &bid, &brev, &bname, &e.SourceLabel, &e.JobID, &e.Actor); err != nil {
			return nil, err
		}
		if e.Key != "" {
			e.Resource = Describe(e.Key)
		}
		if dj != "" {
			var r Resource
			if json.Unmarshal([]byte(dj), &r) == nil {
				e.Desired = &r
			}
		}
		_ = json.Unmarshal([]byte(fj), &e.Diffs)
		if e.Diffs == nil {
			e.Diffs = []Diff{}
		}
		if bid != "" {
			e.Baseline = &BaselineRef{ID: bid, Name: bname, Revision: brev}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// FileReview is Review difference for a managed file.
type FileReview struct {
	// State: create (missing), identical, replace (content differs),
	// metadata (only owner/group/mode differ).
	State    string                `json:"state"`
	Current  *filedeploy.FileState `json:"current,omitempty"`
	Desired  filedeploy.Meta       `json:"desired"`
	Diff     string                `json:"diff,omitempty"`
	DiffNote string                `json:"diff_note,omitempty"`
	// Artifact: the desired content's identity in the artifact store.
	Artifact     string `json:"artifact"`
	ArtifactSize int64  `json:"artifact_size"`
	ArtifactText bool   `json:"artifact_text"`
}

// ReviewFile compares a managed file on the host with its desired content
// (read-only: stat, hash, and for small text files their content). The
// caller decides whether content may be shown (files.read).
func (s *Service) ReviewFile(ctx context.Context, hostID, key string, content bool) (*FileReview, error) {
	s.mu.Lock()
	hs, ok := s.eff[hostID]
	s.mu.Unlock()
	if !ok {
		return nil, ErrNotFound
	}
	e, ok := hs.Resources[key]
	if !ok || e.Kind != KindFile {
		return nil, ErrNotFound
	}
	if e.Resource == nil {
		return nil, &ValidationError{Msg: "this file's desired state is a conflict; there is no single desired content to compare with"}
	}
	r := *e.Resource
	op := r.Corrective(false).FileDeploy
	if err := (&model.Operation{Type: model.OpFileDeploy, FileDeploy: op}).Normalize(); err != nil {
		return nil, err
	}
	fsys, err := s.d.Prober.OpenFS(ctx, hostID, r.Elevated)
	if err != nil {
		return nil, &ValidationError{Msg: "couldn't read the host: " + errText(err)}
	}
	defer fsys.Close()
	out := &FileReview{Artifact: r.Artifact}
	if s.d.Artifacts != nil {
		if a, err := s.d.Artifacts.Get(ctx, r.Artifact); err == nil {
			out.ArtifactSize, out.ArtifactText = a.Size, a.IsText
		}
	}
	src := s.d.Artifacts
	if !content {
		src = nil
	}
	pr, err := previewFile(ctx, fsys, src, op, content)
	if err != nil {
		return nil, &ValidationError{Msg: errText(err)}
	}
	out.State, out.Current, out.Desired, out.Diff, out.DiffNote = pr.State, pr.Current, pr.Desired, pr.Diff, pr.DiffNote
	if !content && out.State == filedeploy.StateReplace {
		out.DiffNote = "content differs; showing file contents needs the files.read permission"
	}
	return out, nil
}
