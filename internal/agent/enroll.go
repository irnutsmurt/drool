package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"runtime"
	"time"

	"drool/internal/agentpki"
	"drool/internal/agentrpc"
	"drool/internal/agentrpc/pb"
)

// Enroll enrolls this agent with the controller named in the bundle
// (DESIGN §6.1):
//
//  1. generate the identity key locally (it never leaves this host);
//  2. fetch the root and trust manifest, and check the root against the
//     fingerprint pinned in the token (no web/browser TLS trust involved);
//  3. connect with full verification (pinned root + manifest);
//  4. present the token, the public key with proof of possession and the
//     local capability ceiling; receive the operational certificate.
func Enroll(ctx context.Context, paths Paths, tokenBundle, version string) (*Identity, error) {
	b, err := agentrpc.ParseBundle(tokenBundle)
	if err != nil {
		return nil, err
	}
	file, err := LoadCeiling(paths.Config)
	if err != nil {
		return nil, err
	}
	st, err := OpenStore(paths.State)
	if err != nil {
		return nil, err
	}
	if id, err := st.Identity(); err == nil {
		return nil, fmt.Errorf("this agent is already enrolled as %s (host %s); to enroll again, stop the agent and remove %s first", id.IdentityID, id.HostName, paths.State)
	}
	key, err := st.IdentityKey(true)
	if err != nil {
		return nil, err
	}
	pub := key.Public().(ed25519.PublicKey)
	ceiling, _ := marshalCaps(file.Capabilities)

	var lastErr error
	for _, ep := range b.Endpoints {
		id, err := enrollAt(ctx, st, ep, b, key, pub, ceiling, version)
		if err == nil {
			return id, nil
		}
		lastErr = fmt.Errorf("%s: %w", ep, err)
		var perm *permanentError
		if errors.As(err, &perm) {
			break
		}
	}
	return nil, lastErr
}

// permanentError stops trying other endpoints (e.g. a rejected token).
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

func enrollAt(ctx context.Context, st *Store, ep string, b agentrpc.Bundle, key ed25519.PrivateKey, pub ed25519.PublicKey, ceiling []byte, version string) (*Identity, error) {
	// Step 2: root + manifest, without trusting the channel.
	var root *x509.Certificate
	var sm *agentpki.SignedManifest
	{
		var gotRoot *x509.Certificate
		cfg := &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true}
		conn, err := dial(ep, cfg)
		if err != nil {
			return nil, err
		}
		cctx, cancel := callCtx(ctx)
		tb, err := pb.NewAgentClient(conn).GetTrust(cctx, &pb.GetTrustRequest{})
		cancel()
		conn.Close()
		if err != nil {
			return nil, fmt.Errorf("could not reach the controller's agent endpoint: %w", err)
		}
		gotRoot, err = x509.ParseCertificate(tb.RootCert)
		if err != nil {
			return nil, err
		}
		if fp := agentpki.Fingerprint(gotRoot.Raw); fp != b.Root {
			return nil, &permanentError{fmt.Errorf("the controller presented DROOL Agent Root CA %s, but the enrollment token pins %s: refusing to enroll (wrong controller, or someone in between)", fp[:23], b.Root[:min(23, len(b.Root))])}
		}
		root = gotRoot
		sm = &agentpki.SignedManifest{Payload: tb.Manifest.GetPayload(), Signature: tb.Manifest.GetSignature()}
	}
	man, err := agentpki.VerifyManifest(sm, root)
	if err != nil {
		return nil, &permanentError{err}
	}

	// Step 3: full verification for the enrollment call itself.
	cfg := &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			_, err := agentpki.VerifyController(raw, root, man, time.Now())
			return err
		}}
	conn, err := dial(ep, cfg)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	host, _ := os.Hostname()
	req := &pb.EnrollRequest{Token: b.Secret, PublicKey: pub,
		Proof:       ed25519.Sign(key, agentrpc.EnrollProofMessage(b.Secret, pub)),
		CeilingJson: ceiling,
		Info: &pb.AgentInfo{AgentVersion: version, ProtocolMin: agentrpc.Local().Min, ProtocolMax: agentrpc.Local().Max,
			Hostname: host, MachineId: machineID(), Os: runtime.GOOS, Arch: runtime.GOARCH, BootId: bootID()}}
	cctx, cancel := callCtx(ctx)
	defer cancel()
	resp, err := pb.NewAgentClient(conn).Enroll(cctx, req)
	if err != nil {
		return nil, &permanentError{fmt.Errorf("enrollment refused: %w", err)}
	}
	chain := resp.CertChain
	if len(chain) == 0 {
		return nil, errors.New("the controller returned no certificate")
	}
	leaf, err := x509.ParseCertificate(chain[0])
	if err != nil || !agentpki.MatchesKey(leaf, key) {
		return nil, errors.New("the controller returned a certificate for a different key")
	}
	newest := sm
	if resp.Manifest != nil && len(resp.Manifest.Payload) > 0 {
		cand := &agentpki.SignedManifest{Payload: resp.Manifest.Payload, Signature: resp.Manifest.Signature}
		if m2, changed, err := agentpki.Accept(man, sm, cand, root); err == nil && changed {
			newest, man = cand, m2
		}
	}
	endpoints := Endpoints{Primary: ep}
	for _, e := range resp.Endpoints {
		if e != ep {
			endpoints.Fallbacks = append(endpoints.Fallbacks, e)
		}
	}
	id := Identity{IdentityID: resp.IdentityId, HostID: resp.HostId, HostName: resp.HostName,
		ControllerEpoch: resp.ControllerEpoch, EnrolledAt: time.Now().UTC()}
	if err := st.SaveEnrollment(id, root, chain, newest, endpoints); err != nil {
		return nil, err
	}
	return &id, nil
}
