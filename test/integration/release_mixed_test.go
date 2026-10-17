//go:build integration

package integration

import (
	"bytes"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type compatT struct {
	Identity *struct {
		AgentVersion  string `json:"agent_version"`
		Connected     bool   `json:"connected"`
		Compatibility struct {
			Status  string   `json:"status"`
			Summary string   `json:"summary"`
			Missing []string `json:"missing"`
		} `json:"compatibility"`
	} `json:"identity"`
}

func compat(t *testing.T, hostID string) compatT {
	t.Helper()
	var c compatT
	ctl.must(t, "GET", "/api/v1/hosts/"+hostID+"/agent", nil, &c)
	return c
}

// signArgs is `drool release sign ARGS`; a development build of this tree
// may be from modified source, which only a release check refuses.
func signArgs(args ...string) []string {
	a := []string{"release", "sign"}
	if rel == nil {
		a = append(a, "--allow-modified")
	}
	return append(a, args...)
}

// enrollOld installs an older Shepherd binary on a host DROOL already
// manages over SSH, by hand with a token bound to that host, trusting the
// release key: what a Shepherd installed by an older controller is.
func enrollOld(t *testing.T, c *container, hostID, bin, keyLine string) {
	t.Helper()
	var tok struct {
		Token string `json:"token"`
	}
	ctl.must(t, "POST", "/api/v1/agents/tokens", map[string]any{"preset": "full", "description": "mixed versions", "host_id": hostID}, &tok)
	mustDocker(t, "cp", bin, c.Name+":/tmp/drool-agent")
	c.exec(t, "cat > /tmp/release.pub <<'EOF'\n"+keyLine+"\nEOF")
	out, err := dockerStdin(c.Name, tok.Token+"\n", "/tmp/drool-agent", "install", "--preset", "full", "--token-file", "-",
		"--trust-release-key", "/tmp/release.pub")
	if err != nil || !strings.Contains(out, "Enrolled as host") {
		t.Fatalf("installing the older Shepherd: %v\n%s", err, out)
	}
}

// TestMixedVersionShepherds runs real Shepherds of the builds homelabs run
// (M15 and M16) next to this release's against this release's controller
// (M17's compatibility promise): they connect and work; the status says
// what each lacks; nothing is sent that a Shepherd can't do and SSH stands
// in only where the host's fallback allows it; after a restore the M15
// Shepherd, which can't move to the new controller epoch, waits — and once
// updated through an ordinary signed rollout, it reconciles and works
// through the Shepherd again.
func TestMixedVersionShepherds(t *testing.T) {
	c := freshController(t)
	d := selected()[0]
	m15 := m15Build.build(t, "drool-agent", "amd64")
	m16 := m16Build.build(t, "drool-agent", "amd64")
	m15v, m16v := m15Build.version(t), m16Build.version(t)
	key := newReleaseKey(t)
	ctl.must(t, "POST", "/api/v1/agents/release-keys", map[string]any{"public_key": key.line}, nil)

	type node struct {
		name string
		c    *container
		h    host
		ver  string
	}
	var nodes []*node
	for _, n := range []struct{ name, bin, ver string }{
		{"mix-m15-ssh", m15, m15v}, {"mix-m15-only", m15, m15v}, {"mix-m16", m16, m16v}, {"mix-cur", "", agentV1},
	} {
		ct := startHost(t, image(t, d), n.name, true, netOuter)
		h := addHost(t, n.name, ct, hostOpts{})
		if n.bin == "" {
			installAgent(t, h.ID, "full", key.line)
		} else {
			enrollOld(t, ct, h.ID, n.bin, key.line)
		}
		nodes = append(nodes, &node{n.name, ct, h, n.ver})
	}
	m15ssh, m15only, m16n, cur := nodes[0], nodes[1], nodes[2], nodes[3]
	ctl.must(t, "PUT", "/api/v1/hosts/"+m15only.h.ID+"/management", map[string]any{"preferred": "agent", "fallback": "none"}, nil)
	t.Cleanup(func() {
		for _, n := range nodes {
			stopAgent(t, n.c)
		}
	})

	// Every Shepherd connects and runs what it could always run.
	for _, n := range nodes {
		waitAgent(t, n.h.ID, 2*time.Minute, n.name+" connected", func(a hostAgent) bool {
			return a.Management.Active == "agent" && a.Identity != nil && a.Identity.Connected && a.Identity.AgentVersion == n.ver
		})
		jh, _ := runT(t, n.h.ID, cmd("echo hello-from-"+n.name, false))
		expect(t, jh.State == "succeeded" && jh.Transport == "agent", "%s (%s): a job: %+v", n.name, n.ver, jh)
		waitFor(t, 3*time.Minute, n.name+" monitoring", func() bool {
			var m struct {
				HasData bool   `json:"has_data"`
				Source  string `json:"source"`
			}
			st, _ := ctl.do("GET", "/api/v1/hosts/"+n.h.ID+"/monitoring", nil, &m)
			return st == 200 && m.HasData && m.Source == "agent"
		})
	}
	// Desired State checks go through every Shepherd, M15's included (it
	// answers them without announcing query-1).
	ctl.must(t, "POST", "/api/v1/desired/baselines", map[string]any{"name": "mixed",
		"assignments": []map[string]any{{"scope": "host", "host_id": m15ssh.h.ID}, {"scope": "host", "host_id": m16n.h.ID}},
		"resources":   []map[string]any{{"kind": "package", "name": d.Pkg, "state": "absent"}}}, nil)
	for _, n := range []*node{m15ssh, m16n} {
		r := dsWait(t, n.h.ID, "package:"+d.Pkg, "compliant", 3*time.Minute)
		expect(t, r.Status == "compliant", "%s: Desired State %+v", n.name, r)
	}

	// What DROOL says about each.
	missing := func(n *node) string { return strings.Join(compat(t, n.h.ID).Identity.Compatibility.Missing, "; ") }
	status := func(n *node) string { return compat(t, n.h.ID).Identity.Compatibility.Status }
	expect(t, status(m15ssh) == "reduced" && strings.Contains(missing(m15ssh), "restored controller"), "M15: %s (%s)", status(m15ssh), missing(m15ssh))
	// M16 works, without removing itself when asked (dogfooding correction
	// batch): told, so it can be updated or removed over SSH.
	expect(t, status(m16n) == "reduced" && strings.Contains(missing(m16n), "removing itself") && status(cur) == "compatible", "M16 %s (%s), current %s",
		status(m16n), missing(m16n), status(cur))
	t.Logf("before the restore: M15 %s (%s), M16 %s, current %s", status(m15ssh), missing(m15ssh), status(m16n), status(cur))

	// Back up, restore: a new controller epoch. The current and M16
	// Shepherds move to it; M15's can't and waits — no transition is sent.
	dir := t.TempDir()
	file := filepath.Join(dir, "backup.tar.gz")
	if out, err := droolCLI(t, binPath, c.dataDir, "backup", "--include-master-key", "-o", file); err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}
	c.stop()
	if out, err := droolCLI(t, binPath, c.dataDir, "restore", "--yes", file); err != nil || !strings.Contains(out, "new controller epoch") {
		t.Fatalf("restore: %v\n%s", err, out)
	}
	if err := c.start(); err != nil {
		t.Fatal(err)
	}
	if err := c.login(); err != nil {
		t.Fatal(err)
	}
	recState := func(n *node) (string, string) {
		var rec struct {
			State  string `json:"state"`
			Reason string `json:"reason"`
		}
		ctl.must(t, "GET", "/api/v1/hosts/"+n.h.ID+"/reconciliation", nil, &rec)
		return rec.State, rec.Reason
	}
	for _, n := range []*node{m16n, cur} {
		waitFor(t, 3*time.Minute, n.name+" reconciled", func() bool { s, _ := recState(n); return s == "reconciled" })
	}
	for _, n := range []*node{m15ssh, m15only} {
		waitFor(t, 3*time.Minute, n.name+" held as incompatible", func() bool { s, _ := recState(n); return s == "incompatible" })
		_, reason := recState(n)
		t.Logf("%s after the restore: incompatible — %s", n.name, reason)
		expect(t, strings.Contains(n.c.exec(t, "cat /var/lib/drool-agent/identity.json"), `"controller_epoch"`),
			"%s: no identity", n.name)
	}
	expect(t, status(m15ssh) == "update_required", "M15 after the restore: %s (%s)", status(m15ssh), missing(m15ssh))
	// SSH stands in only where the host allows it.
	jh, _ := runT(t, m15ssh.h.ID, cmd("echo over-ssh", false))
	expect(t, jh.State == "succeeded" && jh.Transport == "ssh", "M15 with SSH fallback after the restore: %+v", jh)
	jh, _ = runT(t, m15only.h.ID, cmd("echo must-not-run", false))
	expect(t, jh.State == "failed" && jh.ErrorClass == "ConfigurationError" && jh.Transport != "ssh",
		"M15 without a fallback after the restore: %+v", jh)
	jh, _ = runT(t, m16n.h.ID, cmd("echo m16-reconciled", false))
	expect(t, jh.State == "succeeded" && jh.Transport == "agent", "M16 after reconciling: %+v", jh)

	// The ordinary update path: this release's Shepherd, signed, rolled
	// out; the M15 Shepherds update in the old epoch, come back with
	// epoch-1, and reconcile.
	signed := filepath.Join(dir, "drool-agent")
	b, _ := os.ReadFile(filepath.Join(agentDir, "drool-agent-linux-amd64"))
	os.WriteFile(signed, b, 0o755)
	os.WriteFile(filepath.Join(dir, "release.key"), key.priv, 0o600)
	out, err := exec.Command(binPath, signArgs("--key", filepath.Join(dir, "release.key"), "--version", agentV1, signed)...).CombinedOutput()
	if err != nil {
		t.Fatalf("release sign: %v\n%s", err, out)
	}
	manifest, _ := os.ReadFile(signed + ".release.json")
	manualShepherdUpdates(t) // the test rolls it out to the hosts it chooses
	req, _ := newRawRequest("POST", "/api/v1/agents/releases", bytes.NewReader(b))
	req.Header.Set("X-DROOL-Release", base64.StdEncoding.EncodeToString(manifest))
	r, err := ctl.client.Do(req)
	if err != nil || (r.StatusCode != 201 && r.StatusCode != 409) {
		t.Fatalf("upload the release: %v %v", err, r)
	}
	r.Body.Close()
	var ro rolloutT
	ctl.must(t, "POST", "/api/v1/agents/rollouts", map[string]any{"version": agentV1, "host_ids": []string{m15ssh.h.ID, m15only.h.ID, m16n.h.ID}}, &ro)
	for _, n := range []*node{m15ssh, m15only, m16n} {
		waitAgent(t, n.h.ID, 5*time.Minute, n.name+" updated", func(a hostAgent) bool {
			return a.Identity != nil && a.Identity.Connected && a.Identity.AgentVersion == agentV1
		})
	}
	for _, n := range []*node{m15ssh, m15only} {
		waitFor(t, 3*time.Minute, n.name+" reconciled after the update", func() bool { s, _ := recState(n); return s == "reconciled" })
		jh, _ := runT(t, n.h.ID, cmd("echo updated-and-reconciled", false))
		expect(t, jh.State == "succeeded" && jh.Transport == "agent", "%s after the update: %+v", n.name, jh)
	}
	for _, n := range nodes {
		expect(t, status(n) == "compatible", "%s after the update: %s (%s)", n.name, status(n), missing(n))
	}
	t.Logf("after the update: every Shepherd is %s and compatible; the M15 ones reconciled", agentV1)
}
