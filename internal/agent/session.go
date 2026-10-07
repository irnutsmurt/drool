package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"runtime"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"

	"drool/internal/agentcaps"
	"drool/internal/agentpki"
	"drool/internal/agentrpc"
	"drool/internal/agentrpc/pb"
)

// session is one connected Session stream.
type session struct {
	stream     pb.Agent_SessionClient
	client     pb.AgentClient
	conn       *grpc.ClientConn
	controller string // controller certificate fingerprint (for the op log)
	endpoint   string
	mu         sync.Mutex
	renewing   bool
}

func (s *session) send(m *pb.AgentMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stream.Send(m)
}

// errIncompatible means the controller refused our protocol range.
type errIncompatible struct{ reason string }

func (e *errIncompatible) Error() string { return "incompatible with the controller: " + e.reason }

// errRevoked means the controller revoked this identity.
type errRevoked struct{ reason string }

func (e *errRevoked) Error() string { return e.reason }

// Run connects to the controller and serves it until ctx ends,
// reconnecting with backoff. While the controller is unreachable the
// agent simply keeps its last committed state (DESIGN §6.6).
func (a *Agent) Run(ctx context.Context) error {
	a.mu.Lock()
	a.ctx = ctx
	a.mu.Unlock()
	if _, err := a.store.Identity(); err != nil {
		return err
	}
	a.reportUpdateOutcome() // a finished or rolled-back update, if any
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := a.connectAny(ctx)
		if ctx.Err() != nil {
			return nil
		}
		wait := backoff
		var inc *errIncompatible
		var rev *errRevoked
		switch {
		case errors.As(err, &inc):
			wait = 5 * time.Minute
			a.log.Error("controller refused this agent's protocol version", "reason", inc.reason)
		case errors.As(err, &rev):
			wait = 10 * time.Minute
			a.log.Error("this agent's identity is not accepted by the controller", "reason", rev.reason)
		case errors.Is(err, errReconnect):
			// A deliberate reconnect (firewall verification): immediately,
			// and it doesn't count as a failure.
			a.log.Info("reconnecting to the controller over a new connection")
			continue
		default:
			a.log.Warn("controller session ended", "err", err)
		}
		if time.Since(started) > 2*time.Minute {
			backoff = time.Second // a session that lasted resets the backoff
		} else {
			backoff = min(backoff*2, time.Minute)
		}
		jitter := time.Duration(rand.Int64N(int64(wait/4) + 1))
		select {
		case <-ctx.Done():
		case <-time.After(wait + jitter):
		}
	}
	return nil
}

func (a *Agent) endpoints() []string {
	ep, _ := a.store.Endpoints()
	out := ep.All()
	for _, e := range a.file.Controller.Endpoints {
		dup := false
		for _, o := range out {
			dup = dup || o == e
		}
		if !dup {
			out = append(out, e)
		}
	}
	return out
}

func (a *Agent) connectAny(ctx context.Context) error {
	eps := a.endpoints()
	if len(eps) == 0 {
		return errors.New("no controller endpoint known")
	}
	var last error
	for _, ep := range eps {
		err := a.connect(ctx, ep)
		var inc *errIncompatible
		var rev *errRevoked
		if err == nil || errors.As(err, &inc) || errors.As(err, &rev) || ctx.Err() != nil {
			return err
		}
		last = fmt.Errorf("%s: %w", ep, err)
	}
	return last
}

// refreshTrust fetches the controller's newest trust manifest and accepts
// it if it is newer and signed by the pinned root. This happens before
// any management traffic, so an agent that was offline during an
// intermediate rotation learns about it first (DESIGN §4.3.1).
func (a *Agent) refreshTrust(ctx context.Context, ep string) error {
	cfg, err := a.clientTLS(verifyRootOnly, nil)
	if err != nil {
		return err
	}
	conn, err := dial(ep, cfg)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, cur, _ := a.store.Manifest()
	have := uint64(0)
	if cur != nil {
		if m, _, err := a.store.Manifest(); err == nil {
			have = m.Version
		}
	}
	cctx, cancel := callCtx(ctx)
	defer cancel()
	tb, err := pb.NewAgentClient(conn).GetTrust(cctx, &pb.GetTrustRequest{HaveVersion: have})
	if err != nil {
		return err
	}
	if tb.Manifest == nil {
		return nil
	}
	m, changed, err := a.store.OfferManifest(&agentpki.SignedManifest{Payload: tb.Manifest.Payload, Signature: tb.Manifest.Signature})
	if err != nil {
		if errors.Is(err, agentpki.ErrManifestRollback) {
			a.log.Warn("controller offered an older trust manifest; keeping the newer one", "have", have)
			return nil
		}
		return fmt.Errorf("trust manifest: %w", err)
	}
	if changed {
		a.log.Info("accepted trust manifest", "version", m.Version)
		a.oplog.Write(OpEvent{Event: "manifest", Summary: fmt.Sprintf("trust manifest %d accepted", m.Version)})
	}
	return nil
}

// recover renews an expired certificate through the recovery endpoint:
// proof of possession of the identity key, within the grace period.
func (a *Agent) recover(ctx context.Context, ep string, expired *tls.Certificate) error {
	id, err := a.store.Identity()
	if err != nil {
		return err
	}
	key, err := a.store.IdentityKey(false)
	if err != nil {
		return err
	}
	cfg, err := a.clientTLS(verifyFull, expired)
	if err != nil {
		return err
	}
	conn, err := dial(ep, cfg)
	if err != nil {
		return err
	}
	defer conn.Close()
	c := pb.NewAgentClient(conn)
	cctx, cancel := callCtx(ctx)
	defer cancel()
	ch, err := c.RecoveryChallenge(cctx, &pb.RecoveryChallengeRequest{IdentityId: id.IdentityID})
	if err != nil {
		return fmt.Errorf("certificate recovery: %w", err)
	}
	resp, err := c.Recover(cctx, &pb.RecoverRequest{IdentityId: id.IdentityID, Nonce: ch.Nonce,
		Signature: ed25519.Sign(key, agentrpc.RecoverMessage(id.IdentityID, ch.Nonce))})
	if err != nil {
		return &errRevoked{reason: "certificate recovery refused: " + err.Error() + " (re-enroll this agent if it was revoked or the grace period passed)"}
	}
	if err := a.store.SaveChain(resp.CertChain); err != nil {
		return err
	}
	a.log.Info("recovered an expired operational certificate")
	a.oplog.Write(OpEvent{Event: "recovered", Summary: "expired certificate renewed through the recovery endpoint"})
	return nil
}

func (a *Agent) connect(ctx context.Context, ep string) error {
	if err := a.refreshTrust(ctx, ep); err != nil {
		return fmt.Errorf("trust refresh: %w", err)
	}
	cert, leaf, err := a.operationalCert()
	if err != nil {
		return err
	}
	issuerRevoked := false
	if chain, err := a.store.Chain(); err == nil && len(chain) > 1 {
		if m, _, err := a.store.Manifest(); err == nil && m.Revoked(agentpki.Fingerprint(chain[1].Raw)) {
			issuerRevoked = true
		}
	}
	if a.now().After(leaf.NotAfter) || issuerRevoked {
		a.log.Warn("operational certificate expired or issued by a revoked intermediate; recovering", "expires", leaf.NotAfter, "issuer_revoked", issuerRevoked)
		if err := a.recover(ctx, ep, cert); err != nil {
			return err
		}
		if cert, leaf, err = a.operationalCert(); err != nil {
			return err
		}
	}
	cfg, err := a.clientTLS(verifyFull, cert)
	if err != nil {
		return err
	}
	conn, err := dial(ep, cfg)
	if err != nil {
		return err
	}
	defer conn.Close()
	sctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	a.mu.Lock()
	a.sessionCancel = cancel
	a.mu.Unlock()
	client := pb.NewAgentClient(conn)
	var pr peer.Peer
	stream, err := client.Session(sctx, grpc.Peer(&pr))
	if err != nil {
		return err
	}
	s := &session{stream: stream, client: client, conn: conn, endpoint: ep}
	hello := a.hello(leaf)
	hello.FirewallJson = a.firewallReport(sctx)
	if err := s.send(&pb.AgentMessage{Msg: &pb.AgentMessage_Hello{Hello: hello}}); err != nil {
		return err
	}
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	if ti, ok := pr.AuthInfo.(credentials.TLSInfo); ok && len(ti.State.PeerCertificates) > 0 {
		s.controller = agentpki.Fingerprint(ti.State.PeerCertificates[0].Raw)
	}
	if d := first.GetDisconnect(); d != nil {
		if d.Code == "revoked" || d.Code == "unknown" {
			return &errRevoked{reason: d.Reason}
		}
		return errors.New(d.Reason)
	}
	ack := first.GetHelloAck()
	if ack == nil {
		return errors.New("controller did not acknowledge the hello")
	}
	if !ack.Compatible {
		return &errIncompatible{reason: ack.Reason}
	}
	a.log.Info("connected to controller", "endpoint", ep, "protocol", ack.Protocol, "host", ack.HostName, "quarantined", ack.Quarantined)
	a.mu.Lock()
	a.session = s
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		if a.session == s {
			a.session = nil
		}
		a.mu.Unlock()
	}()
	if a.updateHealthy(ack) { // a new version that got this far is healthy
		go a.reportCommit(sctx, s)
	}
	hb := time.Duration(ack.HeartbeatSeconds) * time.Second
	if hb <= 0 {
		hb = 15 * time.Second
	}
	go a.heartbeats(sctx, s, hb)
	go a.telemetryLoop(sctx, s)
	go a.renewLoop(sctx, s)
	go a.learnLoop(sctx, s)
	err = a.serve(sctx, s)
	if errors.Is(context.Cause(sctx), errReconnect) {
		return errReconnect
	}
	return err
}

// hello describes this agent: versions, the authoritative local ceiling,
// the applied state versions and the outcome of any update.
func (a *Agent) hello(leaf *x509.Certificate) *pb.Hello {
	id, _ := a.store.Identity()
	host, _ := os.Hostname()
	ep, _ := a.store.Endpoints()
	r := agentrpc.Local()
	h := &pb.Hello{
		Info: &pb.AgentInfo{AgentVersion: a.opt.Version, ProtocolMin: r.Min, ProtocolMax: r.Max, Hostname: host,
			MachineId: machineID(), Os: runtime.GOOS, Arch: runtime.GOARCH, BootId: bootID()},
		SessionNonce: a.nonce, CeilingJson: caps2json(a.ceiling), EffectiveJson: caps2json(a.Effective()),
		CertNotAfterUnix: leaf.NotAfter.Unix(), Endpoints: ep.All(), State: &pb.StateReport{},
		Update: a.updateReport(), Features: Features,
	}
	if id != nil {
		h.ControllerEpoch = id.ControllerEpoch
	}
	if m, _, err := a.store.Manifest(); err == nil {
		h.ManifestVersion = m.Version
	}
	doms, _ := a.store.Domains()
	for _, d := range doms {
		h.State.Domains = append(h.State.Domains, &pb.DomainState{Domain: d.Domain, Epoch: d.Epoch, Revision: d.Revision, ContentHash: d.ContentHash})
	}
	return h
}

func caps2json(c agentcaps.Caps) []byte { b, _ := json.Marshal(c); return b }

func marshalCaps(c agentcaps.Caps) ([]byte, error) { return json.Marshal(c) }

func (a *Agent) serve(ctx context.Context, s *session) error {
	for {
		m, err := s.stream.Recv()
		if err != nil {
			return err
		}
		switch msg := m.Msg.(type) {
		case *pb.ControllerMessage_Ping:
			s.send(&pb.AgentMessage{Msg: &pb.AgentMessage_Heartbeat{Heartbeat: &pb.Heartbeat{TimeUnix: a.now().Unix(), BootId: bootID()}}})
		case *pb.ControllerMessage_OpenChannel:
			go a.handleChannel(ctx, s, msg.OpenChannel.ChannelId, msg.OpenChannel.Purpose)
		case *pb.ControllerMessage_Renew:
			s.mu.Lock()
			s.renewing = false
			s.mu.Unlock()
			if msg.Renew.Error != "" {
				a.log.Warn("certificate renewal refused", "err", msg.Renew.Error)
				continue
			}
			if err := a.store.SaveChain(msg.Renew.CertChain); err != nil {
				a.log.Error("store renewed certificate", "err", err)
				continue
			}
			a.log.Info("operational certificate renewed")
			a.oplog.Write(OpEvent{Event: "renewed", Controller: s.controller})
		case *pb.ControllerMessage_QueryOp:
			s.send(&pb.AgentMessage{Msg: &pb.AgentMessage_OpStatus{OpStatus: a.opStatus(msg.QueryOp)}})
		case *pb.ControllerMessage_CancelOp:
			a.cancelOp(msg.CancelOp.OpId)
		case *pb.ControllerMessage_EndpointCandidate:
			go a.testCandidate(ctx, s, msg.EndpointCandidate)
		case *pb.ControllerMessage_EndpointCommit:
			a.commitCandidate(s, msg.EndpointCommit)
		case *pb.ControllerMessage_UpdateOffer:
			go a.handleUpdateOffer(ctx, s, msg.UpdateOffer)
		case *pb.ControllerMessage_Manifest:
			ack := &pb.ManifestAck{}
			m, _, err := a.store.OfferManifest(&agentpki.SignedManifest{Payload: msg.Manifest.Payload, Signature: msg.Manifest.Signature})
			if err != nil {
				ack.Error = err.Error()
			} else {
				ack.Version = m.Version
			}
			s.send(&pb.AgentMessage{Msg: &pb.AgentMessage_ManifestAck{ManifestAck: ack}})
		case *pb.ControllerMessage_Config:
			s.send(&pb.AgentMessage{Msg: &pb.AgentMessage_ConfigAck{ConfigAck: a.applyConfig(msg.Config)}})
		case *pb.ControllerMessage_RequestTelemetry:
			go a.sendTelemetry(ctx, s, msg.RequestTelemetry.Kind)
		case *pb.ControllerMessage_FirewallConfig:
			a.setFirewallConfig(msg.FirewallConfig)
		case *pb.ControllerMessage_Disconnect:
			if msg.Disconnect.Code == "revoked" {
				return &errRevoked{reason: msg.Disconnect.Reason}
			}
			if msg.Disconnect.Code == "reconnect" {
				return errReconnect
			}
			return errors.New("controller closed the session: " + msg.Disconnect.Reason)
		}
	}
}

func (a *Agent) heartbeats(ctx context.Context, s *session, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.send(&pb.AgentMessage{Msg: &pb.AgentMessage_Heartbeat{Heartbeat: &pb.Heartbeat{TimeUnix: a.now().Unix(), BootId: bootID()}}}); err != nil {
				return
			}
		}
	}
}

// renewLoop asks for a new certificate at about two thirds of its life.
func (a *Agent) renewLoop(ctx context.Context, s *session) {
	check := func() {
		_, leaf, err := a.operationalCert()
		if err != nil {
			return
		}
		life := leaf.NotAfter.Sub(leaf.NotBefore)
		if a.now().Before(leaf.NotBefore.Add(life * 2 / 3)) {
			return
		}
		s.mu.Lock()
		if s.renewing {
			s.mu.Unlock()
			return
		}
		s.renewing = true
		s.mu.Unlock()
		a.log.Info("renewing the operational certificate", "not_after", leaf.NotAfter)
		s.send(&pb.AgentMessage{Msg: &pb.AgentMessage_Renew{Renew: &pb.RenewRequest{}}})
	}
	check()
	t := time.NewTicker(a.opt.RenewCheck)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			check()
		}
	}
}

// handleChannel opens a data channel the controller asked for and serves
// one operation or file session on it.
func (a *Agent) handleChannel(ctx context.Context, s *session, id, purpose string) {
	cs, err := s.client.Channel(ctx)
	if err != nil {
		s.send(&pb.AgentMessage{Msg: &pb.AgentMessage_ChannelFailed{ChannelFailed: &pb.ChannelFailed{ChannelId: id, Error: err.Error()}}})
		return
	}
	c := &channel{cs: cs}
	if err := cs.Send(&pb.Frame{ChannelId: id, Kind: agentrpc.FrameOpen}); err != nil {
		return
	}
	f, err := cs.Recv()
	if err != nil {
		return
	}
	if f.Kind != agentrpc.FrameOp {
		return
	}
	var env agentrpc.OpEnvelope
	if err := json.Unmarshal(f.Json, &env); err != nil {
		c.send(agentrpc.FrameRejected, agentrpc.MustJSON(agentrpc.Rejection{Reason: agentrpc.RejectInvalid, Message: "unreadable operation"}), nil)
		return
	}
	if env.Purpose == "" {
		env.Purpose = purpose
	}
	a.oplog.Write(OpEvent{Event: "received", OpID: env.OpID, Type: opType(&env), Controller: s.controller, JobID: env.JobID, OpHash: opHash(&env)})
	if rej := a.admit(&env); rej != nil {
		a.oplog.Write(OpEvent{Event: "rejected", OpID: env.OpID, Type: opType(&env), Controller: s.controller, JobID: env.JobID, Outcome: "rejected", Reason: rej.Reason})
		c.send(agentrpc.FrameRejected, agentrpc.MustJSON(rej), nil)
		cs.CloseSend()
		return
	}
	if err := c.send(agentrpc.FrameAccepted, nil, nil); err != nil {
		// The controller can't know we accepted: it treats the outcome as
		// unknown and asks with QueryOp. Running is still right: the
		// ledger has the id, so a resend is a duplicate.
	}
	if env.Purpose == agentrpc.PurposeFiles {
		a.serveFiles(&env, c, s.controller)
	} else {
		a.runOp(&env, c, s.controller)
	}
	cs.CloseSend()
}

// applyConfig applies a controller narrowing request if its state version
// is newer than the applied one. The result is always ceiling ∩ request.
func (a *Agent) applyConfig(cp *pb.ConfigPush) *pb.ConfigAck {
	ack := &pb.ConfigAck{Version: cp.Version}
	id, err := a.store.Identity()
	if err != nil {
		ack.Error = err.Error()
		return ack
	}
	v := cp.Version
	if v == nil || v.Domain != "agent-config" {
		ack.Error = "missing agent-config state version"
		return ack
	}
	if v.Epoch != id.ControllerEpoch {
		ack.Error = "controller epoch mismatch: not applied"
		return ack
	}
	doms, _ := a.store.Domains()
	if cur, ok := doms["agent-config"]; ok && v.Revision <= cur.Revision {
		ack.Applied = v.Revision == cur.Revision
		if !ack.Applied {
			ack.Error = fmt.Sprintf("stale configuration revision %d (applied %d)", v.Revision, cur.Revision)
		}
		ack.EffectiveJson = caps2json(a.Effective())
		return ack
	}
	h := sha256.Sum256(cp.RequestedCapsJson)
	if v.ContentHash != "" && v.ContentHash != "sha256:"+hex.EncodeToString(h[:]) {
		ack.Error = "configuration content does not match its hash"
		return ack
	}
	var req agentcaps.Caps
	if err := json.Unmarshal(cp.RequestedCapsJson, &req); err != nil {
		ack.Error = "unreadable configuration"
		return ack
	}
	if err := a.store.SaveRequested(cp.RequestedCapsJson); err != nil {
		ack.Error = err.Error()
		return ack
	}
	a.store.ApplyDomain(agentrpc.StateVersion{Domain: v.Domain, Epoch: v.Epoch, Revision: v.Revision, ContentHash: v.ContentHash})
	a.mu.Lock()
	a.effective = a.computeEffective()
	eff := a.effective
	a.mu.Unlock()
	a.oplog.Write(OpEvent{Event: "config", Summary: fmt.Sprintf("agent-config revision %d applied", v.Revision)})
	ack.Applied, ack.EffectiveJson = true, caps2json(eff)
	return ack
}

// --- controller endpoint migration (DESIGN §6.5) ---

func (a *Agent) testCandidate(ctx context.Context, s *session, c *pb.EndpointCandidate) {
	res := &pb.EndpointTestResult{Id: c.Id, Endpoint: c.Endpoint}
	ep, _ := a.store.Endpoints()
	ep.Candidate, ep.CandidateID, ep.CandidateOK = c.Endpoint, c.Id, false
	a.store.SaveEndpoints(ep)
	err := a.probeEndpoint(ctx, c.Endpoint)
	if err == nil {
		ep.CandidateOK = true
		a.store.SaveEndpoints(ep)
		res.Ok = true
	} else {
		res.Error = err.Error()
	}
	s.send(&pb.AgentMessage{Msg: &pb.AgentMessage_EndpointTest{EndpointTest: res}})
}

// probeEndpoint proves a candidate is a genuine controller of this fleet:
// trust refresh, then a fully verified mTLS call with our certificate.
func (a *Agent) probeEndpoint(ctx context.Context, ep string) error {
	if err := a.refreshTrust(ctx, ep); err != nil {
		return err
	}
	cert, _, err := a.operationalCert()
	if err != nil {
		return err
	}
	cfg, err := a.clientTLS(verifyFull, cert)
	if err != nil {
		return err
	}
	conn, err := dial(ep, cfg)
	if err != nil {
		return err
	}
	defer conn.Close()
	cctx, cancel := callCtx(ctx)
	defer cancel()
	_, err = pb.NewAgentClient(conn).GetTrust(cctx, &pb.GetTrustRequest{})
	return err
}

// commitCandidate makes a tested candidate primary; the previous primary
// stays as the first fallback. Untested or failed candidates are never
// committed, so a bad address can't strand the agent.
func (a *Agent) commitCandidate(s *session, c *pb.EndpointCommit) {
	ep, _ := a.store.Endpoints()
	if ep.Candidate != c.Endpoint || ep.CandidateID != c.Id || !ep.CandidateOK {
		a.log.Warn("ignoring endpoint commit for an untested candidate", "endpoint", c.Endpoint)
		return
	}
	old := ep.Primary
	ep.Primary = c.Endpoint
	fb := []string{old}
	for _, f := range ep.Fallbacks {
		if f != old && f != c.Endpoint {
			fb = append(fb, f)
		}
	}
	ep.Fallbacks, ep.Candidate, ep.CandidateID, ep.CandidateOK = fb, "", "", false
	if err := a.store.SaveEndpoints(ep); err != nil {
		a.log.Error("save endpoints", "err", err)
		return
	}
	a.log.Info("controller endpoint changed", "primary", ep.Primary, "previous", old)
	a.oplog.Write(OpEvent{Event: "config", Controller: s.controller, Summary: "controller endpoint now " + ep.Primary})
}
