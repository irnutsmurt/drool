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

// TestVMHardening (M16) on real systemd guests under QEMU/TCG: the SSH
// certificate authority's trust step against the distribution's own sshd
// (reloaded through systemctl; Ubuntu 24.04 starts ssh from ssh.socket),
// certificate-only work afterwards; and, on the first VM, a backup, a
// restore and reconciliation with a real Shepherd and a committed nftables
// policy — the ruleset must come through the epoch transition byte for byte.
//
//	go test -tags integration ./test/integration -run TestVMHardening -vm debian-12,ubuntu-24.04 -v -timeout 4h
func TestVMHardening(t *testing.T) {
	if *flagVMs == "" {
		t.Skip("set -vm to run the VM tests")
	}
	freshController(t) // a controller of its own: nothing from other tests (M17)
	want := map[string]bool{}
	for _, n := range strings.Split(*flagVMs, ",") {
		want[strings.TrimSpace(n)] = true
	}
	first := true
	for _, def := range vmDefs {
		if !want[def.Name] {
			continue
		}
		restore := first
		first = false
		t.Run(def.Name, func(t *testing.T) {
			v := startVM(t, def)
			h := addVMHost(t, "m16-"+def.Name, v, hostOpts{})
			// The VM goes away with this subtest. A certificate host left
			// behind would rightly keep the next VM's rotation from
			// activating an authority it never accepted.
			t.Cleanup(func() { ctl.do("DELETE", "/api/v1/hosts/"+h.ID, nil, nil) })
			if restore {
				t.Run("restore and reconcile a real firewall", func(t *testing.T) { testVMRestoreFirewall(t, def, v, h) })
			}
			t.Run("SSH certificates with the distribution's sshd", func(t *testing.T) { testVMSSHCertificates(t, def, v, h) })
		})
	}
}

func testVMSSHCertificates(t *testing.T, def vmDef, v *vm, h host) {
	var ca struct {
		Authorities []struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"authorities"`
	}
	ctl.must(t, "GET", "/api/v1/ssh-ca", nil, &ca)
	active := false
	for _, a := range ca.Authorities {
		active = active || a.State == "active"
	}
	if !active {
		ctl.must(t, "POST", "/api/v1/ssh-ca", map[string]string{"name": "vm-" + runID}, &ca)
	}
	var j struct{ ID string }
	ctl.must(t, "POST", "/api/v1/jobs", map[string]any{"steps": []any{map[string]any{"type": "ssh_ca_trust"}},
		"targets": map[string]any{"hosts": []string{h.ID}}}, &j)
	waitJob(t, j.ID, 15*time.Minute)
	jh := getJobHost(t, j.ID, h.ID)
	out := output(t, j.ID, h.ID)
	if jh.State != "succeeded" || !strings.Contains(out, "accepted on a fresh connection") {
		diag := mustVMSSH(t, v, "systemctl status ssh sshd ssh.socket --no-pager 2>&1 | head -40; sshd -T | grep -i trustedusercakeys")
		t.Fatalf("trust step on %s: %v\n%s\n--- host ---\n%s", def.Name, jh, out, diag)
	}
	expect(t, strings.Contains(mustVMSSH(t, v, "sshd -T | grep -i ^trustedusercakeys"), "/etc/ssh/drool_user_ca.pub"), "sshd -T")
	// It was reloaded through systemd, which still runs it.
	unit := mustVMSSH(t, v, "systemctl is-active "+def.SSHUnit+" || true")
	expect(t, strings.Contains(unit, "active"), "%s after the reload: %s", def.SSHUnit, unit)

	ctl.must(t, "PUT", "/api/v1/hosts/"+h.ID+"/ssh-auth", map[string]string{"mode": "certificate"}, nil)
	// The static key goes away entirely; the side channel keeps its own key.
	mustVMSSH(t, v, "grep -v -F "+shq(strings.Fields(ctl.controllerKey(t))[1])+" /home/deploy/.ssh/authorized_keys > /tmp/ak; cat /tmp/ak > /home/deploy/.ssh/authorized_keys")
	since := time.Now().Add(-time.Minute).UTC().Format("2006-01-02 15:04:05")
	jh2 := run(t, h.ID, cmd("id -un; sudo -n true && echo root-ok", false))
	expect(t, jh2.State == "succeeded", "work with certificates only on %s: %v", def.Name, jh2)
	logs := mustVMSSH(t, v, "journalctl -u "+def.SSHUnit+" --since "+shq(since)+" --no-pager | grep -i 'cert' | tail -5")
	expect(t, strings.Contains(logs, "ED25519-CERT") && strings.Contains(logs, "ID drool:"), "sshd's log on %s:\n%s", def.Name, logs)

	// Rotation with the host on certificates only: introduce, trust both,
	// activate, retire, trust again — sshd reloaded by systemd each time,
	// and the host never unreachable.
	trust := func() {
		t.Helper()
		var j struct{ ID string }
		ctl.must(t, "POST", "/api/v1/jobs", map[string]any{"steps": []any{map[string]any{"type": "ssh_ca_trust"}},
			"targets": map[string]any{"hosts": []string{h.ID}}}, &j)
		waitJob(t, j.ID, 15*time.Minute)
		if jh := getJobHost(t, j.ID, h.ID); jh.State != "succeeded" {
			t.Fatalf("trust during the rotation on %s: %v\n%s", def.Name, jh, output(t, j.ID, h.ID))
		}
	}
	var rot struct {
		Authorities []struct {
			ID        string `json:"id"`
			State     string `json:"state"`
			PublicKey string `json:"public_key"`
		} `json:"authorities"`
	}
	ctl.must(t, "POST", "/api/v1/ssh-ca/rotate", map[string]string{"name": "vm-next-" + def.Name}, &rot)
	var next, old, oldKey string
	for _, a := range rot.Authorities {
		switch a.State {
		case "next":
			next = a.ID
		case "active":
			old, oldKey = a.ID, a.PublicKey
		}
	}
	trust()
	ctl.must(t, "POST", "/api/v1/ssh-ca/"+next+"/activate", nil, nil)
	if jh := run(t, h.ID, cmd("true", false)); jh.State != "succeeded" {
		t.Fatalf("with the new authority on %s: %v", def.Name, jh)
	}
	ctl.must(t, "POST", "/api/v1/ssh-ca/"+old+"/retire", nil, nil)
	trust()
	keys := mustVMSSH(t, v, "cat /etc/ssh/drool_user_ca.pub")
	expect(t, !strings.Contains(keys, strings.Fields(oldKey)[1]), "the retired authority is still trusted on %s", def.Name)
	if jh := run(t, h.ID, cmd("true", false)); jh.State != "succeeded" {
		t.Fatalf("after the rotation on %s: %v", def.Name, jh)
	}
}

// testVMRestoreFirewall runs a controller of its own against the VM.
func testVMRestoreFirewall(t *testing.T, def vmDef, v *vm, sharedHost host) {
	dir := t.TempDir()
	port := freePort()
	rc := &controller{dataDir: filepath.Join(dir, "data"), extraEnv: []string{
		"DROOL_AGENT_BIND=" + agentGateway, fmt.Sprintf("DROOL_AGENT_PORT=%d", port),
		fmt.Sprintf("DROOL_AGENT_ADVERTISE=%s:%d", agentGateway, port), "DROOL_AGENT_BINARIES_DIR=" + agentDir,
		"DROOL_TEST_AUTH_TIMING=fast"}}
	if err := rc.start(); err != nil {
		t.Fatal(err)
	}
	defer rc.stop()
	if err := rc.claim(); err != nil {
		t.Fatal(err)
	}
	// The VM trusts the shared controller's key; let this one in too.
	key := rc.controllerKey(t)
	mustVMSSH(t, v, "echo "+shq(key)+" >> /home/deploy/.ssh/authorized_keys")
	shared := ctl
	ctl = rc
	defer func() { ctl = shared }()
	h := addVMHost(t, "rst-"+def.Name, v, hostOpts{})
	ensureNft(t, def.Name, h)
	installAgent(t, h.ID, "full", "")
	waitAgent(t, h.ID, 5*time.Minute, "Shepherd connected", func(a hostAgent) bool { return a.Management.Active == "agent" })
	ctl.must(t, "PUT", "/api/v1/firewall/hosts/"+h.ID, map[string]any{"mode": "enforce", "rollback_timeout_s": 120}, nil)
	fwPolicy(t, map[string]any{"name": "web-" + def.Name, "from": []any{map[string]any{"kind": "internal"}},
		"to": []any{map[string]any{"kind": "host", "id": h.ID}}, "services": []string{"https"}})
	jh, tx := fwApply(t, h.ID, nil)
	if jh.State != "succeeded" || tx.State != "committed" || tx.Transport != "agent" {
		t.Fatalf("apply through the Shepherd: %v %+v\n%s", jh, tx, tx.timeline())
	}
	ruleset := mustVMSSH(t, v, "nft list table inet drool | sha256sum")
	var pki struct {
		ControllerEpoch string `json:"controller_epoch"`
	}
	ctl.must(t, "GET", "/api/v1/agents/pki", nil, &pki)
	oldEpoch := pki.ControllerEpoch
	expect(t, strings.Contains(mustVMSSH(t, v, "cat /var/lib/drool/firewall/committed.json"), oldEpoch), "committed in the old epoch")

	env := append(os.Environ(), "DROOL_DATA_DIR="+rc.dataDir)
	file := filepath.Join(dir, "backup.tar.gz")
	b := exec.Command(binPath, "backup", "--include-master-key", "-o", file)
	b.Env = env
	if out, err := b.CombinedOutput(); err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}
	rc.stop()
	r := exec.Command(binPath, "restore", "--yes", file)
	r.Env = env
	if out, err := r.CombinedOutput(); err != nil {
		t.Fatalf("restore: %v\n%s", err, out)
	}
	if err := rc.start(); err != nil {
		t.Fatal(err)
	}
	if err := rc.login(); err != nil {
		t.Fatal(err)
	}
	ctl.must(t, "GET", "/api/v1/agents/pki", nil, &pki)
	waitFor(t, 10*time.Minute, "the VM reconciled", func() bool {
		var rec struct {
			State string `json:"state"`
		}
		ctl.must(t, "GET", "/api/v1/hosts/"+h.ID+"/reconciliation", nil, &rec)
		return rec.State == "reconciled"
	})
	committed := mustVMSSH(t, v, "cat /var/lib/drool/firewall/committed.json")
	expect(t, strings.Contains(committed, pki.ControllerEpoch) && !strings.Contains(committed, oldEpoch), "committed after the rebase:\n%s", committed)
	expect(t, mustVMSSH(t, v, "nft list table inet drool | sha256sum") == ruleset, "the ruleset changed during reconciliation")
	timers := mustVMSSH(t, v, "systemctl list-units --all --no-legend 'drool-fw-rollback-*' | wc -l")
	expect(t, strings.TrimSpace(timers) == "0", "a rollback timer is armed after the reconciliation: %s", timers)
	// The next transaction, in the new epoch, through the Shepherd.
	jh, tx = fwApply(t, h.ID, nil)
	expect(t, jh.State == "succeeded" && tx.State == "committed" && tx.Revision == 2, "apply after the restore: %v %+v\n%s", jh, tx, tx.timeline())
	// Leave the VM to the shared controller: this one's Shepherd stops.
	mustVMSSH(t, v, "systemctl disable --now drool-agent 2>/dev/null || true")
	_ = sharedHost
}
