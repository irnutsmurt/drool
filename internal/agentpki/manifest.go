package agentpki

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

// Manifest is the root-signed list of trusted and revoked intermediates
// (DESIGN §4.3.1). Only the root key can create one; any controller can
// distribute it.
type Manifest struct {
	Format               int       `json:"format"` // 1
	Version              uint64    `json:"version"`
	IssuedAt             time.Time `json:"issued_at"`
	Root                 string    `json:"root"` // fingerprint of the root that signed it
	TrustedIntermediates []string  `json:"trusted_intermediates"`
	RevokedIntermediates []string  `json:"revoked_intermediates"`
}

// Trusted reports whether fp is a trusted, unrevoked intermediate.
func (m *Manifest) Trusted(fp string) bool {
	return slices.Contains(m.TrustedIntermediates, fp) && !m.Revoked(fp)
}

// Revoked reports whether fp is revoked.
func (m *Manifest) Revoked(fp string) bool { return slices.Contains(m.RevokedIntermediates, fp) }

// SignedManifest is a manifest's exact signed bytes and the root signature.
type SignedManifest struct {
	Payload   []byte `json:"payload"`
	Signature []byte `json:"signature"`
}

const manifestDomain = "drool-trust-manifest-v1\x00"

// SignManifest signs m with the root key. Signing requires the root
// private key: online on the controller, or offline via `drool agent-ca
// sign-manifest --root-key`.
func SignManifest(root *CA, m Manifest) (*SignedManifest, error) {
	if root.Key == nil {
		return nil, ErrRootOffline
	}
	if m.Version == 0 {
		return nil, errors.New("manifest version must be at least 1")
	}
	m.Format = 1
	m.Root = Fingerprint(root.Cert.Raw)
	slices.Sort(m.TrustedIntermediates)
	slices.Sort(m.RevokedIntermediates)
	for _, fp := range m.RevokedIntermediates {
		if slices.Contains(m.TrustedIntermediates, fp) {
			return nil, fmt.Errorf("intermediate %s is both trusted and revoked", fp)
		}
	}
	payload, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	sig := ed25519.Sign(root.Key, append([]byte(manifestDomain), payload...))
	return &SignedManifest{Payload: payload, Signature: sig}, nil
}

// VerifyManifest checks a signed manifest against the pinned root and
// returns it.
func VerifyManifest(sm *SignedManifest, root *x509.Certificate) (*Manifest, error) {
	if sm == nil || len(sm.Payload) == 0 {
		return nil, errors.New("empty trust manifest")
	}
	pub, ok := root.PublicKey.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("root CA key is not Ed25519")
	}
	if !ed25519.Verify(pub, append([]byte(manifestDomain), sm.Payload...), sm.Signature) {
		return nil, errors.New("trust manifest signature is not valid for the pinned root")
	}
	var m Manifest
	if err := json.Unmarshal(sm.Payload, &m); err != nil {
		return nil, fmt.Errorf("trust manifest: %w", err)
	}
	if m.Format != 1 {
		return nil, fmt.Errorf("unsupported trust manifest format %d", m.Format)
	}
	if m.Root != Fingerprint(root.Raw) {
		return nil, errors.New("trust manifest was issued for a different root")
	}
	if m.Version == 0 {
		return nil, errors.New("trust manifest has no version")
	}
	return &m, nil
}

// ErrManifestRollback is returned for a manifest older than the newest one
// already accepted.
var ErrManifestRollback = errors.New("trust manifest is older than the one already accepted (rollback refused)")

// Accept decides whether candidate replaces current (which may be nil):
// it must verify against the root and have a higher version. An equal
// version with identical bytes is a no-op; an equal version with different
// bytes is refused (a root never signs two manifests with one version).
func Accept(current *Manifest, currentRaw *SignedManifest, candidate *SignedManifest, root *x509.Certificate) (*Manifest, bool, error) {
	m, err := VerifyManifest(candidate, root)
	if err != nil {
		return nil, false, err
	}
	if current == nil {
		return m, true, nil
	}
	switch {
	case m.Version < current.Version:
		return nil, false, ErrManifestRollback
	case m.Version == current.Version:
		if currentRaw != nil && string(currentRaw.Payload) == string(candidate.Payload) {
			return current, false, nil
		}
		return nil, false, fmt.Errorf("conflicting trust manifests with version %d", m.Version)
	}
	return m, true, nil
}
