package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"drool/internal/agentcaps"
	"drool/internal/agentrpc"
	"drool/internal/agentrpc/pb"
	"drool/internal/driver"
	"drool/internal/hostops"
	"drool/internal/model"
)

func randomID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// maxIssueSkew is how far in the future an operation's issue time may be
// (clock differences between controller and host).
const maxIssueSkew = 10 * time.Minute

// checkOp applies the capability ceiling to an operation.
func checkOp(c agentcaps.Caps, op model.Operation) error {
	switch op.Type {
	case model.OpCommand, model.OpScript:
		return c.AllowShell()
	case model.OpPackage:
		return c.AllowPackages(op.Package.Names)
	case model.OpSystemUpdate:
		if err := c.AllowPackages(nil); err != nil {
			return err
		}
		if op.SystemUpdate.Reboot != "" && op.SystemUpdate.Reboot != "never" {
			return c.AllowReboot()
		}
		return nil
	case model.OpService:
		return c.AllowService(op.Service.Name)
	case model.OpReboot:
		return c.AllowReboot()
	case model.OpFirewall:
		if op.Firewall.Mutating() {
			return c.AllowFirewall()
		}
		return c.AllowMonitoring()
	}
	return &agentcaps.Error{Capability: "", Message: fmt.Sprintf("the agent does not run %q operations directly", op.Type)}
}

// admit decides whether an operation may run, in this order: well formed,
// not a duplicate, not expired, same controller epoch, not stale, allowed by
// the ceiling. Only then is the id recorded durably; nothing runs before.
func (a *Agent) admit(env *agentrpc.OpEnvelope) *agentrpc.Rejection {
	now := a.now()
	if env.OpID == "" || env.ExpiresAt.IsZero() {
		return &agentrpc.Rejection{Reason: agentrpc.RejectInvalid, Message: "the command has no operation id or expiry"}
	}
	if _, seen := a.ledger.Get(env.OpID); seen {
		return &agentrpc.Rejection{Reason: agentrpc.RejectDuplicate, Message: "operation " + env.OpID + " was already received; not running it again"}
	}
	if now.After(env.ExpiresAt) {
		return &agentrpc.Rejection{Reason: agentrpc.RejectExpired, Message: fmt.Sprintf("the command expired at %s (agent time %s)", env.ExpiresAt.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))}
	}
	if env.IssuedAt.After(now.Add(maxIssueSkew)) {
		return &agentrpc.Rejection{Reason: agentrpc.RejectInvalid, Message: "the command was issued in the future; the controller's or this host's clock is wrong"}
	}
	id, err := a.store.Identity()
	if err != nil {
		return &agentrpc.Rejection{Reason: agentrpc.RejectInvalid, Message: err.Error()}
	}
	if env.Epoch != id.ControllerEpoch {
		return &agentrpc.Rejection{Reason: agentrpc.RejectEpoch,
			Message: fmt.Sprintf("the command comes from controller epoch %s, but this agent belongs to epoch %s; it is never applied automatically", short(env.Epoch), short(id.ControllerEpoch))}
	}
	if sv := env.State; sv != nil {
		doms, err := a.store.Domains()
		if err != nil {
			return &agentrpc.Rejection{Reason: agentrpc.RejectInvalid, Message: err.Error()}
		}
		if cur, ok := doms[sv.Domain]; ok {
			if cur.Epoch != sv.Epoch {
				return &agentrpc.Rejection{Reason: agentrpc.RejectEpoch, Message: fmt.Sprintf("%s state belongs to epoch %s, the command to %s", sv.Domain, short(cur.Epoch), short(sv.Epoch))}
			}
			if sv.Revision <= cur.Revision {
				return &agentrpc.Rejection{Reason: agentrpc.RejectStale, Message: fmt.Sprintf("stale %s configuration: revision %d, this host already applied %d", sv.Domain, sv.Revision, cur.Revision)}
			}
		}
	}
	caps := a.Effective()
	var cerr error
	switch env.Purpose {
	case agentrpc.PurposeFiles:
		if !caps.Files.Enabled {
			cerr = caps.AllowPath("/")
		}
	case agentrpc.PurposeOp, agentrpc.PurposeSimulate:
		if env.Operation == nil {
			return &agentrpc.Rejection{Reason: agentrpc.RejectInvalid, Message: "the command has no operation"}
		}
		if err := env.Operation.Normalize(); err != nil {
			return &agentrpc.Rejection{Reason: agentrpc.RejectInvalid, Message: err.Error()}
		}
		cerr = checkOp(caps, *env.Operation)
		if env.Purpose == agentrpc.PurposeSimulate && env.Operation.Type != model.OpPackage && env.Operation.Type != model.OpSystemUpdate {
			return &agentrpc.Rejection{Reason: agentrpc.RejectInvalid, Message: "only package and system-update operations can be simulated"}
		}
	default:
		return &agentrpc.Rejection{Reason: agentrpc.RejectInvalid, Message: "unknown purpose " + env.Purpose}
	}
	var ce *agentcaps.Error
	if errors.As(cerr, &ce) {
		return &agentrpc.Rejection{Reason: agentrpc.RejectCapability, Message: ce.Message, Capability: ce.Capability}
	}
	if err := a.ledger.Accept(env.OpID, opType(env), env.ExpiresAt); err != nil {
		if errors.Is(err, ErrDuplicate) {
			return &agentrpc.Rejection{Reason: agentrpc.RejectDuplicate, Message: "operation " + env.OpID + " was already received"}
		}
		return &agentrpc.Rejection{Reason: agentrpc.RejectBusy, Message: "could not record the operation: " + err.Error()}
	}
	return nil
}

func opType(env *agentrpc.OpEnvelope) string {
	if env.Purpose == agentrpc.PurposeFiles || env.Operation == nil {
		return "files"
	}
	return env.Operation.Type
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// summary is the non-secret part of an operation for the agent's own log.
func summary(env *agentrpc.OpEnvelope) string {
	if env.Operation == nil {
		return env.Summary
	}
	op := env.Operation
	switch op.Type {
	case model.OpPackage:
		return op.Package.Action + " " + strings.Join(op.Package.Names, " ")
	case model.OpService:
		return op.Service.Name
	case model.OpSystemUpdate:
		return op.SystemUpdate.Mode
	}
	return env.Summary
}

func opHash(env *agentrpc.OpEnvelope) string {
	b, _ := json.Marshal(env.Operation)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// --- channels ---

// channel wraps an agent-opened Channel stream; Send is serialized.
type channel struct {
	cs   pb.Agent_ChannelClient
	mu   sync.Mutex
	dead bool
}

func (c *channel) send(kind string, js []byte, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dead {
		return io.ErrClosedPipe
	}
	if err := c.cs.Send(&pb.Frame{Kind: kind, Json: js, Data: data}); err != nil {
		c.dead = true
		return err
	}
	return nil
}

// frameSink streams operation output as frames. When the channel breaks,
// output is dropped but the operation continues.
type frameSink struct {
	c      *channel
	stdout frameWriter
	stderr frameWriter
}

type frameWriter struct {
	c    *channel
	kind string
}

func (w frameWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		w.c.send(w.kind, nil, append([]byte(nil), p...))
	}
	return len(p), nil
}

func newFrameSink(c *channel) *frameSink {
	return &frameSink{c: c, stdout: frameWriter{c, agentrpc.FrameStdout}, stderr: frameWriter{c, agentrpc.FrameStderr}}
}

func (s *frameSink) Stdout() io.Writer { return s.stdout }
func (s *frameSink) Stderr() io.Writer { return s.stderr }
func (s *frameSink) Phase(state string) {
	s.c.send(agentrpc.FramePhase, []byte(`"`+state+`"`), nil)
}

// runOp executes an admitted operation. Its context is the agent's, not
// the channel's: a dropped connection doesn't kill a package transaction
// half-way; the outcome is kept in the ledger for the controller to ask.
func (a *Agent) runOp(env *agentrpc.OpEnvelope, c *channel, controller string) {
	timeout := time.Duration(env.TimeoutS) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(a.rootCtx(), timeout)
	a.mu.Lock()
	a.running[env.OpID] = cancel
	a.mu.Unlock()
	defer func() {
		cancel()
		a.mu.Lock()
		delete(a.running, env.OpID)
		a.mu.Unlock()
	}()
	// Cancellation from the controller arrives on the channel.
	go func() {
		for {
			f, err := c.cs.Recv()
			if err != nil {
				return
			}
			if f.Kind == agentrpc.FrameCancel {
				cancel()
			}
		}
	}()
	a.oplog.Write(OpEvent{Event: "accepted", OpID: env.OpID, Type: opType(env), Controller: controller,
		JobID: env.JobID, Summary: summary(env), OpHash: opHash(env)})
	out := newFrameSink(c)
	started := func() {
		a.ledger.Running(env.OpID)
		c.send(agentrpc.FrameStarted, nil, nil)
	}
	var res *driver.OpResult
	var err error
	if env.Purpose == agentrpc.PurposeSimulate {
		var sr *hostops.SimResult
		sr, err = hostops.Host{R: a.run, B: hostops.Become{Method: "none"}}.Simulate(ctx, *env.Operation)
		zero := 0
		res = &driver.OpResult{ExitCode: &zero, Data: map[string]any{"simulation": sr}}
	} else {
		res, err = a.execute(ctx, *env.Operation, out, started)
	}
	if err != nil && ctx.Err() != nil && model.ClassOf(err) != model.ClassCancelled && model.ClassOf(err) != model.ClassTimeout {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = &model.OpError{Class: model.ClassTimeout, Message: "timed out", Err: err}
		} else {
			err = &model.OpError{Class: model.ClassCancelled, Message: "cancelled", Err: err}
		}
	}
	r := &agentrpc.OpResult{Error: agentrpc.ToWire(err)}
	if res != nil {
		r.ExitCode, r.Changed = res.ExitCode, res.Changed
		if res.Data != nil {
			b, _ := json.Marshal(res.Data)
			json.Unmarshal(b, &r.Data)
		}
	}
	a.ledger.Complete(env.OpID, r)
	if err == nil && env.State != nil {
		if serr := a.store.ApplyDomain(*env.State); serr != nil {
			a.log.Error("record state version", "err", serr)
		}
	}
	ev := OpEvent{Event: "completed", OpID: env.OpID, Type: opType(env), Controller: controller, JobID: env.JobID, Outcome: "ok"}
	if err != nil {
		ev.Outcome, ev.ErrorClass = "failed", string(model.ClassOf(err))
	}
	a.oplog.Write(ev)
	c.send(agentrpc.FrameResult, agentrpc.MustJSON(r), nil)
	if env.Operation != nil && env.Operation.Type == model.OpFirewall && err == nil {
		a.afterFirewallOp(env.Operation.Firewall, res)
	}
}

// execute runs one operation locally with the shared implementations.
func (a *Agent) execute(ctx context.Context, op model.Operation, out driver.OutputSink, started func()) (*driver.OpResult, error) {
	root := hostops.Become{Method: "none"}
	h := hostops.Host{R: a.run, B: root} // WaitReboot nil: the controller waits for the new boot id
	switch op.Type {
	case model.OpCommand:
		return hostops.Shell(ctx, a.run, root, "sh", op.Command.Command+"\n", out, started)
	case model.OpScript:
		body := op.Script.Body.Text
		if !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		return hostops.Shell(ctx, a.run, root, op.Script.Interpreter, body, out, started)
	case model.OpPackage:
		return h.Package(ctx, op.Package, out, started)
	case model.OpSystemUpdate:
		return h.SystemUpdate(ctx, op.SystemUpdate, out, started)
	case model.OpService:
		return h.Service(ctx, op.Service, out, started)
	case model.OpReboot:
		return h.RebootOp(ctx, op.Reboot, out, started)
	case model.OpFirewall:
		if started != nil {
			started()
		}
		return a.runFirewall(ctx, op.Firewall)
	}
	return nil, model.Errorf(model.ClassValidation, false, "the agent cannot run %q operations", op.Type)
}

func (a *Agent) rootCtx() context.Context {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ctx == nil {
		return context.Background()
	}
	return a.ctx
}

// cancelOp cancels a running operation (CancelOp from the session).
func (a *Agent) cancelOp(id string) {
	a.mu.Lock()
	c := a.running[id]
	a.mu.Unlock()
	if c != nil {
		c()
	}
}

// opStatus answers QueryOp from the ledger.
func (a *Agent) opStatus(q *pb.QueryOp) *pb.OpStatus {
	id := q.OpId
	var e LedgerEntry
	var ok bool
	if q.Abandon {
		e, ok = a.ledger.Abandon(id, time.Unix(q.ExpiresAtUnix, 0))
	} else {
		e, ok = a.ledger.Get(id)
	}
	if !ok {
		return &pb.OpStatus{OpId: id, State: "unknown"}
	}
	st := &pb.OpStatus{OpId: id, State: e.State}
	if e.Result != nil {
		st.ResultJson = agentrpc.MustJSON(e.Result)
	}
	return st
}
