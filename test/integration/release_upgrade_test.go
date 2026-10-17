//go:build integration

package integration

import (
	"bytes"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestReleaseUpgrade upgrades real older DROOL builds to the candidate (the
// release under -release, else this tree's build) on the same data
// directory, as an operator replaces a binary, and goes back the documented
// way. Each part has controllers of its own.
func TestReleaseUpgrade(t *testing.T) {
	t.Run("from-v0.1.0-alpha", testUpgradeFromAlpha)
	t.Run("from-M16", testUpgradeFromM16)
}

// useController points the domain helpers at c for the rest of t.
func useController(t *testing.T, c *controller) {
	shared := ctl
	ctl = c
	t.Cleanup(func() { ctl = shared })
}

// drool runs a drool CLI command against a data directory.
func droolCLI(t *testing.T, bin, dataDir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "DROOL_DATA_DIR="+dataDir, "DROOL_LOGGING_OUTPUT=stdout")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func serveLog(c *controller) string {
	b, _ := os.ReadFile(filepath.Join(filepath.Dir(c.dataDir), "serve.log"))
	return string(b)
}

func jobIDs(t *testing.T) map[string]bool {
	var list struct {
		Items []struct{ ID string } `json:"items"`
	}
	ctl.must(t, "GET", "/api/v1/jobs?limit=200", nil, &list)
	out := map[string]bool{}
	for _, j := range list.Items {
		out[j.ID] = true
	}
	return out
}

// testUpgradeFromAlpha: the tagged Technical Alpha (schema 5) → candidate.
// The candidate backs the database up before migrating it; everything the
// alpha knew is still there and works (the alpha's master key opens its
// credentials, its pinned host key is trusted); going back means restoring
// that backup with the alpha.
func testUpgradeFromAlpha(t *testing.T) {
	old := alphaBuild.build(t, "drool", runtime.GOARCH)
	dir := t.TempDir()
	oc := &controller{bin: old, dataDir: filepath.Join(dir, "data")}
	if err := oc.start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			oc.stop()
		}
	})
	if err := oc.claim(); err != nil {
		t.Fatal(err)
	}
	useController(t, oc)
	d := selected()[0]
	c := startHost(t, image(t, d), "upg-alpha", true, netOuter)
	h := addHost(t, "upg-alpha", c, hostOpts{})
	expect(t, h.Status == "ready" || h.Status == "unsupported", "on the alpha: %s %s", h.Status, h.Detail)
	pw := createSecret(t, "upg-alpha-pw", "password", "alpha secret value")
	var g struct{ ID string }
	ctl.must(t, "POST", "/api/v1/groups", map[string]any{"name": "upg-alpha"}, &g)
	before := submit(t, []string{h.ID}, cmd("echo before-upgrade", false))
	waitJob(t, before, 5*time.Minute)
	expect(t, getJobHost(t, before, h.ID).State == "succeeded", "a job on the alpha: %v", getJobHost(t, before, h.ID))
	oc.stop()
	stopped = true

	// Replace the binary, start again.
	nc := &controller{dataDir: oc.dataDir, port: oc.port, setupKey: oc.setupKey, client: insecureClient(), extraEnv: controllerEnv(freePort())}
	if err := nc.start(); err != nil {
		t.Fatalf("the candidate on the alpha's data: %v\n%s", err, tail(serveLog(nc), 30))
	}
	t.Cleanup(nc.stop)
	log := serveLog(nc)
	expect(t, strings.Contains(log, "Backed up the database before upgrading it from schema 5 to"), "no pre-upgrade backup announced:\n%s", tail(log, 30))
	pre, _ := filepath.Glob(filepath.Join(oc.dataDir, "backups", "drool-pre-upgrade-*-schema5.tar.gz"))
	if len(pre) != 1 {
		t.Fatalf("pre-upgrade backups: %v", pre)
	}
	if err := nc.login(); err != nil {
		t.Fatalf("the alpha's administrator after the upgrade: %v", err)
	}
	useController(t, nc)
	hh := getHost(t, h.ID)
	expect(t, hh.Name == "upg-alpha", "the alpha's host: %+v", hh)
	expect(t, jobIDs(t)[before], "the alpha's job history is gone")
	var secrets struct {
		Items []struct{ ID, Name string } `json:"items"`
	}
	ctl.must(t, "GET", "/api/v1/secrets", nil, &secrets)
	found := false
	for _, s := range secrets.Items {
		found = found || s.ID == pw
	}
	expect(t, found, "the alpha's credential is gone")
	// The alpha's SSH key (sealed with its master key) and its pinned host
	// key still work.
	after := submit(t, []string{h.ID}, cmd("echo after-upgrade", false))
	waitJob(t, after, 5*time.Minute)
	expect(t, getJobHost(t, after, h.ID).State == "succeeded", "a job after the upgrade: %v", getJobHost(t, after, h.ID))
	out, err := droolCLI(t, binPath, oc.dataDir, "backup", "verify", "--master-key", filepath.Join(oc.dataDir, "master.key"), pre[0])
	expect(t, err == nil && strings.Contains(out, "Restorable and complete") && strings.Contains(out, "schema 5"),
		"the pre-upgrade backup: %v\n%s", err, out)
	nc.stop()

	// The alpha refuses the upgraded database rather than run on it, and
	// leaves it alone.
	db := filepath.Join(oc.dataDir, "drool.db")
	sum, _ := fileSHA256(db)
	out, err = droolCLI(t, old, oc.dataDir, "migrate")
	expect(t, err != nil && strings.Contains(out, "newer than this binary supports"), "the alpha on the upgraded database: %v\n%s", err, out)
	sum2, _ := fileSHA256(db)
	expect(t, sum == sum2, "the alpha changed the upgraded database")

	// Back to the alpha, the documented way: restore the pre-upgrade backup
	// with the older version, start it.
	out, err = droolCLI(t, old, oc.dataDir, "restore", "--yes", pre[0])
	if err != nil {
		t.Fatalf("restoring the pre-upgrade backup with the alpha: %v\n%s", err, out)
	}
	t.Logf("the alpha restored its pre-upgrade backup:\n%s", out)
	back := &controller{bin: old, dataDir: oc.dataDir, port: oc.port, setupKey: oc.setupKey, client: insecureClient()}
	if err := back.start(); err != nil {
		t.Fatalf("the alpha after rolling back: %v\n%s", err, tail(serveLog(back), 30))
	}
	t.Cleanup(back.stop)
	if err := back.login(); err != nil {
		t.Fatal(err)
	}
	useController(t, back)
	ids := jobIDs(t)
	expect(t, ids[before] && !ids[after], "after rolling back: the job before the upgrade %v, the one after %v", ids[before], ids[after])
	again := submit(t, []string{h.ID}, cmd("echo rolled-back", false))
	waitJob(t, again, 5*time.Minute)
	expect(t, getJobHost(t, again, h.ID).State == "succeeded", "a job after rolling back: %v", getJobHost(t, again, h.ID))
}

// testUpgradeFromM16: the build homelabs run (M16, Shepherds, SSH
// certificates, Desired State, monitoring) → candidate, and back.
func testUpgradeFromM16(t *testing.T) {
	old := m16Build.build(t, "drool", runtime.GOARCH)
	oldAgents := m16Build.agentDirOf(t)
	m16Version := m16Build.version(t)
	dir := t.TempDir()
	agentPortFixed := freePort() // Shepherds keep the address they enrolled with
	env := func(agents string) []string {
		e := controllerEnv(agentPortFixed)
		for i, kv := range e {
			if strings.HasPrefix(kv, "DROOL_AGENT_BINARIES_DIR=") {
				e[i] = "DROOL_AGENT_BINARIES_DIR=" + agents
			}
		}
		return e
	}
	oc := &controller{bin: old, dataDir: filepath.Join(dir, "data"), extraEnv: env(oldAgents)}
	if err := oc.start(); err != nil {
		t.Fatal(err)
	}
	running := oc
	t.Cleanup(func() { running.stop() })
	if err := oc.claim(); err != nil {
		t.Fatal(err)
	}
	useController(t, oc)
	d := selected()[0]

	// A Shepherd host (M16's Shepherd, installed over SSH)…
	ca := startHost(t, image(t, d), "upg-shep", true, netOuter)
	ha := addHost(t, "upg-shep", ca, hostOpts{})
	installAgent(t, ha.ID, "full", "")
	waitAgent(t, ha.ID, 2*time.Minute, "the M16 Shepherd", func(a hostAgent) bool {
		return a.Management.Active == "agent" && a.Identity != nil && a.Identity.AgentVersion == m16Version
	})
	// …a host on short-lived SSH certificates only…
	cb := startHost(t, image(t, d), "upg-cert", true, netOuter)
	hb := addHost(t, "upg-cert", cb, hostOpts{})
	ctl.must(t, "POST", "/api/v1/ssh-ca", map[string]string{"name": "upg-ca"}, nil)
	var tj struct{ ID string }
	ctl.must(t, "POST", "/api/v1/jobs", map[string]any{"steps": []any{map[string]any{"type": "ssh_ca_trust"}},
		"targets": map[string]any{"hosts": []string{hb.ID}}}, &tj)
	waitJob(t, tj.ID, 5*time.Minute)
	expect(t, getJobHost(t, tj.ID, hb.ID).State == "succeeded", "SSH CA trust on M16: %v", getJobHost(t, tj.ID, hb.ID))
	ctl.must(t, "PUT", "/api/v1/hosts/"+hb.ID+"/ssh-auth", map[string]string{"mode": "certificate"}, nil)
	cb.exec(t, ": > /home/deploy/.ssh/authorized_keys")
	// …a Desired State baseline the Shepherd host is checked against.
	ctl.must(t, "POST", "/api/v1/desired/baselines", map[string]any{"name": "upg-base",
		"assignments": []map[string]any{{"scope": "host", "host_id": ha.ID}},
		"resources":   []map[string]any{{"kind": "package", "name": d.Pkg, "state": "absent"}}}, nil)
	dsWait(t, ha.ID, "package:"+d.Pkg, "compliant", 3*time.Minute)
	before := submit(t, []string{ha.ID}, cmd("echo before-upgrade", false))
	waitJob(t, before, 5*time.Minute)
	oc.stop()

	// The candidate on the same data; no schema change since M16 means no
	// pre-upgrade backup (one is made only when the database is migrated).
	nc := &controller{dataDir: oc.dataDir, port: oc.port, setupKey: oc.setupKey, client: insecureClient(), extraEnv: env(agentDir)}
	if err := nc.start(); err != nil {
		t.Fatalf("the candidate on M16's data: %v\n%s", err, tail(serveLog(nc), 30))
	}
	running = nc
	upgradedAt := time.Now().UnixMilli()
	if err := nc.login(); err != nil {
		t.Fatal(err)
	}
	useController(t, nc)
	var info struct {
		Version       string `json:"version"`
		SchemaVersion int    `json:"schema_version"`
	}
	ctl.must(t, "GET", "/api/v1/system/info", nil, &info)
	pre, _ := filepath.Glob(filepath.Join(oc.dataDir, "backups", "drool-pre-upgrade-*.tar.gz"))
	migrated := strings.Contains(serveLog(nc), "Backed up the database before upgrading it")
	expect(t, migrated == (len(pre) > 0), "a pre-upgrade backup (%v) without the upgrade saying so (%v), or the reverse", pre, migrated)
	t.Logf("M16 → %s: schema %d, pre-upgrade backups %v", info.Version, info.SchemaVersion, pre)

	// The M16 Shepherd reconnects and keeps working; DROOL says what it is.
	waitAgent(t, ha.ID, 3*time.Minute, "the M16 Shepherd reconnecting", func(a hostAgent) bool {
		return a.Management.Active == "agent" && a.Identity != nil && a.Identity.Connected && a.Identity.AgentVersion == m16Version
	})
	jh, _ := runT(t, ha.ID, cmd("echo through-the-m16-shepherd", false))
	expect(t, jh.State == "succeeded" && jh.Transport == "agent", "a job through the M16 Shepherd after the upgrade: %+v", jh)
	expect(t, jobIDs(t)[before], "M16's job history is gone")
	// Certificates are still issued for the certificate-only host.
	jhb := run(t, hb.ID, cmd("echo with-a-certificate", false))
	expect(t, jhb.State == "succeeded", "the certificate host after the upgrade: %v", jhb)
	logs, _ := exec.Command("docker", "logs", cb.Name).CombinedOutput() // sshd -e logs to stderr
	expect(t, strings.Contains(string(logs), "ID drool:"), "no certificate login on the host after the upgrade")
	t.Logf("after the upgrade: the M16 Shepherd ran a job, the certificate-only host signed in with a fresh certificate")
	// Desired State is checked again by the new version.
	ca.exec(t, installPkgCmd(d, d.Pkg))
	dsWait(t, ha.ID, "package:"+d.Pkg, "drift", 3*time.Minute)
	t.Logf("after the upgrade: Desired State found the drift made after it")
	// Monitoring arrives from the Shepherd.
	waitFor(t, 3*time.Minute, "monitoring data from the M16 Shepherd after the upgrade", func() bool {
		var m struct {
			HasData   bool   `json:"has_data"`
			Source    string `json:"source"`
			UpdatedAt int64  `json:"updated_at"`
		}
		st, _ := ctl.do("GET", "/api/v1/hosts/"+ha.ID+"/monitoring", nil, &m)
		return st == 200 && m.HasData && m.Source == "agent" && m.UpdatedAt > upgradedAt
	})

	// Update the Shepherd to the candidate's, signed the documented way.
	keyDir := t.TempDir()
	if out, err := droolCLI(t, binPath, keyDir, "release", "keygen", "-o", filepath.Join(keyDir, "drool-release")); err != nil {
		t.Fatalf("release keygen: %v\n%s", err, out)
	}
	pub, _ := os.ReadFile(filepath.Join(keyDir, "drool-release.pub"))
	ctl.must(t, "POST", "/api/v1/agents/release-keys", map[string]any{"public_key": strings.TrimSpace(string(pub))}, nil)
	// The Shepherd must trust the key too (DROOL can't do that remotely).
	tmp := "/tmp/drool-release.pub"
	ca.exec(t, "cat > "+tmp+" <<'EOF'\n"+strings.TrimSpace(string(pub))+"\nEOF\n/usr/libexec/drool-agent/current/drool-agent trust-release-key "+tmp)
	bin := filepath.Join(keyDir, "drool-agent")
	b, _ := os.ReadFile(filepath.Join(agentDir, "drool-agent-linux-amd64"))
	os.WriteFile(bin, b, 0o755)
	out, err := droolCLI(t, binPath, keyDir, signArgs("--key", filepath.Join(keyDir, "drool-release.key"), bin)...)
	if err != nil {
		// Only a build from a release tag records its version; others name it.
		expect(t, strings.Contains(out, "give it with --version"), "release sign without --version: %v\n%s", err, out)
		out, err = droolCLI(t, binPath, keyDir, signArgs("--key", filepath.Join(keyDir, "drool-release.key"), "--version", agentV1, bin)...)
	}
	if err != nil {
		t.Fatalf("release sign: %v\n%s", err, out)
	}
	manifest, _ := os.ReadFile(bin + ".release.json")
	manualShepherdUpdates(t) // the test rolls it out itself
	req, _ := newRawRequest("POST", "/api/v1/agents/releases", bytes.NewReader(b))
	req.Header.Set("X-DROOL-Release", base64.StdEncoding.EncodeToString(manifest))
	r, err := ctl.client.Do(req)
	if err != nil || r.StatusCode != 201 {
		t.Fatalf("upload the signed Shepherd: %v %d", err, r.StatusCode)
	}
	r.Body.Close()
	var ro rolloutT
	ctl.must(t, "POST", "/api/v1/agents/rollouts", map[string]any{"version": agentV1, "host_ids": []string{ha.ID}}, &ro)
	waitAgent(t, ha.ID, 5*time.Minute, "the Shepherd updated to "+agentV1, func(a hostAgent) bool {
		return a.Identity != nil && a.Identity.Connected && a.Identity.AgentVersion == agentV1
	})
	jh, _ = runT(t, ha.ID, cmd("echo through-the-updated-shepherd", false))
	expect(t, jh.State == "succeeded" && jh.Transport == "agent", "a job through the updated Shepherd: %+v", jh)
	t.Logf("monitoring arrived after the upgrade; the Shepherd updated %s → %s (signed with drool release sign) and works", m16Version, agentV1)
	nc.stop()

	// Back to M16. Without a schema change the older version just runs on
	// the same data again; the Shepherd stays on the newer version (Shepherds
	// never downgrade) and works with it over the same protocol.
	if len(pre) > 0 {
		out, err := droolCLI(t, old, oc.dataDir, "restore", "--yes", pre[len(pre)-1])
		if err != nil {
			t.Fatalf("restoring the pre-upgrade backup with M16: %v\n%s", err, out)
		}
	}
	back := &controller{bin: old, dataDir: oc.dataDir, port: oc.port, setupKey: oc.setupKey, client: insecureClient(), extraEnv: env(oldAgents)}
	if err := back.start(); err != nil {
		t.Fatalf("M16 after going back: %v\n%s", err, tail(serveLog(back), 30))
	}
	running = back
	if err := back.login(); err != nil {
		t.Fatal(err)
	}
	useController(t, back)
	waitAgent(t, ha.ID, 3*time.Minute, "the updated Shepherd on M16 again", func(a hostAgent) bool {
		return a.Management.Active == "agent" && a.Identity != nil && a.Identity.Connected
	})
	if len(pre) == 0 {
		jh, _ = runT(t, ha.ID, cmd("echo back-on-m16", false))
		expect(t, jh.State == "succeeded" && jh.Transport == "agent", "a job through the newer Shepherd on M16: %+v", jh)
		t.Logf("back on M16 (no schema change, so no restore needed): the %s Shepherd reconnected and ran a job", agentV1)
	}
}

func installPkgCmd(d distro, pkg string) string {
	if d.PkgManager == "apt" {
		return "DEBIAN_FRONTEND=noninteractive apt-get install -y -q " + pkg + " >/dev/null 2>&1 || (apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -q " + pkg + " >/dev/null)"
	}
	return "dnf install -y -q " + pkg + " >/dev/null"
}
