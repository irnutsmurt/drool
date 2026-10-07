package agentsvc

import (
	"context"
	"time"

	"drool/internal/agentrpc/pb"
)

// FirewallHooks connect agent sessions to the firewall service (M8).
type FirewallHooks interface {
	// OnSession runs after a session's hello was acknowledged.
	OnSession(hostID string, firewallJSON []byte, features []string, connectedAt time.Time)
	// OnTelemetry receives "firewall" status and "flows" observations.
	OnTelemetry(hostID, kind string, payload []byte)
	// UpdateBlocked returns why the agent must not be updated right now
	// (a firewall transaction is pending), or "".
	UpdateBlocked(hostID string) string
}

func (s *Service) firewallHooks() FirewallHooks {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hooks.Firewall
}

// FirewallSession describes the agent session a firewall verification
// relies on.
type FirewallSession struct {
	FirewallJSON []byte
	Features     []string
	ConnectedAt  time.Time
	Nonce        string
}

// HostFirewallSession returns the current session's firewall view.
func (s *Service) HostFirewallSession(hostID string) (FirewallSession, bool) {
	se, ok := s.SessionFor(hostID)
	if !ok || se == nil {
		return FirewallSession{}, false
	}
	return FirewallSession{FirewallJSON: se.hello.FirewallJson, Features: se.hello.Features,
		ConnectedAt: se.connectedAt, Nonce: se.hello.SessionNonce}, true
}

// RequestReconnect asks the host's agent to drop its session and connect
// again over a brand-new connection (firewall verification, DESIGN §17.1).
func (s *Service) RequestReconnect(hostID string) {
	if se, ok := s.SessionFor(hostID); ok && se != nil {
		se.send(&pb.ControllerMessage{Msg: &pb.ControllerMessage_Disconnect{Disconnect: &pb.Disconnect{
			Code: "reconnect", Reason: "verify the management path over a new connection"}}})
	}
}

// SendFirewallConfig turns continuous connection learning on or off.
func (s *Service) SendFirewallConfig(hostID string, learning bool, sampleSeconds int) {
	if se, ok := s.SessionFor(hostID); ok && se != nil {
		se.send(&pb.ControllerMessage{Msg: &pb.ControllerMessage_FirewallConfig{FirewallConfig: &pb.FirewallConfig{
			Learning: learning, SampleSeconds: uint32(sampleSeconds)}}})
	}
}

type sessionAfterKey struct{}

// WithSessionAfter makes agent operations on ctx use only a session that
// connected after t: the firewall commit must travel over the new
// connection, never the one that existed when the candidate was applied.
func WithSessionAfter(ctx context.Context, t time.Time) context.Context {
	return context.WithValue(ctx, sessionAfterKey{}, t)
}

func sessionAfter(ctx context.Context) (time.Time, bool) {
	t, ok := ctx.Value(sessionAfterKey{}).(time.Time)
	return t, ok
}

// SetFirewallHooks installs the firewall service's hooks.
func (s *Service) SetFirewallHooks(h FirewallHooks) {
	s.mu.Lock()
	s.hooks.Firewall = h
	s.mu.Unlock()
}
