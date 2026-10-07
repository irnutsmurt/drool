// Package agentsvc is the controller side of the DROOL agent
// (ARCHITECTURE-ALPHA §23): the Agent CA, enrollment, the mTLS gRPC
// endpoint and agent sessions, the agent driver (operations and files.FS),
// telemetry, certificate lifecycle, identity conflicts, updates and
// endpoint migration.
package agentsvc

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"drool/internal/agentpki"
	"drool/internal/ids"
	"drool/internal/secrets"
	"drool/internal/store"
	"drool/internal/store/db"
)

// PKI is the controller's view of the Agent PKI. The root key is usually
// offline; the intermediate key is online (sealed with the master key in
// the database) so the controller can restart unattended (DESIGN §4.3).
type PKI struct {
	st  *store.Store
	box *secrets.Box
	now func() time.Time

	mu       sync.RWMutex
	root     *agentpki.CA // Key nil when offline
	rootRow  db.AgentCa
	inter    *agentpki.CA
	interRow db.AgentCa
	// trusted are the intermediates whose agent certificates are accepted
	// (active and retired, never revoked).
	trusted   []*x509.Certificate
	manifest  *agentpki.SignedManifest
	manVer    uint64
	server    *tls.Certificate
	serverExp time.Time
	hosts     []string
}

func kindAAD(kind string) string { return "agent-ca-" + kind }

func (p *PKI) seal(id, kind string, key ed25519.PrivateKey) ([]byte, []byte, int64, error) {
	ct, nonce, ver, err := p.box.Seal(id, kindAAD(kind), agentpki.KeyPEM(key))
	return ct, nonce, int64(ver), err
}

func (p *PKI) open(r db.AgentCa) (*agentpki.CA, error) {
	c, err := x509.ParseCertificate(r.CertDer)
	if err != nil {
		return nil, err
	}
	ca := &agentpki.CA{Cert: c}
	if len(r.KeyCiphertext) > 0 {
		pemKey, err := p.box.Open(r.ID, kindAAD(r.Kind), r.KeyCiphertext, r.KeyNonce, int(r.KeyVersion.Int64))
		if err != nil {
			return nil, fmt.Errorf("agent CA %s key: %w", r.Kind, err)
		}
		if ca.Key, err = agentpki.ParseKeyPEM(pemKey); err != nil {
			return nil, err
		}
	}
	return ca, nil
}

// OpenPKI loads the Agent PKI, creating it on first start: root,
// intermediate and trust manifest 1. created reports a first start (the UI
// then urges exporting the root key).
func OpenPKI(ctx context.Context, st *store.Store, box *secrets.Box, name string) (*PKI, bool, error) {
	p := &PKI{st: st, box: box, now: time.Now}
	created := false
	if _, err := st.R.GetAgentRoot(ctx); errors.Is(err, sql.ErrNoRows) {
		if err := p.create(ctx, name); err != nil {
			return nil, false, err
		}
		created = true
	} else if err != nil {
		return nil, false, err
	}
	return p, created, p.load(ctx)
}

func (p *PKI) create(ctx context.Context, name string) error {
	now := p.now()
	root, err := agentpki.NewRoot(name, now)
	if err != nil {
		return err
	}
	inter, err := agentpki.NewIntermediate(root, "1", now)
	if err != nil {
		return err
	}
	sm, err := agentpki.SignManifest(root, agentpki.Manifest{Version: 1, IssuedAt: now.UTC(),
		TrustedIntermediates: []string{agentpki.Fingerprint(inter.Cert.Raw)}})
	if err != nil {
		return err
	}
	return p.st.InTx(ctx, func(q *db.Queries) error {
		for _, c := range []struct {
			kind string
			ca   *agentpki.CA
		}{{"root", root}, {"intermediate", inter}} {
			id := ids.New("aca")
			ct, nonce, ver, err := p.seal(id, c.kind, c.ca.Key)
			if err != nil {
				return err
			}
			if err := q.InsertAgentCA(ctx, db.InsertAgentCAParams{ID: id, Kind: c.kind, CertDer: c.ca.Cert.Raw,
				Fingerprint: agentpki.Fingerprint(c.ca.Cert.Raw), KeyCiphertext: ct, KeyNonce: nonce,
				KeyVersion: sql.NullInt64{Int64: ver, Valid: true}, Status: "active",
				NotAfter: c.ca.Cert.NotAfter.UnixMilli(), CreatedAt: now.UnixMilli()}); err != nil {
				return err
			}
		}
		return q.InsertTrustManifest(ctx, db.InsertTrustManifestParams{Version: 1, Payload: sm.Payload, Signature: sm.Signature, CreatedAt: now.UnixMilli()})
	})
}

func (p *PKI) load(ctx context.Context) error {
	rows, err := p.st.R.ListAgentCA(ctx)
	if err != nil {
		return err
	}
	m, err := p.st.R.LatestTrustManifest(ctx)
	if err != nil {
		return fmt.Errorf("no trust manifest: %w", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.trusted = nil
	p.inter = nil
	for _, r := range rows {
		ca, err := p.open(r)
		if err != nil {
			return err
		}
		switch {
		case r.Kind == "root":
			p.root, p.rootRow = ca, r
		case r.Status == "active":
			p.inter, p.interRow = ca, r
			p.trusted = append(p.trusted, ca.Cert)
		case r.Status == "retired":
			p.trusted = append(p.trusted, ca.Cert)
		}
	}
	if p.root == nil || p.inter == nil || p.inter.Key == nil {
		return ErrPKIUnusable
	}
	p.manifest = &agentpki.SignedManifest{Payload: m.Payload, Signature: m.Signature}
	mm, err := agentpki.VerifyManifest(p.manifest, p.root.Cert)
	if err != nil {
		return fmt.Errorf("stored trust manifest: %w", err)
	}
	if !mm.Trusted(agentpki.Fingerprint(p.inter.Cert.Raw)) {
		return errors.New("the active intermediate CA is not in the newest trust manifest; agents would reject this controller")
	}
	p.manVer = mm.Version
	p.server = nil
	return nil
}

// ErrPKIUnusable: the intermediate CA key is not available (it was
// discarded with the credentials after a lost master key). Issue a new
// intermediate with the offline root key: drool agent-ca rotate --root-key.
var ErrPKIUnusable = errors.New("the Agent PKI has no usable intermediate CA key; issue a new intermediate with the offline root key: drool agent-ca rotate --root-key FILE")

// RecoverIntermediate issues a new intermediate with a supplied root key
// when the PKI can't load (ErrPKIUnusable), revoking intermediates whose
// keys are gone. Agents move to it through certificate recovery.
func RecoverIntermediate(ctx context.Context, st *store.Store, box *secrets.Box, rootKeyPEM []byte) (uint64, error) {
	rootRow, err := st.R.GetAgentRoot(ctx)
	if err != nil {
		return 0, err
	}
	rootCert, err := x509.ParseCertificate(rootRow.CertDer)
	if err != nil {
		return 0, err
	}
	k, err := agentpki.ParseKeyPEM(rootKeyPEM)
	if err != nil {
		return 0, err
	}
	if !agentpki.MatchesKey(rootCert, k) {
		return 0, errors.New("that private key does not belong to this controller's Agent Root CA")
	}
	root := &agentpki.CA{Cert: rootCert, Key: k}
	latest, err := st.R.LatestTrustManifest(ctx)
	if err != nil {
		return 0, err
	}
	cur, err := agentpki.VerifyManifest(&agentpki.SignedManifest{Payload: latest.Payload, Signature: latest.Signature}, rootCert)
	if err != nil {
		return 0, err
	}
	rows, err := st.R.ListAgentCA(ctx)
	if err != nil {
		return 0, err
	}
	now := time.Now()
	inter, err := agentpki.NewIntermediate(root, fmt.Sprint(cur.Version+1), now)
	if err != nil {
		return 0, err
	}
	m := agentpki.Manifest{Version: cur.Version + 1, IssuedAt: now.UTC(), TrustedIntermediates: []string{agentpki.Fingerprint(inter.Cert.Raw)},
		RevokedIntermediates: append([]string(nil), cur.RevokedIntermediates...)}
	for _, r := range rows {
		if r.Kind == "intermediate" && r.Status != "revoked" {
			m.RevokedIntermediates = append(m.RevokedIntermediates, r.Fingerprint)
		}
	}
	sm, err := agentpki.SignManifest(root, m)
	if err != nil {
		return 0, err
	}
	p := &PKI{st: st, box: box, now: time.Now}
	err = st.InTx(ctx, func(q *db.Queries) error {
		for _, r := range rows {
			if r.Kind == "intermediate" && r.Status != "revoked" {
				if err := q.SetAgentCAStatus(ctx, db.SetAgentCAStatusParams{Status: "revoked", ID: r.ID}); err != nil {
					return err
				}
			}
		}
		id := ids.New(ids.AgentCA)
		ct, nonce, ver, err := p.seal(id, "intermediate", inter.Key)
		if err != nil {
			return err
		}
		if err := q.InsertAgentCA(ctx, db.InsertAgentCAParams{ID: id, Kind: "intermediate", CertDer: inter.Cert.Raw,
			Fingerprint: agentpki.Fingerprint(inter.Cert.Raw), KeyCiphertext: ct, KeyNonce: nonce, KeyVersion: sql.NullInt64{Int64: ver, Valid: true},
			Status: "active", NotAfter: inter.Cert.NotAfter.UnixMilli(), CreatedAt: now.UnixMilli()}); err != nil {
			return err
		}
		return q.InsertTrustManifest(ctx, db.InsertTrustManifestParams{Version: int64(m.Version), Payload: sm.Payload, Signature: sm.Signature, CreatedAt: now.UnixMilli()})
	})
	return m.Version, err
}

// SetHosts records the names agents use (for the server certificate).
func (p *PKI) SetHosts(hosts []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hosts = hosts
	p.server = nil
}

// ServerCertificate returns the controller's agent-endpoint certificate,
// issued from the active intermediate in memory and renewed long before
// it expires. It is unrelated to the browser HTTPS certificate.
func (p *PKI) ServerCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.server != nil && p.now().Before(p.serverExp) {
		return p.server, nil
	}
	pub, priv := agentpki.NewKey()
	c, err := agentpki.IssueController(p.inter, pub, p.hosts, p.now())
	if err != nil {
		return nil, err
	}
	p.server = &tls.Certificate{Certificate: [][]byte{c.Raw, p.inter.Cert.Raw}, PrivateKey: priv, Leaf: c}
	p.serverExp = c.NotBefore.Add(c.NotAfter.Sub(c.NotBefore) / 2)
	return p.server, nil
}

// IssueAgent issues an operational certificate; the chain includes the
// intermediate.
func (p *PKI) IssueAgent(identityID string, pub ed25519.PublicKey, lifetime time.Duration) ([][]byte, *x509.Certificate, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	c, err := agentpki.IssueAgent(p.inter, identityID, pub, lifetime, p.now())
	if err != nil {
		return nil, nil, err
	}
	return [][]byte{c.Raw, p.inter.Cert.Raw}, c, nil
}

// VerifyAgent checks an agent certificate chain against the root and the
// controller's trusted intermediates.
func (p *PKI) VerifyAgent(chain [][]byte, allowExpired bool) (*x509.Certificate, string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return agentpki.VerifyAgent(chain, p.root.Cert, p.trusted, p.now(), allowExpired)
}

// VerifyAgentForRecovery: see agentpki.VerifyAgentForRecovery.
func (p *PKI) VerifyAgentForRecovery(chain [][]byte) (*x509.Certificate, string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return agentpki.VerifyAgentForRecovery(chain, p.root.Cert, p.now())
}

// Root returns the root certificate.
func (p *PKI) Root() *x509.Certificate {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.root.Cert
}

// RootFingerprint is what enrollment tokens pin.
func (p *PKI) RootFingerprint() string { return agentpki.Fingerprint(p.Root().Raw) }

// Manifest returns the newest signed trust manifest.
func (p *PKI) Manifest() (*agentpki.SignedManifest, uint64) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.manifest, p.manVer
}

// RootOnline reports whether the root private key is on this controller
// (a persistent security warning while it is).
func (p *PKI) RootOnline() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.root.Key != nil
}

// Status describes the PKI for the UI.
type Status struct {
	RootFingerprint         string     `json:"root_fingerprint"`
	RootNotAfter            time.Time  `json:"root_not_after"`
	RootKeyOnline           bool       `json:"root_key_online"`
	RootKeyRemovedAt        *time.Time `json:"root_key_removed_at,omitempty"`
	IntermediateFingerprint string     `json:"intermediate_fingerprint"`
	IntermediateNotAfter    time.Time  `json:"intermediate_not_after"`
	ManifestVersion         uint64     `json:"manifest_version"`
	TrustedIntermediates    []string   `json:"trusted_intermediates"`
	RevokedIntermediates    []string   `json:"revoked_intermediates"`
}

// Status returns the PKI state.
func (p *PKI) Status() Status {
	p.mu.RLock()
	defer p.mu.RUnlock()
	s := Status{RootFingerprint: agentpki.Fingerprint(p.root.Cert.Raw), RootNotAfter: p.root.Cert.NotAfter,
		RootKeyOnline: p.root.Key != nil, IntermediateFingerprint: agentpki.Fingerprint(p.inter.Cert.Raw),
		IntermediateNotAfter: p.inter.Cert.NotAfter, ManifestVersion: p.manVer}
	if p.rootRow.KeyRemovedAt.Valid {
		t := time.UnixMilli(p.rootRow.KeyRemovedAt.Int64)
		s.RootKeyRemovedAt = &t
	}
	if m, err := agentpki.VerifyManifest(p.manifest, p.root.Cert); err == nil {
		s.TrustedIntermediates, s.RevokedIntermediates = m.TrustedIntermediates, m.RevokedIntermediates
	}
	return s
}

// ExportRoot returns the root certificate and private key (PEM) so the
// administrator can store them offline. It needs the key to be online.
func (p *PKI) ExportRoot() (certPEM, keyPEM []byte, err error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.root.Key == nil {
		return nil, nil, agentpki.ErrRootOffline
	}
	return agentpki.CertPEM(p.root.Cert), agentpki.KeyPEM(p.root.Key), nil
}

// RemoveRootKey deletes the root private key from the controller. From
// then on it is not in the database or any later backup.
func (p *PKI) RemoveRootKey(ctx context.Context) error {
	p.mu.RLock()
	id := p.rootRow.ID
	p.mu.RUnlock()
	if err := p.st.W.RemoveAgentCAKey(ctx, db.RemoveAgentCAKeyParams{KeyRemovedAt: sql.NullInt64{Int64: p.now().UnixMilli(), Valid: true}, ID: id}); err != nil {
		return err
	}
	return p.load(ctx)
}

// rootWith returns the root CA with a key: the online one, or one supplied
// for this operation only (offline root brought in temporarily; it is
// verified against the root certificate and not stored).
func (p *PKI) rootWith(keyPEM []byte) (*agentpki.CA, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(keyPEM) == 0 {
		if p.root.Key == nil {
			return nil, agentpki.ErrRootOffline
		}
		return p.root, nil
	}
	k, err := agentpki.ParseKeyPEM(keyPEM)
	if err != nil {
		return nil, err
	}
	if !agentpki.MatchesKey(p.root.Cert, k) {
		return nil, errors.New("that private key does not belong to this controller's Agent Root CA")
	}
	return &agentpki.CA{Cert: p.root.Cert, Key: k}, nil
}

// Rotate creates a new intermediate and manifest N+1 that trusts it. With
// revokeOld (compromise), the old intermediate is revoked: agents stop
// accepting it and its agent certificates are no longer accepted here
// either; otherwise it stays trusted until it expires (planned rotation).
func (p *PKI) Rotate(ctx context.Context, rootKeyPEM []byte, revokeOld bool) (uint64, error) {
	root, err := p.rootWith(rootKeyPEM)
	if err != nil {
		return 0, err
	}
	p.mu.RLock()
	oldRow, oldFP := p.interRow, agentpki.Fingerprint(p.inter.Cert.Raw)
	cur, _ := agentpki.VerifyManifest(p.manifest, p.root.Cert)
	p.mu.RUnlock()
	now := p.now()
	inter, err := agentpki.NewIntermediate(root, fmt.Sprint(cur.Version+1), now)
	if err != nil {
		return 0, err
	}
	newFP := agentpki.Fingerprint(inter.Cert.Raw)
	m := agentpki.Manifest{Version: cur.Version + 1, IssuedAt: now.UTC(), TrustedIntermediates: []string{newFP},
		RevokedIntermediates: append([]string(nil), cur.RevokedIntermediates...)}
	if revokeOld {
		m.RevokedIntermediates = append(m.RevokedIntermediates, oldFP)
	} else {
		for _, fp := range cur.TrustedIntermediates {
			if fp != newFP {
				m.TrustedIntermediates = append(m.TrustedIntermediates, fp)
			}
		}
	}
	sm, err := agentpki.SignManifest(root, m)
	if err != nil {
		return 0, err
	}
	err = p.st.InTx(ctx, func(q *db.Queries) error {
		st := "retired"
		if revokeOld {
			st = "revoked"
		}
		if err := q.SetAgentCAStatus(ctx, db.SetAgentCAStatusParams{Status: st, ID: oldRow.ID}); err != nil {
			return err
		}
		id := ids.New("aca")
		ct, nonce, ver, err := p.seal(id, "intermediate", inter.Key)
		if err != nil {
			return err
		}
		if err := q.InsertAgentCA(ctx, db.InsertAgentCAParams{ID: id, Kind: "intermediate", CertDer: inter.Cert.Raw,
			Fingerprint: newFP, KeyCiphertext: ct, KeyNonce: nonce, KeyVersion: sql.NullInt64{Int64: ver, Valid: true},
			Status: "active", NotAfter: inter.Cert.NotAfter.UnixMilli(), CreatedAt: now.UnixMilli()}); err != nil {
			return err
		}
		return q.InsertTrustManifest(ctx, db.InsertTrustManifestParams{Version: int64(m.Version), Payload: sm.Payload, Signature: sm.Signature, CreatedAt: now.UnixMilli()})
	})
	if err != nil {
		return 0, err
	}
	return m.Version, p.load(ctx)
}

// ImportManifest stores a manifest signed elsewhere (offline root) after
// verifying it against the root; it must be newer than the current one.
func (p *PKI) ImportManifest(ctx context.Context, sm *agentpki.SignedManifest) (uint64, error) {
	p.mu.RLock()
	cur, _ := agentpki.VerifyManifest(p.manifest, p.root.Cert)
	root := p.root.Cert
	p.mu.RUnlock()
	m, changed, err := agentpki.Accept(cur, nil, sm, root)
	if err != nil {
		return 0, err
	}
	if !changed {
		return m.Version, nil
	}
	if err := p.st.W.InsertTrustManifest(ctx, db.InsertTrustManifestParams{Version: int64(m.Version), Payload: sm.Payload,
		Signature: sm.Signature, CreatedAt: p.now().UnixMilli()}); err != nil {
		return 0, err
	}
	return m.Version, p.load(ctx)
}
