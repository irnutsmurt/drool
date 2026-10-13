package agentsvc

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"drool/internal/agentcaps"
	"drool/internal/agentrpc"
	"drool/internal/agentrpc/pb"
	"drool/internal/audit"
	"drool/internal/config"
	"drool/internal/ids"
	"drool/internal/store/db"
)

// --- enrollment tokens ---

// TokenOptions describe a new enrollment token.
type TokenOptions struct {
	Description string
	HostID      string // bind to an existing host
	GroupID     string // new hosts join this group
	Preset      string
	TTL         time.Duration
	UserID      string
	// WebHost is the host name the administrator uses for the web UI,
	// used for the advertised endpoint when agent.advertise is not set.
	WebHost string
}

// NewToken is a created token. Secret is shown once.
type NewToken struct {
	ID        string    `json:"id"`
	Token     string    `json:"token"` // the bundle to give the agent
	ExpiresAt time.Time `json:"expires_at"`
	Endpoints []string  `json:"endpoints"`
}

// Endpoints is where Shepherds connect to this controller (host:port),
// for an administrator who reached the web UI at webHost. It is the one
// place DROOL decides that: enrollment tokens, installation over SSH,
// the endpoint shown before an installation and the enrollment answer
// all come from here. Configured agent.advertise entries win; without
// them, the host name the web UI was opened with. An address without a
// port of its own gets agent.public_port, else agent.port: the port
// hosts reach, which in a container is the published one, not the one
// the controller listens on inside it.
func (s *Service) Endpoints(webHost string) []string {
	if eps := s.advertised(); eps != nil {
		return eps
	}
	h := webHost
	if hh, _, err := net.SplitHostPort(webHost); err == nil {
		h = hh
	}
	h = strings.Trim(h, "[]")
	if h == "" {
		h = "localhost"
	}
	return []string{net.JoinHostPort(h, strconv.Itoa(s.agentCfg().AdvertisedPort()))}
}

// EndpointsConfigured reports whether agent.advertise names the endpoints
// (otherwise they follow the web UI's host name).
func (s *Service) EndpointsConfigured() bool { return len(s.agentCfg().Advertise) > 0 }

// ListenPort and AdvertisedPort: where the endpoint listens, and the port
// Shepherds are told to use when an address doesn't name one.
func (s *Service) ListenPort() int     { return s.agentCfg().Port }
func (s *Service) AdvertisedPort() int { return s.agentCfg().AdvertisedPort() }

func (s *Service) agentCfg() config.Agent {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.cfg
	c.Advertise = append([]string(nil), s.cfg.Advertise...)
	return c
}

// advertised is agent.advertise with each entry's port filled in; nil
// when it isn't configured.
func (s *Service) advertised() []string {
	c := s.agentCfg()
	if len(c.Advertise) == 0 {
		return nil
	}
	out := make([]string, 0, len(c.Advertise))
	for _, a := range c.Advertise {
		out = append(out, withPort(a, c.AdvertisedPort()))
	}
	return out
}

// withPort is addr, with port added when addr doesn't name one.
func withPort(addr string, port int) string {
	if _, _, err := net.SplitHostPort(addr); err == nil {
		return addr
	}
	return net.JoinHostPort(strings.Trim(addr, "[]"), strconv.Itoa(port))
}

// Endpoint is an address an administrator gives for Shepherds (a new
// controller address), as host:port by the agent.advertise rule: one
// without a port of its own gets AdvertisedPort.
func (s *Service) Endpoint(addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	ep := withPort(addr, s.AdvertisedPort())
	h, p, err := net.SplitHostPort(ep)
	n, perr := strconv.Atoi(p)
	if addr == "" || strings.ContainsAny(addr, " \t/") || err != nil || h == "" || strings.ContainsAny(h, "[]") || perr != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("give the new address as a host name or IP address, with :port only if Shepherds must use another port than %d (e.g. drool.example.net)", s.AdvertisedPort())
	}
	return ep, nil
}

// CreateToken creates a single-use enrollment token.
func (s *Service) CreateToken(ctx context.Context, o TokenOptions) (*NewToken, error) {
	if o.TTL <= 0 {
		o.TTL = s.cfg.TokenTTL
	}
	if o.TTL > 7*24*time.Hour {
		return nil, errors.New("tokens can live at most 7 days")
	}
	if o.Preset != "" {
		if _, err := agentcaps.Preset(o.Preset); err != nil {
			return nil, err
		}
	}
	b := make([]byte, 32)
	rand.Read(b)
	secret := base64.RawURLEncoding.EncodeToString(b)
	id := ids.New(ids.AgentToken)
	now := s.now()
	eps := s.Endpoints(o.WebHost)
	err := s.st.InTx(ctx, func(q *db.Queries) error {
		if o.HostID != "" {
			if _, err := q.GetHost(ctx, o.HostID); err != nil {
				return errors.New("unknown host")
			}
		}
		if err := q.InsertEnrollmentToken(ctx, db.InsertEnrollmentTokenParams{ID: id, TokenHash: agentrpc.TokenHash(secret),
			Description: o.Description, HostID: sql.NullString{String: o.HostID, Valid: o.HostID != ""},
			GroupID: sql.NullString{String: o.GroupID, Valid: o.GroupID != ""}, Preset: o.Preset,
			CreatedBy: sql.NullString{String: o.UserID, Valid: o.UserID != ""}, CreatedAt: now.UnixMilli(),
			ExpiresAt: now.Add(o.TTL).UnixMilli()}); err != nil {
			return err
		}
		_, err := s.audit.RecordTx(ctx, q, audit.Event{Action: "agent.token.create", TargetType: "agent_token", TargetID: id,
			Details: map[string]any{"host_id": o.HostID, "group_id": o.GroupID, "expires_in_s": int(o.TTL / time.Second), "description": o.Description}})
		return err
	})
	if err != nil {
		return nil, err
	}
	return &NewToken{ID: id, ExpiresAt: now.Add(o.TTL), Endpoints: eps,
		Token: agentrpc.Bundle{Endpoints: eps, Root: s.pki.RootFingerprint(), Secret: secret}.Encode()}, nil
}

// RevokeToken revokes an unused token.
func (s *Service) RevokeToken(ctx context.Context, id string) error {
	n, err := s.st.W.RevokeEnrollmentToken(ctx, db.RevokeEnrollmentTokenParams{RevokedAt: sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}, ID: id})
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("the token is unknown, already used or already revoked")
	}
	return nil
}

// --- identities ---

// RevokeIdentity revokes an agent identity immediately: its sessions are
// closed now and all of its certificates stop working (DESIGN §6.4). The
// host is not managed again until the agent re-enrolls or an
// administrator switches it to SSH management.
func (s *Service) RevokeIdentity(ctx context.Context, q *db.Queries, id, userID, reason string) (db.AgentIdentity, error) {
	ident, err := q.GetAgentIdentity(ctx, id)
	if err != nil {
		return ident, err
	}
	n, err := q.RevokeIdentity(ctx, db.RevokeIdentityParams{RevokedAt: sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true},
		RevokedBy: sql.NullString{String: userID, Valid: userID != ""}, RevokeReason: reason, ID: id})
	if err != nil {
		return ident, err
	}
	if n == 0 {
		return ident, errors.New("this identity is already revoked")
	}
	return ident, nil
}

// DisconnectIdentity closes all sessions of an identity (after revocation).
func (s *Service) DisconnectIdentity(id, code, reason string) {
	for _, se := range s.liveSessions(id) {
		se.closeReason = reason
		se.send(&pb.ControllerMessage{Msg: &pb.ControllerMessage_Disconnect{Disconnect: &pb.Disconnect{Code: code, Reason: reason}}})
	}
}

// ResolveConflict releases a quarantine. It refuses while more than one
// session of the identity is connected: the administrator has to shut down
// (or re-enroll) the clone first; DROOL never picks a side.
func (s *Service) ResolveConflict(ctx context.Context, q *db.Queries, id string) error {
	if n := len(s.liveSessions(id)); n > 1 {
		return fmt.Errorf("%d machines are still connected with this identity; stop the agent on the clone (or revoke the identity and re-enroll both) first", n)
	}
	n, err := q.ReleaseQuarantine(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("this identity is not quarantined")
	}
	for _, se := range s.liveSessions(id) {
		se.mu.Lock()
		se.quarantined = false
		se.mu.Unlock()
	}
	return nil
}
