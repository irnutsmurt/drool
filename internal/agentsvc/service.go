package agentsvc

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"drool/internal/agentpki"
	"drool/internal/agentrpc"
	"drool/internal/agentrpc/pb"
	"drool/internal/artifacts"
	"drool/internal/audit"
	"drool/internal/config"
	"drool/internal/filedeploy"
	"drool/internal/ids"
	"drool/internal/inventory"
	"drool/internal/store"
	"drool/internal/store/db"
)

// Service is the controller's agent endpoint and session hub.
type Service struct {
	pb.UnimplementedAgentServer

	cfg   config.Agent
	st    *store.Store
	pki   *PKI
	audit *audit.Writer
	inv   *inventory.Service
	arts  *artifacts.Store
	rec   filedeploy.Recorder
	log   *slog.Logger
	epoch string
	now   func() time.Time

	// ControllerRange is the agent protocol range this controller supports
	// (documented in ARCHITECTURE-ALPHA §23.4).
	ControllerRange agentrpc.Range

	mu         sync.Mutex
	sessions   map[string]*Session // identity id → session
	channels   map[string]*pendingChannel
	challenges map[string]challenge
	hooks      Hooks
	locker     Locker
	grpc       *grpc.Server
	listener   net.Listener
}

// Hooks let the rest of the controller react to agent events.
type Hooks struct {
	// HostChanged: management state or inventory of a host changed.
	HostChanged func(hostID string)
	// Firewall connects sessions and telemetry to the firewall service.
	Firewall FirewallHooks
	// Monitor connects sessions and telemetry to system monitoring (M11).
	Monitor MonitorHooks
	// Recovery connects sessions to restore reconciliation (M16).
	Recovery RecoveryHooks
}

type challenge struct {
	nonce   []byte
	expires time.Time
}

// Options configure the service.
type Options struct {
	Config    config.Agent
	Store     *store.Store
	PKI       *PKI
	Audit     *audit.Writer
	Inventory *inventory.Service
	Artifacts *artifacts.Store
	Recorder  filedeploy.Recorder
	Log       *slog.Logger
}

// New creates the service and loads (or creates) the controller epoch.
func New(ctx context.Context, o Options) (*Service, error) {
	s := &Service{cfg: o.Config, st: o.Store, pki: o.PKI, audit: o.Audit, inv: o.Inventory, arts: o.Artifacts, rec: o.Recorder,
		log: o.Log, now: time.Now, ControllerRange: agentrpc.Local(),
		sessions: map[string]*Session{}, channels: map[string]*pendingChannel{}, challenges: map[string]challenge{}}
	if s.log == nil {
		s.log = slog.Default()
	}
	ep, err := ControllerEpoch(ctx, o.Store)
	if err != nil {
		return nil, err
	}
	s.epoch = ep
	if err := o.Store.W.ResetIdentitiesConnected(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// ControllerEpoch returns the controller epoch (DESIGN §9), creating it on
// first use. drool restore gives the restored controller a new one, and
// agents move to it only through reconciliation (internal/recovery).
func ControllerEpoch(ctx context.Context, st *store.Store) (string, error) {
	row, err := st.R.GetSetting(ctx, "controller_epoch")
	if err == nil {
		var e string
		if err := json.Unmarshal([]byte(row.ValueJson), &e); err == nil && e != "" {
			return e, nil
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	b := make([]byte, 16)
	rand.Read(b)
	e := hex.EncodeToString(b)
	v, _ := json.Marshal(e)
	if err := st.W.UpsertSetting(ctx, db.UpsertSettingParams{Key: "controller_epoch", ValueJson: string(v), UpdatedAt: time.Now().UnixMilli()}); err != nil {
		return "", err
	}
	return e, nil
}

// Epoch returns the controller epoch.
func (s *Service) Epoch() string { return s.epoch }

// PKI returns the Agent CA.
func (s *Service) PKI() *PKI { return s.pki }

// SetHooks installs event hooks.
func (s *Service) SetHooks(h Hooks) {
	s.mu.Lock()
	s.hooks = h
	s.mu.Unlock()
}

func (s *Service) hostChanged(hostID string) {
	s.mu.Lock()
	fn := s.hooks.HostChanged
	s.mu.Unlock()
	if fn != nil {
		fn(hostID)
	}
}

// Listen starts the gRPC endpoint on addr (":9443").
func (s *Service) Listen(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("agent endpoint: %w", err)
	}
	return s.Serve(ln)
}

// Serve serves the agent endpoint on ln (in the background).
func (s *Service) Serve(ln net.Listener) error {
	tlsCfg := &tls.Config{
		MinVersion:     tls.VersionTLS13,
		GetCertificate: s.pki.ServerCertificate,
		// Requested, not verified by crypto/tls: every RPC authenticates
		// the agent itself, because the recovery RPCs must accept an
		// expired certificate and enrollment none at all.
		ClientAuth: tls.RequestClientCert,
	}
	s.grpc = grpc.NewServer(
		grpc.Creds(credentials.NewTLS(tlsCfg)),
		grpc.KeepaliveParams(keepalive.ServerParameters{Time: time.Minute, Timeout: 20 * time.Second}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 20 * time.Second, PermitWithoutStream: true}),
		grpc.MaxRecvMsgSize(16<<20),
	)
	pb.RegisterAgentServer(s.grpc, s)
	s.listener = ln
	go s.grpc.Serve(ln)
	return nil
}

// Addr is the listening address (tests).
func (s *Service) Addr() string { return s.listener.Addr().String() }

// Stop closes the endpoint and all sessions.
func (s *Service) Stop() {
	if s.grpc != nil {
		s.grpc.Stop()
	}
}

// --- authentication helpers ---

func peerInfo(ctx context.Context) ([][]byte, string) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return nil, ""
	}
	addr := ""
	if p.Addr != nil {
		addr = p.Addr.String()
	}
	ti, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return nil, addr
	}
	var chain [][]byte
	for _, c := range ti.State.PeerCertificates {
		chain = append(chain, c.Raw)
	}
	return chain, addr
}

func hostOnly(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// authAgent authenticates the agent behind ctx by its certificate and
// returns its identity. Revoked identities are refused.
//
// recovery (only RecoveryChallenge/Recover) also accepts expired
// certificates and certificates from revoked intermediates: those RPCs
// authenticate by proof of possession of the registered identity key.
func (s *Service) authAgent(ctx context.Context, recovery bool) (db.AgentIdentity, *x509.Certificate, error) {
	chain, _ := peerInfo(ctx)
	if len(chain) == 0 {
		return db.AgentIdentity{}, nil, status.Error(codes.Unauthenticated, "no agent certificate presented")
	}
	var leaf *x509.Certificate
	var id string
	var err error
	if recovery {
		leaf, id, err = s.pki.VerifyAgentForRecovery(chain)
	} else {
		leaf, id, err = s.pki.VerifyAgent(chain, false)
	}
	if err != nil {
		return db.AgentIdentity{}, nil, status.Error(codes.Unauthenticated, err.Error())
	}
	ident, err := s.st.R.GetAgentIdentity(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return db.AgentIdentity{}, nil, status.Error(codes.PermissionDenied, "unknown agent identity")
	} else if err != nil {
		return db.AgentIdentity{}, nil, status.Error(codes.Internal, "lookup failed")
	}
	pub, ok := leaf.PublicKey.(ed25519.PublicKey)
	if !ok || !pub.Equal(ed25519.PublicKey(ident.PublicKey)) {
		return db.AgentIdentity{}, nil, status.Error(codes.PermissionDenied, "certificate key does not match the enrolled identity key")
	}
	if ident.Status == "revoked" {
		return ident, leaf, status.Error(codes.PermissionDenied, "this agent identity was revoked: "+ident.RevokeReason)
	}
	return ident, leaf, nil
}

func agentActor(id db.AgentIdentity) *audit.Actor {
	return &audit.Actor{Type: audit.ActorSystem, ID: id.ID, Name: "agent " + id.Hostname}
}

// --- trust ---

// GetTrust implements pb.AgentServer (unauthenticated: manifests are
// root-signed).
func (s *Service) GetTrust(ctx context.Context, _ *pb.GetTrustRequest) (*pb.TrustBundle, error) {
	sm, _ := s.pki.Manifest()
	return &pb.TrustBundle{RootCert: s.pki.Root().Raw, Manifest: &pb.SignedManifest{Payload: sm.Payload, Signature: sm.Signature}}, nil
}

// --- enrollment ---

var errToken = status.Error(codes.PermissionDenied, "the enrollment token is invalid, expired, revoked or already used; create a new one")

// Enroll implements pb.AgentServer (DESIGN §6.1). Everything happens in one
// transaction: the host is created or associated, the identity recorded,
// the certificate issued and the token consumed, or nothing at all.
func (s *Service) Enroll(ctx context.Context, req *pb.EnrollRequest) (*pb.EnrollResponse, error) {
	_, addr := peerInfo(ctx)
	fail := func(reason string, details map[string]any) {
		if details == nil {
			details = map[string]any{}
		}
		details["reason"], details["address"] = reason, hostOnly(addr)
		s.audit.Record(context.WithoutCancel(ctx), audit.Event{Action: "agent.enroll", Result: audit.Failure,
			Actor: &audit.Actor{Type: audit.ActorAnonymous, Name: "agent enrollment"}, Details: details})
	}
	pub := ed25519.PublicKey(req.PublicKey)
	if len(pub) != ed25519.PublicKeySize || req.Info == nil {
		fail("malformed request", nil)
		return nil, status.Error(codes.InvalidArgument, "malformed enrollment request")
	}
	tok, err := s.st.R.GetEnrollmentTokenByHash(ctx, agentrpc.TokenHash(req.Token))
	if err != nil {
		fail("unknown token", nil)
		return nil, errToken
	}
	now := s.now()
	switch {
	case tok.UsedAt.Valid:
		fail("token already used", map[string]any{"token_id": tok.ID})
		return nil, errToken
	case tok.RevokedAt.Valid:
		fail("token revoked", map[string]any{"token_id": tok.ID})
		return nil, errToken
	case now.UnixMilli() >= tok.ExpiresAt:
		fail("token expired", map[string]any{"token_id": tok.ID})
		return nil, errToken
	}
	if !ed25519.Verify(pub, agentrpc.EnrollProofMessage(req.Token, pub), req.Proof) {
		fail("bad proof of possession", map[string]any{"token_id": tok.ID})
		return nil, status.Error(codes.PermissionDenied, "proof of possession of the identity key failed")
	}
	agentRange := agentrpc.Range{Min: req.Info.ProtocolMin, Max: req.Info.ProtocolMax}
	if agentrpc.Negotiate(s.ControllerRange, agentRange) == 0 {
		fail("incompatible protocol", map[string]any{"token_id": tok.ID})
		return nil, status.Error(codes.FailedPrecondition, agentrpc.Incompatibility(s.ControllerRange, agentRange))
	}
	identityID := ids.New(ids.AgentIdentity)
	chain, leaf, err := s.pki.IssueAgent(identityID, pub, s.cfg.CertLifetime)
	if err != nil {
		return nil, status.Error(codes.Internal, "certificate issuance failed")
	}
	var hostID, hostName string
	var emit func()
	err = s.st.InTx(ctx, func(q *db.Queries) error {
		if tok.HostID.Valid {
			h, err := q.GetHost(ctx, tok.HostID.String)
			if err != nil {
				return errors.New("the host this token was created for no longer exists")
			}
			if _, err := q.GetLiveIdentityForHost(ctx, h.ID); err == nil {
				return fmt.Errorf("host %s already has an agent identity; revoke it before enrolling a new agent", h.Name)
			}
			hostID, hostName = h.ID, h.Name
			fallback := "none"
			if h.SshConfigured != 0 {
				fallback = "ssh" // the SSH path keeps working as the fallback
			}
			if err := q.SetHostManagement(ctx, db.SetHostManagementParams{ManagementMethod: "agent", ManagementFallback: fallback, UpdatedAt: now.UnixMilli(), ID: h.ID}); err != nil {
				return err
			}
		} else {
			// A new host. Never matched to an existing host by name or
			// address: identity is the key, not the hostname.
			hostID = ids.New(ids.Host)
			hostName, err = uniqueHostName(ctx, q, req.Info.Hostname)
			if err != nil {
				return err
			}
			if err := q.InsertAgentOnlyHost(ctx, db.InsertAgentOnlyHostParams{ID: hostID, Name: hostName,
				ConnectAddress: hostOnly(addr), CreatedAt: now.UnixMilli(), UpdatedAt: now.UnixMilli()}); err != nil {
				return err
			}
			if tok.GroupID.Valid {
				if err := q.InsertGroupMember(ctx, db.InsertGroupMemberParams{GroupID: tok.GroupID.String, HostID: hostID}); err != nil {
					return err
				}
			}
		}
		if err := q.InsertAgentIdentity(ctx, db.InsertAgentIdentityParams{ID: identityID, HostID: hostID, PublicKey: pub,
			Fingerprint: agentpki.KeyFingerprint(pub), EnrolledAt: now.UnixMilli(), TokenID: sql.NullString{String: tok.ID, Valid: true},
			CertSerial: leaf.SerialNumber.Text(16), CertNotAfter: leaf.NotAfter.UnixMilli(), CertIssuedAt: now.UnixMilli(),
			AgentVersion: req.Info.AgentVersion, ProtocolMin: int64(req.Info.ProtocolMin), ProtocolMax: int64(req.Info.ProtocolMax),
			Hostname: req.Info.Hostname, MachineID: req.Info.MachineId, Os: req.Info.Os, Arch: req.Info.Arch,
			CeilingJson: string(req.CeilingJson)}); err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return errors.New("this identity key is already enrolled")
			}
			return err
		}
		n, err := q.UseEnrollmentToken(ctx, db.UseEnrollmentTokenParams{UsedAt: sql.NullInt64{Int64: now.UnixMilli(), Valid: true},
			UsedByIdentity: sql.NullString{String: identityID, Valid: true}, ID: tok.ID, ExpiresAt: now.UnixMilli()})
		if err != nil {
			return err
		}
		if n != 1 {
			return errToken // used concurrently
		}
		emit, err = s.audit.RecordTx(ctx, q, audit.Event{Action: "agent.enroll", TargetType: "host", TargetID: hostID,
			Actor: &audit.Actor{Type: audit.ActorSystem, ID: identityID, Name: "agent " + req.Info.Hostname},
			Details: map[string]any{"identity_id": identityID, "host": hostName, "token_id": tok.ID, "key_fingerprint": agentpki.KeyFingerprint(pub),
				"address": hostOnly(addr), "agent_version": req.Info.AgentVersion, "new_host": !tok.HostID.Valid, "ceiling": json.RawMessage(nonEmpty(req.CeilingJson))}})
		return err
	})
	if err != nil {
		fail(err.Error(), map[string]any{"token_id": tok.ID})
		if errors.Is(err, errToken) {
			return nil, errToken
		}
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	emit()
	s.hostChanged(hostID)
	sm, _ := s.pki.Manifest()
	return &pb.EnrollResponse{IdentityId: identityID, HostId: hostID, HostName: hostName, CertChain: chain,
		Manifest: &pb.SignedManifest{Payload: sm.Payload, Signature: sm.Signature}, Endpoints: s.cfg.Advertise, ControllerEpoch: s.epoch}, nil
}

func nonEmpty(b []byte) []byte {
	if len(b) == 0 {
		return []byte("{}")
	}
	return b
}

func uniqueHostName(ctx context.Context, q *db.Queries, base string) (string, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "agent-host"
	}
	name := base
	for i := 2; i < 1000; i++ {
		if _, err := q.GetHostByName(ctx, name); errors.Is(err, sql.ErrNoRows) {
			return name, nil
		}
		name = base + "-" + strconv.Itoa(i)
	}
	return "", errors.New("could not find a free host name")
}

// --- expired-certificate recovery (DESIGN §6.4) ---

// RecoveryChallenge implements pb.AgentServer.
func (s *Service) RecoveryChallenge(ctx context.Context, req *pb.RecoveryChallengeRequest) (*pb.RecoveryChallengeResponse, error) {
	ident, _, err := s.authAgent(ctx, true)
	if err != nil {
		return nil, err
	}
	if ident.ID != req.IdentityId {
		return nil, status.Error(codes.PermissionDenied, "identity does not match the certificate")
	}
	nonce := make([]byte, 32)
	rand.Read(nonce)
	s.mu.Lock()
	s.challenges[ident.ID] = challenge{nonce: nonce, expires: s.now().Add(2 * time.Minute)}
	s.mu.Unlock()
	return &pb.RecoveryChallengeResponse{Nonce: nonce}, nil
}

// Recover implements pb.AgentServer: it accepts only certificate recovery
// and requires proof of possession of the known identity key, an identity
// that is neither revoked nor in conflict, and an expiry within the grace
// period.
func (s *Service) Recover(ctx context.Context, req *pb.RecoverRequest) (*pb.RecoverResponse, error) {
	ident, leaf, err := s.authAgent(ctx, true)
	deny := func(reason string) error {
		s.audit.Record(context.WithoutCancel(ctx), audit.Event{Action: "agent.recover", TargetType: "host", TargetID: ident.HostID,
			Result: audit.Denied, Actor: agentActor(ident), Details: map[string]any{"identity_id": ident.ID, "reason": reason}})
		return status.Error(codes.PermissionDenied, reason)
	}
	if err != nil {
		if ident.ID != "" {
			return nil, deny(status.Convert(err).Message())
		}
		return nil, err
	}
	if ident.ID != req.IdentityId {
		return nil, deny("identity does not match the certificate")
	}
	if ident.Status == "quarantined" {
		return nil, deny("this identity is quarantined (identity conflict); an administrator must resolve it first")
	}
	s.mu.Lock()
	ch, ok := s.challenges[ident.ID]
	delete(s.challenges, ident.ID)
	s.mu.Unlock()
	if !ok || s.now().After(ch.expires) || string(ch.nonce) != string(req.Nonce) {
		return nil, deny("no valid recovery challenge")
	}
	if !ed25519.Verify(ed25519.PublicKey(ident.PublicKey), agentrpc.RecoverMessage(ident.ID, req.Nonce), req.Signature) {
		return nil, deny("proof of possession of the identity key failed")
	}
	if since := s.now().Sub(leaf.NotAfter); since > s.cfg.RecoveryGrace {
		return nil, deny(fmt.Sprintf("the certificate expired %s ago, beyond the %s grace period; re-enroll this agent",
			since.Round(time.Hour), s.cfg.RecoveryGrace))
	}
	chain, cert, err := s.pki.IssueAgent(ident.ID, ed25519.PublicKey(ident.PublicKey), s.cfg.CertLifetime)
	if err != nil {
		return nil, status.Error(codes.Internal, "certificate issuance failed")
	}
	now := s.now()
	err = s.st.InTx(ctx, func(q *db.Queries) error {
		if err := q.UpdateIdentityCert(ctx, db.UpdateIdentityCertParams{CertSerial: cert.SerialNumber.Text(16), CertNotAfter: cert.NotAfter.UnixMilli(),
			CertIssuedAt: now.UnixMilli(), ID: ident.ID}); err != nil {
			return err
		}
		if err := q.MarkIdentityRecovered(ctx, db.MarkIdentityRecoveredParams{RecoveredAt: sql.NullInt64{Int64: now.UnixMilli(), Valid: true}, ID: ident.ID}); err != nil {
			return err
		}
		_, err := s.audit.RecordTx(ctx, q, audit.Event{Action: "agent.recover", TargetType: "host", TargetID: ident.HostID, Actor: agentActor(ident),
			Details: map[string]any{"identity_id": ident.ID, "expired_at": leaf.NotAfter.UTC(), "new_not_after": cert.NotAfter.UTC()}})
		return err
	})
	if err != nil {
		return nil, status.Error(codes.Internal, "could not record the recovery")
	}
	return &pb.RecoverResponse{CertChain: chain}, nil
}

// SetAdvertise overrides the advertised agent endpoints (tests, and the
// committed result of an endpoint migration).
func (s *Service) SetAdvertise(eps []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.Advertise = eps
}

// SetLifetimes overrides certificate lifetime and recovery grace (tests).
func (s *Service) SetLifetimes(cert, grace time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.CertLifetime, s.cfg.RecoveryGrace = cert, grace
}
