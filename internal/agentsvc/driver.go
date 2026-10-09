package agentsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"drool/internal/agentrpc"
	"drool/internal/agentrpc/pb"
	"drool/internal/driver"
	"drool/internal/hostops"
	"drool/internal/ids"
	"drool/internal/model"
)

// --- data channels ---

type pendingChannel struct {
	identityID string
	ready      chan *Chan
	failed     chan string
}

// Chan is an agent-opened data channel the controller uses for one
// operation or file session.
type Chan struct {
	stream pb.Agent_ChannelServer
	done   chan struct{}
	once   sync.Once
	sendMu sync.Mutex
}

// Send sends one frame.
func (c *Chan) Send(kind string, js, data []byte) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return c.stream.Send(&pb.Frame{Kind: kind, Json: js, Data: data})
}

// Recv receives one frame.
func (c *Chan) Recv() (*pb.Frame, error) { return c.stream.Recv() }

// Close releases the channel (the agent sees the stream end).
func (c *Chan) Close() { c.once.Do(func() { close(c.done) }) }

// Channel implements pb.AgentServer: the agent attaches a data channel the
// controller asked for on the session.
func (s *Service) Channel(stream pb.Agent_ChannelServer) error {
	ident, _, err := s.authAgent(stream.Context(), false)
	if err != nil {
		return err
	}
	f, err := stream.Recv()
	if err != nil {
		return err
	}
	s.mu.Lock()
	pc := s.channels[f.ChannelId]
	s.mu.Unlock()
	if pc == nil || pc.identityID != ident.ID || f.Kind != agentrpc.FrameOpen {
		return status.Error(codes.PermissionDenied, "unknown channel")
	}
	c := &Chan{stream: stream, done: make(chan struct{})}
	select {
	case pc.ready <- c:
	default:
		return status.Error(codes.FailedPrecondition, "nobody is waiting for this channel")
	}
	select {
	case <-c.done:
	case <-stream.Context().Done():
	}
	return nil
}

func (s *Service) failChannel(id, msg string) {
	s.mu.Lock()
	pc := s.channels[id]
	s.mu.Unlock()
	if pc != nil {
		select {
		case pc.failed <- msg:
		default:
		}
	}
}

// openChannel asks the agent behind se to open a data channel.
func (s *Service) openChannel(ctx context.Context, se *Session, purpose string) (*Chan, error) {
	id := ids.New(ids.AgentChannel)
	pc := &pendingChannel{identityID: se.ident.ID, ready: make(chan *Chan, 1), failed: make(chan string, 1)}
	s.mu.Lock()
	s.channels[id] = pc
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.channels, id)
		s.mu.Unlock()
	}()
	if err := se.send(&pb.ControllerMessage{Msg: &pb.ControllerMessage_OpenChannel{OpenChannel: &pb.OpenChannel{ChannelId: id, Purpose: purpose}}}); err != nil {
		return nil, err
	}
	select {
	case c := <-pc.ready:
		return c, nil
	case msg := <-pc.failed:
		return nil, errors.New("the agent could not open a channel: " + msg)
	case <-se.done:
		return nil, errors.New("the agent disconnected")
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(30 * time.Second):
		return nil, errors.New("the agent did not open a channel in time")
	}
}

// --- operations ---

// ErrNoSession means no usable agent session exists right now.
var ErrNoSession = errors.New("the DROOL agent is not connected")

func notConnected(reason string) error {
	return &driver.NotStartedError{Err: &model.OpError{Class: model.ClassConnection, Message: reason,
		Hint: "Check that drool-agent is running on the host (systemctl status drool-agent) and can reach the controller's agent port."}}
}

// usable returns the host's session if operations may go through it.
func (s *Service) usable(ctx context.Context, hostID string) (*Session, error) {
	se, single := s.SessionFor(hostID)
	if se == nil {
		return nil, notConnected("the DROOL agent is not connected")
	}
	if after, ok := sessionAfter(ctx); ok && !se.connectedAt.After(after) {
		return nil, notConnected("the agent has not reconnected over a new connection yet")
	}
	if !single {
		return nil, &model.OpError{Class: model.ClassAuthorization, Message: "agent identity conflict: management is quarantined",
			Hint: "Two machines present this host's agent identity (a cloned VM?). Resolve the conflict on the host page."}
	}
	se.mu.Lock()
	q := se.quarantined
	se.mu.Unlock()
	if q {
		return nil, &model.OpError{Class: model.ClassAuthorization, Message: "agent identity conflict: management is quarantined",
			Hint: "Resolve the conflict on the host page."}
	}
	if !se.alive() {
		return nil, notConnected("the DROOL agent stopped responding")
	}
	return se, nil
}

func rejectionError(r agentrpc.Rejection) error {
	switch r.Reason {
	case agentrpc.RejectCapability:
		return &model.OpError{Class: model.ClassCapability, Message: r.Message,
			Hint: "Only root on the host can change its ceiling (/etc/drool/agent.yaml). DROOL never retries a refused operation over SSH."}
	case agentrpc.RejectStale, agentrpc.RejectEpoch:
		return &model.OpError{Class: model.ClassConfiguration, Message: r.Message, Hint: "Reconciliation is required before DROOL changes this state again."}
	case agentrpc.RejectExpired:
		return &model.OpError{Class: model.ClassValidation, Message: r.Message, Hint: "Check that the controller's and the host's clocks are correct."}
	}
	return &model.OpError{Class: model.ClassValidation, Message: "the agent refused the operation: " + r.Message}
}

func (s *Service) envelope(info driver.StepInfo, purpose string, op *model.Operation, timeoutS int, state *agentrpc.StateVersion, summary string) *agentrpc.OpEnvelope {
	now := s.now()
	if timeoutS <= 0 {
		timeoutS = 600
	}
	return &agentrpc.OpEnvelope{OpID: ids.New(ids.AgentOp), IssuedAt: now.UTC(),
		ExpiresAt: now.Add(time.Duration(timeoutS)*time.Second + 2*time.Minute).UTC(), Epoch: s.epoch, State: state,
		Operation: op, TimeoutS: timeoutS, JobID: info.JobID, HostID: info.HostID, Purpose: purpose, Summary: summary}
}

// Dispatched is set when the agent accepted an operation: from then on
// its outcome may be unknown and it must not be re-run on another path.
type Dispatched func()

// Execute runs one operation through the host's agent. Errors before the
// agent accepted the operation are NotStartedErrors (the caller may use a
// fallback path); afterwards a lost connection becomes "outcome unknown",
// resolved by asking the agent once it reconnects.
func (s *Service) Execute(ctx context.Context, hostID string, op model.Operation, out driver.OutputSink, started func(), dispatched Dispatched) (*driver.OpResult, error) {
	se, err := s.usable(ctx, hostID)
	if err != nil {
		return nil, err
	}
	if op.Type == model.OpFileDeploy {
		return s.executeFileDeploy(ctx, se, op.FileDeploy, out, started, dispatched)
	}
	info := driver.StepInfoFrom(ctx)
	info.HostID = hostID
	env := s.envelope(info, agentrpc.PurposeOp, &op, op.Meta().TimeoutS, nil, "")
	res, err := s.runEnvelope(ctx, se, env, out, started, dispatched)
	if err != nil || res == nil {
		return res, err
	}
	// Reboots: the agent can't wait for its own return; the controller
	// waits for it to reconnect with a new boot id.
	if rb, ok := rebootPending(res); ok {
		after, werr := s.waitReboot(ctx, se, rb, out)
		res.Data["reboot"] = after
		if werr != nil {
			if op.Type == model.OpReboot && after.NotRebooted {
				f := false
				res.Changed = &f
			}
			return res, werr
		}
	}
	return res, nil
}

// runEnvelope sends env on a new channel and follows it to the end.
func (s *Service) runEnvelope(ctx context.Context, se *Session, env *agentrpc.OpEnvelope, out driver.OutputSink, started func(), dispatched Dispatched) (*driver.OpResult, error) {
	ch, err := s.openChannel(ctx, se, env.Purpose)
	if err != nil {
		return nil, notConnected(err.Error())
	}
	defer ch.Close()
	if err := ch.Send(agentrpc.FrameOp, agentrpc.MustJSON(env), nil); err != nil {
		return nil, notConnected("could not send the operation: " + err.Error())
	}
	// Until the agent answers accepted/rejected nothing has run.
	f, err := ch.Recv()
	if err != nil {
		// The agent may have recorded and started it before the reply was
		// lost: find out instead of assuming either way.
		return s.resolveUnknown(ctx, se.ident.ID, env, out, err)
	}
	switch f.Kind {
	case agentrpc.FrameRejected:
		var r agentrpc.Rejection
		json.Unmarshal(f.Json, &r)
		return nil, rejectionError(r)
	case agentrpc.FrameAccepted:
	default:
		return nil, notConnected("unexpected reply " + f.Kind)
	}
	if dispatched != nil {
		dispatched()
	}
	startedOnce := sync.OnceFunc(func() {
		if started != nil {
			started()
		}
	})
	cancelSent := false
	recv := make(chan *pb.Frame)
	recvErr := make(chan error, 1)
	go func() {
		for {
			f, err := ch.Recv()
			if err != nil {
				recvErr <- err
				return
			}
			recv <- f
		}
	}()
	for {
		select {
		case <-ctx.Done():
			if !cancelSent {
				cancelSent = true
				ch.Send(agentrpc.FrameCancel, nil, nil)
				se.send(&pb.ControllerMessage{Msg: &pb.ControllerMessage_CancelOp{CancelOp: &pb.CancelOp{OpId: env.OpID}}})
			}
			select {
			case f := <-recv:
				if f.Kind == agentrpc.FrameResult {
					return s.toResult(f.Json, ctx)
				}
				continue
			case <-recvErr:
			case <-time.After(15 * time.Second):
			}
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, &model.OpError{Class: model.ClassTimeout, Message: "timed out; the agent was told to stop the operation"}
			}
			return nil, &model.OpError{Class: model.ClassCancelled, Message: "cancelled; the agent was told to stop the operation"}
		case err := <-recvErr:
			return s.resolveUnknown(ctx, se.ident.ID, env, out, err)
		case f := <-recv:
			switch f.Kind {
			case agentrpc.FrameStarted:
				startedOnce()
			case agentrpc.FrameStdout:
				startedOnce()
				out.Stdout().Write(f.Data)
			case agentrpc.FrameStderr:
				startedOnce()
				out.Stderr().Write(f.Data)
			case agentrpc.FramePhase:
				var ph string
				json.Unmarshal(f.Json, &ph)
				driver.SetPhase(out, ph)
			case agentrpc.FrameResult:
				startedOnce()
				return s.toResult(f.Json, ctx)
			}
		}
	}
}

func (s *Service) toResult(js []byte, ctx context.Context) (*driver.OpResult, error) {
	var r agentrpc.OpResult
	if err := json.Unmarshal(js, &r); err != nil {
		return nil, &model.OpError{Class: model.ClassInternal, Message: "unreadable result from the agent"}
	}
	res := &driver.OpResult{ExitCode: r.ExitCode, Changed: r.Changed, Data: r.Data}
	if res.Data == nil {
		res.Data = map[string]any{}
	}
	return res, r.Error.Err()
}

// resolveUnknown handles a connection lost after the operation may have
// started (DESIGN §9, fault model of ARCHITECTURE-ALPHA §11.4): wait for
// the agent to come back and ask it. The operation is never re-sent.
func (s *Service) resolveUnknown(ctx context.Context, identityID string, env *agentrpc.OpEnvelope, out driver.OutputSink, cause error) (*driver.OpResult, error) {
	fmt.Fprintf(out.Stderr(), "[drool] connection to the agent lost (%v); waiting for it to report the outcome\n", cause)
	unknown := func(detail string) error {
		return &model.OpError{Class: model.ClassConnection, Message: "connection to the agent lost; outcome unknown (" + detail + ")",
			Hint: "The agent may have finished the operation. Check the host before running it again."}
	}
	deadline := time.Now().Add(5 * time.Minute)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return nil, unknown("cancelled while waiting")
		}
		for _, se := range s.liveSessions(identityID) {
			st, err := se.queryOp(ctx, env.OpID, env.ExpiresAt)
			if err != nil {
				continue
			}
			switch st.State {
			case "completed":
				fmt.Fprintf(out.Stderr(), "[drool] the agent reported the outcome after reconnecting; output after the connection loss was not received\n")
				return s.toResult(st.ResultJson, ctx)
			case "unknown", "abandoned":
				// The agent never recorded it, and has now recorded it as
				// abandoned, so it can't start later: it did not run.
				return nil, &driver.NotStartedError{Err: &model.OpError{Class: model.ClassConnection, Message: "the agent never received the operation"}}
			case "interrupted":
				return nil, unknown("the agent restarted while the operation ran")
			}
			// accepted/running: keep waiting.
		}
		time.Sleep(3 * time.Second)
	}
	return nil, unknown("the agent did not report back in time")
}

// queryOp asks the agent about an operation id.
func (se *Session) queryOp(ctx context.Context, opID string, expires time.Time) (*pb.OpStatus, error) {
	w := make(chan *pb.OpStatus, 1)
	se.mu.Lock()
	se.opWait[opID] = w
	se.mu.Unlock()
	defer func() {
		se.mu.Lock()
		delete(se.opWait, opID)
		se.mu.Unlock()
	}()
	if err := se.send(&pb.ControllerMessage{Msg: &pb.ControllerMessage_QueryOp{QueryOp: &pb.QueryOp{OpId: opID, Abandon: true, ExpiresAtUnix: expires.Unix()}}}); err != nil {
		return nil, err
	}
	select {
	case st := <-w:
		return st, nil
	case <-se.done:
		return nil, ErrNoSession
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(15 * time.Second):
		return nil, errors.New("no answer")
	}
}

// --- reboots ---

func rebootPending(res *driver.OpResult) (*hostops.RebootResult, bool) {
	raw, ok := res.Data["reboot"]
	if !ok || raw == nil {
		return nil, false
	}
	b, _ := json.Marshal(raw)
	var rb hostops.RebootResult
	if json.Unmarshal(b, &rb) != nil || rb.BootIDBefore == "" || rb.BootIDAfter != "" {
		return nil, false
	}
	return &rb, rb.Pending
}

// waitReboot waits for the agent to reconnect with a new boot id: the only
// proof the reboot happened (ARCHITECTURE-ALPHA §10.4).
func (s *Service) waitReboot(ctx context.Context, se *Session, rb *hostops.RebootResult, out driver.OutputSink) (*hostops.RebootResult, error) {
	driver.SetPhase(out, "waiting_host")
	start := time.Now()
	var wentAway time.Time
	rb.Pending = false
	for {
		for _, cur := range s.liveSessions(se.ident.ID) {
			if id := cur.info().BootID; id != "" && id != rb.BootIDBefore {
				driver.SetPhase(out, "verifying")
				rb.BootIDAfter = id
				rb.TotalSeconds = time.Since(start).Seconds()
				if !wentAway.IsZero() {
					rb.DownSeconds = time.Since(wentAway).Seconds()
				}
				hostops.Note(out, "host is back after %s with boot id %s (agent reconnected)", time.Since(start).Round(time.Second), id)
				return rb, nil
			}
		}
		if wentAway.IsZero() && !se.alive() {
			wentAway = time.Now()
			hostops.Note(out, "agent disconnected after %s", time.Since(start).Round(time.Second))
		}
		select {
		case <-ctx.Done():
			if wentAway.IsZero() {
				rb.NotRebooted = true
				return rb, &model.OpError{Class: model.ClassTimeout,
					Message: "the host never went away; its agent stayed connected with the old boot id (the reboot did not happen)",
					Hint:    "Something may be blocking shutdown; check the host."}
			}
			return rb, &model.OpError{Class: model.ClassTimeout, Message: "the host went down but its agent did not come back within the timeout",
				Hint: "Check the host's console or power state."}
		case <-time.After(2 * time.Second):
		}
	}
}

// --- dry runs ---

// Simulate runs a package/system-update dry run through the agent.
func (s *Service) Simulate(ctx context.Context, hostID string, op model.Operation) (*hostops.SimResult, error) {
	se, err := s.usable(ctx, hostID)
	if err != nil {
		return nil, err
	}
	env := s.envelope(driver.StepInfo{HostID: hostID}, agentrpc.PurposeSimulate, &op, 300, nil, "")
	res, err := s.runEnvelope(ctx, se, env, discardSink{}, nil, nil)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(res.Data["simulation"])
	var sr hostops.SimResult
	if err := json.Unmarshal(b, &sr); err != nil {
		return nil, err
	}
	return &sr, nil
}

// PackageVersions asks the agent which packages are installed (a
// read-only query; agents older than M14 refuse it).
func (s *Service) PackageVersions(ctx context.Context, hostID string, names []string) (string, map[string]string, error) {
	se, err := s.usable(ctx, hostID)
	if err != nil {
		return "", nil, err
	}
	env := s.envelope(driver.StepInfo{HostID: hostID}, agentrpc.PurposeQuery, nil, 120, nil, "package state")
	env.Query = &agentrpc.StateQuery{Packages: names}
	res, err := s.runEnvelope(ctx, se, env, discardSink{}, nil, nil)
	if err != nil {
		if oe := model.AsOpError(err); strings.Contains(oe.Message, "unknown purpose") {
			return "", nil, &model.OpError{Class: model.ClassConfiguration, Message: "this agent version can't report package state; update the agent"}
		}
		return "", nil, err
	}
	var out struct {
		Manager  string            `json:"manager"`
		Versions map[string]string `json:"versions"`
	}
	b, _ := json.Marshal(res.Data["packages"])
	if err := json.Unmarshal(b, &out); err != nil || out.Versions == nil {
		return "", nil, &model.OpError{Class: model.ClassConfiguration, Message: "the agent's package report is malformed"}
	}
	return out.Manager, out.Versions, nil
}

type discardSink struct{}

func (discardSink) Stdout() io.Writer { return io.Discard }
func (discardSink) Stderr() io.Writer { return io.Discard }
