//go:build integration

package integration

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestVMFirewallUpgrade: a firewall that an older controller enforces on a
// real host, through an in-place upgrade of the controller to the build
// under test (M17 upgrade qualification). The older version is M15, whose
// host tool is drool-fw version 1 (M16 brought version 2). Over SSH: the
// committed policy is recognized after the upgrade, not taken for drift, and
// the next revision commits with the new tool. Through a Shepherd: the M15
// Shepherd keeps applying revisions with its own tool for the upgraded
// controller, and after a signed update to this version's Shepherd, with
// the new one. Both end with a reboot: the boot loader brings back the last
// committed policy. One VM at a time (amd64: the older builds are built for
// it):
//
//	go test -tags integration ./test/integration -run TestVMFirewallUpgrade -vm debian-12 -v -timeout 3h
func TestVMFirewallUpgrade(t *testing.T) {
	if *flagVMs == "" {
		t.Skip("set -vm to run the VM tests")
	}
	want := wantedVMs()
	for _, def := range vmDefs {
		if !want[def.Name] || def.Arch != "" {
			continue
		}
		t.Run(def.Name, func(t *testing.T) {
			t.Run("over SSH", func(t *testing.T) { testFirewallUpgrade(t, def, false) })
			t.Run("through a Shepherd", func(t *testing.T) { testFirewallUpgrade(t, def, true) })
		})
	}
}

func testFirewallUpgrade(t *testing.T, def vmDef, shepherd bool) {
	old := m15Build.build(t, "drool", runtime.GOARCH)
	oldAgents := m15Build.agentDirOf(t)
	m15Version := m15Build.version(t)
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
	oc := &controller{bin: old, dataDir: filepath.Join(t.TempDir(), "data"), extraEnv: env(oldAgents)}
	if err := oc.start(); err != nil {
		t.Fatal(err)
	}
	running := oc
	t.Cleanup(func() { running.stop() })
	if err := oc.claim(); err != nil {
		t.Fatal(err)
	}
	useController(t, oc)

	v := startVM(t, def)
	h := addVMHost(t, "fwup-"+def.Name, v, hostOpts{})
	ensureNft(t, def.Name, h)
	key := newReleaseKey(t) // signs the update the Shepherd gets after the upgrade
	transport := "ssh"
	if shepherd {
		transport = "agent"
		ctl.must(t, "POST", "/api/v1/agents/release-keys", map[string]any{"public_key": key.line}, nil)
		installAgent(t, h.ID, "full", key.line)
		waitAgent(t, h.ID, 5*time.Minute, "the M15 Shepherd", func(a hostAgent) bool {
			return a.Management.Active == "agent" && a.Identity != nil && a.Identity.Connected && a.Identity.AgentVersion == m15Version
		})
	}
	tool := func() string { return mustVMSSH(t, v, "/usr/libexec/drool-firewall/drool-fw version") }
	var policies []string
	commit := func(what, service string) fwTxT {
		t.Helper()
		name := "up-" + service
		fwPolicy(t, map[string]any{"name": name, "from": []any{map[string]any{"kind": "internal"}},
			"to": []any{map[string]any{"kind": "host", "id": h.ID}}, "services": []string{service}})
		policies = append(policies, name)
		jh, tx := fwApply(t, h.ID, nil)
		if jh.State != "succeeded" || tx.State != "committed" || tx.Transport != transport {
			t.Fatalf("%s: %v %+v\n%s", what, jh, tx, tx.timeline())
		}
		live := mustVMSSH(t, v, "nft list table inet drool")
		for _, p := range policies {
			expect(t, strings.Contains(live, "policy "+p), "%s: the live table lacks %s:\n%s", what, p, live)
		}
		return tx
	}
	ctl.must(t, "PUT", "/api/v1/firewall/hosts/"+h.ID, map[string]any{"mode": "enforce", "rollback_timeout_s": 90}, nil)
	first := commit("M15 enforcing", "https")
	expect(t, tool() == "1", "M15's host tool is version %q", tool())
	oc.stop()

	// The build under test on M15's data, as an operator replaces a binary.
	nc := &controller{dataDir: oc.dataDir, port: oc.port, setupKey: oc.setupKey, client: insecureClient(), extraEnv: env(agentDir)}
	if err := nc.start(); err != nil {
		t.Fatalf("the build under test on M15's data: %v\n%s", err, tail(serveLog(nc), 30))
	}
	running = nc
	if err := nc.login(); err != nil {
		t.Fatal(err)
	}
	useController(t, nc)
	pre, _ := filepath.Glob(filepath.Join(oc.dataDir, "backups", "drool-pre-upgrade-*.tar.gz"))
	t.Logf("M15 → %s; pre-upgrade backup %v", agentV1, pre)
	if shepherd {
		waitAgent(t, h.ID, 3*time.Minute, "the M15 Shepherd reconnecting", func(a hostAgent) bool {
			return a.Management.Active == "agent" && a.Identity != nil && a.Identity.Connected && a.Identity.AgentVersion == m15Version
		})
	}
	// What M15 committed is what this version expects: no drift, same revision.
	fwDetect(t, h.ID)
	p := fwPlan(t, h.ID)
	expect(t, p.Applied.Enforced && p.Applied.Revision == first.Revision && p.Summary.Status != "drift" && p.Summary.Status != "pending_transaction",
		"after the upgrade: %+v (M15 committed revision %d)", p, first.Revision)
	t.Logf("after the upgrade: %s, revision %d", p.Summary.Status, p.Applied.Revision)

	if shepherd {
		// The M15 Shepherd applies with its own tool.
		commit("the M15 Shepherd, for the upgraded controller", "imap")
		expect(t, tool() == "1", "the M15 Shepherd's host tool is version %q", tool())
		// A signed rollout of this version's Shepherd; then the new tool.
		if st, body := uploadReleaseFor(t, key, filepath.Join(agentDir, "drool-agent-linux-amd64"), agentV1, "amd64"); st != 201 && st != 409 {
			t.Fatalf("upload %s: %d %s", agentV1, st, body)
		}
		var ro rolloutT
		ctl.must(t, "POST", "/api/v1/agents/rollouts", map[string]any{"version": agentV1, "host_ids": []string{h.ID}}, &ro)
		waitAgent(t, h.ID, 10*time.Minute, "the Shepherd updated to "+agentV1, func(a hostAgent) bool {
			return a.Identity != nil && a.Identity.Connected && a.Identity.AgentVersion == agentV1
		})
		commit("the updated Shepherd", "dns")
	} else {
		commit("over SSH after the upgrade", "imap")
	}
	expect(t, tool() == "2", "after the upgrade the host tool is version %q", tool())

	// A reboot: the boot loader brings back what was committed last.
	vmSSH(v, "systemctl reboot")
	time.Sleep(10 * time.Second)
	waitSSHBack(t, v)
	live := mustVMSSH(t, v, "nft list table inet drool")
	for _, name := range policies {
		expect(t, strings.Contains(live, "policy "+name), "after a reboot the live table lacks %s:\n%s", name, live)
	}
	if shepherd {
		waitAgent(t, h.ID, 5*time.Minute, "the Shepherd back after the reboot", func(a hostAgent) bool {
			return a.Identity != nil && a.Identity.Connected
		})
	} else {
		waitFor(t, 5*time.Minute, "DROOL reaching the host after the reboot", func() bool { return probe(t, h.ID).OK })
	}
	fwDetect(t, h.ID)
	p = fwPlan(t, h.ID)
	expect(t, p.Summary.Status == "in_sync", "after the reboot: %+v", p.Summary)
	t.Logf("%s: M15's policy recognized after the upgrade; revisions %d → %d committed %s; tool 1 → 2; the boot loader restored %d policies",
		def.Name, first.Revision, p.Applied.Revision, map[bool]string{false: "over SSH", true: "through the M15 Shepherd, then the updated one"}[shepherd], len(policies))
}
