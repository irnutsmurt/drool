// Package agent is the DROOL agent (cmd/drool-agent): enrollment, the
// mTLS session with the controller, local execution under the capability
// ceiling, telemetry, the local operation log and self-updates
// (ARCHITECTURE-ALPHA §23).
package agent

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"drool/internal/agentpki"
	"drool/internal/agentrpc"
)

// Paths are where the agent keeps its files. Production uses Default();
// tests point everything into a temporary directory.
type Paths struct {
	Config      string // /etc/drool/agent.yaml (root-owned ceiling)
	ReleaseKeys string // /etc/drool/release-keys.d (local trust for releases)
	State       string // /var/lib/drool-agent
	Log         string // /var/log/drool-agent
	Install     string // /usr/libexec/drool-agent (versions, current, previous)
}

// Default returns the production paths.
func Default() Paths {
	return Paths{Config: "/etc/drool/agent.yaml", ReleaseKeys: "/etc/drool/release-keys.d",
		State: "/var/lib/drool-agent", Log: "/var/log/drool-agent", Install: "/usr/libexec/drool-agent"}
}

// Under returns paths rooted at dir (tests, unprivileged runs).
func Under(dir string) Paths {
	return Paths{Config: filepath.Join(dir, "etc/agent.yaml"), ReleaseKeys: filepath.Join(dir, "etc/release-keys.d"),
		State: filepath.Join(dir, "state"), Log: filepath.Join(dir, "log"), Install: filepath.Join(dir, "lib")}
}

// writeFileAtomic replaces name with data: temporary file, fsync, rename,
// fsync of the directory. A crash leaves either the old or the new file.
func writeFileAtomic(name string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(name)+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, name); err != nil {
		return err
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Identity is what enrollment established. It never changes afterwards
// (re-enrollment creates a new identity).
type Identity struct {
	IdentityID      string    `json:"identity_id"`
	HostID          string    `json:"host_id"`
	HostName        string    `json:"host_name"`
	ControllerEpoch string    `json:"controller_epoch"`
	EnrolledAt      time.Time `json:"enrolled_at"`
}

// Endpoints are the controller addresses the agent knows (DESIGN §6.5).
type Endpoints struct {
	Primary   string   `json:"primary"`
	Fallbacks []string `json:"fallbacks,omitempty"`
	// Candidate was proposed by the controller and is being tested; it
	// becomes primary only after a successful test and the controller's
	// commit.
	Candidate   string `json:"candidate,omitempty"`
	CandidateID string `json:"candidate_id,omitempty"`
	CandidateOK bool   `json:"candidate_ok,omitempty"`
}

// All returns the endpoints to try, primary first.
func (e Endpoints) All() []string {
	out := []string{}
	if e.Primary != "" {
		out = append(out, e.Primary)
	}
	for _, f := range e.Fallbacks {
		if f != e.Primary {
			out = append(out, f)
		}
	}
	return out
}

// Domains are the applied state versions (DESIGN §9). The agent stores
// what the controller sent; it never advances a revision itself.
type Domains map[string]agentrpc.StateVersion

// Store is the agent's persistent state in Paths.State. Methods are safe
// for concurrent use.
type Store struct {
	dir string
	mu  sync.Mutex
}

// OpenStore opens (and creates) the state directory with mode 0700.
func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func (s *Store) path(name string) string { return filepath.Join(s.dir, name) }

func (s *Store) readJSON(name string, v any) error {
	b, err := os.ReadFile(s.path(name))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func (s *Store) writeJSON(name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.path(name), b, 0o600)
}

// ErrNotEnrolled means there is no identity yet.
var ErrNotEnrolled = errors.New("this agent is not enrolled; run `drool-agent enroll`")

// IdentityKey loads the long-lived identity key, creating it if asked.
// The private key never leaves this file.
func (s *Store) IdentityKey(create bool) (ed25519.PrivateKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path("identity.key"))
	if err == nil {
		return agentpki.ParseKeyPEM(b)
	}
	if !errors.Is(err, os.ErrNotExist) || !create {
		return nil, err
	}
	_, priv := agentpki.NewKey()
	if err := writeFileAtomic(s.path("identity.key"), agentpki.KeyPEM(priv), 0o600); err != nil {
		return nil, err
	}
	return priv, nil
}

// Identity returns the enrollment record.
func (s *Store) Identity() (*Identity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var id Identity
	if err := s.readJSON("identity.json", &id); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotEnrolled
		}
		return nil, err
	}
	return &id, nil
}

// SaveEnrollment stores everything enrollment produced. The root is
// pinned here and never replaced by the controller afterwards.
func (s *Store) SaveEnrollment(id Identity, root *x509.Certificate, chain [][]byte, m *agentpki.SignedManifest, ep Endpoints) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := writeFileAtomic(s.path("root.pem"), agentpki.CertPEM(root), 0o600); err != nil {
		return err
	}
	if err := s.saveChainLocked(chain); err != nil {
		return err
	}
	if err := s.writeJSON("manifest.json", m); err != nil {
		return err
	}
	if err := s.writeJSON("endpoints.json", ep); err != nil {
		return err
	}
	return s.writeJSON("identity.json", id) // last: marks enrollment complete
}

// Root returns the pinned Agent Root CA.
func (s *Store) Root() (*x509.Certificate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path("root.pem"))
	if err != nil {
		return nil, err
	}
	cs, err := agentpki.ParseCertsPEM(b)
	if err != nil {
		return nil, err
	}
	return cs[0], nil
}

func (s *Store) saveChainLocked(chain [][]byte) error {
	var out []byte
	for _, der := range chain {
		c, err := x509.ParseCertificate(der)
		if err != nil {
			return err
		}
		out = append(out, agentpki.CertPEM(c)...)
	}
	return writeFileAtomic(s.path("cert.pem"), out, 0o600)
}

// SaveChain replaces the operational certificate (renewal, recovery).
func (s *Store) SaveChain(chain [][]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveChainLocked(chain)
}

// Chain returns the operational certificate chain (leaf first).
func (s *Store) Chain() ([]*x509.Certificate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path("cert.pem"))
	if err != nil {
		return nil, err
	}
	return agentpki.ParseCertsPEM(b)
}

// Manifest returns the newest accepted trust manifest.
func (s *Store) Manifest() (*agentpki.Manifest, *agentpki.SignedManifest, error) {
	root, err := s.Root()
	if err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var sm agentpki.SignedManifest
	if err := s.readJSON("manifest.json", &sm); err != nil {
		return nil, nil, err
	}
	m, err := agentpki.VerifyManifest(&sm, root)
	if err != nil {
		return nil, nil, fmt.Errorf("stored trust manifest: %w", err)
	}
	return m, &sm, nil
}

// OfferManifest accepts a newer manifest (verified against the pinned
// root) and persists it; older ones are refused (no rollback).
func (s *Store) OfferManifest(sm *agentpki.SignedManifest) (*agentpki.Manifest, bool, error) {
	root, err := s.Root()
	if err != nil {
		return nil, false, err
	}
	cur, curRaw, err := s.Manifest()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	m, changed, err := agentpki.Accept(cur, curRaw, sm, root)
	if err != nil || !changed {
		return m, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writeJSON("manifest.json", sm); err != nil {
		return nil, false, err
	}
	return m, true, nil
}

// Endpoints returns the known controller endpoints.
func (s *Store) Endpoints() (Endpoints, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var e Endpoints
	err := s.readJSON("endpoints.json", &e)
	return e, err
}

// SaveEndpoints persists endpoints.
func (s *Store) SaveEndpoints(e Endpoints) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeJSON("endpoints.json", e)
}

// Domains returns the applied state versions.
func (s *Store) Domains() (Domains, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := Domains{}
	if err := s.readJSON("domains.json", &d); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return d, nil
}

// ApplyDomain records a state version the controller sent, after the
// change it described was applied.
func (s *Store) ApplyDomain(v agentrpc.StateVersion) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := Domains{}
	if err := s.readJSON("domains.json", &d); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	d[v.Domain] = v
	return s.writeJSON("domains.json", d)
}

// Revocation is what a controller said when it revoked this identity.
type Revocation struct {
	Reason          string    `json:"reason"`
	At              time.Time `json:"at"`
	ControllerEpoch string    `json:"controller_epoch"`
}

// SetRevocation remembers a revocation (the first one counts).
func (s *Store) SetRevocation(r Revocation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var cur Revocation
	if s.readJSON("revoked.json", &cur) == nil {
		return nil
	}
	return s.writeJSON("revoked.json", r)
}

// Revocation returns the remembered revocation, if any.
func (s *Store) Revocation() *Revocation {
	s.mu.Lock()
	defer s.mu.Unlock()
	var r Revocation
	if s.readJSON("revoked.json", &r) != nil {
		return nil
	}
	return &r
}

// ClearRevocation forgets it: a controller moved this agent to its epoch
// knowing about it (an administrator accepted it).
func (s *Store) ClearRevocation() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.path("revoked.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// SetControllerEpoch moves the identity to a new controller epoch (the
// commit point of an epoch transition after a controller restore).
func (s *Store) SetControllerEpoch(epoch string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var id Identity
	if err := s.readJSON("identity.json", &id); err != nil {
		return err
	}
	id.ControllerEpoch = epoch
	return s.writeJSON("identity.json", id)
}

// Requested returns the controller's narrowing request (nil: none).
func (s *Store) Requested() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path("requested-caps.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

// SaveRequested persists the controller's narrowing request.
func (s *Store) SaveRequested(b []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeFileAtomic(s.path("requested-caps.json"), b, 0o600)
}
