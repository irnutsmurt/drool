//go:build integration

package integration

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"drool/internal/release"
)

// TestOfficialShepherdUpdates is the 1.0 update promise end to end, with
// real binaries in real containers: install DROOL, install Shepherds,
// update DROOL — and the Shepherds follow by themselves. Nobody uploads a
// binary or a manifest or trusts a key: the controller fetches its own
// Shepherd's signed description from the release source, verifies it
// against the project key built into it and against the binary it ships,
// and rolls it out, canary first; each Shepherd verifies the signature
// again with the key built into it, and switches only after its health
// check. A host whose agent.yaml trusts only its own keys is left alone,
// and the status says why.
//
// The DROOL project's real key can't sign in a test, so the binaries here
// are test builds (-tags drooltestofficial) that trust a key made for the
// run as their built-in project key. The release source is a local
// stand-in for GitHub's release pages, serving the same paths.
func TestOfficialShepherdUpdates(t *testing.T) {
	pub, privPEM, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	priv, _ := release.ParsePrivateKey(privPEM)
	keyB64 := base64.StdEncoding.EncodeToString(pub)
	arch := runtime.GOARCH
	dir := t.TempDir()
	build := func(v, pkg, out, goarch string) {
		t.Helper()
		ld := "-X drool/internal/version.Version=" + v + " -X drool/internal/version.Channel=stable -X drool/internal/release.testOfficialKey=" + keyB64
		cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-tags", "drooltestofficial", "-ldflags", ld, "-o", out, "./cmd/"+pkg)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+goarch)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s %s: %v\n%s", pkg, v, err, b)
		}
	}
	type droolRelease struct{ version, drool, agents, agent string }
	releases := map[string]droolRelease{}
	for _, v := range []string{"v1.4.0", "v1.5.0"} {
		r := droolRelease{version: v, drool: filepath.Join(dir, v, "drool"), agents: filepath.Join(dir, v, "agents")}
		r.agent = filepath.Join(r.agents, "drool-agent-linux-"+arch)
		build(v, "drool", r.drool, runtime.GOARCH)
		build(v, "drool-agent", r.agent, arch)
		releases[v] = r
	}

	// The release source: each release's signed Shepherd descriptions, and
	// the newest release's tag.
	var mu sync.Mutex
	files := map[string][]byte{}
	latest := ""
	publish := func(r droolRelease) {
		b, _ := os.ReadFile(r.agent)
		s, err := release.Sign(priv, release.Manifest{Version: r.version, Channel: "stable", OS: "linux", Arch: arch, ProtocolMin: 1, ProtocolMax: 1}, bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		mj, _ := json.Marshal(s)
		mu.Lock()
		files["/releases/download/"+r.version+"/drool-agent_"+strings.TrimPrefix(r.version, "v")+"_linux_"+arch+".release.json"] = mj
		latest = r.version
		mu.Unlock()
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/releases/latest" {
			http.Redirect(w, r, "/releases/tag/"+latest, http.StatusFound)
			return
		}
		if b, ok := files[r.URL.Path]; ok {
			w.Write(b)
			return
		}
		http.NotFound(w, r)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	source := "http://" + ln.Addr().String() + "/releases"

	// DROOL v1.4.0, its official Shepherd release published.
	publish(releases["v1.4.0"])
	data := filepath.Join(t.TempDir(), "data")
	env := append(controllerEnv(freePort()), "DROOL_AGENT_UPDATE_SOURCE="+source, "DROOL_TEST_UPDATE_TIMING=fast")
	c := &controller{bin: releases["v1.4.0"].drool, dataDir: data, extraEnv: append(env, "DROOL_AGENT_BINARIES_DIR="+releases["v1.4.0"].agents)}
	if err := c.start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.stop)
	if err := c.claim(); err != nil {
		t.Fatal(err)
	}
	useController(t, c)
	// Each read decodes into a new value: fields the status leaves out
	// (latest) mustn't linger from the one before.
	status := func() officialStatusT {
		var s officialStatusT
		ctl.must(t, "GET", "/api/v1/agents/updates/status", nil, &s)
		return s
	}
	var st officialStatusT
	waitFor(t, time.Minute, "v1.4.0's official Shepherd release verified", func() bool {
		st = status()
		return st.Official.State == "verified" && st.Target == "v1.4.0"
	})
	expect(t, st.Policy.Policy == "automatic" && st.Policy.Channel == "stable" && st.Check.LastSuccess != nil, "a new installation's status: %+v", st)

	// Shepherds installed from it (over SSH, one click each): v1.4.0, the
	// official release.
	d := selected()[0]
	var hosts []host
	var boxes []*container
	for _, n := range []string{"off-a", "off-b", "off-c"} {
		cont := startHost(t, image(t, d), n, true, netOuter)
		h := addHost(t, n, cont, hostOpts{})
		installAgent(t, h.ID, "managed", "")
		hosts, boxes = append(hosts, h), append(boxes, cont)
	}
	for _, h := range hosts {
		waitAgent(t, h.ID, 2*time.Minute, h.Name+" on its Shepherd", func(a hostAgent) bool {
			return a.Identity != nil && a.Identity.Connected && a.Identity.AgentVersion == "v1.4.0"
		})
	}
	// off-c's administrator trusts only their own builds there.
	boxes[2].exec(t, "printf 'updates:\\n  official: deny\\n' >> /etc/drool/agent.yaml")
	stopAgent(t, boxes[2])
	startSupervisor(t, boxes[2])
	waitAgent(t, hosts[2].ID, 2*time.Minute, "off-c back", func(a hostAgent) bool { return a.Identity != nil && a.Identity.Connected })
	if out := boxes[0].exec(t, "/usr/libexec/drool-agent/current/drool-agent status"); !strings.Contains(out, "Official DROOL releases: trusted") {
		t.Errorf("drool-agent status on off-a:\n%s", out)
	}
	st = status()
	expect(t, st.State == "current" && st.Counts.Current == 3, "with every Shepherd on v1.4.0: %s %+v", st.State, st.Counts)

	// DROOL v1.5.0 is released: the check says so, and nothing moves.
	publish(releases["v1.5.0"])
	ctl.must(t, "POST", "/api/v1/agents/updates/check", nil, nil)
	waitFor(t, time.Minute, "the newer release reported", func() bool {
		st = status()
		return st.Latest != nil && st.Latest.Version == "v1.5.0"
	})
	expect(t, st.Target == "v1.4.0", "Shepherds never move past the controller: target %s", st.Target)

	// The administrator updates DROOL (the same data, the new program and
	// its Shepherds), and does nothing else.
	c.stop()
	c.bin = releases["v1.5.0"].drool
	c.extraEnv = append(env, "DROOL_AGENT_BINARIES_DIR="+releases["v1.5.0"].agents)
	if err := c.start(); err != nil {
		t.Fatal(err)
	}
	for _, h := range hosts[:2] {
		waitAgent(t, h.ID, 6*time.Minute, h.Name+" updated to v1.5.0 by itself", func(a hostAgent) bool {
			return a.Identity != nil && a.Identity.Connected && a.Identity.AgentVersion == "v1.5.0"
		})
	}
	var rolls struct {
		Items []struct {
			ID        string `json:"id"`
			Version   string `json:"version"`
			Status    string `json:"status"`
			Automatic bool   `json:"automatic"`
		} `json:"items"`
	}
	var rollout rolloutT
	waitFor(t, 2*time.Minute, "the automatic rollout completed", func() bool {
		ctl.must(t, "GET", "/api/v1/agents/rollouts", nil, &rolls)
		for _, r := range rolls.Items {
			if r.Version == "v1.5.0" && r.Automatic && r.Status == "completed" {
				rollout = getRollout(t, r.ID)
				return true
			}
		}
		return false
	})
	waves := map[string]int{}
	var wv struct {
		Hosts []struct {
			HostID string `json:"host_id"`
			Wave   int    `json:"wave"`
			State  string `json:"state"`
		} `json:"hosts"`
	}
	ctl.must(t, "GET", "/api/v1/agents/rollouts/"+rollout.ID, nil, &wv)
	for _, h := range wv.Hosts {
		waves[h.HostID] = h.Wave
		expect(t, h.State == "healthy", "rollout host %s: %s", h.HostID, h.State)
	}
	expect(t, len(wv.Hosts) == 2 && waves[hosts[0].ID]+waves[hosts[1].ID] == 1, "one canary, then the batch: %+v", wv.Hosts)
	for _, b := range boxes[:2] {
		out := b.exec(t, "readlink /usr/libexec/drool-agent/current; readlink /usr/libexec/drool-agent/previous")
		expect(t, out == "versions/v1.5.0\nversions/v1.4.0", "%s version links: %q", b.Name, out)
	}
	// off-c stays, and DROOL says why.
	expect(t, getHostAgent(t, hosts[2].ID).Identity.AgentVersion == "v1.4.0", "off-c changed version")
	st = status()
	reason := ""
	for _, b := range st.Blocked {
		if b.HostID == hosts[2].ID {
			reason = b.Reason
		}
	}
	expect(t, st.Official.State == "verified" && st.Target == "v1.5.0" && st.Counts.Current == 2 && st.Counts.Blocked == 1 &&
		strings.Contains(reason, "updates.official: deny") && st.Latest == nil, "after DROOL's update: %+v", st)
	// Nobody uploaded anything: DROOL stored the official releases itself.
	var audit struct {
		Items []struct {
			Action string `json:"action"`
		} `json:"items"`
	}
	ctl.must(t, "GET", "/api/v1/audit?action=agent.&limit=500", nil, &audit)
	counts := map[string]int{}
	for _, e := range audit.Items {
		counts[e.Action]++
	}
	expect(t, counts["agent.release.add"] == 0 && counts["agent.release.official"] >= 1 && counts["agent.release_key.add"] == 0,
		"audit: %v", fmt.Sprint(counts))
}

type officialStatusT struct {
	Policy struct {
		Policy  string `json:"policy"`
		Channel string `json:"channel"`
	} `json:"policy"`
	State    string `json:"state"`
	Target   string `json:"target"`
	Official struct {
		State  string `json:"state"`
		Detail string `json:"detail"`
	} `json:"official"`
	Check struct {
		LastSuccess *string `json:"last_success"`
		LastError   string  `json:"last_error"`
	} `json:"check"`
	Latest *struct {
		Version string `json:"version"`
	} `json:"latest"`
	Counts struct {
		Total, Current, Behind, Updating, Blocked int
	} `json:"counts"`
	Blocked []struct {
		HostID string `json:"host_id"`
		Reason string `json:"reason"`
	} `json:"blocked"`
}
