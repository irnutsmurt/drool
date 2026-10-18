package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"drool/internal/agent"
	"drool/internal/agentcaps"
	"drool/internal/agentsvc"
	"drool/internal/auth"
	"drool/internal/release"
	"drool/internal/version"
)

// Shepherd updates for 1.0 (ARCHITECTURE-ALPHA §42), in one process: the
// official release check against a release source the test controls, the
// update overview, and rollouts to real in-process Shepherds that verify
// and stage what they're offered (only the helper that would switch this
// machine's Shepherd is replaced).

// officialSource plays the DROOL project: a release key built into this
// test's DROOL and Shepherds, the Shepherd binary this controller ships (a
// script that prints its version, which is all a Shepherd runs before the
// switch), and a release source with the signed descriptions.
type officialSource struct {
	t      *testing.T
	ver    string
	priv   ed25519.PrivateKey
	keyID  string
	binDir string
	bin    []byte
	srv    *httptest.Server

	mu     sync.Mutex
	files  map[string][]byte
	latest string
	hits   int
}

func newOfficialSource(t *testing.T, ver, channel string) *officialSource {
	t.Helper()
	pub, privPEM, _ := release.GenerateKey()
	priv, _ := release.ParsePrivateKey(privPEM)
	t.Cleanup(release.SetOfficialKeysForTest([]release.PublicKey{{Key: pub, Name: "DROOL (test)"}}))
	oldV, oldC := version.Version, version.Channel
	version.Version, version.Channel = ver, channel
	t.Cleanup(func() { version.Version, version.Channel = oldV, oldC })
	o := &officialSource{t: t, ver: ver, priv: priv, keyID: release.KeyID(pub), binDir: t.TempDir(), files: map[string][]byte{}}
	o.bin = []byte("#!/bin/sh\necho " + ver + "\n")
	if err := os.WriteFile(filepath.Join(o.binDir, "drool-agent-linux-"+runtime.GOARCH), o.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	o.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o.mu.Lock()
		defer o.mu.Unlock()
		o.hits++
		if r.URL.Path == "/releases/latest" {
			loc := "/releases"
			if o.latest != "" {
				loc = "/releases/tag/" + o.latest
			}
			http.Redirect(w, r, loc, http.StatusFound)
			return
		}
		if b, ok := o.files[r.URL.Path]; ok {
			w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(o.srv.Close)
	return o
}

func (o *officialSource) url() string { return o.srv.URL + "/releases" }

// signed is a release description of bin, signed with key.
func (o *officialSource) signed(key ed25519.PrivateKey, m release.Manifest, bin []byte) []byte {
	o.t.Helper()
	if m.OS == "" {
		m.OS, m.Arch = "linux", runtime.GOARCH
	}
	if m.ProtocolMin == 0 {
		m.ProtocolMin, m.ProtocolMax = 1, 1
	}
	s, err := release.Sign(key, m, bytes.NewReader(bin))
	if err != nil {
		o.t.Fatal(err)
	}
	b, _ := json.Marshal(s)
	return b
}

// publish puts a description where the source serves it for version ver.
func (o *officialSource) publish(ver string, body []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.files["/releases/download/"+ver+"/drool-agent_"+strings.TrimPrefix(ver, "v")+"_linux_"+runtime.GOARCH+".release.json"] = body
}

// publishOwn publishes the correct description of the bundled binary.
func (o *officialSource) publishOwn() {
	o.publish(o.ver, o.signed(o.priv, release.Manifest{Version: o.ver, Channel: version.Channel}, o.bin))
}

func (o *officialSource) requests() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.hits
}

// attach points a controller at the source and its own Shepherd binary.
func (o *officialSource) attach(e *agentEnv) {
	e.h.agents.SetBinariesDir(o.binDir)
	e.h.agents.ConfigureReleaseCheck(agentsvc.ReleaseCheckOptions{Source: o.url(), Client: o.srv.Client()})
}

type updStatus struct {
	Policy struct {
		Policy  string `json:"policy"`
		Channel string `json:"channel"`
	} `json:"policy"`
	PolicySaved bool     `json:"policy_saved"`
	Channels    []string `json:"channels"`
	State       string   `json:"state"`
	Summary     string   `json:"summary"`
	Target      string   `json:"target"`
	Official    struct {
		State  string `json:"state"`
		Detail string `json:"detail"`
	} `json:"official"`
	Check struct {
		Enabled     bool    `json:"enabled"`
		LastSuccess *string `json:"last_success"`
		LastError   string  `json:"last_error"`
	} `json:"check"`
	Latest *struct {
		Version string `json:"version"`
	} `json:"latest"`
	Counts struct {
		Total, Current, Behind, Updating, Waiting, Blocked, Offline int
	} `json:"counts"`
	Blocked []struct {
		HostID       string `json:"host_id"`
		Reason       string `json:"reason"`
		RetryVersion string `json:"retry_version"`
	} `json:"blocked"`
	Problems []struct {
		Kind   string `json:"kind"`
		HostID string `json:"host_id"`
		Detail string `json:"detail"`
	} `json:"problems"`
	Active  []map[string]any `json:"active"`
	History []map[string]any `json:"history"`
}

func (e *agentEnv) updStatus(t *testing.T) updStatus {
	t.Helper()
	var s updStatus
	r := e.c.do("GET", "/api/v1/agents/updates/status", nil)
	if r.status != 200 {
		t.Fatalf("update status: %d %s", r.status, r.body)
	}
	r.json(t, &s)
	return s
}

func (e *agentEnv) check(t *testing.T) {
	t.Helper()
	e.h.agents.CheckReleases(context.Background())
}

type releaseRow struct {
	Version  string `json:"version"`
	KeyID    string `json:"key_id"`
	Official bool   `json:"official"`
}

func (e *agentEnv) releases(t *testing.T) []releaseRow {
	t.Helper()
	var out struct {
		Items []releaseRow `json:"items"`
	}
	e.c.do("GET", "/api/v1/agents/releases", nil).json(t, &out)
	return out.Items
}

// A new installation updates its Shepherds automatically, from stable
// releases, one canary then batches of five; and says why official
// releases can't work when they can't.
func TestShepherdUpdateDefaults(t *testing.T) {
	e := newAgentEnv(t)
	var p agentsvc.UpdatePolicy
	e.c.do("GET", "/api/v1/agents/updates/policy", nil).json(t, &p)
	if p != (agentsvc.UpdatePolicy{Policy: "automatic", Channel: "stable", Canary: 1, BatchSize: 5, MaxFailures: 1, HealthWindowS: 120}) {
		t.Errorf("default policy: %+v", p)
	}
	st := e.updStatus(t)
	if st.State != "none" || st.PolicySaved || strings.Join(st.Channels, ",") != "stable,prerelease" {
		t.Errorf("status without Shepherds: %+v", st)
	}
	// This tree has no DROOL release key: the status says official
	// releases can't be verified, never that updates work.
	if st.Official.State != agentsvc.OfficialNoKey || st.Check.Enabled {
		t.Errorf("official state without a project key: %+v %+v", st.Official, st.Check)
	}
	// An explicit choice is kept.
	p.Policy = "manual"
	if r := e.c.do("PUT", "/api/v1/agents/updates/policy", p); r.status != 200 {
		t.Fatalf("save: %d %s", r.status, r.body)
	}
	if st := e.updStatus(t); st.Policy.Policy != "manual" || !st.PolicySaved {
		t.Errorf("saved manual policy: %+v", st.Policy)
	}
	for _, bad := range []map[string]any{{"policy": "sometimes"}, {"policy": "automatic", "channel": "Stable!"}, {"policy": "automatic", "version": "latest"}} {
		body := map[string]any{"channel": "stable", "canary": 1, "batch_size": 5, "max_failures": 1, "health_window_s": 120}
		for k, v := range bad {
			body[k] = v
		}
		if r := e.c.do("PUT", "/api/v1/agents/updates/policy", body); r.status != 422 {
			t.Errorf("policy %v accepted: %d", bad, r.status)
		}
	}
}

// The official release check: the signed description of this DROOL's own
// Shepherd, from the release source, verified against the project's key
// and the binary installed with DROOL; anything else is refused and said.
func TestOfficialReleaseCheck(t *testing.T) {
	o := newOfficialSource(t, "v1.4.0", "stable")
	e := newAgentEnv(t)
	o.attach(e)

	e.check(t)
	st := e.updStatus(t)
	if st.Official.State != agentsvc.OfficialNotPublished || len(e.releases(t)) != 0 || st.Check.LastSuccess == nil || st.Latest != nil {
		t.Fatalf("before publication: %+v %+v", st.Official, st.Check)
	}

	o.publishOwn()
	e.check(t)
	st = e.updStatus(t)
	rels := e.releases(t)
	if st.Official.State != agentsvc.OfficialVerified || len(rels) != 1 || !rels[0].Official || rels[0].Version != "v1.4.0" || rels[0].KeyID != o.keyID {
		t.Fatalf("published: %+v %+v", st.Official, rels)
	}
	if st.Target != "v1.4.0" || !strings.Contains(st.Official.Detail, "verified") {
		t.Errorf("target %q, detail %q", st.Target, st.Official.Detail)
	}

	// A newer DROOL, its description verified: reported, never installed
	// (Shepherds follow DROOL).
	o.publish("v1.5.0", o.signed(o.priv, release.Manifest{Version: "v1.5.0", Channel: "stable"}, []byte("a newer shepherd")))
	o.mu.Lock()
	o.latest = "v1.5.0"
	o.mu.Unlock()
	e.check(t)
	st = e.updStatus(t)
	if st.Latest == nil || st.Latest.Version != "v1.5.0" || st.Target != "v1.4.0" || len(e.releases(t)) != 1 {
		t.Errorf("newer release: latest %+v, target %s, releases %d", st.Latest, st.Target, len(e.releases(t)))
	}
	// A "newer release" with another key's signature is ignored, and said.
	other, otherPEM, _ := release.GenerateKey()
	_ = other
	otherPriv, _ := release.ParsePrivateKey(otherPEM)
	o.publish("v1.6.0", o.signed(otherPriv, release.Manifest{Version: "v1.6.0", Channel: "stable"}, []byte("x")))
	o.mu.Lock()
	o.latest = "v1.6.0"
	o.mu.Unlock()
	e.check(t)
	st = e.updStatus(t)
	if st.Latest == nil || st.Latest.Version != "v1.5.0" || !strings.Contains(st.Check.LastError, "didn't verify") {
		t.Errorf("forged newer release: latest %+v, error %q", st.Latest, st.Check.LastError)
	}
}

// Every way an official description can be wrong is refused: nothing is
// stored, and the status shows a verification problem.
func TestOfficialReleaseRejected(t *testing.T) {
	_, otherPEM, _ := release.GenerateKey()
	otherPriv, _ := release.ParsePrivateKey(otherPEM)
	for _, c := range []struct {
		name, want string
		body       func(o *officialSource) []byte
	}{
		{"another key", "does not trust", func(o *officialSource) []byte {
			return o.signed(otherPriv, release.Manifest{Version: o.ver, Channel: "stable"}, o.bin)
		}},
		{"a tampered description", "signature is invalid", func(o *officialSource) []byte {
			var s release.Signed
			json.Unmarshal(o.signed(o.priv, release.Manifest{Version: o.ver, Channel: "stable"}, o.bin), &s)
			s.Payload = bytes.Replace(s.Payload, []byte(`"protocol_max":1`), []byte(`"protocol_max":9`), 1)
			b, _ := json.Marshal(s)
			return b
		}},
		{"another version", "not v1.4.0", func(o *officialSource) []byte {
			return o.signed(o.priv, release.Manifest{Version: "v1.3.9", Channel: "stable"}, o.bin)
		}},
		{"another channel", "prerelease channel", func(o *officialSource) []byte {
			return o.signed(o.priv, release.Manifest{Version: o.ver, Channel: "prerelease"}, o.bin)
		}},
		{"another binary", "another binary", func(o *officialSource) []byte {
			return o.signed(o.priv, release.Manifest{Version: o.ver, Channel: "stable"}, []byte("corrupted or replaced"))
		}},
		{"an incompatible protocol", "protocol 7–8", func(o *officialSource) []byte {
			return o.signed(o.priv, release.Manifest{Version: o.ver, Channel: "stable", ProtocolMin: 7, ProtocolMax: 8}, o.bin)
		}},
		{"not a description", "not a DROOL release manifest", func(o *officialSource) []byte { return []byte("<html>nope</html>") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			o := newOfficialSource(t, "v1.4.0", "stable")
			e := newAgentEnv(t)
			o.attach(e)
			o.publish(o.ver, c.body(o))
			e.check(t)
			st := e.updStatus(t)
			if st.Official.State != agentsvc.OfficialRejected || !strings.Contains(st.Official.Detail, c.want) || len(e.releases(t)) != 0 {
				t.Fatalf("%s: %+v, releases %v", c.name, st.Official, e.releases(t))
			}
			found := false
			for _, p := range st.Problems {
				found = found || p.Kind == "verification"
			}
			if !found || st.Target != "" {
				t.Errorf("problems %+v, target %q", st.Problems, st.Target)
			}
		})
	}
}

// When there is nothing to verify with or against, DROOL doesn't ask the
// source at all, and says why.
func TestOfficialReleaseCheckNotPossible(t *testing.T) {
	t.Run("development build", func(t *testing.T) {
		o := newOfficialSource(t, "v1.4.0-12-gabcdef0", "development")
		e := newAgentEnv(t)
		o.attach(e)
		o.publishOwn()
		e.check(t)
		if st := e.updStatus(t); st.Official.State != agentsvc.OfficialDevelopment || len(e.releases(t)) != 0 {
			t.Errorf("development build: %+v", st.Official)
		}
	})
	t.Run("no project key", func(t *testing.T) {
		o := newOfficialSource(t, "v1.4.0", "stable")
		release.SetOfficialKeysForTest(nil) // undone by the source's cleanup
		e := newAgentEnv(t)
		o.attach(e)
		e.check(t)
		if st := e.updStatus(t); st.Official.State != agentsvc.OfficialNoKey || st.Check.Enabled || o.requests() != 0 {
			t.Errorf("no key: %+v, %d requests", st.Official, o.requests())
		}
	})
	t.Run("turned off", func(t *testing.T) {
		o := newOfficialSource(t, "v1.4.0", "stable")
		e := newAgentEnv(t)
		e.h.agents.SetBinariesDir(o.binDir)
		e.h.agents.ConfigureReleaseCheck(agentsvc.ReleaseCheckOptions{Source: "off"})
		e.check(t)
		if st := e.updStatus(t); st.Official.State != agentsvc.OfficialOff || st.Check.Enabled || o.requests() != 0 {
			t.Errorf("off: %+v, %d requests", st.Official, o.requests())
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		o := newOfficialSource(t, "v1.4.0", "stable")
		e := newAgentEnv(t)
		o.attach(e)
		o.srv.Close()
		e.check(t)
		if st := e.updStatus(t); st.Official.State != agentsvc.OfficialUnreachable || st.Check.LastError == "" || st.Check.LastSuccess != nil {
			t.Errorf("unreachable: %+v %+v", st.Official, st.Check)
		}
	})
}

// testUpdater stands in for the update helper, which would switch this
// machine's Shepherd: it records the call and leaves the outcome the real
// helper would (result.json); the test then restarts the Shepherd at the
// version the helper would have left running.
type testUpdater struct {
	mu      sync.Mutex
	calls   []string
	outcome string
	paths   agent.Paths
}

func (u *testUpdater) start(id, from, to string, _ time.Duration) error {
	u.mu.Lock()
	u.calls = append(u.calls, from+">"+to)
	outcome := u.outcome
	u.mu.Unlock()
	b, _ := json.Marshal(map[string]any{"release_id": id, "from": from, "to": to, "state": outcome, "detail": "test helper: " + outcome,
		"at": time.Now().UTC()})
	os.MkdirAll(filepath.Join(u.paths.State, "update"), 0o700)
	return os.WriteFile(filepath.Join(u.paths.State, "update", "result.json"), b, 0o600)
}

func (u *testUpdater) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.calls)
}

type shepherdT struct {
	host  string
	paths agent.Paths
	upd   *testUpdater
	run   *runningAgent
}

// shepherd enrolls an in-process Shepherd at version v; extra goes into its
// agent.yaml.
func (e *agentEnv) shepherd(t *testing.T, v, extra string) *shepherdT {
	t.Helper()
	p, id := e.enroll(t, map[string]any{}, fullCaps(""), v)
	if extra != "" {
		f, _ := os.OpenFile(p.Config, os.O_APPEND|os.O_WRONLY, 0)
		f.WriteString(extra)
		f.Close()
	}
	sh := &shepherdT{host: id.HostID, paths: p, upd: &testUpdater{outcome: "healthy", paths: p}}
	sh.start(t, e, v)
	return sh
}

func (sh *shepherdT) start(t *testing.T, e *agentEnv, v string) {
	t.Helper()
	a, err := agent.New(agent.Options{Paths: sh.paths, Version: v, StatusEvery: time.Second, RenewCheck: 200 * time.Millisecond, Supervisor: "exit",
		Monitor: fixtureMonitor(t), StartRemoval: (&testRemoval{}).start, RemovalDir: t.TempDir(), StartUpdate: sh.upd.start})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &runningAgent{a: a, cancel: cancel, done: make(chan struct{})}
	go func() { a.Run(ctx); close(r.done) }()
	t.Cleanup(func() {
		select {
		case <-r.done:
		default:
			r.stop()
		}
	})
	sh.run = r
	eventually(t, 10*time.Second, "Shepherd "+v+" connected", func() bool { return e.connected(sh.host) })
}

// restart is what the update helper does: the Shepherd comes back at v.
func (sh *shepherdT) restart(t *testing.T, e *agentEnv, v string) {
	t.Helper()
	sh.run.stop()
	eventually(t, 10*time.Second, "the old session gone", func() bool { return !e.connected(sh.host) })
	sh.start(t, e, v)
}

func (e *agentEnv) rolloutHost(t *testing.T, rolloutID, hostID string) (state, detail string, wave int64) {
	t.Helper()
	var ro struct {
		Hosts []struct {
			HostID string `json:"host_id"`
			State  string `json:"state"`
			Detail string `json:"detail"`
			Wave   int64  `json:"wave"`
		} `json:"hosts"`
	}
	e.c.do("GET", "/api/v1/agents/rollouts/"+rolloutID, nil).json(t, &ro)
	for _, h := range ro.Hosts {
		if h.HostID == hostID {
			return h.State, h.Detail, h.Wave
		}
	}
	return "", "", -1
}

type rolloutRow struct {
	ID           string `json:"id"`
	Version      string `json:"version"`
	Status       string `json:"status"`
	Automatic    bool   `json:"automatic"`
	Retry        bool   `json:"retry"`
	PausedReason string `json:"paused_reason"`
}

func (e *agentEnv) rollouts(t *testing.T) []rolloutRow {
	t.Helper()
	var out struct {
		Items []rolloutRow `json:"items"`
	}
	e.c.do("GET", "/api/v1/agents/rollouts", nil).json(t, &out)
	return out.Items
}

// The whole automatic path: DROOL ships a newer Shepherd than its hosts
// run, verifies its official release, and rolls it out by itself — a
// canary first (a connected host that can take it), then the batch; an
// offline host catches up when it returns; a host that trusts only its own
// keys is left alone, and the status says why.
func TestOfficialShepherdUpdateAutomatic(t *testing.T) {
	o := newOfficialSource(t, "v1.4.0", "stable")
	e := newAgentEnv(t)
	o.attach(e)
	o.publishOwn()
	a := e.shepherd(t, "v1.3.0", "")
	b := e.shepherd(t, "v1.3.0", "")
	c := e.shepherd(t, "v1.3.0", "")
	d := e.shepherd(t, "v1.3.0", "updates:\n  official: deny\n")
	c.run.stop() // offline when the rollout starts
	eventually(t, 10*time.Second, "c offline", func() bool { return !e.connected(c.host) })

	e.check(t) // verifies v1.4.0; the automatic policy acts on it
	e.h.agents.RunUpdatePass(context.Background())
	var ro rolloutRow
	eventually(t, 10*time.Second, "an automatic rollout", func() bool {
		for _, r := range e.rollouts(t) {
			if r.Version == "v1.4.0" && r.Automatic {
				ro = r
				return true
			}
		}
		return false
	})
	if st, _, wave := e.rolloutHost(t, ro.ID, d.host); st != "" || wave != -1 {
		t.Errorf("the host that denies official releases is in the automatic rollout: %s", st)
	}
	// The canary: a, the first connected host that can take it.
	eventually(t, 20*time.Second, "the canary staged and switching", func() bool { return a.upd.count() == 1 })
	if st, _, wave := e.rolloutHost(t, ro.ID, a.host); wave != 0 || (st != "updating" && st != "staged") {
		t.Errorf("canary a: %s wave %d", st, wave)
	}
	time.Sleep(time.Second)
	if b.upd.count() != 0 {
		t.Fatal("the batch was offered before the canary was healthy")
	}
	a.restart(t, e, "v1.4.0")
	eventually(t, 20*time.Second, "the canary healthy", func() bool { st, _, _ := e.rolloutHost(t, ro.ID, a.host); return st == "healthy" })
	eventually(t, 20*time.Second, "the batch offered", func() bool { return b.upd.count() == 1 })
	b.restart(t, e, "v1.4.0")
	eventually(t, 20*time.Second, "b healthy", func() bool { st, _, _ := e.rolloutHost(t, ro.ID, b.host); return st == "healthy" })
	st := e.updStatus(t)
	if st.State != "updating" || st.Counts.Waiting != 1 || st.Counts.Updating != 0 || !strings.Contains(st.Summary, "1 offline Shepherd will update when it reconnects") {
		t.Errorf("with c offline: %s %q %+v", st.State, st.Summary, st.Counts)
	}
	// c comes back and catches up.
	c.start(t, e, "v1.3.0")
	eventually(t, 20*time.Second, "c offered after it reconnected", func() bool { return c.upd.count() == 1 })
	c.restart(t, e, "v1.4.0")
	eventually(t, 20*time.Second, "the rollout completed", func() bool {
		for _, r := range e.rollouts(t) {
			if r.ID == ro.ID {
				return r.Status == "completed"
			}
		}
		return false
	})
	st = e.updStatus(t)
	if st.Counts.Total != 4 || st.Counts.Current != 3 || st.Counts.Blocked != 1 || len(st.Blocked) != 1 || st.Blocked[0].HostID != d.host ||
		!strings.Contains(st.Blocked[0].Reason, "updates.official: deny") || st.State != "blocked" {
		t.Errorf("after the update: %s %+v %+v", st.State, st.Counts, st.Blocked)
	}
	if d.upd.count() != 0 {
		t.Error("the host that denies official releases was offered one")
	}
	// The automatic policy doesn't start again for the same hosts.
	e.h.agents.RunUpdatePass(context.Background())
	if n := len(e.rollouts(t)); n != 1 {
		t.Errorf("%d rollouts after another pass", n)
	}
}

// A canary that doesn't stay healthy rolls back by itself and pauses the
// rollout: the rest of the fleet isn't offered the release. A person can
// try it again on that host.
func TestOfficialShepherdUpdateRollback(t *testing.T) {
	o := newOfficialSource(t, "v1.4.0", "stable")
	e := newAgentEnv(t)
	o.attach(e)
	o.publishOwn()
	a := e.shepherd(t, "v1.3.0", "")
	b := e.shepherd(t, "v1.3.0", "")
	a.upd.outcome = "rolled_back"
	e.check(t)
	e.h.agents.RunUpdatePass(context.Background())
	eventually(t, 20*time.Second, "the canary switched", func() bool { return a.upd.count() == 1 })
	a.restart(t, e, "v1.3.0") // the helper rolled back
	var ro rolloutRow
	eventually(t, 20*time.Second, "the rollout paused", func() bool {
		for _, r := range e.rollouts(t) {
			if r.Version == "v1.4.0" && r.Status == "paused" {
				ro = r
				return true
			}
		}
		return false
	})
	if st, _, _ := e.rolloutHost(t, ro.ID, a.host); st != "rolled_back" {
		t.Errorf("canary: %s", st)
	}
	time.Sleep(2 * time.Second)
	if b.upd.count() != 0 {
		t.Fatal("the rest of the fleet was offered a release that failed at the canary")
	}
	st := e.updStatus(t)
	kinds := map[string]bool{}
	for _, p := range st.Problems {
		kinds[p.Kind] = true
	}
	if st.State != "attention" || !kinds["paused"] || !kinds["rolled_back"] || !strings.HasPrefix(st.Summary, "The update to v1.4.0 is paused: it failed on ") {
		t.Errorf("status: %s %q %+v", st.State, st.Summary, st.Problems)
	}
	retry := ""
	for _, bl := range st.Blocked {
		if bl.HostID == a.host {
			retry = bl.RetryVersion
		}
	}
	if retry != "v1.4.0" {
		t.Errorf("no Try again for the rolled-back host: %+v", st.Blocked)
	}
	// Without a retry a person's rollout skips the host and says why.
	r := e.c.do("POST", "/api/v1/agents/rollouts", map[string]any{"version": "v1.4.0", "host_ids": []string{a.host}})
	if r.status != 422 || !strings.Contains(string(r.body), "failed on this host before") {
		t.Errorf("rollout to the failed host without retry: %d %s", r.status, r.body)
	}
	// Try again, after the cause is fixed.
	a.upd.mu.Lock()
	a.upd.outcome = "healthy"
	a.upd.mu.Unlock()
	var again rolloutRow
	r = e.c.do("POST", "/api/v1/agents/rollouts", map[string]any{"version": "v1.4.0", "host_ids": []string{a.host}, "retry": true})
	if r.status != 201 {
		t.Fatalf("try again: %d %s", r.status, r.body)
	}
	r.json(t, &again)
	if !again.Retry {
		t.Errorf("retry rollout: %+v", again)
	}
	eventually(t, 20*time.Second, "offered again", func() bool { return a.upd.count() == 2 })
	// Accepting the Try again set the earlier result aside.
	var prev struct{ State, To string }
	if b, err := os.ReadFile(filepath.Join(a.paths.State, "update", "result.previous.json")); err != nil || json.Unmarshal(b, &prev) != nil ||
		prev.State != "rolled_back" || prev.To != "v1.4.0" {
		t.Errorf("the earlier result wasn't set aside: %+v %v", prev, err)
	}
	a.restart(t, e, "v1.4.0")
	eventually(t, 20*time.Second, "healthy on the second try", func() bool { st, _, _ := e.rolloutHost(t, again.ID, a.host); return st == "healthy" })
}

// Resume after the failure limit paused an update: the failures so far are
// accepted, the rollout continues with a new canary instead of pausing
// again at once, and it completes (ARCHITECTURE-ALPHA §43).
func TestOfficialShepherdUpdateResume(t *testing.T) {
	o := newOfficialSource(t, "v1.4.0", "stable")
	e := newAgentEnv(t)
	o.attach(e)
	o.publishOwn()
	a := e.shepherd(t, "v1.3.0", "")
	b := e.shepherd(t, "v1.3.0", "")
	a.upd.outcome = "rolled_back"
	e.check(t)
	e.h.agents.RunUpdatePass(context.Background())
	eventually(t, 20*time.Second, "the canary switched", func() bool { return a.upd.count() == 1 })
	a.restart(t, e, "v1.3.0") // the helper rolled back
	var ro rolloutRow
	eventually(t, 20*time.Second, "the rollout paused", func() bool {
		for _, r := range e.rollouts(t) {
			if r.Version == "v1.4.0" && r.Status == "paused" {
				ro = r
				return true
			}
		}
		return false
	})
	if r := e.c.do("POST", "/api/v1/agents/rollouts/"+ro.ID+"/resume", nil); r.status != 200 {
		t.Fatalf("resume: %d %s", r.status, r.body)
	}
	eventually(t, 20*time.Second, "b offered as the new canary", func() bool { return b.upd.count() == 1 })
	for _, r := range e.rollouts(t) {
		if r.ID == ro.ID && r.Status != "running" {
			t.Fatalf("paused again after resume: %+v", r)
		}
	}
	b.restart(t, e, "v1.4.0")
	eventually(t, 20*time.Second, "the resumed update completed", func() bool {
		for _, r := range e.rollouts(t) {
			if r.ID == ro.ID {
				return r.Status == "completed"
			}
		}
		return false
	})
	if st, _, _ := e.rolloutHost(t, ro.ID, a.host); st != "rolled_back" {
		t.Errorf("the canary's failure is kept in the history: %s", st)
	}
	if a.upd.count() != 1 {
		t.Error("resuming offered the version again where it failed")
	}
}

// An update cut off with the Shepherd's session before its helper started
// is offered again when the Shepherd reconnects, not counted as a failure;
// a result the Shepherd still has from an earlier attempt at the same
// version is never taken for the new attempt's (ARCHITECTURE-ALPHA §43).
func TestOfficialShepherdUpdateInterrupted(t *testing.T) {
	o := newOfficialSource(t, "v1.4.0", "stable")
	e := newAgentEnv(t)
	o.attach(e)
	o.publishOwn()
	p := agentsvc.DefaultUpdatePolicy()
	p.Policy = "manual"
	if r := e.c.do("PUT", "/api/v1/agents/updates/policy", p); r.status != 200 {
		t.Fatal(r.status)
	}
	a := e.shepherd(t, "v1.3.0", "")
	e.check(t)
	// "Offered, then the session ended": the host went away before the
	// offer reached its helper.
	interrupt := func(rolloutID string) {
		t.Helper()
		if _, err := e.h.st.Writer.Exec(`UPDATE agent_rollout_hosts SET state = 'offered' WHERE rollout_id = ? AND host_id = ?`, rolloutID, a.host); err != nil {
			t.Fatal(err)
		}
	}
	start := func(body map[string]any) rolloutRow {
		t.Helper()
		var ro rolloutRow
		r := e.c.do("POST", "/api/v1/agents/rollouts", body)
		if r.status != 201 {
			t.Fatalf("start: %d %s", r.status, r.body)
		}
		r.json(t, &ro)
		return ro
	}
	a.run.stop()
	eventually(t, 10*time.Second, "a offline", func() bool { return !e.connected(a.host) })
	ro := start(map[string]any{"version": "v1.4.0"})
	interrupt(ro.ID)
	// The helper fails this time, from the start: the first real attempt.
	a.upd.outcome = "rolled_back"
	a.start(t, e, "v1.3.0")
	eventually(t, 20*time.Second, "offered again on reconnect", func() bool { return a.upd.count() == 1 })
	a.restart(t, e, "v1.3.0") // rolled back
	eventually(t, 20*time.Second, "the update rolled back and paused", func() bool {
		st, _, _ := e.rolloutHost(t, ro.ID, a.host)
		return st == "rolled_back"
	})
	// Try again, cut off the same way: the Shepherd accepted it, which sets
	// the rollback above aside as an earlier result, and its session ended
	// before the update's helper started. The attempt is offered again; the
	// earlier result doesn't answer for it.
	a.upd.mu.Lock()
	a.upd.outcome = "healthy"
	a.upd.mu.Unlock()
	a.run.stop()
	eventually(t, 10*time.Second, "a offline", func() bool { return !e.connected(a.host) })
	again := start(map[string]any{"version": "v1.4.0", "host_ids": []string{a.host}, "retry": true})
	interrupt(again.ID)
	upd := filepath.Join(a.paths.State, "update")
	if err := os.Rename(filepath.Join(upd, "result.json"), filepath.Join(upd, "result.previous.json")); err != nil {
		t.Fatal(err)
	}
	a.start(t, e, "v1.3.0")
	eventually(t, 20*time.Second, "the retry offered again on reconnect", func() bool { return a.upd.count() == 2 })
	if st, d, _ := e.rolloutHost(t, again.ID, a.host); st == "rolled_back" || st == "failed" {
		t.Fatalf("the earlier attempt's result was taken for the retry: %s %q", st, d)
	}
	a.restart(t, e, "v1.4.0")
	eventually(t, 20*time.Second, "healthy", func() bool { st, _, _ := e.rolloutHost(t, again.ID, a.host); return st == "healthy" })

	// A host that stays away past the health window doesn't fail the
	// rollout either: an attempt that never reached its helper waits.
	b := e.shepherd(t, "v1.3.0", "")
	b.run.stop()
	eventually(t, 10*time.Second, "b offline", func() bool { return !e.connected(b.host) })
	third := start(map[string]any{"version": "v1.4.0", "host_ids": []string{b.host}})
	if _, err := e.h.st.Writer.Exec(`UPDATE agent_rollout_hosts SET state = 'offered', updated_at = ? WHERE rollout_id = ? AND host_id = ?`,
		time.Now().Add(-time.Hour).UnixMilli(), third.ID, b.host); err != nil {
		t.Fatal(err)
	}
	e.h.agents.RunUpdatePass(context.Background())
	if st, d, _ := e.rolloutHost(t, third.ID, b.host); st != "pending" || !strings.Contains(d, "interrupted") {
		t.Errorf("an attempt cut off with an offline host: %s %q", st, d)
	}
	for _, r := range e.rollouts(t) {
		if r.ID == third.ID && r.Status != "running" {
			t.Errorf("rollout %s: %+v", r.Status, r)
		}
	}
}

// A person who cancels the update to the target stops automatic updates for
// that version: the status says so instead of promising them, and the
// update can be started by hand. A host that was only busy finishing
// another update is offered again (ARCHITECTURE-ALPHA §43).
func TestOfficialShepherdUpdateStopped(t *testing.T) {
	o := newOfficialSource(t, "v1.4.0", "stable")
	e := newAgentEnv(t)
	o.attach(e)
	o.publishOwn()
	a := e.shepherd(t, "v1.3.0", "")
	a.run.stop()
	eventually(t, 10*time.Second, "a offline", func() bool { return !e.connected(a.host) })
	e.check(t)
	e.h.agents.RunUpdatePass(context.Background())
	var ro rolloutRow
	eventually(t, 10*time.Second, "an automatic rollout", func() bool {
		for _, r := range e.rollouts(t) {
			if r.Version == "v1.4.0" && r.Automatic {
				ro = r
				return true
			}
		}
		return false
	})
	if r := e.c.do("POST", "/api/v1/agents/rollouts/"+ro.ID+"/cancel", nil); r.status != 200 {
		t.Fatalf("cancel: %d %s", r.status, r.body)
	}
	e.h.agents.RunUpdatePass(context.Background())
	if n := len(e.rollouts(t)); n != 1 {
		t.Fatalf("automatic updates started the cancelled version again: %d rollouts", n)
	}
	var st struct {
		updStatus
		Stopped string `json:"stopped"`
	}
	e.c.do("GET", "/api/v1/agents/updates/status", nil).json(t, &st)
	if st.State != "available" || st.Stopped != "cancelled by admin" || strings.Contains(st.Summary, "automatically") ||
		!strings.Contains(st.Summary, "Automatic updates won't start it again (cancelled by admin)") {
		t.Errorf("status after a cancel: %s %q stopped=%q", st.State, st.Summary, st.Stopped)
	}

	// A busy refusal isn't a decision about the version.
	b := e.shepherd(t, "v1.3.0", "")
	if _, err := e.h.st.Writer.Exec(`UPDATE agent_rollouts SET status = 'completed', paused_reason = '' WHERE id = ?`, ro.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.h.st.Writer.Exec(`INSERT INTO agent_rollout_hosts (rollout_id, host_id, wave, state, from_version, detail, updated_at)
		VALUES (?, ?, 1, 'skipped', 'v1.3.0', 'another update is still in progress', ?)`, ro.ID, b.host, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	e.h.agents.RunUpdatePass(context.Background())
	eventually(t, 20*time.Second, "b offered after a busy refusal", func() bool { return b.upd.count() == 1 })
}

// An explicit "manual" is kept: DROOL says an update is available and
// starts nothing by itself.
func TestOfficialShepherdUpdateManualKept(t *testing.T) {
	o := newOfficialSource(t, "v1.4.0", "stable")
	e := newAgentEnv(t)
	o.attach(e)
	o.publishOwn()
	p := agentsvc.DefaultUpdatePolicy()
	p.Policy = "manual"
	if r := e.c.do("PUT", "/api/v1/agents/updates/policy", p); r.status != 200 {
		t.Fatal(r.status)
	}
	a := e.shepherd(t, "v1.3.0", "")
	e.check(t)
	e.h.agents.RunUpdatePass(context.Background())
	time.Sleep(time.Second)
	if n := len(e.rollouts(t)); n != 0 || a.upd.count() != 0 {
		t.Fatalf("manual policy: %d rollouts, %d offers", n, a.upd.count())
	}
	st := e.updStatus(t)
	if st.State != "available" || st.Counts.Behind != 1 || !strings.Contains(st.Summary, "An update to v1.4.0 is available for 1 Shepherd") {
		t.Errorf("manual status: %s %q %+v", st.State, st.Summary, st.Counts)
	}
	// Update now: a person's rollout.
	if r := e.c.do("POST", "/api/v1/agents/rollouts", map[string]any{"version": "v1.4.0"}); r.status != 201 {
		t.Fatalf("update now: %d %s", r.status, r.body)
	}
	eventually(t, 20*time.Second, "offered", func() bool { return a.upd.count() == 1 })
	// Nothing to do once every Shepherd has it.
	a.restart(t, e, "v1.4.0")
	eventually(t, 10*time.Second, "a at v1.4.0", func() bool { return e.updStatus(t).Counts.Current == 1 })
	if r := e.c.do("POST", "/api/v1/agents/rollouts", map[string]any{"version": "v1.4.0"}); r.status != 409 {
		t.Errorf("a rollout with nothing to update: %d %s", r.status, r.body)
	}
}

// Without internet access, the official description is uploaded by hand:
// the binary is the one DROOL ships, so the manifest alone is enough. A
// manifest for any other binary still needs that binary.
func TestOfficialReleaseManifestUpload(t *testing.T) {
	o := newOfficialSource(t, "v1.4.0", "stable")
	e := newAgentEnv(t)
	o.attach(e)
	up := func(manifest []byte) (int, string) {
		r := e.c.doHeader("POST", "/api/v1/agents/releases", nil, "X-DROOL-Release", base64.StdEncoding.EncodeToString(manifest))
		return r.status, string(r.body)
	}
	if st, body := up(o.signed(o.priv, release.Manifest{Version: "v1.4.1", Channel: "stable"}, []byte("not shipped here"))); st != 422 || !strings.Contains(body, "doesn't have") {
		t.Errorf("manifest of a binary DROOL doesn't have: %d %s", st, body)
	}
	if st, body := up(o.signed(o.priv, release.Manifest{Version: o.ver, Channel: "stable"}, o.bin)); st != 201 {
		t.Fatalf("manifest of the shipped binary: %d %s", st, body)
	}
	if rels := e.releases(t); len(rels) != 1 || !rels[0].Official {
		t.Errorf("releases: %+v", rels)
	}
	// An official release newer than this DROOL, uploaded with its binary,
	// is stored, but automatic updates don't take Shepherds past DROOL.
	newer := []byte("#!/bin/sh\necho v1.5.0\n")
	signed, err := agentsvc.ParseSignedRelease(o.signed(o.priv, release.Manifest{Version: "v1.5.0", Channel: "stable"}, newer))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.h.agents.AddRelease(context.Background(), signed, bytes.NewReader(newer), ""); err != nil {
		t.Fatal(err)
	}
	if got := e.h.agents.AutoTarget(context.Background(), agentsvc.DefaultUpdatePolicy()); got != "v1.4.0" {
		t.Errorf("automatic target with an official release newer than DROOL: %s", got)
	}
}

// Every role that sees Shepherds can read the update status; only
// administrators change anything.
func TestShepherdUpdateRBAC(t *testing.T) {
	e := newAgentEnv(t)
	e.h.mkUser("viewer", auth.RoleViewer)
	e.h.mkUser("op", auth.RoleOperator)
	for _, who := range []string{"viewer", "op"} {
		c := e.h.login(who)
		if r := c.do("GET", "/api/v1/agents/updates/status", nil); r.status != 200 {
			t.Errorf("%s reads the status: %d", who, r.status)
		}
		for _, w := range []struct{ method, path string }{{"POST", "/api/v1/agents/updates/check"}, {"PUT", "/api/v1/agents/updates/policy"},
			{"POST", "/api/v1/agents/rollouts"}, {"POST", "/api/v1/agents/release-keys"}, {"POST", "/api/v1/agents/releases"}} {
			if r := c.do(w.method, w.path, map[string]any{}); r.status != 403 {
				t.Errorf("%s %s %s: %d", who, w.method, w.path, r.status)
			}
		}
	}
}

var _ = agentcaps.PresetFull

// A release candidate's Shepherd is official on the prerelease channel: a
// fleet on Stable keeps its version and is told how to follow the
// candidate; on Release candidates it updates.
func TestOfficialShepherdUpdateReleaseCandidate(t *testing.T) {
	o := newOfficialSource(t, "v1.4.0-rc.1", "prerelease")
	e := newAgentEnv(t)
	o.attach(e)
	o.publishOwn()
	a := e.shepherd(t, "v1.3.0", "")
	e.check(t)
	e.h.agents.RunUpdatePass(context.Background())
	st := e.updStatus(t)
	if st.Official.State != agentsvc.OfficialVerified || st.Target != "" || len(st.Blocked) != 1 ||
		!strings.Contains(st.Blocked[0].Reason, "choose the Release candidates channel") || a.upd.count() != 0 {
		t.Fatalf("a release candidate on the stable channel: %+v %q %+v", st.Official, st.Target, st.Blocked)
	}
	p := agentsvc.DefaultUpdatePolicy()
	p.Channel = "prerelease"
	if r := e.c.do("PUT", "/api/v1/agents/updates/policy", p); r.status != 200 {
		t.Fatal(r.status)
	}
	e.h.agents.RunUpdatePass(context.Background())
	eventually(t, 20*time.Second, "the candidate offered", func() bool { return a.upd.count() == 1 })
	if st := e.updStatus(t); st.Target != "v1.4.0-rc.1" {
		t.Errorf("target on the prerelease channel: %q", st.Target)
	}
}
