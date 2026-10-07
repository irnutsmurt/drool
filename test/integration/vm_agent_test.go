//go:build integration

package integration

import (
	"strings"
	"testing"
	"time"
)

// TestVMAgent runs the real agent under systemd on the VM families: the
// unit, its ExecStartPre update guard, services, reboots, a real update and
// an automatic rollback through systemd, and revocation.
func TestVMAgent(t *testing.T) {
	if *flagVMs == "" {
		t.Skip("set -vm to run the VM tests")
	}
	want := map[string]bool{}
	for _, n := range strings.Split(*flagVMs, ",") {
		want[strings.TrimSpace(n)] = true
	}
	key := newReleaseKey(t)
	ctl.must(t, "POST", "/api/v1/agents/release-keys", map[string]any{"public_key": key.line}, nil)
	for _, v := range []string{agentV2, agentBroken} {
		if st, body := uploadRelease(t, key, releaseDir+"/drool-agent-"+v, v); st != 201 && st != 409 {
			t.Fatalf("upload %s: %d %s", v, st, body)
		}
	}
	for _, def := range vmDefs {
		if !want[def.Name] {
			continue
		}
		t.Run(def.Name, func(t *testing.T) {
			t.Parallel()
			v := startVM(t, def)
			h := addVMHost(t, "vma-"+def.Name, v, hostOpts{})
			testVMAgent(t, def, h, key)
		})
	}
}

func testVMAgent(t *testing.T, def vmDef, h host, key releaseKey) {
	sh := func(script string) (jobHostT, string) {
		jh, out := runT(t, h.ID, cmd(script, true))
		return jh, strings.TrimSpace(out)
	}

	t.Run("install over SSH as a systemd service", func(t *testing.T) {
		var res struct {
			OK     bool   `json:"ok"`
			Output string `json:"output"`
			Error  string `json:"error"`
		}
		ctl.must(t, "POST", "/api/v1/hosts/"+h.ID+"/agent/install", map[string]any{"preset": "full", "release_key": key.line}, &res)
		if !res.OK {
			_, diag := sh("systemctl status drool-agent --no-pager; journalctl -u drool-agent --no-pager -n 60; ls -laZ /usr/libexec/drool-agent /usr/libexec/drool-agent/versions/*; getenforce; ausearch -m avc -ts recent 2>/dev/null | tail -20")
			t.Fatalf("install: %s\n%s\n--- diagnostics ---\n%s", res.Error, res.Output, diag)
		}
		waitAgent(t, h.ID, 2*time.Minute, "agent connected", func(a hostAgent) bool { return a.Management.Active == "agent" })
		jh, out := sh("systemctl is-active drool-agent; systemctl is-enabled drool-agent; systemctl show -p ExecStartPre drool-agent | grep -c update-guard; stat -c '%U %a' /etc/drool/agent.yaml")
		if jh.Transport != "agent" || out != "active\nenabled\n1\nroot 600" {
			t.Fatalf("unit state via %s: %q", jh.Transport, out)
		}
	})

	t.Run("services and packages through the agent", func(t *testing.T) {
		// Debian's cloud image has no cron; installing it goes through the agent too.
		cronPkg := map[string]string{"apt": "cron", "dnf": "cronie"}[def.Family]
		jh, out := runT(t, h.ID, map[string]any{"type": "package", "action": "install", "names": []string{cronPkg}})
		if jh.State != "succeeded" || jh.Transport != "agent" {
			t.Fatalf("install %s via agent: %+v\n%s", cronPkg, jh, out)
		}
		jh, _ = runT(t, h.ID, svc(def.CronUnit, "stopped", nil))
		if jh.State != "succeeded" || jh.Transport != "agent" {
			t.Fatalf("stop %s via agent: %+v", def.CronUnit, jh)
		}
		jh, _ = runT(t, h.ID, svc(def.CronUnit, "running", nil))
		if jh.State != "succeeded" || jh.Transport != "agent" {
			t.Fatalf("start %s via agent: %+v", def.CronUnit, jh)
		}
	})

	pid := func() string { _, out := sh("systemctl show -p MainPID --value drool-agent"); return out }

	t.Run("update under systemd", func(t *testing.T) {
		before := pid()
		var ro rolloutT
		ctl.must(t, "POST", "/api/v1/agents/rollouts", map[string]any{"version": agentV2, "host_ids": []string{h.ID}}, &ro)
		waitFor(t, 5*time.Minute, "healthy", func() bool {
			r := getRollout(t, ro.ID)
			return len(r.Hosts) == 1 && (r.Hosts[0].State == "healthy" || r.Hosts[0].State == "failed" || r.Hosts[0].State == "rolled_back")
		})
		if r := getRollout(t, ro.ID); r.Hosts[0].State != "healthy" {
			t.Fatalf("update: %+v", r.Hosts[0])
		}
		waitAgent(t, h.ID, 2*time.Minute, "connected on "+agentV2, func(a hostAgent) bool {
			return a.Management.Active == "agent" && a.Identity.AgentVersion == agentV2
		})
		_, out := sh("readlink /usr/libexec/drool-agent/current; systemctl is-active drool-agent")
		if out != "versions/"+agentV2+"\nactive" || pid() == before {
			t.Errorf("after update: %q (pid %s → %s)", out, before, pid())
		}
	})

	t.Run("broken update rolls back under systemd", func(t *testing.T) {
		var ro rolloutT
		ctl.must(t, "POST", "/api/v1/agents/rollouts", map[string]any{"version": agentBroken, "host_ids": []string{h.ID}}, &ro)
		waitFor(t, 8*time.Minute, "rollback", func() bool {
			r := getRollout(t, ro.ID)
			return len(r.Hosts) == 1 && (r.Hosts[0].State == "rolled_back" || r.Hosts[0].State == "failed" || r.Hosts[0].State == "healthy")
		})
		r := getRollout(t, ro.ID)
		if r.Hosts[0].State != "rolled_back" || r.Status != "paused" {
			t.Fatalf("broken update: %+v %+v", r, r.Hosts[0])
		}
		t.Logf("rollback detail: %s", r.Hosts[0].Detail)
		waitAgent(t, h.ID, 3*time.Minute, "back on "+agentV2, func(a hostAgent) bool {
			return a.Management.Active == "agent" && a.Identity.AgentVersion == agentV2
		})
		_, out := sh("readlink /usr/libexec/drool-agent/current; systemctl is-active drool-agent")
		if out != "versions/"+agentV2+"\nactive" {
			t.Errorf("after rollback: %q", out)
		}
		ctl.must(t, "POST", "/api/v1/agents/rollouts/"+ro.ID+"/cancel", nil, nil)
	})

	t.Run("reboot through the agent", func(t *testing.T) {
		id := submit(t, []string{h.ID}, map[string]any{"type": "reboot"})
		waitJob(t, id, 15*time.Minute)
		jh := getJobHost(t, id, h.ID)
		var jt jobHostT
		ctl.must(t, "GET", "/api/v1/jobs/"+id+"/hosts/"+h.ID, nil, &jt)
		reb, _ := result(jh)["reboot"].(map[string]any)
		if jh.State != "succeeded" || jt.Transport != "agent" || reb == nil || reb["boot_id_after"] == reb["boot_id_before"] {
			t.Fatalf("reboot via agent: %v %+v %v", jh, jt, result(jh))
		}
		waitAgent(t, h.ID, 3*time.Minute, "agent back after reboot", func(a hostAgent) bool { return a.Management.Active == "agent" })
	})

	t.Run("revoked identity blocks management", func(t *testing.T) {
		a := getHostAgent(t, h.ID)
		ctl.must(t, "POST", "/api/v1/agents/"+a.Identity.ID+"/revoke", map[string]any{"reason": "integration test"}, nil)
		jh, _ := runT(t, h.ID, cmd("true", false))
		if jh.State != "failed" || jh.Transport == "ssh" {
			t.Fatalf("command after revocation: %+v", jh)
		}
		if a := getHostAgent(t, h.ID); !strings.Contains(a.Management.AgentState, "revoked") {
			t.Errorf("management after revocation: %+v", a.Management)
		}
	})
}
