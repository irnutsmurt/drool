//go:build integration

package integration

import (
	"strings"
	"testing"
)

// TestVMPlatform qualifies managed-host platforms (M17, README "Supported
// platforms"): one VM per distribution of the support matrix, with real
// systemd, sshd and nftables, runs the suites that need them —
//
//   - SSH: the connection test, services, file deployment with service
//     actions, packages and a cancelled live transaction, reboots
//     (directly and through a jump host);
//   - SSH certificates with the distribution's own sshd, certificate-only
//     work and a CA rotation;
//   - the Shepherd under systemd: installed over SSH, services and packages
//     through it, a signed update, a broken update rolled back, a reboot,
//     revocation;
//   - firewall transactions over SSH (commit over a fresh connection,
//     enforcement, the boot loader, rollback by the host's timer, drift,
//     refusal next to other rules) and through the Shepherd.
//
// arm64 runs as an emulated aarch64 guest (debian-12-arm64). The test has a
// controller of its own. Slow (software emulation, about 45 minutes per
// distribution; two at a time on a 16 GB machine):
//
//	go test -tags integration ./test/integration -run TestVMPlatform -vm debian-13,ubuntu-22.04 -v -timeout 8h
func TestVMPlatform(t *testing.T) {
	if *flagVMs == "" {
		t.Skip("set -vm to run the VM tests")
	}
	freshController(t)
	key := newReleaseKey(t)
	ctl.must(t, "POST", "/api/v1/agents/release-keys", map[string]any{"public_key": key.line}, nil)
	manualShepherdUpdates(t)
	for _, v := range []string{agentV2, agentBroken} {
		for arch, sfx := range map[string]string{"amd64": "", "arm64": "-arm64"} {
			if st, body := uploadReleaseFor(t, key, releaseDir+"/drool-agent-"+v+sfx, v, arch); st != 201 && st != 409 {
				t.Fatalf("upload %s %s: %d %s", v, arch, st, body)
			}
		}
	}
	jump := startHost(t, image(t, allDistros[0]), "plat-jump", true, netOuter)
	jumpHost := addHost(t, "plat-jump", jump, hostOpts{JumpOnly: true})
	want := wantedVMs()
	for _, def := range vmDefs {
		if !want[def.Name] {
			continue
		}
		t.Run(def.Name, func(t *testing.T) {
			t.Parallel()
			v := startVM(t, def)
			h := addVMHost(t, "plat-"+def.Name, v, hostOpts{})
			// Every suite runs whatever the others did: one failure must not
			// hide the state of the rest. Between them the host is left the
			// way an operator would leave it.
			t.Run("ssh", func(t *testing.T) { testVM(t, v, h, jumpHost) })
			packageDatabaseRepaired(t, def, v)
			t.Run("ssh certificates", func(t *testing.T) { testVMSSHCertificates(t, def, v, h) })
			t.Run("shepherd", func(t *testing.T) { testVMAgent(t, def, h, key) })
			// The Shepherd suite ends by revoking the host's identity; DROOL
			// then says to re-enroll or switch the host to SSH. The firewall
			// suites start over SSH (and enroll a new Shepherd later).
			ctl.must(t, "PUT", "/api/v1/hosts/"+h.ID+"/management", map[string]string{"preferred": "ssh", "fallback": "none"}, nil)
			t.Run("firewall over ssh", func(t *testing.T) { testVMFirewallSSH(t, def, v, h) })
			t.Run("firewall through the shepherd", func(t *testing.T) { testVMFirewallAgent(t, def, v, h) })
		})
	}
}

// packageDatabaseRepaired: the SSH suite cancels a live package transaction
// on purpose, and DROOL explains what that leaves without repairing it
// (README: the next operation says what to run). The later suites need a
// working package manager, so the test does what an operator is told to,
// and logs what it found.
func packageDatabaseRepaired(t *testing.T, def vmDef, v *vm) {
	t.Helper()
	if def.Family != "apt" {
		return
	}
	state := mustVMSSH(t, v, "ls -A /var/lib/dpkg/updates; dpkg --audit")
	if strings.TrimSpace(state) == "" {
		return
	}
	procs, _ := vmSSH(v, "pgrep -a 'apt|dpkg|unattended' || true")
	t.Logf("%s: the package database needs dpkg --configure -a:\n%s\nrunning: %s", def.Name, state, procs)
	mustVMSSH(t, v, "for i in $(seq 60); do dpkg --configure -a && DEBIAN_FRONTEND=noninteractive apt-get -y -f install >/dev/null && exit 0; sleep 5; done; exit 1")
}
