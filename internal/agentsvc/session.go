package agentsvc

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"drool/internal/agentcaps"
	"drool/internal/agentrpc"
	"drool/internal/agentrpc/pb"
	"drool/internal/audit"
	"drool/internal/discovery"
	"drool/internal/ids"
	"drool/internal/inventory"
	"drool/internal/store/db"
)

// Heartbeat interval asked of agents; a session without traffic for three
// intervals is considered gone.
const heartbeatEvery = 15 * time.Second

// Session is one connected agent.
type Session struct {
	svc         *Service
	ident       db.AgentIdentity
	stream      pb.Agent_SessionServer
	sendMu      sync.Mutex
	hello       *pb.Hello
	addr        string
	connectedAt time.Time
	protocol    uint32
	ceiling     agentcaps.Caps

	mu          sync.Mutex
	effective   agentcaps.Caps
	epoch       string          // the controller epoch the agent answers to
	state       *pb.StateReport // its applied domains (hello, then each transition)
	firewall    []byte          // its latest firewall report (hello, then telemetry)
	transWait   map[string]chan *pb.EpochTransitionResult
	bootID      string
	lastSeen    time.Time
	lastTouch   time.Time
	quarantined bool
	pong        chan struct{}
	opWait      map[string]chan *pb.OpStatus
	removeWait  map[string]chan *pb.SelfUninstallResult
	removeReqs  map[string]*pb.SelfUninstall // removal requests sent in this session (to confirm an answer)
	done        chan struct{}
	closeReason string
}

func (se *Session) send(m *pb.ControllerMessage) error {
	se.sendMu.Lock()
	defer se.sendMu.Unlock()
	return se.stream.Send(m)
}

// Info is a session's state for the management router and the UI.
type Info struct {
	IdentityID   string
	HostID       string
	Address      string
	ConnectedAt  time.Time
	LastSeen     time.Time
	Protocol     uint32
	AgentVersion string
	BootID       string
	Quarantined  bool
	Effective    agentcaps.Caps
	Ceiling      agentcaps.Caps
}

func (se *Session) info() Info {
	se.mu.Lock()
	defer se.mu.Unlock()
	return Info{IdentityID: se.ident.ID, HostID: se.ident.HostID, Address: se.addr, ConnectedAt: se.connectedAt,
		LastSeen: se.lastSeen, Protocol: se.protocol, AgentVersion: se.hello.Info.GetAgentVersion(), BootID: se.bootID,
		Quarantined: se.quarantined, Effective: se.effective, Ceiling: se.ceiling}
}

func (se *Session) alive() bool {
	select {
	case <-se.done:
		return false
	default:
	}
	se.mu.Lock()
	recent := time.Since(se.lastSeen) < 3*heartbeatEvery
	se.mu.Unlock()
	return recent
}

// probe asks the session for a heartbeat and reports whether one arrives.
func (se *Session) probe(wait time.Duration) bool {
	se.mu.Lock()
	se.pong = make(chan struct{}, 1)
	pong := se.pong
	se.mu.Unlock()
	if err := se.send(&pb.ControllerMessage{Msg: &pb.ControllerMessage_Ping{Ping: &pb.Ping{Nonce: ids.New("png")}}}); err != nil {
		return false
	}
	select {
	case <-pong:
		return true
	case <-se.done:
		return false
	case <-time.After(wait):
		return false
	}
}

func (s *Service) liveSessions(identityID string) []*Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Session
	for _, se := range s.sessions {
		if se.ident.ID == identityID {
			out = append(out, se)
		}
	}
	return out
}

// SessionFor returns the usable session of a host's agent, if any.
func (s *Service) SessionFor(hostID string) (*Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var found *Session
	n := 0
	for _, se := range s.sessions {
		if se.ident.HostID == hostID {
			found = se
			n++
		}
	}
	return found, n == 1
}

// HostSession returns session information for a host (for the UI and the
// router). ok is false when no agent session exists.
func (s *Service) HostSession(hostID string) (Info, int, bool) {
	s.mu.Lock()
	var list []*Session
	for _, se := range s.sessions {
		if se.ident.HostID == hostID {
			list = append(list, se)
		}
	}
	s.mu.Unlock()
	if len(list) == 0 {
		return Info{}, 0, false
	}
	return list[0].info(), len(list), true
}

// HostSessions lists all sessions of a host (identity conflicts: two).
func (s *Service) HostSessions(hostID string) []Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Info
	for _, se := range s.sessions {
		if se.ident.HostID == hostID {
			out = append(out, se.info())
		}
	}
	return out
}

// Session implements pb.AgentServer: one agent's session.
func (s *Service) Session(stream pb.Agent_SessionServer) error {
	ctx := stream.Context()
	ident, leaf, err := s.authAgent(ctx, false)
	if err != nil {
		if ident.ID != "" && ident.Status == "revoked" {
			stream.Send(&pb.ControllerMessage{Msg: &pb.ControllerMessage_Disconnect{Disconnect: &pb.Disconnect{Code: "revoked", Reason: status.Convert(err).Message()}}})
		} else {
			s.refused(ctx, stream)
		}
		return err
	}
	_, addr := peerInfo(ctx)
	first, err := recvTimeout(stream, 30*time.Second)
	if err != nil {
		return err
	}
	hello := first.GetHello()
	if hello == nil || hello.Info == nil {
		return status.Error(codes.InvalidArgument, "the first message must be a hello")
	}
	now := s.now()
	se := &Session{svc: s, ident: ident, stream: stream, hello: hello, addr: hostOnly(addr), connectedAt: now, lastSeen: now,
		bootID: hello.Info.BootId, opWait: map[string]chan *pb.OpStatus{}, done: make(chan struct{}),
		epoch: hello.ControllerEpoch, state: hello.State, firewall: hello.FirewallJson}
	json.Unmarshal(hello.CeilingJson, &se.ceiling)
	json.Unmarshal(hello.EffectiveJson, &se.effective)

	// Protocol negotiation.
	agentRange := agentrpc.Range{Min: hello.Info.ProtocolMin, Max: hello.Info.ProtocolMax}
	se.protocol = agentrpc.Negotiate(s.ControllerRange, agentRange)
	compatible, reason := se.protocol != 0, ""
	if !compatible {
		reason = agentrpc.Incompatibility(s.ControllerRange, agentRange)
	}
	endpoints, _ := json.Marshal(hello.Endpoints)
	if rh := s.recoveryHooks(); rh != nil {
		if prev, cur := ident.MachineID, hello.Info.MachineId; prev != "" && cur != "" && prev != cur {
			rh.MachineChanged(ident.HostID, prev, cur)
		}
		if hello.Revocation != nil {
			rh.Revoked(ident.HostID, hello.Revocation)
		}
	}
	if err := s.st.W.UpdateIdentityHello(ctx, db.UpdateIdentityHelloParams{AgentVersion: hello.Info.AgentVersion, Protocol: int64(se.protocol),
		ProtocolMin: int64(agentRange.Min), ProtocolMax: int64(agentRange.Max), Compatible: boolInt(compatible), CompatReason: reason,
		Hostname: hello.Info.Hostname, MachineID: hello.Info.MachineId, Os: hello.Info.Os, Arch: hello.Info.Arch,
		CeilingJson: string(nonEmpty(hello.CeilingJson)), EffectiveJson: string(nonEmpty(hello.EffectiveJson)),
		ManifestVersion: int64(hello.ManifestVersion), EpochReported: hello.ControllerEpoch, EndpointsJson: string(endpoints),
		BootID: hello.Info.BootId, LastSeenAt: sql.NullInt64{Int64: now.UnixMilli(), Valid: true}, LastAddress: se.addr, ID: ident.ID}); err != nil {
		return status.Error(codes.Internal, "could not record the session")
	}
	features, _ := json.Marshal(nonNil(hello.Features))
	s.st.W.SetIdentityFeatures(ctx, db.SetIdentityFeaturesParams{FeaturesJson: string(features), ID: ident.ID})
	if !compatible {
		s.log.Warn("agent protocol incompatible", "identity", ident.ID, "agent_range", agentRange, "reason", reason)
		stream.Send(&pb.ControllerMessage{Msg: &pb.ControllerMessage_HelloAck{HelloAck: &pb.HelloAck{Compatible: false, Reason: reason,
			ControllerEpoch: s.epoch, ServerTimeUnix: now.Unix()}}})
		s.st.W.SetIdentityConnected(ctx, db.SetIdentityConnectedParams{Connected: 0, LastSeenAt: sql.NullInt64{Int64: now.UnixMilli(), Valid: true}, ID: ident.ID})
		s.hostChanged(ident.HostID)
		return nil
	}

	// Identity conflicts (DESIGN §6.3): a second live session of the same
	// identity means a cloned machine. Quarantine, and let an
	// administrator decide; never pick a winner automatically.
	for _, old := range s.liveSessions(ident.ID) {
		if old.probe(10 * time.Second) {
			s.quarantine(ctx, ident, old, se)
		} else {
			old.close("replaced by a new session of the same agent")
		}
	}
	if fresh, err := s.st.R.GetAgentIdentity(ctx, ident.ID); err == nil {
		se.ident = fresh
		se.quarantined = fresh.Status == "quarantined"
	}

	s.mu.Lock()
	key := ident.ID + "/" + hello.SessionNonce + "/" + ids.New("s")
	s.sessions[key] = se
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.sessions, key)
		remaining := 0
		for _, o := range s.sessions {
			if o.ident.ID == ident.ID {
				remaining++
			}
		}
		s.mu.Unlock()
		close(se.done)
		if remaining == 0 {
			bg := context.WithoutCancel(ctx)
			s.st.W.SetIdentityConnected(bg, db.SetIdentityConnectedParams{Connected: 0, LastSeenAt: sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}, ID: ident.ID})
			s.agentOnlyStatus(bg, ident.HostID, false)
		}
		s.log.Info("agent session ended", "identity", ident.ID, "host", ident.HostID, "reason", se.closeReason)
		s.hostChanged(ident.HostID)
	}()

	s.compareState(ctx, ident.HostID, hello)
	if hello.Update != nil {
		s.onUpdateReport(ctx, ident, hello.Update)
	}
	ack := &pb.HelloAck{Protocol: se.protocol, Compatible: true, ControllerEpoch: s.epoch, ServerTimeUnix: now.Unix(),
		HeartbeatSeconds: uint32(heartbeatEvery / time.Second), Quarantined: se.quarantined, HostId: ident.HostID}
	if h, err := s.st.R.GetHost(ctx, ident.HostID); err == nil {
		ack.HostName = h.Name
	}
	if err := se.send(&pb.ControllerMessage{Msg: &pb.ControllerMessage_HelloAck{HelloAck: ack}}); err != nil {
		return err
	}
	s.log.Info("agent connected", "identity", ident.ID, "host", ident.HostID, "address", se.addr,
		"version", hello.Info.AgentVersion, "protocol", se.protocol, "cert_not_after", leaf.NotAfter)
	// The newest trust manifest before anything else (DESIGN §4.3.1).
	if sm, v := s.pki.Manifest(); v > hello.ManifestVersion {
		se.send(&pb.ControllerMessage{Msg: &pb.ControllerMessage_Manifest{Manifest: &pb.SignedManifest{Payload: sm.Payload, Signature: sm.Signature}}})
	}
	s.pushConfigIfNeeded(ctx, se, hello)
	if fh := s.firewallHooks(); fh != nil {
		go fh.OnSession(ident.HostID, hello.FirewallJson, hello.Features, se.connectedAt)
	}
	if mh := s.monitorHooks(); mh != nil {
		go mh.OnSession(ident.HostID, hello.Features, hello.EffectiveJson)
	}
	s.agentOnlyStatus(ctx, ident.HostID, true)
	s.hostChanged(ident.HostID)
	go s.offerPendingUpdate(context.WithoutCancel(ctx), se)
	if rh := s.recoveryHooks(); rh != nil {
		rh.OnSession(ident.HostID)
	}

	for {
		m, err := stream.Recv()
		if err != nil {
			if se.closeReason == "" {
				se.closeReason = "connection closed"
				if !errors.Is(err, io.EOF) {
					se.closeReason = err.Error()
				}
			}
			return nil
		}
		se.mu.Lock()
		se.lastSeen = s.now()
		se.mu.Unlock()
		switch msg := m.Msg.(type) {
		case *pb.AgentMessage_Heartbeat:
			se.mu.Lock()
			se.bootID = msg.Heartbeat.BootId
			if se.pong != nil {
				select {
				case se.pong <- struct{}{}:
				default:
				}
			}
			touch := time.Since(se.lastTouch) > time.Minute
			if touch {
				se.lastTouch = time.Now()
			}
			se.mu.Unlock()
			if touch {
				s.st.W.TouchIdentity(ctx, db.TouchIdentityParams{LastSeenAt: sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}, BootID: msg.Heartbeat.BootId, ID: ident.ID})
			}
		case *pb.AgentMessage_Telemetry:
			s.ingestTelemetry(ctx, se, msg.Telemetry)
		case *pb.AgentMessage_Renew:
			s.renew(ctx, se)
		case *pb.AgentMessage_OpStatus:
			se.mu.Lock()
			w := se.opWait[msg.OpStatus.OpId]
			se.mu.Unlock()
			if w != nil {
				select {
				case w <- msg.OpStatus:
				default:
				}
			}
		case *pb.AgentMessage_EndpointTest:
			s.onEndpointTest(ctx, se, msg.EndpointTest)
		case *pb.AgentMessage_Update:
			s.onUpdateReport(ctx, se.ident, msg.Update)
		case *pb.AgentMessage_ManifestAck:
			if msg.ManifestAck.Error == "" {
				s.st.W.SetIdentityManifestVersion(ctx, db.SetIdentityManifestVersionParams{ManifestVersion: int64(msg.ManifestAck.Version), ID: ident.ID})
			} else {
				s.log.Warn("agent refused the trust manifest", "identity", ident.ID, "err", msg.ManifestAck.Error)
			}
		case *pb.AgentMessage_ChannelFailed:
			s.failChannel(msg.ChannelFailed.ChannelId, msg.ChannelFailed.Error)
		case *pb.AgentMessage_ConfigAck:
			s.onConfigAck(ctx, se, msg.ConfigAck)
		case *pb.AgentMessage_EpochTransition:
			se.onTransitionResult(msg.EpochTransition)
		case *pb.AgentMessage_SelfUninstall:
			se.onSelfUninstallResult(msg.SelfUninstall)
		}
	}
}

func recvTimeout(stream pb.Agent_SessionServer, d time.Duration) (*pb.AgentMessage, error) {
	type res struct {
		m   *pb.AgentMessage
		err error
	}
	ch := make(chan res, 1)
	go func() {
		m, err := stream.Recv()
		ch <- res{m, err}
	}()
	select {
	case r := <-ch:
		return r.m, r.err
	case <-time.After(d):
		return nil, status.Error(codes.DeadlineExceeded, "no hello received")
	}
}

func (se *Session) close(reason string) {
	se.closeReason = reason
	se.send(&pb.ControllerMessage{Msg: &pb.ControllerMessage_Disconnect{Disconnect: &pb.Disconnect{Code: "replaced", Reason: reason}}})
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// agentOnlyStatus keeps the host status of hosts without SSH in step with
// the agent session (hybrid hosts keep their SSH-derived status).
func (s *Service) agentOnlyStatus(ctx context.Context, hostID string, connected bool) {
	h, err := s.st.R.GetHost(ctx, hostID)
	if err != nil || h.SshConfigured != 0 {
		return
	}
	st, detail := inventory.StatusReady, "managed by the DROOL agent"
	if !connected {
		st, detail = inventory.StatusUnreachable, "the DROOL agent is not connected"
	}
	if h.Status != st {
		s.st.W.SetHostStatusAgent(ctx, db.SetHostStatusAgentParams{Status: st, StatusDetail: detail, UpdatedAt: s.now().UnixMilli(), ID: hostID})
	}
}

// quarantine records an identity conflict and blocks management through
// that identity until an administrator resolves it.
func (s *Service) quarantine(ctx context.Context, ident db.AgentIdentity, a, b *Session) {
	type seen struct {
		Address     string    `json:"address"`
		ConnectedAt time.Time `json:"connected_at"`
		Hostname    string    `json:"hostname"`
		MachineID   string    `json:"machine_id"`
		BootID      string    `json:"boot_id"`
		Version     string    `json:"agent_version"`
	}
	conf := map[string]any{"detected_at": s.now().UTC(), "sessions": []seen{
		{a.addr, a.connectedAt.UTC(), a.hello.Info.Hostname, a.hello.Info.MachineId, a.bootID, a.hello.Info.AgentVersion},
		{b.addr, b.connectedAt.UTC(), b.hello.Info.Hostname, b.hello.Info.MachineId, b.bootID, b.hello.Info.AgentVersion},
	}}
	js, _ := json.Marshal(conf)
	if ident.Status == "active" {
		s.st.InTx(ctx, func(q *db.Queries) error {
			if err := q.QuarantineIdentity(ctx, db.QuarantineIdentityParams{QuarantinedAt: sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}, ConflictJson: string(js), ID: ident.ID}); err != nil {
				return err
			}
			_, err := s.audit.RecordTx(ctx, q, audit.Event{Action: "agent.conflict", TargetType: "host", TargetID: ident.HostID, Result: audit.Failure,
				Actor: &audit.System, Details: map[string]any{"identity_id": ident.ID, "conflict": json.RawMessage(js)}})
			return err
		})
	}
	for _, se := range []*Session{a, b} {
		se.mu.Lock()
		se.quarantined = true
		se.mu.Unlock()
	}
	s.log.Error("agent identity conflict: two machines present the same identity; management quarantined",
		"identity", ident.ID, "address_a", a.addr, "address_b", b.addr)
}

// renew issues a new certificate over the same identity key.
func (s *Service) renew(ctx context.Context, se *Session) {
	resp := &pb.RenewResponse{}
	ident, err := s.st.R.GetAgentIdentity(ctx, se.ident.ID)
	switch {
	case err != nil:
		resp.Error = "identity lookup failed"
	case ident.Status != "active":
		resp.Error = "identity is " + ident.Status
	default:
		chain, cert, err := s.pki.IssueAgent(ident.ID, ident.PublicKey, s.cfg.CertLifetime)
		if err != nil {
			resp.Error = "issuance failed"
			break
		}
		err = s.st.InTx(ctx, func(q *db.Queries) error {
			if err := q.UpdateIdentityCert(ctx, db.UpdateIdentityCertParams{CertSerial: cert.SerialNumber.Text(16), CertNotAfter: cert.NotAfter.UnixMilli(),
				CertIssuedAt: s.now().UnixMilli(), ID: ident.ID}); err != nil {
				return err
			}
			_, err := s.audit.RecordTx(ctx, q, audit.Event{Action: "agent.renew", TargetType: "host", TargetID: ident.HostID, Actor: agentActor(ident),
				Details: map[string]any{"identity_id": ident.ID, "not_after": cert.NotAfter.UTC()}})
			return err
		})
		if err != nil {
			resp.Error = "could not record the renewal"
			break
		}
		resp.CertChain = chain
	}
	se.send(&pb.ControllerMessage{Msg: &pb.ControllerMessage_Renew{Renew: resp}})
}

// ingestTelemetry stores agent telemetry with the same parsers and
// storage as SSH polling, so stale-data rules stay the same.
func (s *Service) ingestTelemetry(ctx context.Context, se *Session, t *pb.Telemetry) {
	hostID := se.ident.HostID
	if MonitorKinds[t.Kind] {
		if mh := s.monitorHooks(); mh != nil {
			mh.OnTelemetry(hostID, t.Kind, t.Output)
		}
		return
	}
	if t.Kind == "firewall" {
		se.mu.Lock()
		se.firewall = t.Output
		se.mu.Unlock()
	}
	if t.Kind == "firewall" || t.Kind == "flows" || t.Kind == "netflows" {
		if fh := s.firewallHooks(); fh != nil {
			fh.OnTelemetry(hostID, t.Kind, t.Output)
		}
		return
	}
	err := s.st.InTx(ctx, func(q *db.Queries) error {
		switch t.Kind {
		case "facts":
			f, _ := discovery.ParseDiscover(string(t.Output))
			return s.inv.StoreFactsSourceTx(ctx, q, hostID, &f, "agent")
		case "status":
			st := discovery.ParseStatus(string(t.Output))
			_, err := s.inv.RecordStatusTx(ctx, q, hostID, &st, nil)
			return err
		case "updates":
			var u discovery.Updates
			if t.PackageManager == "apt" {
				u = discovery.ParseUpdatesAPT(string(t.Output))
			} else {
				u = discovery.ParseUpdatesDNF(string(t.Output))
			}
			return s.inv.RecordUpdatesTx(ctx, q, hostID, &u)
		}
		return nil
	})
	if err != nil {
		s.log.Warn("store agent telemetry", "host", hostID, "kind", t.Kind, "err", err)
		return
	}
	if t.Kind == "facts" {
		s.hostChanged(hostID)
	}
}

// --- state versions (DESIGN §9) ---

// compareState records what the agent says it applied and flags hosts
// that are ahead of the controller (e.g. after restoring an old backup) or
// belong to another epoch. Flagged domains are never pushed to.
func (s *Service) compareState(ctx context.Context, hostID string, h *pb.Hello) {
	now := s.now().UnixMilli()
	for _, d := range h.State.GetDomains() {
		st := "ok"
		cv, err := s.st.R.GetStateVersion(ctx, db.GetStateVersionParams{HostID: hostID, Domain: d.Domain})
		switch {
		case d.Epoch != s.epoch:
			st = "epoch_mismatch"
		case errors.Is(err, sql.ErrNoRows) && d.Revision > 0:
			st = "host_ahead"
		case err == nil && int64(d.Revision) > cv.Revision:
			st = "host_ahead"
		}
		if errors.Is(err, sql.ErrNoRows) {
			s.st.W.InsertReportedOnlyState(ctx, db.InsertReportedOnlyStateParams{HostID: hostID, Domain: d.Domain, Epoch: s.epoch, UpdatedAt: now,
				ReportedEpoch: d.Epoch, ReportedRevision: int64(d.Revision), ReportedHash: d.ContentHash, ReportedAt: sql.NullInt64{Int64: now, Valid: true}, Status: st})
			continue
		}
		s.st.W.ReportStateVersion(ctx, db.ReportStateVersionParams{ReportedEpoch: d.Epoch, ReportedRevision: int64(d.Revision), ReportedHash: d.ContentHash,
			ReportedAt: sql.NullInt64{Int64: now, Valid: true}, Status: st, HostID: hostID, Domain: d.Domain})
		if st != "ok" {
			s.log.Warn("state version conflict", "host", hostID, "domain", d.Domain, "status", st,
				"host_revision", d.Revision, "controller_revision", cv.Revision)
		}
	}
	if h.ControllerEpoch != "" && h.ControllerEpoch != s.epoch {
		s.log.Error("agent belongs to a different controller epoch; it will refuse commands until reconciled",
			"host", hostID, "agent_epoch", h.ControllerEpoch, "controller_epoch", s.epoch)
	}
}

// nextRevision reserves the next controller-owned revision of a domain.
// It refuses domains flagged as ahead or from another epoch: the controller
// never overwrites newer or foreign state without reconciliation.
func (s *Service) nextRevision(ctx context.Context, hostID, domain, contentHash string) (*agentrpc.StateVersion, error) {
	var sv *agentrpc.StateVersion
	err := s.st.InTx(ctx, func(q *db.Queries) error {
		cur, err := q.GetStateVersion(ctx, db.GetStateVersionParams{HostID: hostID, Domain: domain})
		rev := int64(0)
		if err == nil {
			if cur.Status != "ok" {
				return &ReconcileError{Domain: domain, Status: cur.Status, HostRevision: cur.ReportedRevision, ControllerRevision: cur.Revision}
			}
			rev = cur.Revision
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		sv = &agentrpc.StateVersion{Domain: domain, Epoch: s.epoch, Revision: uint64(rev + 1), ContentHash: contentHash}
		return q.UpsertStateVersion(ctx, db.UpsertStateVersionParams{HostID: hostID, Domain: domain, Epoch: s.epoch,
			Revision: rev + 1, ContentHash: contentHash, UpdatedAt: s.now().UnixMilli()})
	})
	return sv, err
}

// ReconcileError: the host's state is ahead of (or foreign to) the
// controller's; nothing is pushed until an administrator reconciles.
type ReconcileError struct {
	Domain                           string
	Status                           string
	HostRevision, ControllerRevision int64
}

func (e *ReconcileError) Error() string {
	if e.Status == "epoch_mismatch" {
		return fmt.Sprintf("reconciliation required: the host's %s state belongs to another controller epoch", e.Domain)
	}
	return fmt.Sprintf("reconciliation required: the host has %s revision %d, ahead of this controller's %d (restored from an older backup?)",
		e.Domain, e.HostRevision, e.ControllerRevision)
}

// AdoptHostState resolves a host_ahead domain the minimal way M7 offers:
// the controller adopts the host's revision as its own (same epoch), so
// the next change is newer than what the host has. Full reconciliation
// (diffs, choosing a side, new epochs) comes with the Beta.
func (s *Service) AdoptHostState(ctx context.Context, hostID, domain string) error {
	return s.st.InTx(ctx, func(q *db.Queries) error {
		cur, err := q.GetStateVersion(ctx, db.GetStateVersionParams{HostID: hostID, Domain: domain})
		if err != nil {
			return err
		}
		if cur.Status != "host_ahead" {
			return fmt.Errorf("%s is %s, not ahead of the controller", domain, cur.Status)
		}
		if err := q.UpsertStateVersion(ctx, db.UpsertStateVersionParams{HostID: hostID, Domain: domain, Epoch: cur.ReportedEpoch,
			Revision: cur.ReportedRevision, ContentHash: cur.ReportedHash, UpdatedAt: s.now().UnixMilli()}); err != nil {
			return err
		}
		return q.ReportStateVersion(ctx, db.ReportStateVersionParams{ReportedEpoch: cur.ReportedEpoch, ReportedRevision: cur.ReportedRevision,
			ReportedHash: cur.ReportedHash, ReportedAt: cur.ReportedAt, Status: "ok", HostID: hostID, Domain: domain})
	})
}

// --- controller narrowing (agent-config domain) ---

func capsHash(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

// SetRequestedCaps stores the controller's narrowing of a host's agent
// ceiling (a new agent-config revision) and pushes it to the agent.
func (s *Service) SetRequestedCaps(ctx context.Context, hostID string, req *agentcaps.Caps) error {
	var b []byte
	if req != nil {
		r := *req
		if err := r.Normalize(); err != nil {
			return err
		}
		b, _ = json.Marshal(r)
	}
	payload := b
	if payload == nil {
		payload, _ = json.Marshal(agentcaps.All())
	}
	if _, err := s.nextRevision(ctx, hostID, "agent-config", capsHash(payload)); err != nil {
		return err
	}
	if err := s.st.W.SetHostRequestedCaps(ctx, db.SetHostRequestedCapsParams{RequestedCapsJson: string(b), UpdatedAt: s.now().UnixMilli(), ID: hostID}); err != nil {
		return err
	}
	if se, ok := s.SessionFor(hostID); ok {
		s.pushConfig(ctx, se)
	}
	return nil
}

func (s *Service) pushConfig(ctx context.Context, se *Session) {
	h, err := s.st.R.GetHost(ctx, se.ident.HostID)
	if err != nil {
		return
	}
	cv, err := s.st.R.GetStateVersion(ctx, db.GetStateVersionParams{HostID: h.ID, Domain: "agent-config"})
	if err != nil || cv.Status != "ok" {
		return
	}
	payload := []byte(h.RequestedCapsJson)
	if len(payload) == 0 {
		payload, _ = json.Marshal(agentcaps.All())
	}
	se.send(&pb.ControllerMessage{Msg: &pb.ControllerMessage_Config{Config: &pb.ConfigPush{RequestedCapsJson: payload,
		Version: &pb.DomainState{Domain: "agent-config", Epoch: cv.Epoch, Revision: uint64(cv.Revision), ContentHash: cv.ContentHash}}}})
}

func (s *Service) pushConfigIfNeeded(ctx context.Context, se *Session, h *pb.Hello) {
	s.pushConfigIfBehind(ctx, se, h.State)
}

// pushConfigIfBehind pushes the agent-config domain when the agent's
// applied revision is older than the controller's.
func (s *Service) pushConfigIfBehind(ctx context.Context, se *Session, state *pb.StateReport) {
	if se.agentEpoch() != "" && se.agentEpoch() != s.epoch {
		return
	}
	cv, err := s.st.R.GetStateVersion(ctx, db.GetStateVersionParams{HostID: se.ident.HostID, Domain: "agent-config"})
	if err != nil || cv.Status != "ok" {
		return
	}
	for _, d := range state.GetDomains() {
		if d.Domain == "agent-config" && int64(d.Revision) >= cv.Revision {
			return
		}
	}
	s.pushConfig(ctx, se)
}

func (s *Service) onConfigAck(ctx context.Context, se *Session, a *pb.ConfigAck) {
	if a.Error != "" {
		s.log.Warn("agent did not apply its configuration", "host", se.ident.HostID, "err", a.Error)
	}
	if len(a.EffectiveJson) > 0 {
		var eff agentcaps.Caps
		if json.Unmarshal(a.EffectiveJson, &eff) == nil {
			se.mu.Lock()
			se.effective = eff
			se.mu.Unlock()
			s.st.W.SetIdentityEffective(ctx, db.SetIdentityEffectiveParams{EffectiveJson: string(a.EffectiveJson), ID: se.ident.ID})
		}
	}
	// Only a confirmation in this controller's epoch counts: a stale one
	// (from before a restore) never advances the controller's state.
	if a.Applied && a.Version != nil && a.Version.Epoch == s.epoch {
		s.st.W.ReportStateVersion(ctx, db.ReportStateVersionParams{ReportedEpoch: a.Version.Epoch, ReportedRevision: int64(a.Version.Revision),
			ReportedHash: a.Version.ContentHash, ReportedAt: sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}, Status: "ok",
			HostID: se.ident.HostID, Domain: "agent-config"})
	}
	s.hostChanged(se.ident.HostID)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
