package sched

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"drool/internal/ids"
	"drool/internal/model"
)

// A workflow is an ordered list of typed steps (the job engine's
// operations plus the controller-side wait and verification steps) and a
// default rollout. It is deliberately not a DAG, a language or a branching
// engine: it produces an ordinary job spec. Every change is a new
// revision; schedules pin a revision and never change with the workflow.

// Errors.
var (
	ErrNotFound   = errors.New("not found")
	ErrNameTaken  = errors.New("name already in use")
	ErrValidation = errors.New("invalid")
)

// ValidationError is a user-correctable problem.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(format string, args ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, args...)}
}

// WorkflowInput creates or revises a workflow.
type WorkflowInput struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Steps       []model.Operation `json:"steps"`
	Strategy    model.Strategy    `json:"strategy"`
}

// normalizeSteps validates steps that run unattended (workflows and
// schedules). Firewall policy changes go through the Firewall page (its
// review, simulation and transactions); unattended runs don't apply them.
func normalizeSteps(steps []model.Operation) error {
	for i, st := range steps {
		if st.Type == model.OpFirewallApply {
			return invalid("step %d: firewall policy is applied from the Firewall page, not from a workflow or schedule", i+1)
		}
	}
	if err := model.NormalizeSteps(steps); err != nil {
		return &ValidationError{Msg: err.Error()}
	}
	return nil
}

func (in *WorkflowInput) normalize() error {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	if in.Name == "" || len([]rune(in.Name)) > 100 {
		return invalid("a workflow needs a name of at most 100 characters")
	}
	if len([]rune(in.Description)) > 1000 {
		return invalid("the description must be at most 1000 characters")
	}
	if err := normalizeSteps(in.Steps); err != nil {
		return err
	}
	if len(in.Strategy.CanaryHosts) > 0 {
		return invalid("a workflow's default rollout can't name canary hosts (it has no targets); use a canary count")
	}
	if err := model.NormalizeStrategy(&in.Strategy); err != nil {
		return &ValidationError{Msg: err.Error()}
	}
	return nil
}

// Revision is one immutable version of a workflow.
type Revision struct {
	Revision    int               `json:"revision"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Steps       []model.Operation `json:"steps"`
	Strategy    model.Strategy    `json:"strategy"`
	CreatedBy   string            `json:"created_by"`
	CreatedAt   int64             `json:"created_at"`
}

// RevisionSummary lists a revision.
type RevisionSummary struct {
	Revision  int    `json:"revision"`
	Steps     int    `json:"steps"`
	CreatedBy string `json:"created_by"`
	CreatedAt int64  `json:"created_at"`
}

// Workflow is a workflow with its latest revision.
type Workflow struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Description    string            `json:"description"`
	LatestRevision int               `json:"latest_revision"`
	Steps          []model.Operation `json:"steps"`
	Strategy       model.Strategy    `json:"strategy"`
	Archived       bool              `json:"archived"`
	CreatedAt      int64             `json:"created_at"`
	UpdatedAt      int64             `json:"updated_at"`
	// Schedules using it, and how many pin an older revision.
	Schedules       int               `json:"schedules"`
	OutdatedPinning int               `json:"outdated_schedules"`
	Revisions       []RevisionSummary `json:"revisions,omitempty"`
	Summary         []string          `json:"summary"` // one line per step
}

// Actor is who changes something.
type Actor struct {
	UserID, Name string
}

func stepSummaries(steps []model.Operation) []string {
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = s.Summary()
	}
	return out
}

// CreateWorkflow stores a new workflow as revision 1.
func (s *Service) CreateWorkflow(ctx context.Context, in WorkflowInput, by Actor, onTx func(tx *sql.Tx, w *Workflow) error) (*Workflow, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}
	now := s.nowMs()
	w := &Workflow{ID: ids.New(ids.Workflow), Name: in.Name, Description: in.Description, LatestRevision: 1, Steps: in.Steps,
		Strategy: in.Strategy, CreatedAt: now, UpdatedAt: now}
	err := s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflows (id, name, description, latest_revision, created_by, created_at, updated_at)
			VALUES (?, ?, ?, 1, ?, ?, ?)`, w.ID, w.Name, w.Description, nullStr(by.UserID), now, now); err != nil {
			if isUnique(err) {
				return ErrNameTaken
			}
			return err
		}
		if err := insertRevision(ctx, tx, w.ID, 1, in, by, now); err != nil {
			return err
		}
		return onTx(tx, w)
	})
	if err != nil {
		return nil, err
	}
	return s.GetWorkflow(ctx, w.ID)
}

func insertRevision(ctx context.Context, tx *sql.Tx, id string, rev int, in WorkflowInput, by Actor, now int64) error {
	steps, _ := json.Marshal(in.Steps)
	strat, _ := json.Marshal(in.Strategy)
	_, err := tx.ExecContext(ctx, `INSERT INTO workflow_revisions (workflow_id, revision, name, description, steps_json, strategy_json, created_by, created_by_name, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, rev, in.Name, in.Description, string(steps), string(strat), nullStr(by.UserID), by.Name, now)
	return err
}

// UpdateWorkflow stores a new revision (nothing when nothing changed).
// Schedules keep the revision they pinned.
func (s *Service) UpdateWorkflow(ctx context.Context, id string, in WorkflowInput, by Actor, onTx func(tx *sql.Tx, before, after *Revision) error) (*Workflow, bool, error) {
	if err := in.normalize(); err != nil {
		return nil, false, err
	}
	changed := false
	err := s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
		var latest int
		var archived sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT latest_revision, archived_at FROM workflows WHERE id = ?`, id).Scan(&latest, &archived); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if archived.Valid {
			return invalid("this workflow is archived")
		}
		before, err := revision(ctx, tx, id, latest)
		if err != nil {
			return err
		}
		if before.Name == in.Name && before.Description == in.Description && reflect.DeepEqual(before.Steps, in.Steps) && reflect.DeepEqual(before.Strategy, in.Strategy) {
			return nil
		}
		changed = true
		now := s.nowMs()
		if _, err := tx.ExecContext(ctx, `UPDATE workflows SET name = ?, description = ?, latest_revision = ?, updated_at = ? WHERE id = ?`,
			in.Name, in.Description, latest+1, now, id); err != nil {
			if isUnique(err) {
				return ErrNameTaken
			}
			return err
		}
		if err := insertRevision(ctx, tx, id, latest+1, in, by, now); err != nil {
			return err
		}
		after := &Revision{Revision: latest + 1, Name: in.Name, Description: in.Description, Steps: in.Steps, Strategy: in.Strategy, CreatedBy: by.Name, CreatedAt: now}
		return onTx(tx, before, after)
	})
	if err != nil {
		return nil, false, err
	}
	w, err := s.GetWorkflow(ctx, id)
	return w, changed, err
}

// ArchiveWorkflow hides a workflow. Schedules keep running the revision
// they pinned; new schedules can't choose it.
func (s *Service) ArchiveWorkflow(ctx context.Context, id string, onTx func(tx *sql.Tx) error) error {
	return s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE workflows SET archived_at = ? WHERE id = ? AND archived_at IS NULL`, s.nowMs(), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return onTx(tx)
	})
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func revision(ctx context.Context, q queryer, id string, rev int) (*Revision, error) {
	var r Revision
	var steps, strat string
	err := q.QueryRowContext(ctx, `SELECT revision, name, description, steps_json, strategy_json, created_by_name, created_at
		FROM workflow_revisions WHERE workflow_id = ? AND revision = ?`, id, rev).Scan(&r.Revision, &r.Name, &r.Description, &steps, &strat, &r.CreatedBy, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(steps), &r.Steps); err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(strat), &r.Strategy)
	return &r, nil
}

// WorkflowRevision returns one revision.
func (s *Service) WorkflowRevision(ctx context.Context, id string, rev int) (*Revision, error) {
	return revision(ctx, s.d.Store.Reader, id, rev)
}

// GetWorkflow returns a workflow with its latest revision and the list of
// revisions.
func (s *Service) GetWorkflow(ctx context.Context, id string) (*Workflow, error) {
	ws, err := s.listWorkflows(ctx, `w.id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(ws) == 0 {
		return nil, ErrNotFound
	}
	w := ws[0]
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT revision, steps_json, created_by_name, created_at FROM workflow_revisions
		WHERE workflow_id = ? ORDER BY revision DESC LIMIT 100`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r RevisionSummary
		var steps string
		if err := rows.Scan(&r.Revision, &steps, &r.CreatedBy, &r.CreatedAt); err != nil {
			return nil, err
		}
		var ops []json.RawMessage
		json.Unmarshal([]byte(steps), &ops)
		r.Steps = len(ops)
		w.Revisions = append(w.Revisions, r)
	}
	return w, rows.Err()
}

// ListWorkflows lists workflows (archived ones only when asked).
func (s *Service) ListWorkflows(ctx context.Context, archived bool) ([]*Workflow, error) {
	if archived {
		return s.listWorkflows(ctx, `1=1`)
	}
	return s.listWorkflows(ctx, `w.archived_at IS NULL`)
}

func (s *Service) listWorkflows(ctx context.Context, where string, args ...any) ([]*Workflow, error) {
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT w.id, w.name, w.description, w.latest_revision, w.archived_at, w.created_at, w.updated_at,
		r.steps_json, r.strategy_json,
		(SELECT count(*) FROM schedules sc WHERE sc.workflow_id = w.id AND sc.archived_at IS NULL),
		(SELECT count(*) FROM schedules sc WHERE sc.workflow_id = w.id AND sc.archived_at IS NULL AND sc.workflow_revision < w.latest_revision)
		FROM workflows w JOIN workflow_revisions r ON r.workflow_id = w.id AND r.revision = w.latest_revision
		WHERE `+where+` ORDER BY w.name COLLATE NOCASE LIMIT 500`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Workflow{}
	for rows.Next() {
		var w Workflow
		var arch sql.NullInt64
		var steps, strat string
		if err := rows.Scan(&w.ID, &w.Name, &w.Description, &w.LatestRevision, &arch, &w.CreatedAt, &w.UpdatedAt, &steps, &strat,
			&w.Schedules, &w.OutdatedPinning); err != nil {
			return nil, err
		}
		w.Archived = arch.Valid
		json.Unmarshal([]byte(steps), &w.Steps)
		json.Unmarshal([]byte(strat), &w.Strategy)
		w.Summary = stepSummaries(w.Steps)
		out = append(out, &w)
	}
	return out, rows.Err()
}

// --- templates ---

// Template is a recommended starting point. It produces an ordinary,
// editable workflow; nothing executes templates specially.
type Template struct {
	Key         string            `json:"key"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Steps       []model.Operation `json:"steps"`
	Strategy    model.Strategy    `json:"strategy"`
	// Fill names step fields to complete before saving.
	Fill []string `json:"fill,omitempty"`
}

func two() *int { n := 2; return &n }

// Templates are the recommended patterns.
func Templates() []Template {
	update := func() model.Operation {
		return model.Operation{Type: model.OpSystemUpdate, SystemUpdate: &model.SystemUpdateOp{Mode: "upgrade", Reboot: "if_required"}}
	}
	verify := model.Operation{Type: model.OpVerifyHost, VerifyHost: &model.VerifyHostOp{TimeoutS: 300}}
	ts := []Template{
		{Key: "system_update", Name: "Scheduled system update",
			Description: "Update the system, reboot only where the update requires it (DROOL waits for the host to come back), then check the host answers.",
			Steps:       []model.Operation{update(), verify}, Strategy: model.Strategy{Concurrency: 5}},
		{Key: "safe_fleet_update", Name: "Safe fleet update",
			Description: "The same update, rolled out carefully: one canary host first, then batches of five; the rollout pauses at the first canary failure or after two failures.",
			Steps:       []model.Operation{update(), verify},
			Strategy:    model.Strategy{Concurrency: 5, CanaryCount: 1, BatchSize: 5, FailureThreshold: two()}},
		{Key: "update_verify_services", Name: "Update and verify services",
			Description: "Update, reboot if required, then check that the services you name are running and the host has no Critical health issues.",
			Steps: []model.Operation{update(), verify,
				{Type: model.OpVerifyServices, VerifyServices: &model.VerifyServicesOp{TimeoutS: 180}},
				{Type: model.OpVerifyHealth, VerifyHealth: &model.VerifyHealthOp{Policy: model.HealthNoCritical, TimeoutS: 120}}},
			Strategy: model.Strategy{Concurrency: 5, CanaryCount: 1, BatchSize: 5, FailureThreshold: two()},
			Fill:     []string{"steps[2].services"}},
	}
	// Show the defaults each step really runs with (a step that still
	// needs input, like the services to check, keeps what it has).
	for _, t := range ts {
		for i := range t.Steps {
			st := t.Steps[i]
			if st.Normalize() == nil {
				t.Steps[i] = st
			}
		}
	}
	return ts
}
