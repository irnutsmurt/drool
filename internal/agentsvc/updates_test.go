package agentsvc

import (
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"

	"drool/internal/agentrpc/pb"
	"drool/internal/release"
	"drool/internal/store/db"
)

// An agent's refusal is a skip, not a failed update: 1.0 agents say so,
// and older ones are known by the words they have always used. A real
// failure (a bad signature, a broken download) is never taken for one.
func TestRefusalOf(t *testing.T) {
	for _, c := range []struct {
		u    *pb.UpdateReport
		want string
	}{
		{&pb.UpdateReport{State: "failed", Refusal: "updates_denied", Version: "v1.1.0"}, "updates_denied"},
		{&pb.UpdateReport{State: "failed", Detail: "release verification failed: the release is signed by a key this agent does not trust (key id 1a2b)"}, "untrusted_key"},
		{&pb.UpdateReport{State: "failed", Detail: "updates are disabled on this host (updates.policy: deny in agent.yaml)"}, "updates_denied"},
		{&pb.UpdateReport{State: "failed", Detail: "version v1.2.0 already failed its health check on this host; not trying it again automatically"}, "failed_before"},
		{&pb.UpdateReport{State: "failed", Detail: "release v1.0.0 is older than the running v1.1.0; downgrades are refused (updates.allow_downgrade in agent.yaml)"}, "downgrade"},
		{&pb.UpdateReport{State: "failed", Detail: "another update is still in progress"}, "busy"},
		{&pb.UpdateReport{State: "failed", Detail: "release verification failed: release manifest signature is invalid"}, ""},
		{&pb.UpdateReport{State: "failed", Detail: "downloaded binary rejected: binary sha256 … does not match"}, ""},
		{&pb.UpdateReport{State: "rolled_back", Version: "v1.2.0", Detail: "v1.2.0 did not become healthy"}, ""},
		{&pb.UpdateReport{State: "failed", Version: "v1.2.0", Detail: "does not trust"}, ""}, // after it trusted the manifest: not a refusal
	} {
		if got := refusalOf(c.u); got != c.want {
			t.Errorf("%q (%s): %q, want %q", c.u.Detail, c.u.State, got, c.want)
		}
	}
}

func TestChannelsAndPolicy(t *testing.T) {
	for _, c := range []struct {
		policy, rel string
		want        bool
	}{{"stable", "stable", true}, {"stable", "prerelease", false}, {"prerelease", "stable", true}, {"prerelease", "prerelease", true},
		{"beta", "beta", true}, {"beta", "stable", false}, {"stable", "development", false}} {
		if ChannelIncludes(c.policy, c.rel) != c.want {
			t.Errorf("%s takes %s: %v", c.policy, c.rel, !c.want)
		}
	}
	p := DefaultUpdatePolicy()
	if p.Policy != "automatic" || p.Channel != "stable" || p.Canary != 1 || p.BatchSize != 5 || p.MaxFailures != 1 || p.HealthWindowS != 120 {
		t.Errorf("default %+v", p)
	}
	for _, bad := range []UpdatePolicy{{Policy: "auto"}, {Policy: "manual", Channel: "Not A Channel", Canary: 1, BatchSize: 1, MaxFailures: 1, HealthWindowS: 60},
		{Policy: "manual", Version: "newest", Canary: 1, BatchSize: 1, MaxFailures: 1, HealthWindowS: 60},
		{Policy: "manual", Canary: 0, BatchSize: 1, MaxFailures: 1, HealthWindowS: 60}} {
		if bad.normalize() == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

// Who can take which release, before anything is offered.
func TestBlocker(t *testing.T) {
	pub, privPEM, _ := release.GenerateKey()
	priv, _ := release.ParsePrivateKey(privPEM)
	defer release.SetOfficialKeysForTest([]release.PublicKey{{Key: pub}})()
	official := release.KeyID(pub)
	rel := func(key ed25519.PrivateKey, m release.Manifest) *db.AgentRelease {
		m.OS, m.Arch, m.ProtocolMin, m.ProtocolMax = "linux", "amd64", 1, 1
		if m.MinFrom != "" || m.Version == "" {
			m.Version = "v1.4.0"
		}
		s, err := release.Sign(key, m, strings.NewReader("bin"))
		if err != nil {
			t.Fatal(err)
		}
		return &db.AgentRelease{Version: m.Version, Os: "linux", Arch: "amd64", KeyID: s.KeyID, ManifestPayload: s.Payload}
	}
	off := rel(priv, release.Manifest{Version: "v1.4.0"})
	_, customPEM, _ := release.GenerateKey()
	cpriv, _ := release.ParsePrivateKey(customPEM)
	custom := rel(cpriv, release.Manifest{Version: "v1.4.0"})
	id := db.AgentIdentity{AgentVersion: "v1.3.0", Os: "linux", Arch: "amd64", Protocol: 1}
	trusting := &ReleaseTrust{KeyIDs: []string{official}, Official: true}
	for _, c := range []struct {
		name  string
		id    db.AgentIdentity
		trust *ReleaseTrust
		rel   *db.AgentRelease
		retry bool
		want  string // "" can take it
	}{
		{"official, trusted", id, trusting, off, false, ""},
		{"no release for the platform", id, trusting, nil, false, "no v1.4.0 release for linux/amd64"},
		{"updates denied", id, &ReleaseTrust{KeyIDs: []string{official}, UpdatesDenied: true}, off, false, "updates.policy: deny"},
		{"official denied", id, &ReleaseTrust{OfficialDenied: true}, off, false, "updates.official: deny"},
		{"built without the key", id, &ReleaseTrust{}, off, false, "doesn't have the DROOL release key"},
		{"custom, untrusted", id, trusting, custom, false, "doesn't trust the key that signed v1.4.0"},
		{"pre-1.0 Shepherd: unknown, offered", id, nil, off, false, ""},
		{"upgrade path", id, trusting, rel(priv, release.Manifest{MinFrom: "v1.3.5"}), false, "updates only from v1.3.5"},
		{"failed here", db.AgentIdentity{AgentVersion: "v1.3.0", Os: "linux", Arch: "amd64", UpdateState: "rolled_back", UpdateVersion: "v1.4.0", UpdateDetail: "x"}, trusting, off, false, "failed on this host before"},
		{"failed here, retried", db.AgentIdentity{AgentVersion: "v1.3.0", Os: "linux", Arch: "amd64", UpdateState: "rolled_back", UpdateVersion: "v1.4.0"}, trusting, off, true, ""},
	} {
		got := Blocker(c.id, c.trust, "v1.4.0", c.rel, c.retry)
		if c.want == "" && got != "" || c.want != "" && !strings.Contains(got, c.want) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	// The trust a hello reports is what the identity keeps.
	b, _ := json.Marshal(trustFromHello(&pb.ReleaseTrust{KeyIds: []string{official}, Official: true}))
	if tr := IdentityTrust(db.AgentIdentity{ReleaseTrustJson: string(b)}); tr == nil || !tr.Official || tr.KeyIDs[0] != official {
		t.Errorf("trust round trip: %+v", tr)
	}
	if IdentityTrust(db.AgentIdentity{}) != nil {
		t.Error("a Shepherd that says nothing has a trust")
	}
}
