//go:build integration

package integration

import (
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
			// hide the state of the rest.
			t.Run("ssh", func(t *testing.T) { testVM(t, v, h, jumpHost) })
			t.Run("ssh certificates", func(t *testing.T) { testVMSSHCertificates(t, def, v, h) })
			t.Run("shepherd", func(t *testing.T) { testVMAgent(t, def, h, key) })
			t.Run("firewall over ssh", func(t *testing.T) { testVMFirewallSSH(t, def, v, h) })
			t.Run("firewall through the shepherd", func(t *testing.T) { testVMFirewallAgent(t, def, v, h) })
		})
	}
}
