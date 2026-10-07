// Package filedeploy implements managed file deployment (ARCHITECTURE-ALPHA
// §10.7) on top of the transport-neutral files.FS, so the SSH driver today
// and the agent later run the same sequence:
//
//	inspect → keep the previous version → stage → validate → atomic install
//	(only if the file is still what was inspected) → verify → service action
//
// A validation failure leaves the live file untouched. A failed service
// action after a successful install is reported as exactly that.
package filedeploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"

	"drool/internal/artifacts"
	"drool/internal/driver"
	"drool/internal/files"
	"drool/internal/model"
)

// MaxCapture is the largest previous version kept for rollback.
const MaxCapture = 64 << 20

// Source reads and stores artifacts.
type Source interface {
	Open(ctx context.Context, sha string) (io.ReadCloser, *artifacts.Artifact, error)
	Put(ctx context.Context, r io.Reader, name, userID string) (*artifacts.Artifact, error)
	ReadText(ctx context.Context, sha string) (string, bool, error)
}

// ServiceFunc reloads or restarts a unit.
type ServiceFunc func(ctx context.Context, unit, action string) error

// Recorder stores a deployment's outcome (history and audit).
type Recorder interface {
	Record(ctx context.Context, r *Record) error
}

// Meta is ownership and permissions.
type Meta struct {
	Owner string `json:"owner"`
	Group string `json:"group"`
	Mode  string `json:"mode"` // 4 octal digits
	Size  int64  `json:"size"`
}

// FileState is a file's content identity and metadata at one moment.
type FileState struct {
	Exists bool   `json:"exists"`
	SHA256 string `json:"sha256,omitempty"`
	Meta
	// Captured: the content is kept in the artifact store (rollback possible).
	Captured bool `json:"captured"`
}

// ServiceOutcome is the result of the post-install service action.
type ServiceOutcome struct {
	Unit   string `json:"unit"`
	Action string `json:"action"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}

// Outcomes.
const (
	Installed        = "installed"
	Unchanged        = "unchanged"
	Removed          = "removed"
	ValidationFailed = "validation_failed"
	Failed           = "failed"
)

// Result is reported as the job step's result and kept as history.
type Result struct {
	Path         string                  `json:"path"`
	Outcome      string                  `json:"outcome"`
	Changed      bool                    `json:"changed"`
	Note         string                  `json:"note,omitempty"`
	Artifact     string                  `json:"artifact,omitempty"`
	Before       FileState               `json:"before"`
	After        *FileState              `json:"after,omitempty"`
	Validation   *files.ValidationResult `json:"validation,omitempty"`
	Service      *ServiceOutcome         `json:"service,omitempty"`
	DeploymentID string                  `json:"deployment_id,omitempty"`
	RollbackOf   string                  `json:"rollback_of,omitempty"`
}

// Record is what a Recorder persists.
type Record struct {
	Step   driver.StepInfo
	Op     *model.FileDeployOp
	Result *Result
	Err    error
}

// Deps are what a deployment needs.
type Deps struct {
	FS       files.FS
	Source   Source
	Recorder Recorder
	Service  ServiceFunc // may be nil when no service action is requested
	Notes    io.Writer   // "[drool] ..." progress lines
	Output   io.Writer   // validation command output
	Step     driver.StepInfo
}

func note(w io.Writer, format string, args ...any) {
	if w != nil {
		fmt.Fprintf(w, "[drool] "+format+"\n", args...)
	}
}

func metaOf(i files.Info) Meta {
	return Meta{Owner: i.Owner, Group: i.Group, Mode: files.FormatMode(i.Mode), Size: i.Size}
}

// desired computes the metadata the file will have.
func desired(op *model.FileDeployOp, cur *files.Info) Meta {
	m := Meta{Owner: op.Owner, Group: op.Group, Mode: op.Mode}
	if cur != nil {
		if m.Owner == "" {
			m.Owner = cur.Owner
		}
		if m.Group == "" {
			m.Group = cur.Group
		}
		if m.Mode == "" {
			m.Mode = files.FormatMode(cur.Mode)
		}
	}
	if m.Mode == "" {
		m.Mode = "0644"
	}
	return m
}

// metaDiffers reports whether cur differs from what op asks for.
func metaDiffers(op *model.FileDeployOp, cur files.Info) bool {
	if op.Mode != "" && op.Mode != files.FormatMode(cur.Mode) {
		return true
	}
	if op.Owner != "" && op.Owner != cur.Owner && op.Owner != strconv.Itoa(cur.UID) {
		return true
	}
	if op.Group != "" && op.Group != cur.Group && op.Group != strconv.Itoa(cur.GID) {
		return true
	}
	return false
}

// classify gives filesystem errors a job error class; errors that already
// carry one (connection, sudo) keep it.
func classify(err error) error {
	var oe *model.OpError
	var fe *files.Error
	if err == nil || errors.As(err, &oe) || !errors.As(err, &fe) {
		return err
	}
	class := model.ClassExecution
	if fe.Code == files.CodePermission {
		class = model.ClassAuthorization
	}
	return &model.OpError{Class: class, Message: fe.Message, Hint: fe.Hint, Err: err}
}

// checkParent fails early when the destination's directory is missing,
// so a preview doesn't promise to create a file it can't.
func checkParent(ctx context.Context, fs files.FS, p string) error {
	dir := path.Dir(p)
	info, err := fs.Stat(ctx, dir)
	if err != nil {
		var fe *files.Error
		if errors.As(err, &fe) && fe.Code == files.CodeNotFound {
			return files.Errorf(files.CodeNotFound, "the directory %s does not exist", dir).
				WithHint("Create it first (for example with the file manager); deployments don't create directories.")
		}
		return err
	}
	if info.Kind != files.KindDir {
		return files.Errorf(files.CodeNotDir, "%s is not a directory", dir)
	}
	return nil
}

// inspect reads the destination's current state.
func inspect(ctx context.Context, fs files.FS, p string) (*files.Info, string, error) {
	info, err := fs.Stat(ctx, p)
	var fe *files.Error
	if errors.As(err, &fe) && fe.Code == files.CodeNotFound {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	switch info.Kind {
	case files.KindSymlink:
		return nil, "", files.Errorf(files.CodeSymlink, "%s is a symlink to %s; DROOL won't replace a symlink. Deploy to the target path instead.", p, info.Target)
	case files.KindFile:
	default:
		return nil, "", files.Errorf(files.CodeNotRegular, "%s exists and is not a regular file", p)
	}
	sha, err := fs.Hash(ctx, p)
	if err != nil {
		return nil, "", err
	}
	return &info, sha, nil
}

// Run deploys op on one host.
func Run(ctx context.Context, d Deps, op *model.FileDeployOp) (*Result, error) {
	res := &Result{Path: op.Path, Artifact: op.Artifact, RollbackOf: op.RollbackOf}
	err := classify(run(ctx, d, op, res))
	if d.Recorder != nil && res.Outcome != "" {
		if rerr := d.Recorder.Record(context.WithoutCancel(ctx), &Record{Step: d.Step, Op: op, Result: res, Err: err}); rerr != nil && err == nil {
			err = &model.OpError{Class: model.ClassInternal, Message: "the file was handled but its history could not be saved: " + rerr.Error()}
		}
	}
	return res, err
}

func run(ctx context.Context, d Deps, op *model.FileDeployOp, res *Result) error {
	cur, curSHA, err := inspect(ctx, d.FS, op.Path)
	if err != nil {
		return err
	}
	if cur != nil {
		res.Before = FileState{Exists: true, SHA256: curSHA, Meta: metaOf(*cur)}
	}
	if op.ExpectCurrent != "" && curSHA != op.ExpectCurrent {
		res.Outcome = Failed
		return files.Errorf(files.CodeConflict, "%s changed on the host since DROOL deployed it; the rollback was not applied", op.Path).
			WithHint("Inspect the file first. To roll back anyway, deploy the earlier version as a new deployment.")
	}

	// Rollback of a file that didn't exist before: remove it.
	if op.Remove {
		if cur == nil {
			res.Outcome, res.Note = Unchanged, "the file is already absent"
			note(d.Notes, "%s is already absent", op.Path)
			return nil
		}
		capture(ctx, d, op.Path, cur, &res.Before)
		if err := d.FS.Remove(ctx, op.Path, false); err != nil {
			res.Outcome = Failed
			return err
		}
		note(d.Notes, "removed %s", op.Path)
		res.Outcome, res.Changed = Removed, true
		return serviceStep(ctx, d, op, res)
	}

	if cur != nil && op.Overwrite == "create_only" {
		res.Outcome, res.Note = Unchanged, "the file already exists and the deployment only creates missing files"
		note(d.Notes, "%s exists; left as it is (create only)", op.Path)
		return nil
	}
	if cur != nil && curSHA == op.Artifact && !metaDiffers(op, *cur) {
		res.Outcome, res.Note = Unchanged, "identical content and permissions"
		after := res.Before
		res.After = &after
		note(d.Notes, "%s already has this content (sha256 %s)", op.Path, short(curSHA))
		return nil
	}
	if cur != nil {
		capture(ctx, d, op.Path, cur, &res.Before)
	}

	src, art, err := d.Source.Open(ctx, op.Artifact)
	if err != nil {
		res.Outcome = Failed
		if errors.Is(err, artifacts.ErrNotFound) {
			return &model.OpError{Class: model.ClassValidation, Message: "the file to deploy is no longer stored on the controller (" + short(op.Artifact) + ")",
				Hint: "Upload it again and start a new deployment."}
		}
		return err
	}
	defer src.Close()
	so := files.StageOptions{Owner: op.Owner, Group: op.Group, SHA256: op.Artifact}
	if op.Mode != "" {
		m, _ := files.ParseMode(op.Mode)
		so.Mode = &m
	}
	note(d.Notes, "staging %s (%d bytes, sha256 %s)", op.Path, art.Size, short(op.Artifact))
	st, err := d.FS.Stage(ctx, op.Path, src, so)
	if err != nil {
		res.Outcome = Failed
		return err
	}
	defer st.Discard(context.WithoutCancel(ctx))

	if op.Validate != "" {
		note(d.Notes, "validating the staged file: %s", op.Validate)
		v, err := st.Validate(ctx, op.Validate, d.Output)
		if err != nil {
			res.Outcome = Failed
			return err
		}
		res.Validation = &v
		if !v.OK {
			res.Outcome = ValidationFailed
			note(d.Notes, "validation failed (exit %d); %s was not changed", v.ExitCode, op.Path)
			return &model.OpError{Class: model.ClassExecution,
				Message: fmt.Sprintf("validation failed (exit %d); the live file was not changed", v.ExitCode),
				Hint:    "The validation command's output is above."}
		}
	}

	pre := files.Precondition{MustNotExist: cur == nil, ExpectSHA256: curSHA}
	inst, err := st.Install(ctx, pre)
	if err != nil {
		res.Outcome = Failed
		if inst != nil {
			after := FileState{Exists: true, SHA256: inst.SHA256, Meta: metaOf(inst.Info)}
			res.After = &after
		}
		return err
	}
	after := FileState{Exists: true, SHA256: inst.SHA256, Meta: metaOf(inst.Info), Captured: true}
	res.After = &after
	res.Outcome, res.Changed = Installed, true
	note(d.Notes, "installed %s (owner %s:%s, mode %s)", op.Path, after.Owner, after.Group, after.Mode)
	return serviceStep(ctx, d, op, res)
}

func serviceStep(ctx context.Context, d Deps, op *model.FileDeployOp, res *Result) error {
	if op.Service == nil {
		return nil
	}
	so := &ServiceOutcome{Unit: op.Service.UnitName(), Action: op.Service.Action}
	res.Service = so
	if d.Service == nil {
		so.Error = "service actions are not available for this host"
		return &model.OpError{Class: model.ClassService, Message: "the file was installed, but " + so.Error}
	}
	note(d.Notes, "%s %s", op.Service.Action, so.Unit)
	if err := d.Service(ctx, so.Unit, op.Service.Action); err != nil {
		oe := model.AsOpError(err)
		so.Error = oe.Message
		// The file itself is in place: say so.
		return &model.OpError{Class: model.ClassService, Err: err, Hint: oe.Hint,
			Message: fmt.Sprintf("the file was installed, but %s of %s failed: %s", op.Service.Action, so.Unit, oe.Message)}
	}
	so.OK = true
	return nil
}

// capture keeps the current content in the artifact store for rollback.
func capture(ctx context.Context, d Deps, p string, cur *files.Info, st *FileState) {
	if cur.Size > MaxCapture {
		note(d.Notes, "the current %s is larger than %d MiB and is not kept for rollback", p, MaxCapture>>20)
		return
	}
	rc, _, err := d.FS.Open(ctx, p)
	if err != nil {
		note(d.Notes, "could not keep the current version for rollback: %v", err)
		return
	}
	defer rc.Close()
	a, err := d.Source.Put(ctx, rc, cur.Name, d.Step.UserID)
	if err != nil {
		note(d.Notes, "could not keep the current version for rollback: %v", err)
		return
	}
	if a.SHA256 != st.SHA256 {
		note(d.Notes, "%s changed while it was being read; it is not kept for rollback", p)
		return
	}
	st.Captured = true
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// --- preview ---

// Preview states.
const (
	StateCreate    = "create"    // missing: would create
	StateIdentical = "identical" // nothing to do
	StateReplace   = "replace"   // content differs: would replace
	StateMetadata  = "metadata"  // content identical, owner/group/mode differ
	StateKeep      = "keep"      // exists, create_only: left alone
	StateRemove    = "remove"    // rollback removes it
)

// PreviewResult describes what a deployment would do on one host.
type PreviewResult struct {
	State    string     `json:"state"`
	Current  *FileState `json:"current,omitempty"`
	Desired  Meta       `json:"desired"`
	Diff     string     `json:"diff,omitempty"`
	DiffNote string     `json:"diff_note,omitempty"`
}

// Preview inspects the destination without changing anything.
func Preview(ctx context.Context, fs files.FS, src Source, op *model.FileDeployOp) (*PreviewResult, error) {
	cur, curSHA, err := inspect(ctx, fs, op.Path)
	if err != nil {
		return nil, err
	}
	if cur == nil && !op.Remove {
		if err := checkParent(ctx, fs, op.Path); err != nil {
			return nil, err
		}
	}
	pr := &PreviewResult{Desired: desired(op, cur)}
	if cur != nil {
		pr.Current = &FileState{Exists: true, SHA256: curSHA, Meta: metaOf(*cur)}
	}
	switch {
	case op.Remove && cur == nil:
		pr.State = StateIdentical
		return pr, nil
	case op.Remove:
		pr.State = StateRemove
		return pr, nil
	case cur == nil:
		pr.State = StateCreate
	case op.Overwrite == "create_only":
		pr.State = StateKeep
		return pr, nil
	case curSHA == op.Artifact && metaDiffers(op, *cur):
		pr.State = StateMetadata
		return pr, nil
	case curSHA == op.Artifact:
		pr.State = StateIdentical
		return pr, nil
	default:
		pr.State = StateReplace
	}
	pr.Diff, pr.DiffNote = diffAgainst(ctx, fs, src, op, cur)
	return pr, nil
}

func diffAgainst(ctx context.Context, fs files.FS, src Source, op *model.FileDeployOp, cur *files.Info) (string, string) {
	newText, ok, err := src.ReadText(ctx, op.Artifact)
	if err != nil {
		return "", "the new content could not be read: " + err.Error()
	}
	if !ok {
		return "", "binary or large file; no text diff"
	}
	oldText := ""
	if cur != nil {
		if cur.Size > files.MaxTextBytes {
			return "", "the current file is too large to diff"
		}
		rc, _, err := fs.Open(ctx, op.Path)
		if err != nil {
			return "", "the current file could not be read: " + err.Error()
		}
		b, err := io.ReadAll(io.LimitReader(rc, files.MaxTextBytes+1))
		rc.Close()
		if err != nil {
			return "", "the current file could not be read: " + err.Error()
		}
		if !files.IsText(b) {
			return "", "the current file is binary; no text diff"
		}
		oldText = string(b)
	}
	d, ok := files.Diff(op.Path+" (on host)", op.Path+" (new)", oldText, newText)
	if !ok {
		return "", "too many lines to diff"
	}
	return d, ""
}
