package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"

	"drool/internal/agentpki"
)

// verifyMode is how strictly a connection authenticates the controller.
type verifyMode int

const (
	// verifyFull: chain to the pinned root through an intermediate listed
	// in the newest accepted trust manifest (all management traffic).
	verifyFull verifyMode = iota
	// verifyRootOnly: chain to the pinned root through any intermediate.
	// Used only to fetch trust manifests, which are root-signed themselves
	// (a rotated intermediate isn't in the agent's manifest yet).
	verifyRootOnly
)

// clientTLS builds the TLS configuration for talking to the controller.
// It never uses the system trust store or browser certificates: only the
// DROOL Agent Root CA pinned at enrollment.
func (a *Agent) clientTLS(mode verifyMode, cert *tls.Certificate) (*tls.Config, error) {
	root, err := a.store.Root()
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // replaced by VerifyPeerCertificate below
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if mode == verifyRootOnly {
				return verifyChainToRoot(raw, root, a.now())
			}
			m, _, err := a.store.Manifest()
			if err != nil {
				return err
			}
			_, err = agentpki.VerifyController(raw, root, m, a.now())
			return err
		},
	}
	if cert != nil {
		c := *cert
		cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &c, nil }
	}
	return cfg, nil
}

func verifyChainToRoot(raw [][]byte, root *x509.Certificate, now time.Time) error {
	if len(raw) == 0 {
		return errors.New("no server certificate")
	}
	leaf, err := x509.ParseCertificate(raw[0])
	if err != nil {
		return err
	}
	pool := x509.NewCertPool()
	for _, r := range raw[1:] {
		if c, err := x509.ParseCertificate(r); err == nil {
			pool.AddCert(c)
		}
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)
	_, err = leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: pool, CurrentTime: now,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	return err
}

// operationalCert returns the stored certificate with the identity key.
func (a *Agent) operationalCert() (*tls.Certificate, *x509.Certificate, error) {
	key, err := a.store.IdentityKey(false)
	if err != nil {
		return nil, nil, err
	}
	chain, err := a.store.Chain()
	if err != nil {
		return nil, nil, err
	}
	if !agentpki.MatchesKey(chain[0], key) {
		return nil, nil, fmt.Errorf("the stored certificate does not belong to this agent's identity key")
	}
	tc := &tls.Certificate{PrivateKey: ed25519.PrivateKey(key), Leaf: chain[0]}
	for _, c := range chain {
		tc.Certificate = append(tc.Certificate, c.Raw)
	}
	return tc, chain[0], nil
}

// dial connects to one controller endpoint.
func dial(endpoint string, cfg *tls.Config) (*grpc.ClientConn, error) {
	return grpc.NewClient(endpoint,
		grpc.WithTransportCredentials(credentials.NewTLS(cfg)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 30 * time.Second, Timeout: 15 * time.Second, PermitWithoutStream: true}),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(16<<20)),
	)
}

// callCtx bounds unary calls.
func callCtx(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 30*time.Second)
}
