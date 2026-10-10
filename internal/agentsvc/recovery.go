package agentsvc

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"drool/internal/agentpki"
	"drool/internal/agentrpc/pb"
	"drool/internal/model"
)

// Restore reconciliation (DESIGN §10.3, ARCHITECTURE-ALPHA §33.1). A
// restored controller has a new epoch; its agents still answer to the
// epoch of the backup and refuse every command until the controller has
// compared their state with its own and moved them (EpochTransition).
// internal/recovery decides; this file carries the messages.

// RecoveryHooks connect sessions to restore reconciliation.
type RecoveryHooks interface {
	// MachineChanged: an identity connected from a machine other than the
	// one DROOL recorded (called before the new machine id is stored).
	MachineChanged(hostID, previous, current string)
	// Revoked: the agent says a controller revoked its identity, which
	// this one doesn't know (a revocation made after the backup it was
	// restored from).
	Revoked(hostID string, r *pb.Revocation)
	// OnSession runs after a session's hello was acknowledged.
	OnSession(hostID string)
}

// SetRecoveryHooks installs restore reconciliation's hooks.
func (s *Service) SetRecoveryHooks(h RecoveryHooks) {
	s.mu.Lock()
	s.hooks.Recovery = h
	s.mu.Unlock()
}

func (s *Service) recoveryHooks() RecoveryHooks {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hooks.Recovery
}

// AgentView is what reconciliation compares: the agent's own report from
// its current session.
type AgentView struct {
	IdentityID      string
	IdentityStatus  string
	AgentVersion    string
	Sessions        int
	Compatible      bool
	Features        []string
	Epoch           string // the controller epoch the agent answers to
	Domains         []*pb.DomainState
	FirewallJSON    []byte
	ManifestVersion uint64
}

// HasFeature reports whether the agent announced a feature.
func (v AgentView) HasFeature(f string) bool {
	for _, x := range v.Features {
		if x == f {
			return true
		}
	}
	return false
}

// AgentView returns the host's agent as its session reported it; ok is
// false when no session is connected.
func (s *Service) AgentView(hostID string) (AgentView, bool) {
	s.mu.Lock()
	var list []*Session
	for _, se := range s.sessions {
		if se.ident.HostID == hostID {
			list = append(list, se)
		}
	}
	s.mu.Unlock()
	if len(list) == 0 {
		return AgentView{}, false
	}
	se := list[0]
	se.mu.Lock()
	defer se.mu.Unlock()
	st := se.ident.Status
	if se.quarantined {
		st = "quarantined"
	}
	v := AgentView{IdentityID: se.ident.ID, IdentityStatus: st, AgentVersion: se.hello.Info.GetAgentVersion(), Sessions: len(list),
		Compatible: se.protocol != 0, Features: se.hello.Features, Epoch: se.epoch, FirewallJSON: se.firewall,
		ManifestVersion: se.hello.ManifestVersion}
	if se.state != nil {
		v.Domains = se.state.Domains
	}
	return v, true
}

// Transition sends an epoch transition to the host's agent and waits for
// its answer. The agent answers the same transition again with the same
// result, so a lost answer is settled by sending it again.
func (s *Service) Transition(ctx context.Context, hostID string, t *pb.EpochTransition) (*pb.EpochTransitionResult, error) {
	se, single := s.SessionFor(hostID)
	if se == nil || !single {
		return nil, errors.New("the Shepherd is not connected (or more than one machine presents its identity)")
	}
	ch := make(chan *pb.EpochTransitionResult, 1)
	se.mu.Lock()
	if se.transWait == nil {
		se.transWait = map[string]chan *pb.EpochTransitionResult{}
	}
	se.transWait[t.TransitionId] = ch
	se.mu.Unlock()
	defer func() {
		se.mu.Lock()
		delete(se.transWait, t.TransitionId)
		se.mu.Unlock()
	}()
	if err := se.send(&pb.ControllerMessage{Msg: &pb.ControllerMessage_EpochTransition{EpochTransition: t}}); err != nil {
		return nil, fmt.Errorf("send: %w", err)
	}
	select {
	case r := <-ch:
		se.mu.Lock()
		if r.State != nil {
			se.state = r.State // what the agent has now, for the next comparison
		}
		if r.Ok {
			se.epoch = r.ControllerEpoch
		}
		se.mu.Unlock()
		if r.Ok {
			s.st.Writer.ExecContext(context.WithoutCancel(ctx), `UPDATE agent_identities SET epoch_reported = ? WHERE id = ?`, r.ControllerEpoch, se.ident.ID)
		}
		return r, nil
	case <-se.done:
		return nil, errors.New("the Shepherd disconnected before answering")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// PushConfigIfBehind sends the agent-config domain when the host's agent
// applied an older revision than the controller's: after reconciliation,
// configuration DROOL changed that the agent never got (its own intent,
// sent in the new epoch).
func (s *Service) PushConfigIfBehind(ctx context.Context, hostID string) {
	se, single := s.SessionFor(hostID)
	if se == nil || !single {
		return
	}
	se.mu.Lock()
	st := se.state
	se.mu.Unlock()
	s.pushConfigIfBehind(ctx, se, st)
}

func (se *Session) onTransitionResult(r *pb.EpochTransitionResult) {
	se.mu.Lock()
	ch := se.transWait[r.TransitionId]
	se.mu.Unlock()
	if ch != nil {
		select {
		case ch <- r:
		default:
		}
	}
}

func (se *Session) agentEpoch() string {
	se.mu.Lock()
	defer se.mu.Unlock()
	return se.epoch
}

// epochRefusal: operations are never sent to an agent that still answers
// to another controller epoch; it would refuse them, and that refusal is
// not an ordinary command failure.
func (s *Service) epochRefusal(se *Session) error {
	ep := se.agentEpoch()
	if ep == "" || ep == s.epoch {
		return nil
	}
	return &model.OpError{Class: model.ClassConfiguration,
		Message: "this host's Shepherd still answers to the controller epoch of the backup this controller was restored from; it is moved to the new epoch once DROOL has compared its state",
		Hint:    "Nothing was sent to the host. Settings → System → Restore shows what is left to reconcile."}
}

// refused handles an agent whose certificate chains to this controller's
// root but that it can't accept as is: an identity it has no record of
// (enrolled after the backup this controller was restored from) is listed
// and told so; a known identity whose certificate comes from an
// intermediate this controller doesn't trust (rotated after the backup)
// is asked to renew it through certificate recovery, which proves
// possession of the identity key on record (DESIGN §4.3.1).
func (s *Service) refused(ctx context.Context, stream pb.Agent_SessionServer) {
	chain, addr := peerInfo(ctx)
	leaf, id, err := s.pki.VerifyAgentForRecovery(chain)
	if err != nil || id == "" {
		return
	}
	ident, err := s.st.R.GetAgentIdentity(ctx, id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		hello := &pb.Hello{}
		if m, err := recvTimeout(stream, 10*time.Second); err == nil && m.GetHello() != nil {
			hello = m.GetHello()
		}
		fp := ""
		if pub, ok := leaf.PublicKey.(ed25519.PublicKey); ok {
			fp = agentpki.KeyFingerprint(pub)
		}
		s.recordUnknown(ctx, id, hello, hostOnly(addr), leaf.NotAfter, fp)
		stream.Send(&pb.ControllerMessage{Msg: &pb.ControllerMessage_Disconnect{Disconnect: &pb.Disconnect{Code: "unknown",
			Reason: "this controller has no record of this agent's identity (enrolled after the backup it was restored from?); enroll the agent again"}}})
	case err == nil && ident.Status == "active":
		if _, _, verr := s.pki.VerifyAgent(chain, true); verr != nil {
			stream.Send(&pb.ControllerMessage{Msg: &pb.ControllerMessage_Disconnect{Disconnect: &pb.Disconnect{Code: "recover",
				Reason: "this controller doesn't trust the intermediate CA that issued this agent's certificate (rotated after the backup it was restored from?); renew it through certificate recovery"}}})
		}
	}
}

// recordUnknown lists an unknown identity, at most once a minute each.
func (s *Service) recordUnknown(ctx context.Context, id string, h *pb.Hello, addr string, notAfter time.Time, fp string) {
	now := s.now().UnixMilli()
	s.st.Writer.ExecContext(ctx, `INSERT INTO unknown_agents (identity_id, hostname, machine_id, agent_version, address, key_fingerprint,
		cert_not_after, first_seen_at, last_seen_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (identity_id) DO UPDATE SET hostname = excluded.hostname, machine_id = excluded.machine_id,
		agent_version = excluded.agent_version, address = excluded.address, cert_not_after = excluded.cert_not_after,
		last_seen_at = excluded.last_seen_at, attempts = attempts + 1,
		dismissed_at = CASE WHEN unknown_agents.dismissed_at < excluded.last_seen_at - 86400000 THEN NULL ELSE unknown_agents.dismissed_at END
		WHERE unknown_agents.last_seen_at < excluded.last_seen_at - 60000`,
		id, h.Info.GetHostname(), h.Info.GetMachineId(), h.Info.GetAgentVersion(), addr, fp, notAfter.UnixMilli(), now, now)
	s.log.Warn("an agent with an identity this controller has no record of tried to connect", "identity", id,
		"hostname", h.Info.GetHostname(), "address", addr)
}

// UnknownAgent is a refused identity (Shepherds page).
type UnknownAgent struct {
	IdentityID     string    `json:"identity_id"`
	Hostname       string    `json:"hostname"`
	MachineID      string    `json:"machine_id"`
	AgentVersion   string    `json:"agent_version"`
	Address        string    `json:"address"`
	KeyFingerprint string    `json:"key_fingerprint"`
	FirstSeen      time.Time `json:"first_seen_at"`
	LastSeen       time.Time `json:"last_seen_at"`
	Attempts       int64     `json:"attempts"`
}

// UnknownAgents lists refused identities not dismissed.
func (s *Service) UnknownAgents(ctx context.Context) ([]UnknownAgent, error) {
	rows, err := s.st.Reader.QueryContext(ctx, `SELECT identity_id, hostname, machine_id, agent_version, address, key_fingerprint,
		first_seen_at, last_seen_at, attempts FROM unknown_agents WHERE dismissed_at IS NULL ORDER BY last_seen_at DESC LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UnknownAgent{}
	for rows.Next() {
		var u UnknownAgent
		var first, last int64
		if err := rows.Scan(&u.IdentityID, &u.Hostname, &u.MachineID, &u.AgentVersion, &u.Address, &u.KeyFingerprint, &first, &last, &u.Attempts); err != nil {
			return nil, err
		}
		u.FirstSeen, u.LastSeen = time.UnixMilli(first).UTC(), time.UnixMilli(last).UTC()
		out = append(out, u)
	}
	return out, rows.Err()
}

// ErrNoUnknownAgent: DismissUnknownAgent found nothing to dismiss.
var ErrNoUnknownAgent = errors.New("no such unknown agent")

// DismissUnknownAgent hides a refused identity from the list (it is
// listed again if it still tries to connect a day later).
func (s *Service) DismissUnknownAgent(ctx context.Context, id string) error {
	r, err := s.st.Writer.ExecContext(ctx, `UPDATE unknown_agents SET dismissed_at = ? WHERE identity_id = ? AND dismissed_at IS NULL`, s.now().UnixMilli(), id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrNoUnknownAgent
	}
	return nil
}
