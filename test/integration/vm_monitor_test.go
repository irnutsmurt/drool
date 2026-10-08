//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestVMMonitoring runs system monitoring against a real systemd guest
// with emulated storage (NVMe, SATA, USB, an SD card when the guest
// kernel has the driver): SSH polling, virtual disks honestly reported
// (no helper recommended), the agent under its systemd budget,
// a deliberately runaway agent build, degradation under host pressure,
// service transitions, the flash probe and real IO errors on a disk the
// test takes away. Everything destructive happens inside the VM.
//
//	go test -tags integration ./test/integration -run TestVMMonitoring -vm debian-12 -v -timeout 3h
func TestVMMonitoring(t *testing.T) {
	if *flagVMs == "" {
		t.Skip("set -vm to run the VM tests")
	}
	want := map[string]bool{}
	for _, n := range strings.Split(*flagVMs, ",") {
		want[strings.TrimSpace(n)] = true
	}
	key := newReleaseKey(t)
	ctl.must(t, "POST", "/api/v1/agents/release-keys", map[string]any{"public_key": key.line}, nil)
	if st, body := uploadRelease(t, key, releaseDir+"/drool-agent-"+agentRunaway, agentRunaway); st != 201 && st != 409 {
		t.Fatalf("upload runaway build: %d %s", st, body)
	}
	if st, body := uploadRelease(t, key, releaseDir+"/drool-agent-"+agentV2, agentV2); st != 201 && st != 409 {
		t.Fatalf("upload %s: %d %s", agentV2, st, body)
	}
	// Faster SSH polls for the test (the default is five minutes).
	var set map[string]any
	ctl.must(t, "GET", "/api/v1/monitoring/settings", nil, &struct {
		Settings *map[string]any `json:"settings"`
	}{&set})
	set["ssh_poll_s"] = 60
	set["ssh_smart_s"] = 600
	ctl.must(t, "PUT", "/api/v1/monitoring/settings", set, nil)
	for _, def := range vmDefs {
		if !want[def.Name] {
			continue
		}
		t.Run(def.Name, func(t *testing.T) {
			t.Parallel()
			// Every emulated disk carries a QEMU model, so DROOL rightly
			// treats them all as virtual (the hypervisor owns their health)
			// and recommends no helper; physical drives are covered by
			// fixtures captured from real tools. Debian's cloud kernel has
			// no AHCI, USB storage or SD drivers; those parts skip.
			disks := "VM_DISKS=nvme.img:1G sata.img:512M usb.img:512M sd.img:256M"
			extra := "VM_EXTRA=-drive file=/work/nvme.img,if=none,id=nvm,format=raw -device nvme,serial=DROOLNVME01,drive=nvm " +
				"-device ahci,id=ahci -drive file=/work/sata.img,if=none,id=sata,format=raw -device ide-hd,drive=sata,bus=ahci.0,serial=DROOLSATA01 " +
				"-device qemu-xhci,id=xhci -drive file=/work/usb.img,if=none,id=usbd,format=raw -device usb-storage,drive=usbd,bus=xhci.0,serial=DROOLUSB01 " +
				"-device sdhci-pci -drive file=/work/sd.img,if=none,id=sdc,format=raw -device sd-card,drive=sdc"
			v := startVM(t, def, disks, extra)
			h := addVMHost(t, "vmm-"+def.Name, v, hostOpts{})
			testVMMonitoring(t, def, v, h, key)
		})
	}
}

type monOverviewT struct {
	HasData bool   `json:"has_data"`
	Source  string `json:"source"`
	State   *struct {
		Fidelity struct {
			Mode    string   `json:"mode"`
			SampleS int      `json:"sample_s"`
			Reduced bool     `json:"reduced"`
			Reasons []string `json:"reasons"`
		} `json:"fidelity"`
		CPU *struct {
			Percent *float64 `json:"percent"`
		} `json:"cpu"`
		Filesystems []struct {
			Mount    string `json:"mount"`
			ReadOnly bool   `json:"read_only"`
		} `json:"filesystems"`
		Helpers []struct {
			Package   string `json:"package"`
			Installed bool   `json:"installed"`
			Useful    bool   `json:"useful"`
		} `json:"helpers"`
		Agent *struct {
			Enforced    bool     `json:"enforced"`
			CPUQuotaPct *float64 `json:"cpu_quota_percent"`
			MemoryMax   *int64   `json:"memory_max_bytes"`
			MemoryBytes *int64   `json:"memory_bytes"`
		} `json:"agent"`
	} `json:"state"`
	Issues []struct {
		Check, Subject, Severity, Title string
	} `json:"issues"`
	Probes map[string]struct {
		OK    bool   `json:"ok"`
		Stage string `json:"stage"`
		Error string `json:"error"`
	} `json:"probes"`
}

type monDeviceT struct {
	Key, Name, Kind, Transport, Capability, Helper string
	Health                                         struct{ State, Summary string }
	NVMe                                           map[string]any `json:"nvme"`
	SMART                                          map[string]any `json:"smart"`
}

func monOverview(t *testing.T, id string) monOverviewT {
	var o monOverviewT
	ctl.must(t, "GET", "/api/v1/hosts/"+id+"/monitoring", nil, &o)
	return o
}

func monDevices(t *testing.T, id string) []monDeviceT {
	var s struct{ Devices []monDeviceT }
	ctl.must(t, "GET", "/api/v1/hosts/"+id+"/monitoring/storage", nil, &s)
	return s.Devices
}

func hasIssue(o monOverviewT, check, subject string) bool {
	for _, i := range o.Issues {
		if i.Check == check && (subject == "" || strings.Contains(i.Subject, subject)) {
			return true
		}
	}
	return false
}

func testVMMonitoring(t *testing.T, def vmDef, v *vm, h host, key releaseKey) {
	var devs []monDeviceT
	byKind := func(kind string) *monDeviceT {
		for i := range devs {
			if devs[i].Kind == kind || strings.HasPrefix(devs[i].Kind, kind) {
				return &devs[i]
			}
		}
		return nil
	}
	t.Run("SSH polling: real systemd, devices, no tools", func(t *testing.T) {
		ctl.must(t, "POST", "/api/v1/hosts/"+h.ID+"/monitoring/refresh", nil, nil)
		waitFor(t, 10*time.Minute, "an SSH poll", func() bool { return monOverview(t, h.ID).HasData })
		o := monOverview(t, h.ID)
		if o.Source != "ssh" || o.State.Fidelity.Mode != "polled" {
			t.Fatalf("SSH hosts are polled: %+v", o.State.Fidelity)
		}
		var sv struct {
			Supported bool
			Units     []struct{ Name, State string }
		}
		ctl.must(t, "GET", "/api/v1/hosts/"+h.ID+"/monitoring/services", nil, &sv)
		found := false
		for _, u := range sv.Units {
			found = found || u.Name == def.SSHUnit+".service" && u.State == "running"
		}
		if !sv.Supported || !found {
			t.Fatalf("services over SSH: %+v", sv)
		}
		inv, _ := vmSSH(v, "lsblk -d -o NAME,TRAN,MODEL,SERIAL; ls /sys/block")
		t.Logf("guest block devices:\n%s", inv)
		var nvme *monDeviceT
		waitFor(t, 10*time.Minute, "devices", func() bool {
			devs = monDevices(t, h.ID)
			for k := range devs {
				if devs[k].Name == "nvme0n1" {
					nvme = &devs[k]
				}
			}
			return nvme != nil
		})
		t.Logf("DROOL: %+v", devs)
		if nvme.Kind != "virtual" || nvme.Transport != "nvme" || nvme.Capability != "unavailable" || nvme.Helper != "" || nvme.Health.State != "unknown" {
			t.Errorf("QEMU's NVMe is a virtual disk: health unknown, no helper suggested: %+v", nvme)
		}
		if d := byKind("virtual"); d == nil || d.Capability != "unavailable" {
			t.Errorf("virtio disk: %+v", d)
		}
		for _, d := range devs {
			if d.Health.State == "ok" {
				t.Errorf("a virtual disk is never called healthy: %+v", d)
			}
		}
	})

	t.Run("no helpers recommended for virtual hardware", func(t *testing.T) {
		var pv struct {
			Counts   map[string]int
			Installs int
		}
		ctl.must(t, "POST", "/api/v1/monitoring/helpers/preview", map[string]any{"host_ids": []string{h.ID}}, &pv)
		if pv.Installs != 0 || len(pv.Counts) != 0 {
			t.Fatalf("a VM's disks and sensors belong to the hypervisor; nothing to install: %+v", pv)
		}
		o := monOverview(t, h.ID)
		for _, hp := range o.State.Helpers {
			if hp.Useful {
				t.Errorf("helper marked useful in a VM: %+v", hp)
			}
		}
	})

	t.Run("install the agent; its budget is enforced", func(t *testing.T) {
		var res struct {
			OK            bool
			Output, Error string
		}
		ctl.must(t, "POST", "/api/v1/hosts/"+h.ID+"/agent/install", map[string]any{"preset": "full", "release_key": key.line}, &res)
		if !res.OK {
			t.Fatalf("install: %s\n%s", res.Error, res.Output)
		}
		waitAgent(t, h.ID, 3*time.Minute, "agent", func(a hostAgent) bool { return a.Management.Active == "agent" })
		waitFor(t, 5*time.Minute, "continuous monitoring", func() bool {
			o := monOverview(t, h.ID)
			return o.Source == "agent" && o.State.Agent != nil && o.State.Agent.Enforced
		})
		out := mustVMSSH(t, v, "systemctl show drool-agent -p CPUQuotaPerSecUSec -p MemoryHigh -p MemoryMax -p CPUWeight")
		t.Logf("agent unit:\n%s", out)
		if !strings.Contains(out, "CPUQuotaPerSecUSec=250ms") || !strings.Contains(out, "MemoryMax=") || strings.Contains(out, "MemoryMax=infinity") {
			t.Fatalf("budget not applied: %s", out)
		}
		// Operations escape the budget: a job runs in its own scope.
		jh, cg := runT(t, h.ID, cmd("cat /proc/self/cgroup", true))
		if jh.Transport != "agent" || strings.Contains(cg, "drool-agent.service") || !strings.Contains(cg, ".scope") {
			t.Fatalf("an operation ran inside the agent's budget (%s): %s", jh.Transport, cg)
		}
	})

	t.Run("benchmark: the agent's real cost", func(t *testing.T) {
		read := func() (usec int64, mem, peak int64) {
			out := mustVMSSH(t, v, "d=/sys/fs/cgroup/system.slice/drool-agent.service; awk '/usage_usec/{print $2}' $d/cpu.stat; cat $d/memory.current; cat $d/memory.peak 2>/dev/null || echo 0")
			f := strings.Fields(out)
			if len(f) < 3 {
				t.Fatalf("cgroup: %q", out)
			}
			usec, _ = strconv.ParseInt(f[0], 10, 64)
			mem, _ = strconv.ParseInt(f[1], 10, 64)
			peak, _ = strconv.ParseInt(f[2], 10, 64)
			return
		}
		u0, _, _ := read()
		start := time.Now()
		ctl.must(t, "POST", "/api/v1/hosts/"+h.ID+"/monitoring/refresh", nil, nil) // a full storage scan inside the window
		time.Sleep(5 * time.Minute)
		u1, mem, peak := read()
		pct := float64(u1-u0) / time.Since(start).Seconds() / 1e4
		t.Logf("BENCHMARK %s (TCG, emulated CPU): agent CPU %.2f%% of one CPU over 5 min incl. a full storage scan; memory now %d MB, peak %d MB",
			def.Name, pct, mem>>20, peak>>20)
		if pct > 20 {
			t.Errorf("the agent used %.1f%% CPU on average", pct)
		}
	})

	t.Run("service transitions through the agent", func(t *testing.T) {
		mustVMSSH(t, v, `printf '[Unit]\nDescription=DROOL test unit\n[Service]\nExecStart=/bin/sleep infinity\n[Install]\nWantedBy=multi-user.target\n' > /etc/systemd/system/drool-mon-test.service && systemctl daemon-reload && systemctl enable --now drool-mon-test.service`)
		ctl.must(t, "PUT", "/api/v1/hosts/"+h.ID+"/monitoring/settings", map[string]any{"watched_services": []string{"drool-mon-test.service"}, "probe_mounts": []string{}, "disabled_checks": []string{}}, nil)
		var hist struct{ Events []struct{ From, To string } }
		path := "/api/v1/hosts/" + h.ID + "/monitoring/services/drool-mon-test.service/history"
		waitFor(t, 3*time.Minute, "unit seen running", func() bool {
			var sv struct {
				Units []struct{ Name, State string }
			}
			ctl.must(t, "GET", "/api/v1/hosts/"+h.ID+"/monitoring/services", nil, &sv)
			for _, u := range sv.Units {
				if u.Name == "drool-mon-test.service" && u.State == "running" {
					return true
				}
			}
			return false
		})
		mustVMSSH(t, v, "systemctl kill -s KILL drool-mon-test.service")
		waitFor(t, 3*time.Minute, "Running → Failed", func() bool {
			ctl.must(t, "GET", path, nil, &hist)
			return len(hist.Events) > 0 && strings.HasPrefix(hist.Events[0].To, "failed")
		})
		waitFor(t, 4*time.Minute, "watched service failed issue", func() bool { return hasIssue(monOverview(t, h.ID), "service_failed", "drool-mon-test") })
		mustVMSSH(t, v, "systemctl reset-failed drool-mon-test.service; systemctl start drool-mon-test.service")
		waitFor(t, 3*time.Minute, "Failed → Running and resolved", func() bool {
			ctl.must(t, "GET", path, nil, &hist)
			return len(hist.Events) > 1 && strings.HasPrefix(hist.Events[0].To, "active") && !hasIssue(monOverview(t, h.ID), "service_failed", "drool-mon-test")
		})
	})

	var sataDev string
	t.Run("flash probe and real IO errors", func(t *testing.T) {
		defer func() {
			if t.Failed() {
				var d json.RawMessage
				ctl.do("POST", "/api/v1/hosts/"+h.ID+"/monitoring/diagnostics", nil, nil)
				time.Sleep(5 * time.Second)
				ctl.do("GET", "/api/v1/hosts/"+h.ID+"/monitoring/diagnostics", nil, &d)
				o := monOverview(t, h.ID)
				out, _ := vmSSH(v, "findmnt -rno TARGET,SOURCE,FSTYPE,OPTIONS /; ls -la /var/lib/drool-agent /var/lib/drool-agent/probe; journalctl -u drool-agent --no-pager -n 40")
				t.Logf("probes %+v\nfilesystems %+v\nagent diagnostics %s\nguest:\n%s", o.Probes, o.State, d, out)
			}
		}()
		ctl.must(t, "PUT", "/api/v1/hosts/"+h.ID+"/monitoring/settings", map[string]any{"watched_services": []string{}, "probe_mounts": []string{"/"}, "probe_s": 60, "disabled_checks": []string{}}, nil)
		waitFor(t, 6*time.Minute, "a passing probe of /", func() bool { p, ok := monOverview(t, h.ID).Probes["/"]; return ok && p.OK })
		out := mustVMSSH(t, v, "ls -l /var/lib/drool-agent/probe/ && stat -c %s /var/lib/drool-agent/probe/probe")
		if !strings.HasSuffix(out, "512") {
			t.Fatalf("the probe writes one 512-byte file in the agent's own directory: %s", out)
		}
		// A disk the test breaks: ext4 on the SATA disk, probed, then the
		// device goes offline (only this VM's emulated disk).
		// The SATA disk when the guest kernel has AHCI, otherwise the NVMe
		// drive (its health checks are done by now).
		sataDev, _ = vmSSH(v, "for d in /sys/block/sd*; do [ \"$(cat $d/device/model 2>/dev/null | tr -d ' ')\" = QEMUHARDDISK ] && basename $d && break; done")
		breakDisk := fmt.Sprintf("echo offline > /sys/block/%s/device/state", sataDev)
		if sataDev == "" {
			sataDev = "nvme0n1"
			breakDisk = "echo 1 > $(readlink -f /sys/class/nvme/nvme0/device)/remove" // the PCI device goes away under the mounted filesystem
		}
		mustVMSSH(t, v, fmt.Sprintf("mkfs.ext4 -q -F /dev/%s && mkdir -p /mnt/probe && mount /dev/%s /mnt/probe", sataDev, sataDev))
		ctl.must(t, "PUT", "/api/v1/hosts/"+h.ID+"/monitoring/settings", map[string]any{"watched_services": []string{}, "probe_mounts": []string{"/", "/mnt/probe"}, "probe_s": 60, "disabled_checks": []string{}}, nil)
		waitFor(t, 6*time.Minute, "probe of /mnt/probe passes", func() bool { p, ok := monOverview(t, h.ID).Probes["/mnt/probe"]; return ok && p.OK })
		mustVMSSH(t, v, breakDisk)
		waitFor(t, 8*time.Minute, "probe failure and kernel IO errors", func() bool {
			o := monOverview(t, h.ID)
			p := o.Probes["/mnt/probe"]
			return !p.OK && hasIssue(o, "probe", "/mnt/probe") && hasIssue(o, "kernel_storage", "")
		})
		o := monOverview(t, h.ID)
		t.Logf("probe after the disk went offline: %+v; issues %+v", o.Probes["/mnt/probe"], o.Issues)
	})

	t.Run("pressure on the host reduces fidelity", func(t *testing.T) {
		mustVMSSH(t, v, "for i in 1 2 3 4 5 6; do nohup sh -c 'end=$(($(date +%s)+240)); while [ $(date +%s) -lt $end ]; do :; done' >/dev/null 2>&1 & done")
		waitFor(t, 6*time.Minute, "reduced fidelity", func() bool {
			o := monOverview(t, h.ID)
			return o.State.Fidelity.Reduced && hasIssue(o, "fidelity", "")
		})
		o := monOverview(t, h.ID)
		t.Logf("reduced: %v", o.State.Fidelity.Reasons)
	})

	t.Run("a runaway collector stays inside the budget", func(t *testing.T) {
		var ro rolloutT
		ctl.must(t, "POST", "/api/v1/agents/rollouts", map[string]any{"version": agentRunaway, "host_ids": []string{h.ID}}, &ro)
		waitFor(t, 10*time.Minute, "runaway build installed", func() bool {
			r := getRollout(t, ro.ID)
			return len(r.Hosts) == 1 && r.Hosts[0].State == "healthy"
		})
		cg := "/sys/fs/cgroup/system.slice/drool-agent.service"
		usage := func() int64 {
			n, _ := strconv.ParseInt(mustVMSSH(t, v, "awk '/usage_usec/{print $2}' "+cg+"/cpu.stat"), 10, 64)
			return n
		}
		mustVMSSH(t, v, "echo cpu > /etc/drool/runaway-test")
		time.Sleep(20 * time.Second)
		u0, t0 := usage(), time.Now()
		time.Sleep(60 * time.Second)
		pct := float64(usage()-u0) / time.Since(t0).Seconds() / 1e4
		t.Logf("runaway CPU: %.1f%% of one CPU (quota 25%%)", pct)
		if pct > 25*1.2 {
			t.Errorf("a spinning collector used %.1f%% CPU, beyond the 25%% quota", pct)
		}
		if out, err := vmSSH(v, "true"); err != nil {
			t.Fatalf("the host stopped answering while the agent spun: %v %s", err, out)
		}
		restarts := func() int {
			n, _ := strconv.Atoi(mustVMSSH(t, v, "systemctl show drool-agent -p NRestarts --value"))
			return n
		}
		r0 := restarts()
		mustVMSSH(t, v, "echo mem > /etc/drool/runaway-test")
		// memory.high throttles a growing agent rather than killing it; the
		// agent's own guard restarts it (or, failing that, MemoryMax's OOM
		// kill does). Either way: bounded, then a fresh agent.
		how := ""
		waitFor(t, 8*time.Minute, "the runaway agent restarted (memory guard or OOM kill)", func() bool {
			out, _ := vmSSH(v, "awk '/oom_kill /{print $2}' "+cg+"/memory.events")
			if n, _ := strconv.Atoi(strings.TrimSpace(out)); n > 0 {
				how = "OOM kill at MemoryMax"
				return true
			}
			if restarts() > r0 {
				how = "the agent's memory guard"
				return true
			}
			return false
		})
		if j, _ := vmSSH(v, "journalctl -u drool-agent --no-pager -g 'memory keeps growing' -o cat | tail -1"); j != "" {
			t.Logf("journal: %s", j)
		}
		t.Logf("runaway memory contained by %s", how)
		peak := mustVMSSH(t, v, "cat "+cg+"/memory.peak 2>/dev/null || cat "+cg+"/memory.max; cat "+cg+"/memory.max; systemctl show drool-agent -p NRestarts --value")
		t.Logf("runaway memory: peak / max / restarts: %s", strings.ReplaceAll(peak, "\n", " / "))
		f := strings.Fields(peak)
		if len(f) >= 2 {
			p, _ := strconv.ParseInt(f[0], 10, 64)
			m, _ := strconv.ParseInt(f[1], 10, 64)
			if m > 0 && p > m {
				t.Errorf("memory peak %d beyond the ceiling %d", p, m)
			}
		}
		mustVMSSH(t, v, "rm -f /etc/drool/runaway-test")
		waitAgent(t, h.ID, 5*time.Minute, "agent back after the restart", func(a hostAgent) bool { return a.Management.Active == "agent" })
		if out, err := vmSSH(v, "systemctl is-active "+def.SSHUnit); err != nil || out != "active" {
			t.Fatalf("the host's own services suffered: %v %s", err, out)
		}
		// The restarted agent (with the trigger file gone, the runaway build
		// behaves normally; agents refuse downgrades) monitors again.
		waitFor(t, 5*time.Minute, "continuous monitoring after the restart", func() bool {
			o := monOverview(t, h.ID)
			return o.Source == "agent" && o.State != nil && o.State.Fidelity.Mode == "continuous"
		})
	})
	_ = json.Marshal
}
