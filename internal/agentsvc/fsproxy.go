package agentsvc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"drool/internal/agentrpc"
	"drool/internal/driver"
	"drool/internal/filedeploy"
	"drool/internal/files"
	"drool/internal/model"
)

// agentFS is files.FS on an agent host: each call is a request on one
// data channel, executed by the agent (internal/agent/localfs) under its
// capability ceiling. The M6 API, deployments, validation, history and
// rollback use it unchanged.
type agentFS struct {
	ch   *Chan
	mu   sync.Mutex // one request at a time
	caps files.Caps
	busy bool // a download is streaming
}

func capabilityFileError(e *agentrpc.FSError) error {
	if e == nil {
		return nil
	}
	if e.Code == "capability" {
		return &files.Error{Code: "capability", Message: e.Message, Hint: e.Hint}
	}
	return &files.Error{Code: e.Code, Message: e.Message, Hint: e.Hint}
}

func (f *agentFS) call(req agentrpc.FSRequest) (*agentrpc.FSResponse, error) {
	if f.busy {
		return nil, errors.New("a download on this file session is still open")
	}
	if err := f.ch.Send(agentrpc.FrameFSReq, agentrpc.MustJSON(req), nil); err != nil {
		return nil, &model.OpError{Class: model.ClassConnection, Message: "connection to the agent lost: " + err.Error()}
	}
	for {
		fr, err := f.ch.Recv()
		if err != nil {
			return nil, &model.OpError{Class: model.ClassConnection, Message: "connection to the agent lost: " + err.Error()}
		}
		if fr.Kind != agentrpc.FrameFSResp {
			continue
		}
		var resp agentrpc.FSResponse
		if err := json.Unmarshal(fr.Json, &resp); err != nil {
			return nil, err
		}
		return &resp, capabilityFileError(resp.Error)
	}
}

// OpenFS opens a file session on an agent host. Every session is an
// operation with an id and expiry; deployments also carry their state
// version.
func (s *Service) OpenFS(ctx context.Context, hostID string, state *agentrpc.StateVersion, summary string, ttl time.Duration) (files.FS, error) {
	se, err := s.usable(ctx, hostID)
	if err != nil {
		return nil, err
	}
	return s.openFS(ctx, se, state, summary, ttl)
}

func (s *Service) openFS(ctx context.Context, se *Session, state *agentrpc.StateVersion, summary string, ttl time.Duration) (*agentFS, error) {
	info := driver.StepInfoFrom(ctx)
	info.HostID = se.ident.HostID
	env := s.envelope(info, agentrpc.PurposeFiles, nil, int(ttl/time.Second), state, summary)
	ch, err := s.openChannel(ctx, se, agentrpc.PurposeFiles)
	if err != nil {
		return nil, notConnected(err.Error())
	}
	fail := func(err error) (*agentFS, error) {
		ch.Close()
		return nil, err
	}
	if err := ch.Send(agentrpc.FrameOp, agentrpc.MustJSON(env), nil); err != nil {
		return fail(notConnected(err.Error()))
	}
	fr, err := ch.Recv()
	if err != nil {
		return fail(notConnected(err.Error()))
	}
	switch fr.Kind {
	case agentrpc.FrameRejected:
		var r agentrpc.Rejection
		json.Unmarshal(fr.Json, &r)
		if r.Reason == agentrpc.RejectCapability {
			return fail(&files.Error{Code: "capability", Message: r.Message,
				Hint: "File access is limited by the host's capability ceiling (/etc/drool/agent.yaml); DROOL does not work around it through SSH."})
		}
		return fail(rejectionError(r))
	case agentrpc.FrameAccepted:
	default:
		return fail(notConnected("unexpected reply " + fr.Kind))
	}
	f := &agentFS{ch: ch}
	resp, err := f.call(agentrpc.FSRequest{Method: "caps"})
	if err != nil {
		return fail(err)
	}
	if resp.Caps != nil {
		f.caps = *resp.Caps
	}
	return f, nil
}

func (f *agentFS) Caps() files.Caps { return f.caps }

func (f *agentFS) Close() error {
	f.ch.Close()
	return nil
}

func (f *agentFS) Home(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.call(agentrpc.FSRequest{Method: "home"})
	if err != nil {
		return "", err
	}
	return r.Path, nil
}

func (f *agentFS) RealPath(ctx context.Context, p string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.call(agentrpc.FSRequest{Method: "realpath", Path: p})
	if err != nil {
		return "", err
	}
	return r.Path, nil
}

func (f *agentFS) Stat(ctx context.Context, p string) (files.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.call(agentrpc.FSRequest{Method: "stat", Path: p})
	if err != nil {
		return files.Info{}, err
	}
	if r.Info == nil {
		return files.Info{}, errors.New("no file information from the agent")
	}
	return *r.Info, nil
}

func (f *agentFS) List(ctx context.Context, dir string) ([]files.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.call(agentrpc.FSRequest{Method: "list", Path: dir})
	if err != nil {
		return nil, err
	}
	return r.Infos, nil
}

func (f *agentFS) Hash(ctx context.Context, p string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.call(agentrpc.FSRequest{Method: "hash", Path: p})
	if err != nil {
		return "", err
	}
	return r.SHA256, nil
}

// download streams data frames until EOF; it holds the session lock.
type download struct {
	f    *agentFS
	buf  []byte
	err  error
	done bool
	once sync.Once
}

func (d *download) Read(p []byte) (int, error) {
	for len(d.buf) == 0 {
		if d.done {
			if d.err != nil {
				return 0, d.err
			}
			return 0, io.EOF
		}
		fr, err := d.f.ch.Recv()
		if err != nil {
			d.done, d.err = true, &model.OpError{Class: model.ClassConnection, Message: "connection to the agent lost during the download"}
			continue
		}
		switch fr.Kind {
		case agentrpc.FrameData:
			d.buf = fr.Data
		case agentrpc.FrameEOF:
			d.done = true
			var resp agentrpc.FSResponse
			if json.Unmarshal(fr.Json, &resp) == nil && resp.Error != nil {
				d.err = capabilityFileError(resp.Error)
			}
		}
	}
	n := copy(p, d.buf)
	d.buf = d.buf[n:]
	return n, nil
}

func (d *download) Close() error {
	d.once.Do(func() {
		// Drain the rest so the channel is usable again.
		io.Copy(io.Discard, readerOnly{d})
		d.f.busy = false
		d.f.mu.Unlock()
	})
	return nil
}

type readerOnly struct{ r io.Reader }

func (r readerOnly) Read(p []byte) (int, error) { return r.r.Read(p) }

func (f *agentFS) Open(ctx context.Context, p string) (io.ReadCloser, files.Info, error) {
	f.mu.Lock()
	r, err := f.call(agentrpc.FSRequest{Method: "open", Path: p})
	if err != nil {
		f.mu.Unlock()
		return nil, files.Info{}, err
	}
	f.busy = true
	var in files.Info
	if r.Info != nil {
		in = *r.Info
	}
	return &download{f: f}, in, nil
}

type stagedRemote struct {
	f    *agentFS
	path string
	sha  string
	size int64
	dest string
	once sync.Once
}

func (s *stagedRemote) Path() string   { return s.path }
func (s *stagedRemote) SHA256() string { return s.sha }
func (s *stagedRemote) Size() int64    { return s.size }

func (s *stagedRemote) Validate(ctx context.Context, cmd string, out io.Writer) (files.ValidationResult, error) {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	r, err := s.f.call(agentrpc.FSRequest{Method: "validate", Command: cmd})
	if r != nil {
		io.WriteString(out, r.Output)
	}
	if err != nil {
		var fe *files.Error
		if errors.As(err, &fe) && fe.Code == "capability" {
			return files.ValidationResult{Command: cmd}, &model.OpError{Class: model.ClassCapability, Message: fe.Message,
				Hint: "Allow the shell capability or list this command in files.allowed_validation_commands in /etc/drool/agent.yaml on the host."}
		}
		return files.ValidationResult{Command: cmd}, err
	}
	if r.Validation == nil {
		return files.ValidationResult{Command: cmd}, errors.New("no validation result")
	}
	return *r.Validation, nil
}

func (s *stagedRemote) Install(ctx context.Context, pre files.Precondition) (*files.Installed, error) {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	r, err := s.f.call(agentrpc.FSRequest{Method: "install", Path: s.dest, Pre: &pre})
	if err != nil {
		return nil, err
	}
	return r.Installed, nil
}

func (s *stagedRemote) Discard(ctx context.Context) {
	s.once.Do(func() {
		s.f.mu.Lock()
		defer s.f.mu.Unlock()
		s.f.call(agentrpc.FSRequest{Method: "discard"})
	})
}

func (f *agentFS) Stage(ctx context.Context, p string, r io.Reader, opt files.StageOptions) (files.Staged, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ch.Send(agentrpc.FrameFSReq, agentrpc.MustJSON(agentrpc.FSRequest{Method: "stage", Path: p, Stage: &opt}), nil); err != nil {
		return nil, &model.OpError{Class: model.ClassConnection, Message: "connection to the agent lost: " + err.Error()}
	}
	buf := make([]byte, 64<<10)
	h := sha256.New()
	var readErr error
	for {
		if err := ctx.Err(); err != nil {
			readErr = err
			break
		}
		n, err := r.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			if serr := f.ch.Send(agentrpc.FrameData, nil, append([]byte(nil), buf[:n]...)); serr != nil {
				return nil, &model.OpError{Class: model.ClassConnection, Message: "connection to the agent lost during the upload"}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			readErr = err
			break
		}
	}
	var eofJSON []byte
	if readErr != nil {
		eofJSON = agentrpc.MustJSON(map[string]string{"error": readErr.Error()}) // the agent discards the partial file
	}
	f.ch.Send(agentrpc.FrameEOF, eofJSON, nil)
	for {
		fr, err := f.ch.Recv()
		if err != nil {
			return nil, &model.OpError{Class: model.ClassConnection, Message: "connection to the agent lost: " + err.Error()}
		}
		if fr.Kind != agentrpc.FrameFSResp {
			continue
		}
		var resp agentrpc.FSResponse
		if err := json.Unmarshal(fr.Json, &resp); err != nil {
			return nil, err
		}
		if readErr != nil {
			return nil, readErr
		}
		if err := capabilityFileError(resp.Error); err != nil {
			return nil, err
		}
		if want := hex.EncodeToString(h.Sum(nil)); resp.SHA256 != want {
			return nil, files.Errorf(files.CodeIntegrity, "the agent received content with sha256 %.12s, sent %.12s", resp.SHA256, want)
		}
		return &stagedRemote{f: f, path: resp.Path, sha: resp.SHA256, size: resp.Size, dest: p}, nil
	}
}

func (f *agentFS) simple(req agentrpc.FSRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, err := f.call(req)
	return err
}

func (f *agentFS) Mkdir(ctx context.Context, p string, mode uint32) error {
	return f.simple(agentrpc.FSRequest{Method: "mkdir", Path: p, Mode: mode})
}

func (f *agentFS) Rename(ctx context.Context, from, to string) error {
	return f.simple(agentrpc.FSRequest{Method: "rename", Path: from, To: to})
}

func (f *agentFS) Remove(ctx context.Context, p string, recursive bool) error {
	return f.simple(agentrpc.FSRequest{Method: "remove", Path: p, Recursive: recursive})
}

func (f *agentFS) Chmod(ctx context.Context, p string, mode uint32) error {
	return f.simple(agentrpc.FSRequest{Method: "chmod", Path: p, Mode: mode})
}

func (f *agentFS) Chown(ctx context.Context, p, owner, group string) error {
	return f.simple(agentrpc.FSRequest{Method: "chown", Path: p, Owner: owner, Group: group})
}

// --- file deployments ---

// executeFileDeploy runs the M6 deployment sequence (internal/filedeploy)
// with the agent's files.FS. It is a state-changing operation in the
// "files" domain, so it carries the next controller-owned revision.
func (s *Service) executeFileDeploy(ctx context.Context, se *Session, op *model.FileDeployOp, out driver.OutputSink, started func(), dispatched Dispatched) (*driver.OpResult, error) {
	if s.rec == nil || s.arts == nil {
		return nil, &model.OpError{Class: model.ClassInternal, Message: "file deployment is not configured on this controller"}
	}
	content := op.Artifact
	if op.Remove {
		content = "remove"
	}
	hsum := sha256.Sum256([]byte(op.Path + "\x00" + content))
	sv, err := s.nextRevision(ctx, se.ident.HostID, "files", "sha256:"+hex.EncodeToString(hsum[:]))
	if err != nil {
		var re *ReconcileError
		if errors.As(err, &re) {
			return nil, &model.OpError{Class: model.ClassConfiguration, Message: re.Error(),
				Hint: "The host has file-deployment state this controller doesn't know (restored from an older backup?). Adopt the host's state on the host page before deploying again."}
		}
		return nil, err
	}
	fsys, err := s.openFS(ctx, se, sv, op.Path, time.Duration(op.TimeoutS+120)*time.Second)
	if err != nil {
		return nil, err
	}
	defer fsys.Close()
	if dispatched != nil {
		dispatched()
	}
	started()
	svc := func(ctx context.Context, unit, action string) error {
		state := map[string]string{"reload": "reloaded", "restart": "restarted"}[action]
		sop := model.Operation{Type: model.OpService, Service: &model.ServiceOp{Name: unit, State: state, TimeoutS: 120}}
		if err := sop.Normalize(); err != nil {
			return err
		}
		env := s.envelope(driver.StepInfoFrom(ctx), agentrpc.PurposeOp, &sop, 120, nil, "")
		_, err := s.runEnvelope(ctx, se, env, out, nil, nil)
		return err
	}
	res, err := filedeploy.Run(ctx, filedeploy.Deps{FS: fsys, Source: s.arts, Recorder: s.rec, Service: svc,
		Notes: out.Stderr(), Output: out.Stdout(), Step: driver.StepInfoFrom(ctx)}, op)
	zero := 0
	changed := res.Changed
	var data map[string]any
	b, _ := json.Marshal(res)
	json.Unmarshal(b, &data)
	return &driver.OpResult{ExitCode: &zero, Changed: &changed, Data: data}, err
}
