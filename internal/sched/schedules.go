package sched

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"drool/internal/auth"
	"drool/internal/engine"
	"drool/internal/ids"
	"drool/internal/model"
	"drool/internal/store/db"
)

// Schedule kinds.
const (
	ScheduleJob         = "job"
	ScheduleMaintenance = "maintenance"
)

// Missed-run and overlap policies.
const (
	MissedSkip    = "skip"     // recurring default: skip what was missed, continue with the next run
	MissedRunOnce = "run_once" // run the most recent missed occurrence once when DROOL returns
	OverlapSkip   = "skip"     // default: skip a run while the previous one is still active
	OverlapQueue  = "queue_one"
)

// MaxWindow bounds a recurring maintenance window.
const MaxWindow = 7 * 24 * time.Hour

// WorkflowRef pins a workflow revision.
type WorkflowRef struct {
	ID       string `json:"id"`
	Revision int    `json:"revision"`
}

// Config is a schedule's complete configuration (one revision).
type Config struct {
	Recurrence Recurrence `json:"recurrence"`
	TimeZone   string     `json:"time_zone"`
	// Job schedules: a pinned workflow revision, or inline steps.
	Workflow *WorkflowRef      `json:"workflow,omitempty"`
	Steps    []model.Operation `json:"steps,omitempty"`
	// Targets are selectors (hosts, groups, tags) resolved again at
	// every run; each job freezes its own run's hosts.
	Targets  model.TargetSpec `json:"targets"`
	Strategy model.Strategy   `json:"strategy"`
	// MissedPolicy (recurring): skip | run_once.
	MissedPolicy string `json:"missed_policy,omitempty"`
	// LateLimitMinutes (one-time): don't start when DROOL returns more
	// than this late (0: start however late).
	LateLimitMinutes int `json:"late_limit_minutes,omitempty"`
	// OverlapPolicy: skip | queue_one.
	OverlapPolicy string `json:"overlap_policy,omitempty"`
	// WindowID associates a recurring maintenance window: the run may
	// proceed on hosts that this window (and only this window) has in
	// maintenance, and runs only while it is active.
	WindowID string `json:"window_id,omitempty"`
	// Maintenance windows.
	DurationMinutes int    `json:"duration_minutes,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

// ScheduleInput creates or edits a schedule.
type ScheduleInput struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Enabled *bool  `json:"enabled,omitempty"`
	Config
}

// Person is a user as shown.
type Person struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Schedule is a schedule as shown.
type Schedule struct {
	ID        string  `json:"id"`
	Kind      string  `json:"kind"`
	Name      string  `json:"name"`
	Enabled   bool    `json:"enabled"`
	Archived  bool    `json:"archived"`
	Owner     *Person `json:"owner"` // nil: the owner's account was deleted
	Revision  int     `json:"revision"`
	Config    Config  `json:"config"`
	When      string  `json:"when"` // "Every Sunday at 03:00 (America/Los_Angeles)"
	NextRunAt *int64  `json:"next_run_at,omitempty"`
	// NextRunLocal is the next run in the schedule's zone; NextRunNote
	// explains a DST shift.
	NextRunLocal string `json:"next_run_local,omitempty"`
	NextRunNote  string `json:"next_run_note,omitempty"`
	// Attention: why it needs attention ("" when fine).
	Attention   string `json:"attention,omitempty"`
	AttentionAt *int64 `json:"attention_at,omitempty"`
	// Workflow: name and whether a newer revision exists.
	WorkflowName    string `json:"workflow_name,omitempty"`
	WorkflowLatest  int    `json:"workflow_latest,omitempty"`
	WorkflowUpdate  bool   `json:"workflow_update_available"`
	WorkflowArchive bool   `json:"workflow_archived,omitempty"`
	WindowName      string `json:"window_name,omitempty"`
	// Action: what runs, in words.
	Action         string      `json:"action"`
	Targets        string      `json:"targets_text"`
	LastOccurrence *Occurrence `json:"last_occurrence,omitempty"`
	// Active: the run in progress (a job) or the active window.
	ActiveJobID         string `json:"active_job_id,omitempty"`
	ActiveMaintenanceID string `json:"active_maintenance_id,omitempty"`
	CreatedAt           int64  `json:"created_at"`
	UpdatedAt           int64  `json:"updated_at"`
	UpdatedBy           string `json:"updated_by,omitempty"`
}

// RequiredPermissions are what running a schedule needs, checked for its
// owner before every occurrence (and for whoever creates or runs it).
func RequiredPermissions(kind string, steps []model.Operation) []auth.Permission {
	if kind == ScheduleMaintenance {
		return []auth.Permission{auth.PermMaintenanceManage}
	}
	need := []auth.Permission{auth.PermJobsExecute}
	add := func(p auth.Permission) {
		for _, x := range need {
			if x == p {
				return
			}
		}
		need = append(need, p)
	}
	for _, st := range steps {
		switch st.Type {
		case model.OpFileDeploy:
			add(auth.PermFilesDeploy)
			if st.FileDeploy != nil && st.FileDeploy.Elevated {
				add(auth.PermFilesElevate)
			}
		case model.OpFirewallApply:
			add(auth.PermFirewallWrite)
			if f := st.FirewallApply; f != nil && (f.OverrideMgmt || f.OverrideTimeout || f.AllowDrift) {
				add(auth.PermFirewallOverride)
			}
		}
	}
	return need
}

// MissingPermission returns the first permission role lacks, or "".
func MissingPermission(role string, perms []auth.Permission) auth.Permission {
	for _, p := range perms {
		if !auth.RoleHas(role, p) {
			return p
		}
	}
	return ""
}

// steps returns what a job schedule runs (inline, or its pinned
// workflow revision).
func (s *Service) steps(ctx context.Context, q queryer, c Config) ([]model.Operation, *Revision, error) {
	if c.Workflow != nil {
		r, err := revision(ctx, q, c.Workflow.ID, c.Workflow.Revision)
		if err != nil {
			return nil, nil, err
		}
		return r.Steps, r, nil
	}
	return c.Steps, nil, nil
}

// normalize validates input for a kind.
func (s *Service) normalize(ctx context.Context, in *ScheduleInput, prev *Config) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len([]rune(in.Name)) > 100 {
		return invalid("a schedule needs a name of at most 100 characters")
	}
	c := &in.Config
	if err := c.Recurrence.Normalize(); err != nil {
		return &ValidationError{Msg: err.Error()}
	}
	loc, err := LoadZone(c.TimeZone)
	if err != nil {
		return &ValidationError{Msg: err.Error()}
	}
	c.TimeZone = loc.String()
	t := &c.Targets
	if t.Empty() {
		return invalid("choose at least one host, group or tag")
	}
	if len(t.Hosts)+len(t.Groups)+len(t.Tags) > 500 {
		return invalid("at most 500 hosts, groups and tags")
	}
	// Selectors must exist now (they are resolved again at every run).
	if _, _, err := engine.ResolveTargets(ctx, s.d.Store.R, *t); err != nil {
		var ve *engine.ValidationError
		if errors.As(err, &ve) {
			return &ValidationError{Msg: ve.Msg}
		}
		return err
	}
	switch in.Kind {
	case ScheduleMaintenance:
		if len(t.Tags) > 0 || len(t.ExcludeHosts) > 0 {
			return invalid("maintenance windows take hosts and groups")
		}
		if c.Workflow != nil || len(c.Steps) > 0 || c.WindowID != "" {
			return invalid("a maintenance window has no steps, workflow or window")
		}
		d := time.Duration(c.DurationMinutes) * time.Minute
		if d < time.Minute || d > MaxWindow {
			return invalid("the window must last 1 minute to 7 days")
		}
		if sp := c.Recurrence.MinSpacing(); sp > 0 && d > sp {
			return invalid("the window lasts longer than the time between its runs (%s); it would overlap itself", sp.Round(time.Hour))
		}
		c.Reason = strings.TrimSpace(c.Reason)
		if len([]rune(c.Reason)) > 200 {
			return invalid("the reason must be at most 200 characters")
		}
		c.Strategy, c.MissedPolicy, c.OverlapPolicy, c.LateLimitMinutes = model.Strategy{}, "", "", 0
		return nil
	case ScheduleJob:
	default:
		return invalid(`kind must be "job" or "maintenance"`)
	}
	c.DurationMinutes, c.Reason = 0, ""
	switch {
	case c.Workflow != nil && len(c.Steps) > 0:
		return invalid("choose a workflow or steps, not both")
	case c.Workflow != nil:
		var archived sql.NullInt64
		var latest int
		if err := s.d.Store.Reader.QueryRowContext(ctx, `SELECT latest_revision, archived_at FROM workflows WHERE id = ?`, c.Workflow.ID).Scan(&latest, &archived); err != nil {
			return invalid("workflow %s not found", c.Workflow.ID)
		}
		if c.Workflow.Revision == 0 {
			c.Workflow.Revision = latest
		}
		if c.Workflow.Revision < 1 || c.Workflow.Revision > latest {
			return invalid("workflow revision %d doesn't exist", c.Workflow.Revision)
		}
		changing := prev == nil || prev.Workflow == nil || prev.Workflow.ID != c.Workflow.ID
		if archived.Valid && changing {
			return invalid("that workflow is archived")
		}
	case len(c.Steps) > 0:
		if err := normalizeSteps(c.Steps); err != nil {
			return err
		}
	default:
		return invalid("choose a workflow, or the steps to run")
	}
	if err := model.NormalizeStrategy(&c.Strategy); err != nil {
		return &ValidationError{Msg: err.Error()}
	}
	switch c.MissedPolicy {
	case "":
		c.MissedPolicy = MissedSkip
	case MissedSkip, MissedRunOnce:
	default:
		return invalid(`missed_policy must be "skip" or "run_once"`)
	}
	if c.LateLimitMinutes < 0 || c.LateLimitMinutes > 7*24*60 {
		return invalid("the late limit must be 0 (none) to 7 days")
	}
	if c.Recurrence.Recurring() {
		c.LateLimitMinutes = 0
	} else {
		c.MissedPolicy = ""
	}
	switch c.OverlapPolicy {
	case "":
		c.OverlapPolicy = OverlapSkip
	case OverlapSkip, OverlapQueue:
	default:
		return invalid(`overlap_policy must be "skip" or "queue_one"`)
	}
	if c.WindowID != "" {
		var kind string
		var archived sql.NullInt64
		if err := s.d.Store.Reader.QueryRowContext(ctx, `SELECT kind, archived_at FROM schedules WHERE id = ?`, c.WindowID).Scan(&kind, &archived); err != nil || kind != ScheduleMaintenance {
			return invalid("maintenance window %s not found", c.WindowID)
		}
		if archived.Valid {
			return invalid("that maintenance window is archived")
		}
	}
	return nil
}

// CreateSchedule stores a schedule owned by its creator.
func (s *Service) CreateSchedule(ctx context.Context, in ScheduleInput, by Actor, onTx func(tx *sql.Tx, sc *Schedule) error) (*Schedule, error) {
	if err := s.normalize(ctx, &in, nil); err != nil {
		return nil, err
	}
	now := s.now()
	id := ids.New(ids.Schedule)
	enabled := in.Enabled == nil || *in.Enabled
	next := s.nextAfter(in.Config, now)
	if enabled && next == nil {
		return nil, invalid("that time has already passed")
	}
	cfg, _ := json.Marshal(in.Config)
	err := s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
		var wid any
		var wrev any
		if in.Workflow != nil {
			wid, wrev = in.Workflow.ID, in.Workflow.Revision
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schedules (id, kind, name, enabled, owner_id, revision, config_json, workflow_id, workflow_revision, window_id,
			next_run_at, created_by, created_at, updated_by, updated_at) VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, in.Kind, in.Name, b2i(enabled), by.UserID, string(cfg), wid, wrev, nullStr(in.WindowID), nullInt(next), nullStr(by.UserID), now.UnixMilli(),
			nullStr(by.UserID), now.UnixMilli()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schedule_revisions (schedule_id, revision, config_json, owner_id, changed_by_name, changed_at) VALUES (?, 1, ?, ?, ?, ?)`,
			id, string(cfg), by.UserID, by.Name, now.UnixMilli()); err != nil {
			return err
		}
		sc, err := s.scheduleTx(ctx, tx, id)
		if err != nil {
			return err
		}
		return onTx(tx, sc)
	})
	if err != nil {
		return nil, err
	}
	return s.GetSchedule(ctx, id)
}

func (s *Service) nextAfter(c Config, after time.Time) *int64 {
	loc, err := LoadZone(c.TimeZone)
	if err != nil {
		return nil
	}
	n, ok := c.Recurrence.Next(after, loc)
	if !ok {
		return nil
	}
	ms := n.UnixMilli()
	return &ms
}

// row is a schedule as stored.
type row struct {
	ID, Kind, Name string
	Enabled        bool
	Archived       bool
	OwnerID        string
	Revision       int
	Config         Config
	NextRunAt      *int64
	Attention      string
}

func scanRow(r interface{ Scan(...any) error }) (*row, error) {
	var x row
	var en int
	var arch, next sql.NullInt64
	var owner sql.NullString
	var cfg string
	if err := r.Scan(&x.ID, &x.Kind, &x.Name, &en, &arch, &owner, &x.Revision, &cfg, &next, &x.Attention); err != nil {
		return nil, err
	}
	x.Enabled, x.Archived, x.OwnerID = en == 1, arch.Valid, owner.String
	if next.Valid {
		x.NextRunAt = &next.Int64
	}
	if err := json.Unmarshal([]byte(cfg), &x.Config); err != nil {
		return nil, err
	}
	return &x, nil
}

const rowCols = `id, kind, name, enabled, archived_at, owner_id, revision, config_json, next_run_at, attention`

func (s *Service) load(ctx context.Context, q queryer, id string) (*row, error) {
	r, err := scanRow(q.QueryRowContext(ctx, `SELECT `+rowCols+` FROM schedules WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// UpdateSchedule stores a new revision of a schedule. It affects future
// occurrences only: a running job, an occurrence already created and the
// specification of past jobs never change. transfer makes the editor the
// owner (someone other than the owner reviewed and changed what runs).
func (s *Service) UpdateSchedule(ctx context.Context, id string, in ScheduleInput, by Actor, transfer bool, onTx func(tx *sql.Tx, before, after *Schedule) error) (*Schedule, error) {
	cur, err := s.load(ctx, s.d.Store.Reader, id)
	if err != nil {
		return nil, err
	}
	if cur.Archived {
		return nil, invalid("this schedule is archived")
	}
	in.Kind = cur.Kind
	if err := s.normalize(ctx, &in, &cur.Config); err != nil {
		return nil, err
	}
	now := s.now()
	enabled := cur.Enabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	next := s.nextAfter(in.Config, now)
	if enabled && next == nil {
		return nil, invalid("that time has already passed")
	}
	cfg, _ := json.Marshal(in.Config)
	err = s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
		before, err := s.scheduleTx(ctx, tx, id)
		if err != nil {
			return err
		}
		owner := cur.OwnerID
		if transfer {
			owner = by.UserID
		}
		var wid, wrev any
		if in.Workflow != nil {
			wid, wrev = in.Workflow.ID, in.Workflow.Revision
		}
		rev := cur.Revision + 1
		if _, err := tx.ExecContext(ctx, `UPDATE schedules SET name = ?, enabled = ?, owner_id = ?, revision = ?, config_json = ?, workflow_id = ?, workflow_revision = ?,
			window_id = ?, next_run_at = ?, updated_by = ?, updated_at = ? WHERE id = ?`, in.Name, b2i(enabled), nullStr(owner), rev, string(cfg), wid, wrev,
			nullStr(in.WindowID), nullInt(next), nullStr(by.UserID), now.UnixMilli(), id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schedule_revisions (schedule_id, revision, config_json, owner_id, changed_by_name, changed_at) VALUES (?, ?, ?, ?, ?, ?)`,
			id, rev, string(cfg), owner, by.Name, now.UnixMilli()); err != nil {
			return err
		}
		after, err := s.scheduleTx(ctx, tx, id)
		if err != nil {
			return err
		}
		return onTx(tx, before, after)
	})
	if err != nil {
		return nil, err
	}
	return s.GetSchedule(ctx, id)
}

// SetEnabled enables or disables a schedule. Disabling stops future
// occurrences only: a job already running continues (cancel it on its
// page). Enabling clears the attention flag and starts from the next
// occurrence after now (what was due while disabled is not caught up).
func (s *Service) SetEnabled(ctx context.Context, id string, on bool, onTx func(tx *sql.Tx, sc *Schedule) error) (*Schedule, error) {
	cur, err := s.load(ctx, s.d.Store.Reader, id)
	if err != nil {
		return nil, err
	}
	if cur.Archived {
		return nil, invalid("this schedule is archived")
	}
	now := s.now()
	var next *int64
	if on {
		if next = s.nextAfter(cur.Config, now); next == nil {
			return nil, invalid("this one-time schedule's time has passed; edit it to run again")
		}
	} else {
		next = cur.NextRunAt
	}
	err = s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
		q := `UPDATE schedules SET enabled = ?, next_run_at = ?, updated_at = ? WHERE id = ?`
		if on {
			q = `UPDATE schedules SET enabled = ?, next_run_at = ?, updated_at = ?, attention = '', attention_at = NULL WHERE id = ?`
		}
		if _, err := tx.ExecContext(ctx, q, b2i(on), nullInt(next), now.UnixMilli(), id); err != nil {
			return err
		}
		sc, err := s.scheduleTx(ctx, tx, id)
		if err != nil {
			return err
		}
		return onTx(tx, sc)
	})
	if err != nil {
		return nil, err
	}
	return s.GetSchedule(ctx, id)
}

// ArchiveSchedule removes a schedule from use. Its occurrence history and
// jobs are kept; a running job continues.
func (s *Service) ArchiveSchedule(ctx context.Context, id string, onTx func(tx *sql.Tx, sc *Schedule) error) error {
	return s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
		sc, err := s.scheduleTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if sc.Archived {
			return ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, `UPDATE schedules SET archived_at = ?, enabled = 0, next_run_at = NULL WHERE id = ?`, s.nowMs(), id); err != nil {
			return err
		}
		// Queued runs of an archived schedule never start.
		if _, err := tx.ExecContext(ctx, `UPDATE schedule_occurrences SET status = 'skipped', reason = 'the schedule was archived before it could run'
			WHERE schedule_id = ? AND status = 'queued'`, id); err != nil {
			return err
		}
		return onTx(tx, sc)
	})
}

// Reassign gives a schedule a new owner (an administrator's action). The
// new owner must hold what running it needs.
func (s *Service) Reassign(ctx context.Context, id, userID string, onTx func(tx *sql.Tx, before, after *Schedule) error) (*Schedule, error) {
	cur, err := s.load(ctx, s.d.Store.Reader, id)
	if err != nil {
		return nil, err
	}
	u, err := s.d.Store.R.GetUser(ctx, userID)
	if err != nil {
		return nil, invalid("user %s not found", userID)
	}
	if u.Disabled != 0 {
		return nil, invalid("%s's account is disabled", u.Username)
	}
	steps, _, err := s.steps(ctx, s.d.Store.Reader, cur.Config)
	if err != nil {
		return nil, err
	}
	if p := MissingPermission(u.Role, RequiredPermissions(cur.Kind, steps)); p != "" {
		return nil, invalid("%s doesn't have the %s permission this schedule needs", u.Username, p)
	}
	err = s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
		before, err := s.scheduleTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE schedules SET owner_id = ?, updated_at = ? WHERE id = ?`, userID, s.nowMs(), id); err != nil {
			return err
		}
		after, err := s.scheduleTx(ctx, tx, id)
		if err != nil {
			return err
		}
		return onTx(tx, before, after)
	})
	if err != nil {
		return nil, err
	}
	return s.GetSchedule(ctx, id)
}

// --- reading ---

// ScheduleFilter selects schedules.
type ScheduleFilter struct {
	Kind     string
	Archived bool
	OwnerID  string
}

// ListSchedules lists schedules, the ones needing attention first, then
// by next run.
func (s *Service) ListSchedules(ctx context.Context, f ScheduleFilter) ([]*Schedule, error) {
	where, args := []string{"1=1"}, []any{}
	if f.Kind != "" {
		where, args = append(where, "kind = ?"), append(args, f.Kind)
	}
	if !f.Archived {
		where = append(where, "archived_at IS NULL")
	}
	if f.OwnerID != "" {
		where, args = append(where, "owner_id = ?"), append(args, f.OwnerID)
	}
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT id FROM schedules WHERE `+strings.Join(where, " AND ")+`
		ORDER BY attention = '', enabled DESC, next_run_at IS NULL, next_run_at, name LIMIT 1000`, args...)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	out := []*Schedule{}
	for _, id := range ids {
		sc, err := s.scheduleTx(ctx, s.d.Store.Reader, id)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, nil
}

// GetSchedule returns one schedule.
func (s *Service) GetSchedule(ctx context.Context, id string) (*Schedule, error) {
	return s.scheduleTx(ctx, s.d.Store.Reader, id)
}

func (s *Service) scheduleTx(ctx context.Context, q queryer, id string) (*Schedule, error) {
	var sc Schedule
	var en int
	var arch, next, attAt sql.NullInt64
	var ownerID, ownerName, wfName, winName, updBy sql.NullString
	var wfLatest sql.NullInt64
	var wfArch sql.NullInt64
	var cfg string
	err := q.QueryRowContext(ctx, `SELECT s.id, s.kind, s.name, s.enabled, s.archived_at, s.owner_id, u.username, s.revision, s.config_json, s.next_run_at,
		s.attention, s.attention_at, w.name, w.latest_revision, w.archived_at, win.name, s.created_at, s.updated_at, ub.username
		FROM schedules s LEFT JOIN users u ON u.id = s.owner_id LEFT JOIN workflows w ON w.id = s.workflow_id
		LEFT JOIN schedules win ON win.id = s.window_id LEFT JOIN users ub ON ub.id = s.updated_by WHERE s.id = ?`, id).Scan(
		&sc.ID, &sc.Kind, &sc.Name, &en, &arch, &ownerID, &ownerName, &sc.Revision, &cfg, &next, &sc.Attention, &attAt,
		&wfName, &wfLatest, &wfArch, &winName, &sc.CreatedAt, &sc.UpdatedAt, &updBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	sc.Enabled, sc.Archived = en == 1, arch.Valid
	if ownerID.Valid {
		sc.Owner = &Person{ID: ownerID.String, Name: ownerName.String}
	}
	if err := json.Unmarshal([]byte(cfg), &sc.Config); err != nil {
		return nil, err
	}
	loc, _ := LoadZone(sc.Config.TimeZone)
	if loc == nil {
		loc = time.UTC
	}
	sc.When = sc.Config.Recurrence.Describe(loc)
	if next.Valid && sc.Enabled {
		sc.NextRunAt = &next.Int64
		t := time.UnixMilli(next.Int64).In(loc)
		sc.NextRunLocal = t.Format("Mon 2 Jan 2006 15:04 MST")
		sc.NextRunNote = sc.Config.Recurrence.Shifted(t, loc)
	}
	if attAt.Valid {
		sc.AttentionAt = &attAt.Int64
	}
	sc.WorkflowName, sc.WindowName, sc.UpdatedBy = wfName.String, winName.String, updBy.String
	if sc.Config.Workflow != nil && wfLatest.Valid {
		sc.WorkflowLatest = int(wfLatest.Int64)
		sc.WorkflowUpdate = sc.WorkflowLatest > sc.Config.Workflow.Revision
		sc.WorkflowArchive = wfArch.Valid
	}
	sc.Action = s.actionText(ctx, q, &sc)
	sc.Targets = s.targetsText(ctx, q, sc.Config.Targets)
	if occ, err := s.occurrences(ctx, q, id, 0, "", 1); err == nil && len(occ) > 0 {
		sc.LastOccurrence = occ[0]
	}
	if sc.Kind == ScheduleJob {
		q.QueryRowContext(ctx, `SELECT j.id FROM schedule_occurrences o JOIN jobs j ON j.id = o.job_id
			WHERE o.schedule_id = ? AND j.status IN ('pending','running','paused') ORDER BY o.created_at DESC LIMIT 1`, id).Scan(&sc.ActiveJobID)
	} else {
		q.QueryRowContext(ctx, `SELECT id FROM maintenance WHERE schedule_id = ? AND ended_at IS NULL AND (ends_at IS NULL OR ends_at > ?) LIMIT 1`,
			id, s.nowMs()).Scan(&sc.ActiveMaintenanceID)
	}
	return &sc, nil
}

func (s *Service) actionText(ctx context.Context, q queryer, sc *Schedule) string {
	c := sc.Config
	if sc.Kind == ScheduleMaintenance {
		t := fmt.Sprintf("Maintenance for %s", (time.Duration(c.DurationMinutes) * time.Minute).String())
		if c.Reason != "" {
			t += ": " + c.Reason
		}
		return t
	}
	if c.Workflow != nil {
		return fmt.Sprintf("Workflow %s (revision %d)", sc.WorkflowName, c.Workflow.Revision)
	}
	return strings.Join(stepSummaries(c.Steps), " → ")
}

func (s *Service) targetsText(ctx context.Context, q queryer, t model.TargetSpec) string {
	var parts []string
	name := func(table, id string) string {
		var n string
		if err := q.QueryRowContext(ctx, `SELECT name FROM `+table+` WHERE id = ?`, id).Scan(&n); err != nil {
			return id + " (deleted)"
		}
		return n
	}
	for i, h := range t.Hosts {
		if i == 5 {
			parts = append(parts, fmt.Sprintf("%d more hosts", len(t.Hosts)-5))
			break
		}
		parts = append(parts, name("hosts", h))
	}
	for _, g := range t.Groups {
		parts = append(parts, "group "+name("groups", g))
	}
	for _, tg := range t.Tags {
		parts = append(parts, "tag "+tg)
	}
	return strings.Join(parts, ", ")
}

// --- helpers ---

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

var _ = db.New
