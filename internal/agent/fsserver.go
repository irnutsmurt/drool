package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"drool/internal/agent/localfs"
	"drool/internal/agentrpc"
	"drool/internal/files"
)

const chunkSize = 64 << 10

func fsErr(err error) *agentrpc.FSError {
	if err == nil {
		return nil
	}
	var fe *files.Error
	if errors.As(err, &fe) {
		e := &agentrpc.FSError{Code: fe.Code, Message: fe.Message, Hint: fe.Hint}
		if fe.Code == "capability" {
			e.Capability = "files"
		}
		return e
	}
	return &agentrpc.FSError{Code: "failed", Message: err.Error()}
}

// serveFiles runs one files.FS session on a channel until the controller
// closes it or the session expires. Every change is written to the agent's
// operation log (method and path, never content).
func (a *Agent) serveFiles(env *agentrpc.OpEnvelope, c *channel, controller string) {
	ctx, cancel := context.WithDeadline(a.rootCtx(), env.ExpiresAt)
	defer cancel()
	go func() { <-ctx.Done(); c.cs.CloseSend() }()
	fs := localfs.New(a.Effective(), a.run)
	a.oplog.Write(OpEvent{Event: "accepted", OpID: env.OpID, Type: "files", Controller: controller, JobID: env.JobID, Summary: env.Summary})
	var staged files.Staged
	defer func() {
		if staged != nil {
			staged.Discard(context.Background())
		}
		a.ledger.Complete(env.OpID, &agentrpc.OpResult{})
		a.oplog.Write(OpEvent{Event: "completed", OpID: env.OpID, Type: "files", Controller: controller, Outcome: "ok"})
	}()
	reply := func(r agentrpc.FSResponse) bool {
		return c.send(agentrpc.FrameFSResp, agentrpc.MustJSON(r), nil) == nil
	}
	logChange := func(req agentrpc.FSRequest, err error) {
		ev := OpEvent{Event: "file", OpID: env.OpID, Type: req.Method, Controller: controller, Summary: req.Path, Outcome: "ok"}
		if req.To != "" {
			ev.Summary += " -> " + req.To
		}
		if err != nil {
			ev.Outcome, ev.ErrorClass = "failed", fsErr(err).Code
		}
		a.oplog.Write(ev)
	}
	for {
		f, err := c.cs.Recv()
		if err != nil {
			return
		}
		if f.Kind != agentrpc.FrameFSReq {
			continue
		}
		var req agentrpc.FSRequest
		if err := json.Unmarshal(f.Json, &req); err != nil {
			reply(agentrpc.FSResponse{Error: &agentrpc.FSError{Code: files.CodeInvalid, Message: "bad request"}})
			continue
		}
		var resp agentrpc.FSResponse
		switch req.Method {
		case "caps":
			c := fs.Caps()
			resp.Caps = &c
		case "home":
			resp.Path, err = fs.Home(ctx)
		case "realpath":
			resp.Path, err = fs.RealPath(ctx, req.Path)
		case "stat":
			var in files.Info
			in, err = fs.Stat(ctx, req.Path)
			resp.Info = &in
		case "list":
			resp.Infos, err = fs.List(ctx, req.Path)
		case "hash":
			resp.SHA256, err = fs.Hash(ctx, req.Path)
		case "open":
			var rc io.ReadCloser
			var in files.Info
			rc, in, err = fs.Open(ctx, req.Path)
			if err != nil {
				break
			}
			resp.Info = &in
			if !reply(resp) {
				rc.Close()
				return
			}
			buf := make([]byte, chunkSize)
			for {
				n, rerr := rc.Read(buf)
				if n > 0 && c.send(agentrpc.FrameData, nil, append([]byte(nil), buf[:n]...)) != nil {
					rc.Close()
					return
				}
				if rerr != nil {
					rc.Close()
					eof := agentrpc.FSResponse{}
					if rerr != io.EOF {
						eof.Error = fsErr(rerr)
					}
					c.send(agentrpc.FrameEOF, agentrpc.MustJSON(eof), nil)
					break
				}
			}
			continue
		case "stage":
			if staged != nil {
				staged.Discard(ctx)
				staged = nil
			}
			pr, pw := io.Pipe()
			readerDone := make(chan struct{})
			go func() {
				defer close(readerDone)
				for {
					f, err := c.cs.Recv()
					if err != nil {
						pw.CloseWithError(err)
						return
					}
					switch f.Kind {
					case agentrpc.FrameData:
						if _, err := pw.Write(f.Data); err != nil {
							return
						}
					case agentrpc.FrameEOF:
						if len(f.Json) > 0 { // the sender failed mid-stream
							pw.CloseWithError(errors.New("upload aborted by the controller"))
						} else {
							pw.Close()
						}
						return
					}
				}
			}()
			opt := files.StageOptions{}
			if req.Stage != nil {
				opt = *req.Stage
			}
			staged, err = fs.Stage(ctx, req.Path, pr, opt)
			// Stop consuming upload frames, and wait until the reader is
			// done so only one goroutine ever receives on the stream.
			// Leftover data frames after an early failure are skipped by the
			// main loop.
			pr.CloseWithError(io.ErrClosedPipe)
			<-readerDone
			if err == nil {
				resp.Path, resp.SHA256, resp.Size = staged.Path(), staged.SHA256(), staged.Size()
			}
			logChange(req, err)
		case "validate":
			if staged == nil {
				err = files.Errorf(files.CodeInvalid, "nothing staged")
				break
			}
			var out strings.Builder
			var vr files.ValidationResult
			vctx, vcancel := context.WithTimeout(ctx, 10*time.Minute)
			vr, err = staged.Validate(vctx, req.Command, &out)
			vcancel()
			resp.Validation, resp.Output = &vr, out.String()
			logChange(agentrpc.FSRequest{Method: "validate", Path: staged.Path()}, err)
		case "install":
			if staged == nil {
				err = files.Errorf(files.CodeInvalid, "nothing staged")
				break
			}
			pre := files.Precondition{}
			if req.Pre != nil {
				pre = *req.Pre
			}
			resp.Installed, err = staged.Install(ctx, pre)
			logChange(agentrpc.FSRequest{Method: "install", Path: req.Path}, err)
			if err == nil && env.State != nil {
				if serr := a.store.ApplyDomain(*env.State); serr != nil {
					a.log.Error("record state version", "err", serr)
				}
			}
		case "discard":
			if staged != nil {
				staged.Discard(ctx)
				staged = nil
			}
		case "mkdir":
			err = fs.Mkdir(ctx, req.Path, req.Mode)
			logChange(req, err)
		case "rename":
			err = fs.Rename(ctx, req.Path, req.To)
			logChange(req, err)
		case "remove":
			err = fs.Remove(ctx, req.Path, req.Recursive)
			logChange(req, err)
		case "chmod":
			err = fs.Chmod(ctx, req.Path, req.Mode)
			logChange(req, err)
		case "chown":
			err = fs.Chown(ctx, req.Path, req.Owner, req.Group)
			logChange(req, err)
		default:
			err = files.Errorf(files.CodeUnsupported, "unknown file method %q", req.Method)
		}
		resp.Error = fsErr(err)
		if !reply(resp) {
			return
		}
	}
}
