package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"drool/internal/artifacts"
	"drool/internal/audit"
	"drool/internal/auth"
	"drool/internal/engine"
	"drool/internal/filedeploy"
	"drool/internal/files"
	"drool/internal/model"
	"drool/internal/store/db"
)

// Managed file deployment (ARCHITECTURE-ALPHA §10.7): artifacts are
// uploaded once, deployments are jobs with file_deploy steps that refer to
// them by SHA-256, and each host's outcome is kept as history for diffs and
// rollback.

func (s *Server) artifactError(err error) *Problem {
	if errors.Is(err, artifacts.ErrNotFound) {
		return errNotFound("artifact")
	}
	var fe *files.Error
	if errors.As(err, &fe) {
		return s.fileError(err)
	}
	s.log.Error("artifact", "err", err)
	return errInternal
}

// handleArtifactUpload streams the body into the artifact store.
func (s *Server) handleArtifactUpload(w http.ResponseWriter, r *http.Request) {
	if s.d.Artifacts == nil {
		writeProblem(w, r, problem(http.StatusNotImplemented, "unavailable", "artifact storage is not configured"))
		return
	}
	_ = http.NewResponseController(w).SetReadDeadline(time.Time{})
	ctx := r.Context()
	a, err := s.d.Artifacts.Put(ctx, r.Body, r.URL.Query().Get("name"), principalFrom(ctx).UserID)
	if err != nil {
		writeProblem(w, r, s.artifactError(err))
		return
	}
	_ = s.d.Audit.Record(ctx, audit.Event{Action: "artifact.upload", TargetType: "artifact", TargetID: a.SHA256,
		Details: map[string]any{"name": a.Name, "size": a.Size, "sha256": a.SHA256}})
	writeJSON(w, http.StatusCreated, a)
}

func (s *Server) handleArtifactGet(w http.ResponseWriter, r *http.Request) {
	if s.d.Artifacts == nil {
		writeProblem(w, r, errNotFound("artifact"))
		return
	}
	a, err := s.d.Artifacts.Get(r.Context(), r.PathValue("sha"))
	if err != nil {
		writeProblem(w, r, s.artifactError(err))
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) handleArtifactContent(w http.ResponseWriter, r *http.Request) {
	if s.d.Artifacts == nil {
		writeProblem(w, r, errNotFound("artifact"))
		return
	}
	rc, a, err := s.d.Artifacts.Open(r.Context(), r.PathValue("sha"))
	if err != nil {
		writeProblem(w, r, s.artifactError(err))
		return
	}
	defer rc.Close()
	name := a.Name
	if name == "" {
		name = a.SHA256
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name))
	w.Header().Set("Content-Length", strconv.FormatInt(a.Size, 10))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	io.Copy(w, rc)
}

// checkFileSteps enforces the file permissions for jobs with file_deploy
// steps (on top of jobs.execute) and that their artifacts exist.
func (s *Server) checkFileSteps(w http.ResponseWriter, r *http.Request, spec model.JobSpec, allowRollbackFields bool) bool {
	p := principalFrom(r.Context())
	for _, st := range spec.Steps {
		if st.Type != model.OpFileDeploy || st.FileDeploy == nil {
			continue
		}
		need := []auth.Permission{auth.PermFilesDeploy}
		if st.FileDeploy.Elevated {
			need = append(need, auth.PermFilesElevate)
		}
		for _, perm := range need {
			if !p.Can(perm) {
				_ = s.d.Audit.Record(r.Context(), audit.Event{Action: "authz.denied", Result: audit.Denied,
					Details: map[string]any{"permission": string(perm), "method": r.Method, "path": r.URL.Path}})
				writeProblem(w, r, errForbidden("missing permission "+string(perm)))
				return false
			}
		}
		fd := st.FileDeploy
		if !allowRollbackFields && (fd.RollbackOf != "" || fd.Remove || fd.ExpectCurrent != "") {
			writeProblem(w, r, errValidation("rollbacks are started from the deployment history (POST /file-deployments/{id}/rollback)"))
			return false
		}
		if !fd.Remove {
			if s.d.Artifacts == nil {
				writeProblem(w, r, errValidation("artifact storage is not configured"))
				return false
			}
			if _, err := s.d.Artifacts.Get(r.Context(), fd.Artifact); err != nil {
				writeProblem(w, r, errValidation("artifact "+fd.Artifact+" is not stored on the controller; upload the file first"))
				return false
			}
		}
	}
	return true
}

type deployPreviewRequest struct {
	Targets model.TargetSpec `json:"targets"`
	Step    *model.Operation `json:"step"` // a file_deploy operation, as in a job spec
}

type deployPreviewHost struct {
	HostID   string                    `json:"host_id"`
	HostName string                    `json:"host_name"`
	Skipped  string                    `json:"skipped,omitempty"`
	Error    *simErrorJSON             `json:"error,omitempty"`
	Preview  *filedeploy.PreviewResult `json:"preview,omitempty"`
}

func (s *Server) handleDeployPreview(w http.ResponseWriter, r *http.Request) {
	var req deployPreviewRequest
	if prob := decodeJSON(w, r, &req); prob != nil {
		writeProblem(w, r, prob)
		return
	}
	if req.Step == nil || req.Step.Type != model.OpFileDeploy || req.Step.FileDeploy == nil {
		writeProblem(w, r, errValidation("step must be a file_deploy operation"))
		return
	}
	op := *req.Step
	if err := op.Normalize(); err != nil {
		writeProblem(w, r, errValidation(err.Error()))
		return
	}
	if !s.checkFileSteps(w, r, model.JobSpec{Steps: []model.Operation{op}}, false) {
		return
	}
	if s.d.Files == nil {
		writeProblem(w, r, problem(http.StatusNotImplemented, "unavailable", "file access is not configured"))
		return
	}
	ctx := r.Context()
	targets, _, err := engine.ResolveTargets(ctx, s.d.Store.R, req.Targets)
	if err != nil {
		writeProblem(w, r, s.jobError(err))
		return
	}
	if len(targets) > 100 {
		writeProblem(w, r, errValidation("preview at most 100 hosts at once"))
		return
	}
	ids := make([]string, len(targets))
	for i, t := range targets {
		ids[i] = t.ID
	}
	// Previews read files on the hosts (as root when elevated).
	if err := s.d.Audit.Record(ctx, audit.Event{Action: "file.preview", TargetType: "file",
		Details: map[string]any{"path": op.FileDeploy.Path, "artifact": op.FileDeploy.Artifact, "elevated": op.FileDeploy.Elevated, "hosts": ids}}); err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	if err := engine.AnnotateSkips(ctx, s.d.Store.R, targets, []model.Operation{op}); err != nil {
		writeProblem(w, r, s.jobError(err))
		return
	}
	out := make([]deployPreviewHost, len(targets))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	pctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	for i, t := range targets {
		out[i] = deployPreviewHost{HostID: t.ID, HostName: t.Name}
		if t.Skip {
			out[i].Skipped = t.Warning
			continue
		}
		wg.Add(1)
		go func(h *deployPreviewHost) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			release, ok := s.d.Engine.Locks().TryAcquire(h.HostID, "preview", model.LockShared)
			if !ok {
				h.Error = &simErrorJSON{Class: "Busy", Message: "a job is changing this host right now"}
				return
			}
			defer release()
			fsys, err := s.d.Files.OpenFS(pctx, h.HostID, op.FileDeploy.Elevated)
			if err == nil {
				defer fsys.Close()
				h.Preview, err = filedeploy.Preview(pctx, fsys, s.d.Artifacts, op.FileDeploy)
			}
			if err != nil {
				h.Error = previewErr(err)
			}
		}(&out[i])
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, map[string]any{"hosts": out})
}

func previewErr(err error) *simErrorJSON {
	var fe *files.Error
	if errors.As(err, &fe) {
		return &simErrorJSON{Class: "FileError", Message: fe.Message, Hint: fe.Hint}
	}
	oe := model.AsOpError(err)
	return &simErrorJSON{Class: string(oe.Class), Message: errMsgOf(oe), Hint: oe.Hint}
}

type fileDeploymentJSON struct {
	ID         string          `json:"id"`
	JobID      string          `json:"job_id,omitempty"`
	HostID     string          `json:"host_id"`
	HostName   string          `json:"host_name"`
	Path       string          `json:"path"`
	Kind       string          `json:"kind"`
	RollbackOf string          `json:"rollback_of,omitempty"`
	Outcome    string          `json:"outcome"`
	Before     json.RawMessage `json:"before"`
	After      json.RawMessage `json:"after"`
	Validation json.RawMessage `json:"validation"`
	Service    json.RawMessage `json:"service"`
	Error      string          `json:"error,omitempty"`
	CreatedBy  string          `json:"created_by,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	// CanRollback: the change can be undone (the previous version is kept,
	// or there was no file before).
	CanRollback bool   `json:"can_rollback"`
	Diff        string `json:"diff,omitempty"`
	DiffNote    string `json:"diff_note,omitempty"`
}

func deploymentState(exists int64, sha string, meta string, artifact sql.NullString) json.RawMessage {
	m := map[string]any{}
	json.Unmarshal([]byte(meta), &m)
	m["exists"] = exists != 0
	if sha != "" {
		m["sha256"] = sha
	}
	m["kept"] = artifact.Valid
	b, _ := json.Marshal(m)
	return b
}

func toDeploymentJSON(d db.FileDeployment, hostName string) fileDeploymentJSON {
	afterExists := int64(0)
	if d.AfterSha256 != "" {
		afterExists = 1
	}
	out := fileDeploymentJSON{
		ID: d.ID, JobID: d.JobID.String, HostID: d.HostID, HostName: hostName, Path: d.Path, Kind: d.Kind,
		RollbackOf: d.RollbackOf.String, Outcome: d.Outcome,
		Before:     deploymentState(d.BeforeExists, d.BeforeSha256, d.BeforeMeta, d.BeforeArtifact),
		After:      deploymentState(afterExists, d.AfterSha256, d.AfterMeta, d.AfterArtifact),
		Validation: json.RawMessage(d.Validation), Service: json.RawMessage(d.Service),
		Error: d.Error, CreatedBy: d.CreatedBy.String, CreatedAt: time.UnixMilli(d.CreatedAt).UTC(),
	}
	out.CanRollback = (d.Outcome == filedeploy.Installed || d.Outcome == filedeploy.Removed) &&
		(d.BeforeExists == 0 || d.BeforeArtifact.Valid)
	return out
}

func (s *Server) handleListFileDeployments(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := int64(100)
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 && v <= 500 {
		limit = int64(v)
	}
	rows, err := s.d.Store.R.ListFileDeployments(r.Context(), db.ListFileDeploymentsParams{
		HostID: q.Get("host"), Path: q.Get("path"), JobID: q.Get("job"), Lim: limit})
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	out := listResponse[fileDeploymentJSON]{Items: make([]fileDeploymentJSON, 0, len(rows))}
	for _, d := range rows {
		out.Items = append(out.Items, toDeploymentJSON(db.FileDeployment{ID: d.ID, JobID: d.JobID, HostID: d.HostID, Path: d.Path,
			Kind: d.Kind, RollbackOf: d.RollbackOf, Outcome: d.Outcome, BeforeExists: d.BeforeExists, BeforeSha256: d.BeforeSha256,
			BeforeArtifact: d.BeforeArtifact, BeforeMeta: d.BeforeMeta, AfterSha256: d.AfterSha256, AfterArtifact: d.AfterArtifact,
			AfterMeta: d.AfterMeta, Validation: d.Validation, Service: d.Service, Error: d.Error, OpJson: d.OpJson,
			CreatedBy: d.CreatedBy, CreatedAt: d.CreatedAt}, d.HostName))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleManagedFiles(w http.ResponseWriter, r *http.Request) {
	rows, err := s.d.Store.R.ListManagedFiles(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	out := listResponse[fileDeploymentJSON]{Items: make([]fileDeploymentJSON, 0, len(rows))}
	for _, d := range rows {
		out.Items = append(out.Items, toDeploymentJSON(db.FileDeployment{ID: d.ID, JobID: d.JobID, HostID: d.HostID, Path: d.Path,
			Kind: d.Kind, RollbackOf: d.RollbackOf, Outcome: d.Outcome, BeforeExists: d.BeforeExists, BeforeSha256: d.BeforeSha256,
			BeforeArtifact: d.BeforeArtifact, BeforeMeta: d.BeforeMeta, AfterSha256: d.AfterSha256, AfterArtifact: d.AfterArtifact,
			AfterMeta: d.AfterMeta, Validation: d.Validation, Service: d.Service, Error: d.Error, OpJson: d.OpJson,
			CreatedBy: d.CreatedBy, CreatedAt: d.CreatedAt}, d.HostName))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getDeployment(ctx context.Context, id string) (db.GetFileDeploymentRow, *Problem) {
	d, err := s.d.Store.R.GetFileDeployment(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return d, errNotFound("deployment")
	}
	if err != nil {
		return d, errInternal
	}
	return d, nil
}

func rowToDeployment(d db.GetFileDeploymentRow) db.FileDeployment {
	return db.FileDeployment{ID: d.ID, JobID: d.JobID, HostID: d.HostID, Path: d.Path,
		Kind: d.Kind, RollbackOf: d.RollbackOf, Outcome: d.Outcome, BeforeExists: d.BeforeExists, BeforeSha256: d.BeforeSha256,
		BeforeArtifact: d.BeforeArtifact, BeforeMeta: d.BeforeMeta, AfterSha256: d.AfterSha256, AfterArtifact: d.AfterArtifact,
		AfterMeta: d.AfterMeta, Validation: d.Validation, Service: d.Service, Error: d.Error, OpJson: d.OpJson,
		CreatedBy: d.CreatedBy, CreatedAt: d.CreatedAt}
}

// handleGetFileDeployment returns one host's deployment with a text diff
// between the version before and after, when both are kept and are text.
func (s *Server) handleGetFileDeployment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d, prob := s.getDeployment(ctx, r.PathValue("id"))
	if prob != nil {
		writeProblem(w, r, prob)
		return
	}
	out := toDeploymentJSON(rowToDeployment(d), d.HostName)
	out.Diff, out.DiffNote = s.deploymentDiff(ctx, d)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deploymentDiff(ctx context.Context, d db.GetFileDeploymentRow) (string, string) {
	if s.d.Artifacts == nil {
		return "", "artifact storage is not configured"
	}
	text := func(a sql.NullString, exists int64) (string, string) {
		if exists == 0 {
			return "", ""
		}
		if !a.Valid {
			return "", "that version was not kept (too large)"
		}
		t, ok, err := s.d.Artifacts.ReadText(ctx, a.String)
		if err != nil {
			return "", "that version is not available: " + err.Error()
		}
		if !ok {
			return "", "binary or large file; no text diff"
		}
		return t, ""
	}
	afterExists := int64(0)
	if d.AfterSha256 != "" {
		afterExists = 1
	}
	before, n1 := text(d.BeforeArtifact, d.BeforeExists)
	after, n2 := text(d.AfterArtifact, afterExists)
	if n1 != "" || n2 != "" {
		return "", n1 + n2
	}
	diff, ok := files.Diff(d.Path+" (before)", d.Path+" (after)", before, after)
	if !ok {
		return "", "too many lines to diff"
	}
	return diff, ""
}

// handleRollbackFileDeployment starts a job that puts back what was there
// before a deployment: the previous content and metadata, or no file. It
// only applies while the file still has the content that deployment
// installed.
func (s *Server) handleRollbackFileDeployment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d, prob := s.getDeployment(ctx, r.PathValue("id"))
	if prob != nil {
		writeProblem(w, r, prob)
		return
	}
	if d.Outcome != filedeploy.Installed && d.Outcome != filedeploy.Removed {
		writeProblem(w, r, errConflict("nothing_to_roll_back", "this deployment didn't change the file ("+d.Outcome+")"))
		return
	}
	var orig model.FileDeployOp
	_ = json.Unmarshal([]byte(d.OpJson), &orig)
	var before filedeploy.Meta
	_ = json.Unmarshal([]byte(d.BeforeMeta), &before)
	op := &model.FileDeployOp{Path: d.Path, Elevated: orig.Elevated, Validate: orig.Validate, Service: orig.Service,
		RollbackOf: d.ID, TimeoutS: orig.TimeoutS}
	if d.AfterSha256 != "" {
		op.ExpectCurrent = d.AfterSha256
	}
	switch {
	case d.BeforeExists == 0:
		op.Remove = true
		op.Validate = "" // nothing to validate when removing
	case !d.BeforeArtifact.Valid:
		writeProblem(w, r, errConflict("previous_version_missing", "the previous version was not kept (it was too large), so this can't be rolled back"))
		return
	default:
		op.Artifact, op.Owner, op.Group, op.Mode = d.BeforeArtifact.String, before.Owner, before.Group, before.Mode
	}
	step := model.Operation{Type: model.OpFileDeploy, FileDeploy: op}
	if err := step.Normalize(); err != nil {
		writeProblem(w, r, errValidation(err.Error()))
		return
	}
	spec := model.JobSpec{Name: fmt.Sprintf("Roll back %s on %s", d.Path, d.HostName), Steps: []model.Operation{step},
		Targets: model.TargetSpec{Hosts: []string{d.HostID}}}
	if !s.checkFileSteps(w, r, spec, true) {
		return
	}
	s.submitJob(w, r, spec, "")
}
