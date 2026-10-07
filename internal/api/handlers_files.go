package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"time"

	"drool/internal/audit"
	"drool/internal/auth"
	"drool/internal/files"
	"drool/internal/model"
)

// File manager (ARCHITECTURE-ALPHA §10.6): interactive access to one host's
// filesystem. Every change is audited (path, action, result, hashes and
// metadata; never content). Elevated requests need files.elevate on top of
// the route's permission and are never implied.

// fileError maps filesystem errors to problems.
func (s *Server) fileError(err error) *Problem {
	var fe *files.Error
	if errors.As(err, &fe) {
		status, code := http.StatusUnprocessableEntity, fe.Code
		switch fe.Code {
		case files.CodeNotFound:
			status = http.StatusNotFound
		case files.CodePermission:
			status = http.StatusForbidden
		case files.CodeExists, files.CodeSymlink, files.CodeNotEmpty:
			status = http.StatusConflict
		case files.CodeConflict:
			status, code = http.StatusConflict, "file_changed"
		case files.CodeTooLarge:
			status = http.StatusRequestEntityTooLarge
		case files.CodeVerification, "failed":
			status = http.StatusBadGateway
		}
		p := problem(status, code, fe.Message)
		if fe.Hint != "" {
			p.Detail += " — " + fe.Hint
		}
		return p
	}
	if errors.Is(err, context.Canceled) {
		return problem(499, "cancelled", "the request was cancelled")
	}
	return s.inventoryError(err)
}

// requireElevate checks files.elevate for elevated requests.
func (s *Server) requireElevate(w http.ResponseWriter, r *http.Request, elevated bool) bool {
	if !elevated {
		return true
	}
	if principalFrom(r.Context()).Can(auth.PermFilesElevate) {
		return true
	}
	_ = s.d.Audit.Record(r.Context(), audit.Event{Action: "authz.denied", Result: audit.Denied,
		Details: map[string]any{"permission": string(auth.PermFilesElevate), "method": r.Method, "path": r.URL.Path}})
	writeProblem(w, r, errForbidden("missing permission files.elevate (administrator privileges)"))
	return false
}

// openFS opens a session for the request. Changes take the host's lock in
// shared mode without waiting, so they don't interleave with a job that
// holds the host exclusively (e.g. a package upgrade).
func (s *Server) openFS(w http.ResponseWriter, r *http.Request, elevated, change bool) (files.FS, func(), bool) {
	if s.d.Files == nil {
		writeProblem(w, r, problem(http.StatusNotImplemented, "unavailable", "file access is not configured"))
		return nil, nil, false
	}
	if !s.requireElevate(w, r, elevated) {
		return nil, nil, false
	}
	hostID := r.PathValue("id")
	release := func() {}
	if change && s.d.Engine != nil {
		rel, ok := s.d.Engine.Locks().TryAcquire(hostID, "files:"+requestIDFrom(r.Context()), model.LockShared)
		if !ok {
			writeProblem(w, r, errConflict("host_busy", "a job is changing this host right now; try again when it has finished"))
			return nil, nil, false
		}
		release = rel
	}
	fsys, err := s.d.Files.OpenFS(r.Context(), hostID, elevated)
	if err != nil {
		release()
		writeProblem(w, r, s.fileError(err))
		return nil, nil, false
	}
	return fsys, func() { fsys.Close(); release() }, true
}

func queryBool(r *http.Request, k string) bool {
	v, _ := strconv.ParseBool(r.URL.Query().Get(k))
	return v
}

func queryPath(w http.ResponseWriter, r *http.Request, k string) (string, bool) {
	p, err := files.CleanPath(r.URL.Query().Get(k))
	if err != nil {
		writeProblem(w, r, s0FileErr(err))
		return "", false
	}
	return p, true
}

func s0FileErr(err error) *Problem {
	var fe *files.Error
	if errors.As(err, &fe) {
		return errValidation(fe.Message)
	}
	return errValidation(err.Error())
}

// auditFile records a file manager action with its outcome.
func (s *Server) auditFile(ctx context.Context, action, hostID string, details map[string]any, err error) {
	ev := audit.Event{Action: action, TargetType: "host", TargetID: hostID, Details: details, Result: audit.Success}
	if err != nil {
		ev.Result = audit.Failure
		ev.Details["error"] = err.Error()
	}
	_ = s.d.Audit.Record(ctx, ev)
}

type fileListJSON struct {
	Path      string       `json:"path"`
	Requested string       `json:"requested"`
	Parent    string       `json:"parent"`
	Entries   []files.Info `json:"entries"`
	Caps      files.Caps   `json:"caps"`
}

func (s *Server) handleFilesList(w http.ResponseWriter, r *http.Request) {
	elevated := queryBool(r, "elevated")
	fsys, done, ok := s.openFS(w, r, elevated, false)
	if !ok {
		return
	}
	defer done()
	ctx := r.Context()
	req := r.URL.Query().Get("path")
	var p string
	if req == "" {
		home, _ := fsys.Home(ctx)
		p = home
	} else {
		var err error
		if p, err = files.CleanPath(req); err != nil {
			writeProblem(w, r, s0FileErr(err))
			return
		}
	}
	// Show the real location: symlinked directories are never browsed under
	// their link's name, so every path shown is the one changes apply to.
	real, err := fsys.RealPath(ctx, p)
	if err != nil {
		writeProblem(w, r, s.fileError(err))
		return
	}
	entries, err := fsys.List(ctx, real)
	if err != nil {
		writeProblem(w, r, s.fileError(err))
		return
	}
	sort.Slice(entries, func(i, j int) bool {
		di, dj := entries[i].Kind == files.KindDir, entries[j].Kind == files.KindDir
		if di != dj {
			return di
		}
		return entries[i].Name < entries[j].Name
	})
	if elevated {
		s.auditFile(ctx, "file.list", r.PathValue("id"), map[string]any{"path": real, "elevated": true}, nil)
	}
	writeJSON(w, http.StatusOK, fileListJSON{Path: real, Requested: p, Parent: path.Dir(real), Entries: entries, Caps: fsys.Caps()})
}

func (s *Server) handleFileStat(w http.ResponseWriter, r *http.Request) {
	p, ok := queryPath(w, r, "path")
	if !ok {
		return
	}
	fsys, done, ok := s.openFS(w, r, queryBool(r, "elevated"), false)
	if !ok {
		return
	}
	defer done()
	info, err := fsys.Stat(r.Context(), p)
	if err != nil {
		writeProblem(w, r, s.fileError(err))
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// handleFileDownload streams a file to the browser.
func (s *Server) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	p, ok := queryPath(w, r, "path")
	if !ok {
		return
	}
	elevated := queryBool(r, "elevated")
	fsys, done, ok := s.openFS(w, r, elevated, false)
	if !ok {
		return
	}
	defer done()
	ctx := r.Context()
	rc, info, err := fsys.Open(ctx, p)
	if err != nil {
		writeProblem(w, r, s.fileError(err))
		return
	}
	defer rc.Close()
	if elevated {
		s.auditFile(ctx, "file.read", r.PathValue("id"), map[string]any{"path": p, "elevated": true, "size": info.Size}, nil)
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(info.Name))
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	// A failure part-way can only be signalled by cutting the connection
	// short: the browser then reports an incomplete download.
	io.Copy(w, rc)
}

type fileWriteJSON struct {
	Path   string     `json:"path"`
	SHA256 string     `json:"sha256"`
	Info   files.Info `json:"info"`
}

// handleFileUpload streams the request body into a private temporary file
// on the host and installs it only once the complete content arrived.
func (s *Server) handleFileUpload(w http.ResponseWriter, r *http.Request) {
	p, ok := queryPath(w, r, "path")
	if !ok {
		return
	}
	// Uploads may take longer than the server's default read timeout.
	_ = http.NewResponseController(w).SetReadDeadline(time.Time{})
	elevated := queryBool(r, "elevated")
	fsys, done, ok := s.openFS(w, r, elevated, true)
	if !ok {
		return
	}
	defer done()
	ctx := r.Context()
	pre := files.Precondition{MustNotExist: !queryBool(r, "overwrite")}
	details := map[string]any{"path": p, "elevated": elevated}
	if !pre.MustNotExist {
		if before, err := fsys.Hash(ctx, p); err == nil {
			pre.ExpectSHA256 = before
			details["before_sha256"] = before
		}
	}
	inst, err := s.stageAndInstall(ctx, fsys, p, r.Body, pre)
	if inst != nil {
		details["after_sha256"], details["size"] = inst.SHA256, inst.Info.Size
		details["owner"], details["group"], details["mode"] = inst.Info.Owner, inst.Info.Group, files.FormatMode(inst.Info.Mode)
	}
	s.auditFile(context.WithoutCancel(ctx), "file.upload", r.PathValue("id"), details, err)
	if err != nil {
		writeProblem(w, r, s.fileError(err))
		return
	}
	writeJSON(w, http.StatusOK, fileWriteJSON{Path: p, SHA256: inst.SHA256, Info: inst.Info})
}

func (s *Server) stageAndInstall(ctx context.Context, fsys files.FS, p string, body io.Reader, pre files.Precondition) (*files.Installed, error) {
	st, err := fsys.Stage(ctx, p, body, files.StageOptions{})
	if err != nil {
		return nil, err
	}
	defer st.Discard(context.WithoutCancel(ctx))
	return st.Install(ctx, pre)
}

type fileTextJSON struct {
	Path    string     `json:"path"`
	Content string     `json:"content"`
	SHA256  string     `json:"sha256"`
	Info    files.Info `json:"info"`
}

func (s *Server) handleFileText(w http.ResponseWriter, r *http.Request) {
	p, ok := queryPath(w, r, "path")
	if !ok {
		return
	}
	elevated := queryBool(r, "elevated")
	fsys, done, ok := s.openFS(w, r, elevated, false)
	if !ok {
		return
	}
	defer done()
	ctx := r.Context()
	info, err := fsys.Stat(ctx, p)
	if err != nil {
		writeProblem(w, r, s.fileError(err))
		return
	}
	if info.Kind == files.KindFile && info.Size > files.MaxTextBytes {
		writeProblem(w, r, problem(http.StatusUnprocessableEntity, "not_text",
			fmt.Sprintf("%s is larger than %d KiB; download it instead", info.Name, files.MaxTextBytes>>10)))
		return
	}
	rc, info, err := fsys.Open(ctx, p)
	if err != nil {
		writeProblem(w, r, s.fileError(err))
		return
	}
	b, err := io.ReadAll(io.LimitReader(rc, files.MaxTextBytes+1))
	rc.Close()
	if err != nil {
		writeProblem(w, r, s.fileError(err))
		return
	}
	if !files.IsText(b) {
		writeProblem(w, r, problem(http.StatusUnprocessableEntity, "not_text", info.Name+" is not a UTF-8 text file; download it instead"))
		return
	}
	if elevated {
		s.auditFile(ctx, "file.read", r.PathValue("id"), map[string]any{"path": p, "elevated": true, "size": info.Size}, nil)
	}
	writeJSON(w, http.StatusOK, fileTextJSON{Path: p, Content: string(b), SHA256: sha256Hex(b), Info: info})
}

type saveTextRequest struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Elevated bool   `json:"elevated"`
	// ExpectSHA256 is the checksum of the version that was opened; empty
	// creates a new file. Force overwrites whatever is there now.
	ExpectSHA256 string `json:"expect_sha256"`
	Force        bool   `json:"force"`
}

func (s *Server) handleFileSaveText(w http.ResponseWriter, r *http.Request) {
	var req saveTextRequest
	r.Body = http.MaxBytesReader(w, r.Body, 3*files.MaxTextBytes)
	if prob := decodeJSONLimit(w, r, &req, 3*files.MaxTextBytes); prob != nil {
		writeProblem(w, r, prob)
		return
	}
	p, err := files.CleanPath(req.Path)
	if err != nil {
		writeProblem(w, r, s0FileErr(err))
		return
	}
	if len(req.Content) > files.MaxTextBytes || !files.IsText([]byte(req.Content)) {
		writeProblem(w, r, errValidation(fmt.Sprintf("text content must be UTF-8 and at most %d KiB", files.MaxTextBytes>>10)))
		return
	}
	fsys, done, ok := s.openFS(w, r, req.Elevated, true)
	if !ok {
		return
	}
	defer done()
	ctx := r.Context()
	pre := files.Precondition{ExpectSHA256: req.ExpectSHA256, MustNotExist: req.ExpectSHA256 == "" && !req.Force}
	details := map[string]any{"path": p, "elevated": req.Elevated, "before_sha256": req.ExpectSHA256, "forced": req.Force}
	if req.Force {
		pre.ExpectSHA256 = ""
		if cur, err := fsys.Hash(ctx, p); err == nil {
			details["before_sha256"] = cur
		}
	}
	inst, err := s.stageAndInstall(ctx, fsys, p, bytes.NewReader([]byte(req.Content)), pre)
	if inst != nil {
		details["after_sha256"], details["size"] = inst.SHA256, inst.Info.Size
	}
	s.auditFile(context.WithoutCancel(ctx), "file.write", r.PathValue("id"), details, err)
	var fe *files.Error
	if errors.As(err, &fe) && fe.Code == files.CodeConflict {
		writeProblem(w, r, errConflict("file_changed",
			"This file changed on the host after you opened it. Reload it or explicitly overwrite the newer version."))
		return
	}
	if err != nil {
		writeProblem(w, r, s.fileError(err))
		return
	}
	writeJSON(w, http.StatusOK, fileWriteJSON{Path: p, SHA256: inst.SHA256, Info: inst.Info})
}

type fileChangeRequest struct {
	Path      string `json:"path"`
	To        string `json:"to"`
	Mode      string `json:"mode"`
	Owner     string `json:"owner"`
	Group     string `json:"group"`
	Recursive bool   `json:"recursive"`
	Elevated  bool   `json:"elevated"`
}

// handleFileChange implements mkdir, rename (move), delete, chmod and chown.
func (s *Server) handleFileChange(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req fileChangeRequest
		if prob := decodeJSON(w, r, &req); prob != nil {
			writeProblem(w, r, prob)
			return
		}
		p, err := files.CleanPath(req.Path)
		if err != nil {
			writeProblem(w, r, s0FileErr(err))
			return
		}
		var mode uint32
		if action == "mkdir" || action == "chmod" {
			if req.Mode == "" && action == "mkdir" {
				req.Mode = "0755"
			}
			if mode, err = files.ParseMode(req.Mode); err != nil {
				writeProblem(w, r, s0FileErr(err))
				return
			}
		}
		to := ""
		if action == "rename" {
			if to, err = files.CleanPath(req.To); err != nil {
				writeProblem(w, r, s0FileErr(err))
				return
			}
		}
		fsys, done, ok := s.openFS(w, r, req.Elevated, true)
		if !ok {
			return
		}
		defer done()
		ctx := r.Context()
		details := map[string]any{"path": p, "elevated": req.Elevated}
		switch action {
		case "mkdir":
			details["mode"] = files.FormatMode(mode)
			err = fsys.Mkdir(ctx, p, mode)
		case "rename":
			details["to"] = to
			err = fsys.Rename(ctx, p, to)
		case "delete":
			details["recursive"] = req.Recursive
			if info, serr := fsys.Stat(ctx, p); serr == nil {
				details["kind"] = info.Kind
				if info.Kind == files.KindFile {
					if h, herr := fsys.Hash(ctx, p); herr == nil {
						details["before_sha256"] = h
					}
				}
			}
			err = fsys.Remove(ctx, p, req.Recursive)
		case "chmod":
			if info, serr := fsys.Stat(ctx, p); serr == nil {
				details["before_mode"] = files.FormatMode(info.Mode)
			}
			details["mode"] = files.FormatMode(mode)
			err = fsys.Chmod(ctx, p, mode)
		case "chown":
			if info, serr := fsys.Stat(ctx, p); serr == nil {
				details["before_owner"], details["before_group"] = info.Owner, info.Group
			}
			details["owner"], details["group"] = req.Owner, req.Group
			err = fsys.Chown(ctx, p, req.Owner, req.Group)
		}
		s.auditFile(context.WithoutCancel(ctx), "file."+action, r.PathValue("id"), details, err)
		if err != nil {
			writeProblem(w, r, s.fileError(err))
			return
		}
		target := p
		if action == "rename" {
			target = to
		}
		if action == "delete" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		info, err := fsys.Stat(ctx, target)
		if err != nil {
			writeProblem(w, r, s.fileError(err))
			return
		}
		writeJSON(w, http.StatusOK, info)
	}
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
