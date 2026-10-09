//go:build integration

package integration

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestVMDesired runs desired state (M14) against a real systemd guest:
// a service (running and enabled), a package to install, a package that
// must be absent and a managed file (content, owner, group, mode) drift on
// the host, are reviewed and restored by one ordinary job — first over SSH,
// then through the agent (whose read-only package query and file checks
// are used) — and an intentional change is accepted for the host. All
// changes happen inside the disposable VM.
//
//	go test -tags integration ./test/integration -run TestVMDesired -vm debian-12 -v -timeout 2h
func TestVMDesired(t *testing.T) {
	if *flagVMs == "" {
		t.Skip("set -vm to run the VM tests")
	}
	want := map[string]bool{}
	for _, n := range strings.Split(*flagVMs, ",") {
		want[strings.TrimSpace(n)] = true
	}
	key := newReleaseKey(t)
	ctl.must(t, "POST", "/api/v1/agents/release-keys", map[string]any{"public_key": key.line}, nil)
	for _, def := range vmDefs {
		if !want[def.Name] {
			continue
		}
		t.Run(def.Name, func(t *testing.T) {
			t.Parallel()
			v := startVM(t, def)
			h := addVMHost(t, "vmd-"+def.Name, v, hostOpts{})
			testVMDesired(t, def, v, h, key)
		})
	}
}

// vmPkg installs packages in the guest directly (the "someone changed the
// host" side), retrying while a boot-time apt/dnf run holds the lock.
func vmPkg(t *testing.T, def vmDef, v *vm, action string, names ...string) {
	t.Helper()
	n := strings.Join(names, " ")
	script := map[string]string{
		"apt": fmt.Sprintf("export DEBIAN_FRONTEND=noninteractive; for i in $(seq 30); do (apt-get update -qq && apt-get %s -y -qq %s) && exit 0; sleep 10; done; exit 1", action, n),
		"dnf": fmt.Sprintf("for i in $(seq 30); do dnf %s -y -q %s && exit 0; sleep 10; done; exit 1", action, n),
	}[def.Family]
	mustVMSSH(t, v, script)
}

// vmInstalled asks the guest's package database directly: installed or absent.
func vmInstalled(t *testing.T, def vmDef, v *vm, name string) string {
	t.Helper()
	q := map[string]string{
		"apt": "s=$(dpkg-query -W -f='${db:Status-Abbrev}' %s 2>/dev/null); [ \"${s%%%% *}\" = ii ] && echo installed || echo absent",
		"dnf": "rpm -q %s >/dev/null 2>&1 && echo installed || echo absent",
	}[def.Family]
	return mustVMSSH(t, v, fmt.Sprintf(q, name))
}

func testVMDesired(t *testing.T, def vmDef, v *vm, h host, key releaseKey) {
	const unit = "drool-ds-test.service"
	const file = "/etc/drool-ds/app.conf"
	// Debian's cloud image has no cron; Rocky's has cronie.
	wantPkg := map[string]string{"apt": "cron", "dnf": "cronie"}[def.Family]
	if def.Family == "dnf" {
		wantPkg = "tmux" // not in the GenericCloud image
	}
	const badPkg = "sl"
	if def.Family == "dnf" {
		vmPkg(t, def, v, "install", "epel-release") // sl lives in EPEL
	}
	mustVMSSH(t, v, `printf '[Unit]\nDescription=DROOL desired-state test unit\n[Service]\nExecStart=/bin/sleep infinity\n[Install]\nWantedBy=multi-user.target\n' > /etc/systemd/system/`+unit+` && systemctl daemon-reload && mkdir -p /etc/drool-ds && printf 'listen = 8080\n' > `+file+` && chmod 0644 `+file)
	vmPkg(t, def, v, "install", badPkg)
	content := "listen = 8080\nworkers = 4\n"
	sha := putArtifact(t, "app.conf", content)
	var b struct{ ID string }
	ctl.must(t, "POST", "/api/v1/desired/baselines", map[string]any{
		"name": "VM " + def.Name, "assignments": []map[string]any{{"scope": "host", "host_id": h.ID}},
		"resources": []map[string]any{
			{"kind": "service", "name": unit, "state": "running", "enabled": true},
			{"kind": "package", "name": wantPkg, "state": "installed"},
			{"kind": "package", "name": badPkg, "state": "absent"},
			{"kind": "file", "path": file, "artifact": sha, "owner": "root", "group": "root", "mode": "0640", "elevated": true},
		},
	}, &b)
	t.Cleanup(func() { ctl.do("DELETE", "/api/v1/desired/baselines/"+b.ID, nil, nil) })
	keys := []string{"service:" + unit, "package:" + wantPkg, "package:" + badPkg, "file:" + file}

	// expectDrift waits for every resource to show its drift.
	expectDrift := func(t *testing.T, via string) {
		t.Helper()
		svc := dsWait(t, h.ID, keys[0], "drift", 10*time.Minute)
		if !strings.HasPrefix(svc.Current, "stopped") || !strings.Contains(svc.Current, "disabled at boot") {
			t.Fatalf("%s service: %+v", via, svc)
		}
		if r := dsWait(t, h.ID, keys[1], "drift", 5*time.Minute); r.Current != "not installed" {
			t.Fatalf("%s %s: %+v", via, wantPkg, r)
		}
		if r := dsWait(t, h.ID, keys[2], "drift", 5*time.Minute); !strings.HasPrefix(r.Current, "installed (") {
			t.Fatalf("%s %s: %+v", via, badPkg, r)
		}
		f := dsWait(t, h.ID, keys[3], "drift", 5*time.Minute)
		fields := map[string]string{}
		for _, d := range f.Diffs {
			fields[d.Field] = d.Desired + " → " + d.Current
		}
		if !strings.HasPrefix(fields["content"], "sha256 "+sha+" → sha256 ") || fields["mode"] != "0640 → 0644" {
			t.Fatalf("%s file diffs: %+v", via, f.Diffs)
		}
	}
	// restoreAll restores everything with one job, then checks the guest.
	restoreAll := func(t *testing.T, via string) {
		t.Helper()
		p, job := dsRestore(t, map[string]any{"host_ids": []string{h.ID}})
		if len(p.Plan.Items) != 4 || len(p.Plan.Jobs[0].StepText) != 4 {
			t.Fatalf("%s plan: %+v", via, p.Plan)
		}
		if st := waitJob(t, job, 20*time.Minute); st != "completed" {
			t.Fatalf("job %s: %s", job, st)
		}
		var hs struct {
			Items []struct{ State, Transport string } `json:"items"`
		}
		ctl.must(t, "GET", "/api/v1/jobs/"+job+"/hosts", nil, &hs)
		if len(hs.Items) != 1 || hs.Items[0].State != "succeeded" || hs.Items[0].Transport != via {
			t.Fatalf("job host: %+v", hs.Items)
		}
		for _, k := range keys {
			waitFor(t, 5*time.Minute, k+" restored", func() bool {
				r := dsGet(t, h.ID)[k]
				if r.Status == "failed" {
					t.Fatalf("%s: %+v", k, r)
				}
				return r.Status == "compliant"
			})
		}
		out := mustVMSSH(t, v, fmt.Sprintf("systemctl is-active %s; systemctl is-enabled %s; stat -c '%%U:%%G %%a' %s; cat %s", unit, unit, file, file))
		t.Logf("guest after the %s restore:\n%s", via, out)
		if lines := strings.Split(out, "\n"); len(lines) < 3 || lines[0] != "active" || lines[1] != "enabled" || lines[2] != "root:root 640" ||
			!strings.Contains(out, "workers = 4") || strings.Contains(out, "debug") {
			t.Fatalf("the guest isn't in the desired state:\n%s", out)
		}
		if got := vmInstalled(t, def, v, wantPkg); got != "installed" {
			t.Fatalf("%s: %s", wantPkg, got)
		}
		if got := vmInstalled(t, def, v, badPkg); got != "absent" {
			t.Fatalf("%s: %s", badPkg, got)
		}
		var hist struct {
			Items []struct {
				Kind, Resolution string
				JobID            string `json:"job_id"`
			} `json:"items"`
		}
		ctl.must(t, "GET", "/api/v1/desired/history?host="+h.ID+"&key="+keys[0], nil, &hist)
		if len(hist.Items) == 0 || hist.Items[0].Kind != "restored" || hist.Items[0].Resolution != "restored" || hist.Items[0].JobID != job {
			t.Fatalf("history: %+v", hist.Items)
		}
	}

	t.Run("over SSH: drift on a real systemd host, restored by one job", func(t *testing.T) {
		expectDrift(t, "ssh")
		var rv struct {
			File struct {
				State, Diff string
			} `json:"file"`
		}
		ctl.must(t, "GET", "/api/v1/desired/hosts/"+h.ID+"/review?key="+keys[3], nil, &rv)
		if rv.File.State != "replace" || !strings.Contains(rv.File.Diff, "+workers = 4") {
			t.Fatalf("review: %+v", rv.File)
		}
		restoreAll(t, "ssh")
	})

	t.Run("through the agent: the same drift checked and restored by the agent", func(t *testing.T) {
		// The correction just changed packages and services, so the poller may
		// be re-reading the host's inventory (a shared lock): the install
		// says "host busy, try again" until it's done.
		var res struct {
			OK     bool   `json:"ok"`
			Output string `json:"output"`
			Error  string `json:"error"`
		}
		waitFor(t, 5*time.Minute, "agent install", func() bool {
			st, err := ctl.do("POST", "/api/v1/hosts/"+h.ID+"/agent/install", map[string]any{"preset": "full", "release_key": key.line}, &res)
			if err != nil || st != 409 && st != 200 {
				t.Fatalf("install: %d %v", st, err)
			}
			return st == 200
		})
		if !res.OK {
			t.Fatalf("install: %s\n%s", res.Error, res.Output)
		}
		waitAgent(t, h.ID, 3*time.Minute, "agent", func(a hostAgent) bool { return a.Management.Active == "agent" })
		mustVMSSH(t, v, "systemctl disable --now "+unit+"; printf 'debug = 1\n' >> "+file+"; chmod 0644 "+file)
		vmPkg(t, def, v, "remove", wantPkg)
		vmPkg(t, def, v, "install", badPkg)
		expectDrift(t, "agent")
		restoreAll(t, "agent")
	})

	t.Run("an intentional change accepted for this host", func(t *testing.T) {
		mustVMSSH(t, v, "systemctl stop "+unit)
		dsWait(t, h.ID, keys[0], "drift", 5*time.Minute)
		ctl.must(t, "POST", "/api/v1/desired/hosts/"+h.ID+"/accept", map[string]any{"keys": []string{keys[0]}, "note": "stopped on purpose"}, nil)
		r := dsGet(t, h.ID)[keys[0]]
		if r.Status != "compliant" || r.Source.Level != "host" || r.Source.Label != "Custom for this host" {
			t.Fatalf("after accept: %+v", r)
		}
		// Nothing restarts it: the host's own declaration says stopped (and
		// still enabled at boot, as observed).
		time.Sleep(30 * time.Second)
		if out := mustVMSSH(t, v, "systemctl is-active "+unit+"; systemctl is-enabled "+unit+"; true"); out != "inactive\nenabled" {
			t.Fatalf("guest: %q", out)
		}
		var bv struct{ Revision int }
		ctl.must(t, "GET", "/api/v1/desired/baselines/"+b.ID, nil, &bv)
		if bv.Revision != 1 {
			t.Fatal("accepting changed the baseline")
		}
	})
}
