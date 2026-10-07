package agentpki

import (
	"crypto/x509"
	"errors"
	"strings"
	"testing"
	"time"
)

type fixture struct {
	root, inter *CA
	man         *Manifest
	signed      *SignedManifest
	now         time.Time
}

func setup(t *testing.T) *fixture {
	t.Helper()
	now := time.Now()
	root, err := NewRoot("test", now)
	if err != nil {
		t.Fatal(err)
	}
	inter, err := NewIntermediate(root, "1", now)
	if err != nil {
		t.Fatal(err)
	}
	sm, err := SignManifest(root, Manifest{Version: 1, IssuedAt: now, TrustedIntermediates: []string{Fingerprint(inter.Cert.Raw)}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := VerifyManifest(sm, root.Cert)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{root: root, inter: inter, man: m, signed: sm, now: now}
}

func controllerChain(t *testing.T, inter *CA, now time.Time) [][]byte {
	pub, _ := NewKey()
	c, err := IssueController(inter, pub, []string{"drool.example", "10.0.0.1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	return [][]byte{c.Raw, inter.Cert.Raw}
}

func TestControllerVerification(t *testing.T) {
	f := setup(t)
	if _, err := VerifyController(controllerChain(t, f.inter, f.now), f.root.Cert, f.man, f.now); err != nil {
		t.Fatalf("valid controller rejected: %v", err)
	}

	// An agent certificate can't pose as the controller.
	pub, _ := NewKey()
	ac, _ := IssueAgent(f.inter, "aid_1", pub, AgentLifetime, f.now)
	if _, err := VerifyController([][]byte{ac.Raw, f.inter.Cert.Raw}, f.root.Cert, f.man, f.now); err == nil {
		t.Error("agent certificate accepted as controller")
	}

	// A different root (an attacker's own PKI) is rejected.
	other := setup(t)
	if _, err := VerifyController(controllerChain(t, other.inter, f.now), f.root.Cert, f.man, f.now); err == nil {
		t.Error("controller from another PKI accepted")
	}

	// An intermediate that chains to the root but isn't in the manifest.
	rogue, _ := NewIntermediate(f.root, "rogue", f.now)
	if _, err := VerifyController(controllerChain(t, rogue, f.now), f.root.Cert, f.man, f.now); err == nil ||
		!strings.Contains(err.Error(), "not in trust manifest") {
		t.Errorf("unlisted intermediate: %v", err)
	}

	// Rotation: manifest 2 trusts the new intermediate and revokes the old.
	newInter, _ := NewIntermediate(f.root, "2", f.now)
	sm2, err := SignManifest(f.root, Manifest{Version: 2, IssuedAt: f.now,
		TrustedIntermediates: []string{Fingerprint(newInter.Cert.Raw)},
		RevokedIntermediates: []string{Fingerprint(f.inter.Cert.Raw)}})
	if err != nil {
		t.Fatal(err)
	}
	m2, changed, err := Accept(f.man, f.signed, sm2, f.root.Cert)
	if err != nil || !changed {
		t.Fatalf("accept manifest 2: %v %v", changed, err)
	}
	if _, err := VerifyController(controllerChain(t, f.inter, f.now), f.root.Cert, m2, f.now); err == nil ||
		!strings.Contains(err.Error(), "revoked") {
		t.Errorf("revoked intermediate accepted: %v", err)
	}
	if _, err := VerifyController(controllerChain(t, newInter, f.now), f.root.Cert, m2, f.now); err != nil {
		t.Errorf("rotated intermediate rejected: %v", err)
	}

	// Rollback to manifest 1 is refused.
	if _, _, err := Accept(m2, sm2, f.signed, f.root.Cert); !errors.Is(err, ErrManifestRollback) {
		t.Errorf("rollback: %v", err)
	}
	// Same version, same bytes: no change. Same version, different bytes: refused.
	if _, changed, err := Accept(m2, sm2, sm2, f.root.Cert); err != nil || changed {
		t.Errorf("same manifest: %v %v", changed, err)
	}
	sm2b, _ := SignManifest(f.root, Manifest{Version: 2, IssuedAt: f.now.Add(time.Second), TrustedIntermediates: []string{Fingerprint(f.inter.Cert.Raw)}})
	if _, _, err := Accept(m2, sm2, sm2b, f.root.Cert); err == nil {
		t.Error("conflicting manifest with the same version accepted")
	}
}

func TestManifestSignature(t *testing.T) {
	f := setup(t)
	bad := *f.signed
	bad.Payload = []byte(strings.Replace(string(f.signed.Payload), `"version":1`, `"version":9`, 1))
	if _, err := VerifyManifest(&bad, f.root.Cert); err == nil {
		t.Error("tampered manifest accepted")
	}
	other := setup(t)
	if _, err := VerifyManifest(other.signed, f.root.Cert); err == nil {
		t.Error("manifest from another root accepted")
	}
	// A controller holding only the intermediate can't sign a manifest.
	if _, err := SignManifest(&CA{Cert: f.root.Cert}, Manifest{Version: 5}); !errors.Is(err, ErrRootOffline) {
		t.Errorf("signing without the root key: %v", err)
	}
}

func TestAgentVerification(t *testing.T) {
	f := setup(t)
	pub, _ := NewKey()
	c, err := IssueAgent(f.inter, "aid_7", pub, AgentLifetime, f.now)
	if err != nil {
		t.Fatal(err)
	}
	chain := [][]byte{c.Raw, f.inter.Cert.Raw}
	inters := []*x509.Certificate{f.inter.Cert}
	_, id, err := VerifyAgent(chain, f.root.Cert, inters, f.now, false)
	if err != nil || id != "aid_7" {
		t.Fatalf("valid agent: %q %v", id, err)
	}
	later := f.now.Add(AgentLifetime + 24*time.Hour)
	if _, _, err := VerifyAgent(chain, f.root.Cert, inters, later, false); err == nil {
		t.Error("expired agent certificate accepted for normal use")
	}
	if _, id, err := VerifyAgent(chain, f.root.Cert, inters, later, true); err != nil || id != "aid_7" {
		t.Errorf("expired agent certificate rejected for recovery: %v", err)
	}
	// A controller certificate can't pose as an agent.
	cc := controllerChain(t, f.inter, f.now)
	if _, _, err := VerifyAgent(cc, f.root.Cert, inters, f.now, false); err == nil {
		t.Error("controller certificate accepted as agent")
	}
	// An agent certificate from a retired intermediate is rejected once
	// that intermediate is no longer in the controller's set.
	other, _ := NewIntermediate(f.root, "x", f.now)
	if _, _, err := VerifyAgent(chain, f.root.Cert, []*x509.Certificate{other.Cert}, f.now, false); err == nil {
		t.Error("agent from an untrusted intermediate accepted")
	}
}

func TestPEMRoundTrip(t *testing.T) {
	f := setup(t)
	k, err := ParseKeyPEM(KeyPEM(f.inter.Key))
	if err != nil || !k.Equal(f.inter.Key) {
		t.Fatalf("key round trip: %v", err)
	}
	cs, err := ParseCertsPEM(append(CertPEM(f.inter.Cert), CertPEM(f.root.Cert)...))
	if err != nil || len(cs) != 2 || !MatchesKey(cs[0], f.inter.Key) {
		t.Fatalf("certs round trip: %v", err)
	}
}
