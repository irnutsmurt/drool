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

// Endpoints returns the agent endpoints put into tokens.
func (s *Service) Endpoints(webHost string) []string {
	if len(s.cfg.Advertise) > 0 {
		out := make([]string, 0, len(s.cfg.Advertise))
		for _, a := range s.cfg.Advertise {
			if _, _, err := net.SplitHostPort(a); err != nil {
				a = net.JoinHostPort(a, strconv.Itoa(s.cfg.Port))
			}
			out = append(out, a)
		}
		return out
	}
	h := webHost
	if hh, _, err := net.SplitHostPort(webHost); err == nil {
		h = hh
	}
	h = strings.Trim(h, "[]")
	if h == "" {
		h = "localhost"
	}
	return []string{net.JoinHostPort(h, strconv.Itoa(s.cfg.Port))}
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
