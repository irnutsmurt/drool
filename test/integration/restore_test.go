//go:build integration

package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRestoreReconciliation runs the real drool backup and drool restore
// against a controller of its own while real Shepherds keep running in
// unprivileged containers (DESIGN §10.3, ARCHITECTURE-ALPHA §33.1): the
// restored controller gets a new epoch; a host that didn't change since the
// backup is moved to it by itself; a host whose configuration changed after
// the backup is held, receives nothing, and moves only when an
// administrator accepts its state.
func TestRestoreReconciliation(t *testing.T) {
	d := selected()[0]
	dir := t.TempDir()
	port := freePort()
	rc := &controller{dataDir: filepath.Join(dir, "data"), extraEnv: []string{
		"DROOL_AGENT_BIND=" + agentGateway,
		fmt.Sprintf("DROOL_AGENT_PORT=%d", port),
		fmt.Sprintf("DROOL_AGENT_ADVERTISE=%s:%d", agentGateway, port),
		"DROOL_AGENT_BINARIES_DIR=" + agentDir,
		"DROOL_TEST_AUTH_TIMING=fast",
	}}
	if err := rc.start(); err != nil {
		t.Fatal(err)
	}
	defer rc.stop()
	if err := rc.claim(); err != nil {
		t.Fatal(err)
	}
	// The domain helpers talk to ctl; this test has the run to itself
	// (it isn't parallel), so they talk to its own controller meanwhile.
	shared := ctl
	ctl = rc
	defer func() { ctl = shared }()

	same := startHost(t, image(t, d), "rst-same", true, netOuter)
	ahead := startHost(t, image(t, d), "rst-ahead", true, netOuter)
	hs := addHost(t, "rst-same", same, hostOpts{})
	ha := addHost(t, "rst-ahead", ahead, hostOpts{})
	type stateVersion struct {
		Domain           string `json:"domain"`
		Revision         int64  `json:"revision"`
		ReportedRevision int64  `json:"reported_revision"`
		Status           string `json:"status"`
	}
	reported := func(id string) int64 {
		var a struct {
			StateVersions []stateVersion `json:"state_versions"`
		}
		ctl.must(t, "GET", "/api/v1/hosts/"+id+"/agent", nil, &a)
		for _, sv := range a.StateVersions {
			if sv.Domain == "agent-config" && sv.Status == "ok" {
				return sv.ReportedRevision
			}
		}
		return 0
	}
	caps := func(id string, packages bool) {
		t.Helper()
		ctl.must(t, "PUT", "/api/v1/hosts/"+id+"/agent/caps", map[string]any{"caps": map[string]any{
			"monitoring": true, "shell": true, "reboot": false, "firewall": true, "users": false,
			"packages": map[string]any{"enabled": packages, "configured_repositories_only": true},
			"services": map[string]any{"enabled": true}, "files": map[string]any{"enabled": true}}}, nil)
	}
	for _, h := range []host{hs, ha} {
		installAgent(t, h.ID, "full", "")
		waitAgent(t, h.ID, 2*time.Minute, "Shepherd connected", func(a hostAgent) bool { return a.Identity != nil && a.Identity.Connected })
		// Shepherd only: nothing may fall back to SSH.
		ctl.must(t, "PUT", "/api/v1/hosts/"+h.ID+"/management", map[string]any{"preferred": "agent", "fallback": "none"}, nil)
		caps(h.ID, true)
		waitFor(t, time.Minute, "configuration applied on "+h.Name, func() bool { return reported(h.ID) == 1 })
	}
	var pki struct {
		ControllerEpoch string `json:"controller_epoch"`
	}
	ctl.must(t, "GET", "/api/v1/agents/pki", nil, &pki)
	oldEpoch := pki.ControllerEpoch

	// The backup, taken while the controller runs.
	env := append(os.Environ(), "DROOL_DATA_DIR="+rc.dataDir)
	file := filepath.Join(dir, "backup.tar.gz")
	b := exec.Command(binPath, "backup", "--include-master-key", "-o", file)
	b.Env = env
	if out, err := b.CombinedOutput(); err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}
	// After the backup, one host's configuration changes again.
	caps(ha.ID, false)
	waitFor(t, time.Minute, "second configuration applied", func() bool { return reported(ha.ID) == 2 })

	rc.stop()
	r := exec.Command(binPath, "restore", "--yes", file)
	r.Env = env
	out, err := r.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "new controller epoch") {
		t.Fatalf("restore: %v\n%s", err, out)
	}
	if err := rc.start(); err != nil {
		t.Fatal(err)
	}
	if err := rc.login(); err != nil {
		t.Fatal(err)
	}
	ctl.must(t, "GET", "/api/v1/agents/pki", nil, &pki)
	if pki.ControllerEpoch == oldEpoch {
		t.Fatalf("the restored controller kept the backup's epoch %s", oldEpoch)
	}
	state := func(id string) (string, string) {
		var rec struct {
			State  string `json:"state"`
			Reason string `json:"reason"`
		}
		ctl.must(t, "GET", "/api/v1/hosts/"+id+"/reconciliation", nil, &rec)
		return rec.State, rec.Reason
	}
	waitFor(t, 3*time.Minute, "the unchanged host reconciled", func() bool { s, _ := state(hs.ID); return s == "reconciled" })
	waitFor(t, 3*time.Minute, "the changed host held", func() bool { s, _ := state(ha.ID); return s == "ahead" })
	identity := func(c *container) string { return c.exec(t, "cat /var/lib/drool-agent/identity.json") }
	expect(t, strings.Contains(identity(same), pki.ControllerEpoch), "the reconciled Shepherd's identity: %s", identity(same))
	expect(t, strings.Contains(identity(ahead), oldEpoch), "the held Shepherd moved: %s", identity(ahead))

	jh := run(t, hs.ID, cmd("echo restored", false))
	expect(t, jh.State == "succeeded" && jh.Transport == "agent", "job on the reconciled host: %v", jh)
	jh = run(t, ha.ID, cmd("echo must-not-run", false))
	expect(t, jh.State == "failed" && jh.ErrorClass == "ConfigurationError", "job on the held host: %v", jh)

	ctl.must(t, "POST", "/api/v1/hosts/"+ha.ID+"/reconciliation/accept", nil, nil)
	waitFor(t, 3*time.Minute, "the accepted host reconciled", func() bool { s, _ := state(ha.ID); return s == "reconciled" })
	expect(t, strings.Contains(identity(ahead), pki.ControllerEpoch), "accepted Shepherd's identity: %s", identity(ahead))
	expect(t, reported(ha.ID) == 2, "the accepted host's configuration was rolled back: revision %d", reported(ha.ID))
	jh = run(t, ha.ID, cmd("echo accepted", false))
	expect(t, jh.State == "succeeded" && jh.Transport == "agent", "job after accepting: %v", jh)

	var st struct {
		Restore *struct {
			Status string `json:"status"`
		} `json:"restore"`
	}
	ctl.must(t, "GET", "/api/v1/system/restore", nil, &st)
	expect(t, st.Restore != nil && st.Restore.Status == "finished", "restore status: %+v", st.Restore)
	for _, c := range []*container{same, ahead} {
		stopAgent(t, c)
	}
}
