// Package agentpki implements the DROOL Agent PKI (DESIGN §4.3, §6.4;
// ARCHITECTURE-ALPHA §23.3):
//
//	Offline Root CA → Controller Intermediate CA → short-lived certificates
//
// plus the root-signed, monotonic trust manifest that tells agents which
// intermediates are trusted. It is pure crypto: no storage, no network.
//
// The Agent PKI is used only for agent identity and agent↔controller mTLS.
// It is never the browser HTTPS certificate, never an OpenSSH CA and never
// the agent release-signing key (DESIGN §4.4).
package agentpki

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"strings"
	"time"
)

// Default lifetimes.
const (
	RootLifetime         = 20 * 365 * 24 * time.Hour
	IntermediateLifetime = 5 * 365 * 24 * time.Hour
	ControllerLifetime   = 90 * 24 * time.Hour
	AgentLifetime        = 30 * 24 * time.Hour // DESIGN §6.4
	clockSkew            = 5 * time.Minute     // backdate NotBefore
)

// URIs carried in leaf certificates mark their role, so an agent
// certificate can never be used as a controller certificate or vice versa.
const (
	ControllerURI  = "urn:drool:controller"
	agentURIPrefix = "urn:drool:agent:"
	organization   = "DROOL Agent PKI"
)

// Fingerprint is "sha256:<hex>" of a certificate's DER bytes.
func Fingerprint(der []byte) string {
	s := sha256.Sum256(der)
	return "sha256:" + hex.EncodeToString(s[:])
}

// KeyFingerprint is "sha256:<hex>" of an Ed25519 public key.
func KeyFingerprint(pub ed25519.PublicKey) string {
	s := sha256.Sum256(pub)
	return "sha256:" + hex.EncodeToString(s[:])
}

// CA is a certificate with its private key (the key may be nil when it is
// held offline).
type CA struct {
	Cert *x509.Certificate
	Key  ed25519.PrivateKey
}

func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		panic(err)
	}
	return n
}

// NewKey generates an Ed25519 key pair.
func NewKey() (ed25519.PublicKey, ed25519.PrivateKey) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	return pub, priv
}

func sign(tmpl, parent *x509.Certificate, pub ed25519.PublicKey, signer crypto.Signer) (*x509.Certificate, error) {
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, pub, signer)
	if err != nil {
		return nil, err
	}
	return x509.ParseCertificate(der)
}

// NewRoot creates a self-signed root CA. name identifies the installation.
func NewRoot(name string, now time.Time) (*CA, error) {
	pub, priv := NewKey()
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "DROOL Agent Root CA " + name, Organization: []string{organization}},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              now.Add(RootLifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}
	tmpl.SubjectKeyId = skid(pub)
	c, err := sign(tmpl, tmpl, pub, priv)
	if err != nil {
		return nil, err
	}
	return &CA{Cert: c, Key: priv}, nil
}

// NewIntermediate creates an intermediate CA signed by root.
func NewIntermediate(root *CA, name string, now time.Time) (*CA, error) {
	if root.Key == nil {
		return nil, ErrRootOffline
	}
	pub, priv := NewKey()
	notAfter := now.Add(IntermediateLifetime)
	if notAfter.After(root.Cert.NotAfter) {
		notAfter = root.Cert.NotAfter
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "DROOL Controller Intermediate CA " + name, Organization: []string{organization}},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
		SubjectKeyId:          skid(pub),
	}
	c, err := sign(tmpl, root.Cert, pub, root.Key)
	if err != nil {
		return nil, err
	}
	return &CA{Cert: c, Key: priv}, nil
}

// ErrRootOffline is returned when an action needs the root private key but
// it was exported and removed from the controller.
var ErrRootOffline = errors.New("the Agent Root CA private key is offline (not on this controller)")

func skid(pub ed25519.PublicKey) []byte {
	s := sha256.Sum256(pub)
	return s[:20]
}

func notAfter(ca *x509.Certificate, want time.Time) time.Time {
	if want.After(ca.NotAfter) {
		return ca.NotAfter
	}
	return want
}

// IssueController issues the controller's agent-endpoint server
// certificate. hosts are the DNS names/IPs agents connect to; they are
// informational (agents authenticate the controller by chain, manifest and
// role, not by name, so a renamed endpoint can't strand them).
func IssueController(inter *CA, pub ed25519.PublicKey, hosts []string, now time.Time) (*x509.Certificate, error) {
	u, _ := url.Parse(ControllerURI)
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: "drool-controller", Organization: []string{organization}},
		NotBefore:    now.Add(-clockSkew),
		NotAfter:     notAfter(inter.Cert, now.Add(ControllerLifetime)),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		URIs:         []*url.URL{u},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else if h != "" {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	return sign(tmpl, inter.Cert, pub, inter.Key)
}

// AgentURI is the URI SAN of an agent certificate.
func AgentURI(identityID string) string { return agentURIPrefix + identityID }

// IssueAgent issues an operational certificate over an agent's long-lived
// identity key.
func IssueAgent(inter *CA, identityID string, pub ed25519.PublicKey, lifetime time.Duration, now time.Time) (*x509.Certificate, error) {
	u, err := url.Parse(AgentURI(identityID))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: identityID, Organization: []string{organization}},
		NotBefore:    now.Add(-clockSkew),
		NotAfter:     notAfter(inter.Cert, now.Add(lifetime)),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		URIs:         []*url.URL{u},
	}
	return sign(tmpl, inter.Cert, pub, inter.Key)
}

// AgentIdentity returns the identity id in an agent certificate.
func AgentIdentity(c *x509.Certificate) (string, bool) {
	for _, u := range c.URIs {
		if s := u.String(); strings.HasPrefix(s, agentURIPrefix) {
			return strings.TrimPrefix(s, agentURIPrefix), true
		}
	}
	return "", false
}

func isController(c *x509.Certificate) bool {
	for _, u := range c.URIs {
		if u.String() == ControllerURI {
			return true
		}
	}
	return false
}

// --- verification ---

// VerifyError explains why a peer certificate was rejected.
type VerifyError struct{ Reason string }

func (e *VerifyError) Error() string { return e.Reason }

func verr(format string, a ...any) error { return &VerifyError{Reason: fmt.Sprintf(format, a...)} }

func parseChain(chain [][]byte) ([]*x509.Certificate, error) {
	if len(chain) == 0 {
		return nil, verr("no certificate presented")
	}
	out := make([]*x509.Certificate, 0, len(chain))
	for _, der := range chain {
		c, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, verr("unparseable certificate: %v", err)
		}
		out = append(out, c)
	}
	return out, nil
}

// VerifyController is the agent's check of a controller certificate: it
// must chain to the pinned root through an intermediate that the newest
// accepted trust manifest lists as trusted (and not revoked), and carry
// the controller role.
func VerifyController(chain [][]byte, root *x509.Certificate, m *Manifest, now time.Time) (*x509.Certificate, error) {
	certs, err := parseChain(chain)
	if err != nil {
		return nil, err
	}
	leaf := certs[0]
	if !isController(leaf) {
		return nil, verr("the server certificate is not a DROOL controller certificate")
	}
	if m == nil {
		return nil, verr("no trust manifest accepted yet")
	}
	pool := x509.NewCertPool()
	trustedSeen := false
	for _, c := range certs[1:] {
		fp := Fingerprint(c.Raw)
		if m.Revoked(fp) {
			return nil, verr("the controller's intermediate CA %s is revoked by trust manifest %d", short(fp), m.Version)
		}
		if m.Trusted(fp) {
			pool.AddCert(c)
			trustedSeen = true
		}
	}
	if !trustedSeen {
		return nil, verr("the controller's intermediate CA is not in trust manifest %d", m.Version)
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: pool, CurrentTime: now,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		return nil, verr("controller certificate does not chain to the pinned DROOL Agent Root CA: %v", err)
	}
	return leaf, nil
}

// VerifyAgent is the controller's check of an agent certificate. With
// allowExpired (only the recovery endpoint), an otherwise valid chain whose
// leaf has expired is accepted; the caller enforces the grace period.
func VerifyAgent(chain [][]byte, root *x509.Certificate, intermediates []*x509.Certificate, now time.Time, allowExpired bool) (*x509.Certificate, string, error) {
	certs, err := parseChain(chain)
	if err != nil {
		return nil, "", err
	}
	leaf := certs[0]
	id, ok := AgentIdentity(leaf)
	if !ok {
		return nil, "", verr("not a DROOL agent certificate")
	}
	pool := x509.NewCertPool()
	for _, c := range intermediates {
		pool.AddCert(c)
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)
	at := now
	if allowExpired && now.After(leaf.NotAfter) {
		at = leaf.NotAfter.Add(-time.Second)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: pool, CurrentTime: at,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return nil, "", verr("agent certificate rejected: %v", err)
	}
	return leaf, id, nil
}

// VerifyAgentForRecovery accepts an agent certificate that chains to the
// root through any root-signed intermediate (including a revoked one) and
// may be expired. It is only for the recovery RPCs, which additionally
// require proof of possession of the identity key the controller has on
// record: that, not the certificate, authenticates the agent there. This
// lets agents move to a new intermediate after a compromise without
// re-enrolling (DESIGN §4.3.1), while a holder of the compromised
// intermediate (but not of the agents' identity keys) gains nothing.
func VerifyAgentForRecovery(chain [][]byte, root *x509.Certificate, now time.Time) (*x509.Certificate, string, error) {
	certs, err := parseChain(chain)
	if err != nil {
		return nil, "", err
	}
	pool := x509.NewCertPool()
	for _, c := range certs[1:] {
		pool.AddCert(c)
	}
	return VerifyAgent(chain, root, certs[1:], now, true)
}

func short(fp string) string {
	if len(fp) > 19 {
		return fp[:19]
	}
	return fp
}

// --- PEM helpers ---

// CertPEM encodes a certificate.
func CertPEM(c *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
}

// ParseCertsPEM parses one or more PEM certificates.
func ParseCertsPEM(b []byte) ([]*x509.Certificate, error) {
	var out []*x509.Certificate
	for {
		var blk *pem.Block
		blk, b = pem.Decode(b)
		if blk == nil {
			break
		}
		if blk.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, errors.New("no PEM certificate found")
	}
	return out, nil
}

// KeyPEM encodes an Ed25519 private key (PKCS#8).
func KeyPEM(k ed25519.PrivateKey) []byte {
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		panic(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// ParseKeyPEM parses an Ed25519 private key.
func ParseKeyPEM(b []byte) (ed25519.PrivateKey, error) {
	blk, _ := pem.Decode(b)
	if blk == nil {
		return nil, errors.New("no PEM private key found")
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, err
	}
	ek, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("not an Ed25519 private key")
	}
	return ek, nil
}

// MatchesKey reports whether cert was issued for key.
func MatchesKey(c *x509.Certificate, k ed25519.PrivateKey) bool {
	pub, ok := c.PublicKey.(ed25519.PublicKey)
	return ok && pub.Equal(k.Public())
}
