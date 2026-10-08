package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"drool/internal/agent"
	"drool/internal/monitor"
	"drool/internal/monitor/agentmon"
	"drool/internal/monitor/monsvc"
	"drool/internal/monitor/services"
	"drool/internal/monitor/source"
	"drool/internal/monitor/storage"
	"drool/internal/sshx/sshtest"
	"drool/internal/store/db"
)

// fixtureMachine is a fake host for in-process agents: they must never
// read the developer's real /proc, /sys, disks or kernel log. The fake
// systemctl reports app.service in the state written to <root>/svc.
func fixtureMachine(t *testing.T) (root, bin string) {
	t.Helper()
	root, bin = t.TempDir(), t.TempDir()
	w := func(p, s string) {
		os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755)
		if err := os.WriteFile(filepath.Join(root, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w("/proc/stat", "cpu  100 0 50 800 50 0 0 0 0 0\ncpu0 100 0 50 800 50 0 0 0 0 0\nbtime 1700000000\n")
	w("/proc/meminfo", "MemTotal: 4000000 kB\nMemAvailable: 3000000 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n")
	w("/proc/loadavg", "0.10 0.20 0.30 1/100 42\n")
	w("/proc/sys/kernel/random/boot_id", "boot-fixture\n")
	w("/proc/self/mountinfo", "22 1 8:1 / / rw,relatime - ext4 /dev/sda1 rw\n")
	w("/svc", "active running")
	os.WriteFile(filepath.Join(bin, "systemctl"), []byte(`#!/bin/sh
set -- "$@"
read a s < `+root+`/svc
case "$1" in
show) printf 'Id=app.service\nDescription=App\nLoadState=loaded\nActiveState=%s\nSubState=%s\nUnitFileState=enabled\nNRestarts=0\n' "$a" "$s";;
list-unit-files) echo 'app.service enabled enabled';;
esac
`), 0o755)
	return root, bin
}

// fixtureMonitor returns agent monitoring options on a fixture machine,
// with a clock that runs 60× fast but stays in the past (a minute's
// window closes every second of real time).
func fixtureMonitor(t *testing.T) *agentmon.Options {
	root, bin := fixtureMachine(t)
	start := time.Now()
	base := start.Add(-time.Hour)
	now := func() time.Time { return base.Add(time.Since(start) * 60) }
	return &agentmon.Options{Source: &source.Local{Root: root, Path: bin + ":/usr/bin:/bin"}, KmsgPath: "-", CgroupRoot: root,
		Tick: 5 * time.Millisecond, Now: now}
}

type monClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *monClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *monClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type monOverview struct {
	HasData bool   `json:"has_data"`
	Source  string `json:"source"`
	Stale   bool   `json:"stale"`
	Tiles   []struct {
		Key, Text, Status, Detail string
		Value                     *float64
	} `json:"tiles"`
	State  *monitor.State                  `json:"state"`
	Issues []monsvc.Issue                  `json:"issues"`
	Agent  *monsvc.AgentView               `json:"agent"`
	Probes map[string]agentmon.ProbeResult `json:"probes"`
}

func (e *fwEnv) overview(t *testing.T, hostID string) monOverview {
	t.Helper()
	var o monOverview
	r := e.c.do("GET", "/api/v1/hosts/"+hostID+"/monitoring", nil)
	if r.status != 200 {
		t.Fatalf("overview: %d %s", r.status, r.body)
	}
	r.json(t, &o)
	return o
}

func (e *fwEnv) issues(t *testing.T, hostID string) map[string]monsvc.Issue {
	t.Helper()
	var out struct{ Issues []monsvc.Issue }
	e.c.do("GET", "/api/v1/monitoring/issues?host_id="+hostID, nil).json(t, &out)
	m := map[string]monsvc.Issue{}
	for _, i := range out.Issues {
		m[i.Check+"|"+i.Subject] = i
	}
	return m
}

func send(t *testing.T, m *monsvc.Service, hostID, kind string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	m.OnTelemetry(hostID, kind, b)
	m.Drain(context.Background())
}

func pointAt(ts time.Time, v map[string]float64) monitor.Point {
	p := monitor.Point{T: ts.Unix() / 60 * 60, S: 60, N: 6, V: map[string][2]float64{}}
	for k, x := range v {
		p.V[k] = [2]float64{x, x}
	}
	return p
}

func stateAt(ts time.Time, fs ...monitor.FS) monitor.State {
	cpu := 10.0
	return monitor.State{Source: "agent", At: ts.UnixMilli(), BootID: "b1", Fidelity: monitor.Fidelity{Mode: "continuous", SampleS: 10},
		CPU: cpu2{}.v(cpu), Memory: &monitor.Memory{TotalBytes: 1 << 30, AvailableBytes: 1 << 29, UsedPercent: 50},
		Filesystems: fs, Interfaces: []monitor.Interface{}, Disks: []monitor.DiskIO{}, Sensors: []monitor.Sensor{}, GPUs: []monitor.GPU{},
		Collectors: []monitor.CollectorStatus{}, Helpers: []monitor.Helper{}}
}

type cpu2 struct{}

func (cpu2) v(x float64) *monitor.CPU { return &monitor.CPU{Percent: &x, Count: 2} }

func TestMonitoringHealthEvaluation(t *testing.T) {
	e := newFwEnv(t)
	hostID, _ := e.sshFwHost(t, "web1")
	m := e.h.mon
	clk := &monClock{t: time.Now().Add(-6 * time.Hour).Truncate(time.Minute)}
	m.SetClock(clk.now)
	ctx := context.Background()
	minute := func(cpu, mem float64) {
		send(t, m, hostID, "metrics", monitor.Batch{Points: []monitor.Point{pointAt(clk.now(), map[string]float64{"cpu": cpu, "mem": mem})}})
		clk.add(time.Minute)
	}
	send(t, m, hostID, "monitor_state", stateAt(clk.now()))

	// A spike: two minutes at 100% is not a problem.
	minute(100, 40)
	minute(100, 40)
	minute(20, 40)
	if _, ok := e.issues(t, hostID)["cpu|"]; ok {
		t.Fatal("a 2-minute CPU spike opened an issue")
	}
	// Sustained: five minutes at or above 95% is.
	for i := 0; i < 5; i++ {
		minute(99, 40)
	}
	cpu, ok := e.issues(t, hostID)["cpu|"]
	if !ok || cpu.Severity != "warning" {
		t.Fatalf("sustained CPU: %+v", e.issues(t, hostID))
	}
	// Hysteresis: 90% (between the 85% clear level and 95%) keeps it open,
	minute(90, 40)
	minute(90, 40)
	minute(90, 40)
	minute(90, 40)
	if _, ok := e.issues(t, hostID)["cpu|"]; !ok {
		t.Fatal("cleared inside the hysteresis band")
	}
	// and it clears after three calm minutes, not one.
	minute(40, 40)
	if _, ok := e.issues(t, hostID)["cpu|"]; !ok {
		t.Fatal("cleared after a single calm minute")
	}
	minute(40, 40)
	minute(40, 40)
	minute(40, 40)
	if _, ok := e.issues(t, hostID)["cpu|"]; ok {
		t.Fatal("not cleared after calm minutes")
	}
	// Memory: warning at 90%, critical at 97%, sustained.
	for i := 0; i < 6; i++ {
		minute(10, 98)
	}
	if i := e.issues(t, hostID)["memory|"]; i.Severity != "critical" {
		t.Fatalf("memory: %+v", e.issues(t, hostID))
	}
	// A gap in the data restarts the clock instead of firing on stale minutes.
	clk.add(time.Hour)
	minute(99, 10)
	if _, ok := e.issues(t, hostID)["cpu|"]; ok {
		t.Fatal("one minute after a gap must not complete a sustained condition")
	}

	// Filesystems: 85% warning (after two observations), 95% critical, read-only flip.
	fs := func(pct float64, ro bool) {
		send(t, m, hostID, "monitor_state", stateAt(clk.now(), monitor.FS{Mount: "/", Device: "/dev/sda1", DevNum: "8:1", Type: "ext4",
			TotalBytes: 100 << 30, UsedBytes: int64(pct) << 30, AvailBytes: int64(100-pct) << 30, UsedPercent: pct, ReadOnly: ro}))
		clk.add(time.Minute)
	}
	fs(87, false)
	if _, ok := e.issues(t, hostID)["filesystem|/"]; ok {
		t.Fatal("one observation over 85% is debounced")
	}
	fs(87, false)
	if i := e.issues(t, hostID)["filesystem|/"]; i.Severity != "warning" {
		t.Fatalf("85%%: %+v", e.issues(t, hostID))
	}
	fs(96, false)
	fs(96, false)
	if i := e.issues(t, hostID)["filesystem|/"]; i.Severity != "critical" {
		t.Fatalf("95%%: %+v", i)
	}
	fs(96, true)
	if i := e.issues(t, hostID)["filesystem_readonly|/"]; i.Severity != "critical" {
		t.Fatalf("read-only flip: %+v", e.issues(t, hostID))
	}
	fs(50, false)
	is := e.issues(t, hostID)
	if _, ok := is["filesystem|/"]; ok {
		t.Fatal("disk issue not resolved")
	}
	if _, ok := is["filesystem_readonly|/"]; ok {
		t.Fatal("read-only issue not resolved after remount rw")
	}

	// Temperatures: only against the hardware's own thresholds.
	st := stateAt(clk.now())
	hi, crit := 80.0, 100.0
	st.Sensors = []monitor.Sensor{{ID: "coretemp/Package id 0", Chip: "coretemp", Label: "Package id 0", Kind: "cpu", Celsius: 85, High: &hi, Critical: &crit},
		{ID: "acpitz/temp1", Chip: "acpitz", Label: "temp1", Kind: "board", Celsius: 120}}
	for i := 0; i < 3; i++ {
		st.At = clk.now().UnixMilli()
		send(t, m, hostID, "monitor_state", st)
		clk.add(time.Minute)
	}
	is = e.issues(t, hostID)
	if i := is["temperature|coretemp/Package id 0"]; i.Severity != "warning" {
		t.Fatalf("hot CPU: %+v", is)
	}
	if _, ok := is["temperature|acpitz/temp1"]; ok {
		t.Fatal("a sensor without thresholds must never be judged (no universal danger temperature)")
	}

	// The overview never turns unknown into zero.
	o := e.overview(t, hostID)
	for _, tl := range o.Tiles {
		if tl.Key == "temperature" && (tl.Value == nil || *tl.Value != 85) {
			t.Fatalf("temperature tile: %+v", tl)
		}
	}
	empty := stateAt(clk.now())
	empty.CPU, empty.Memory = nil, nil
	send(t, m, hostID, "monitor_state", empty)
	for _, tl := range e.overview(t, hostID).Tiles {
		if (tl.Key == "cpu" || tl.Key == "memory") && (tl.Value != nil || tl.Text != "Unknown" || tl.Status != "unknown") {
			t.Fatalf("unknown must not render as 0: %+v", tl)
		}
	}
	_ = ctx
}

func TestMonitoringServicesStaleAndTransitions(t *testing.T) {
	e := newFwEnv(t)
	hostID, _ := e.sshFwHost(t, "app1")
	m := e.h.mon
	clk := &monClock{t: time.Now().Add(-time.Hour).Truncate(time.Minute)}
	m.SetClock(clk.now)
	ctx := context.Background()
	units := func(state, sub string) services.Report {
		return services.Report{Supported: true, Units: []services.Unit{
			{Name: "jellyfin.service", Description: "Jellyfin", Load: "loaded", Active: state, Sub: sub, FileState: "enabled"},
			{Name: "backup.service", Description: "Backup", Load: "loaded", Active: "inactive", Sub: "dead", FileState: "static"},
			{Name: "run-r1.service", Load: "loaded", Active: "inactive", Sub: "dead", FileState: "transient", Transient: true},
		}}
	}
	send(t, m, hostID, "monitor_state", stateAt(clk.now()))
	if err := m.IngestServices(ctx, hostID, ptr(units("active", "running")), true); err != nil {
		t.Fatal(err)
	}
	// Identical reports write nothing new.
	m.IngestServices(ctx, hostID, ptr(units("active", "running")), true)
	var hist struct{ Events []monsvc.ServiceEvent }
	e.c.do("GET", "/api/v1/hosts/"+hostID+"/monitoring/service-events", nil).json(t, &hist)
	if len(hist.Events) != 0 {
		t.Fatalf("no transitions yet: %+v", hist.Events)
	}
	// Default view hides noise; filters work; all=true shows everything.
	var v monsvc.ServicesView
	e.c.do("GET", "/api/v1/hosts/"+hostID+"/monitoring/services", nil).json(t, &v)
	if len(v.Units) != 1 || v.Units[0].Name != "jellyfin.service" || v.Hidden != 2 || v.Counts["running"] != 1 {
		t.Fatalf("default view: %+v", v)
	}
	e.c.do("GET", "/api/v1/hosts/"+hostID+"/monitoring/services?all=true&filter=stopped", nil).json(t, &v)
	if len(v.Units) != 2 {
		t.Fatalf("all units, stopped: %+v", v.Units)
	}
	// Watch jellyfin (M12's "if jellyfin stops for 2 minutes").
	if r := e.c.do("PUT", "/api/v1/hosts/"+hostID+"/monitoring/settings", map[string]any{"watched_services": []string{"jellyfin"}, "probe_mounts": []string{}, "disabled_checks": []string{}}); r.status != 200 {
		t.Fatalf("watch: %d %s", r.status, r.body)
	}
	// Running → Failed.
	clk.add(time.Minute)
	m.IngestServices(ctx, hostID, ptr(units("failed", "failed")), true)
	send(t, m, hostID, "monitor_state", stateAt(clk.now()))
	m.Tick(ctx)
	if _, ok := e.issues(t, hostID)["service_failed|jellyfin.service"]; ok {
		t.Fatal("failed for less than the configured minute")
	}
	clk.add(90 * time.Second)
	send(t, m, hostID, "monitor_state", stateAt(clk.now()))
	m.Tick(ctx)
	if i := e.issues(t, hostID)["service_failed|jellyfin.service"]; i.Severity != "critical" {
		t.Fatalf("watched service failed: %+v", e.issues(t, hostID))
	}
	// Failed → Running resolves.
	m.IngestServices(ctx, hostID, ptr(units("active", "running")), true)
	if _, ok := e.issues(t, hostID)["service_failed|jellyfin.service"]; ok {
		t.Fatal("not resolved after recovery")
	}
	// Running → Stopped, watched: an issue after two minutes.
	m.IngestServices(ctx, hostID, ptr(units("inactive", "dead")), true)
	clk.add(3 * time.Minute)
	send(t, m, hostID, "monitor_state", stateAt(clk.now()))
	m.Tick(ctx)
	if i := e.issues(t, hostID)["service_stopped|jellyfin.service"]; i.Severity != "critical" {
		t.Fatalf("watched service stopped: %+v", e.issues(t, hostID))
	}
	m.IngestServices(ctx, hostID, ptr(units("active", "running")), true)
	// Telemetry lost: the host goes stale and units become unknown.
	clk.add(10 * time.Minute)
	m.Tick(ctx)
	if _, ok := e.issues(t, hostID)["stale|"]; !ok {
		t.Fatalf("stale host: %+v", e.issues(t, hostID))
	}
	e.c.do("GET", "/api/v1/hosts/"+hostID+"/monitoring/services", nil).json(t, &v)
	if v.Units[0].State != "unknown" || v.Units[0].Known {
		t.Fatalf("units of a stale host are unknown: %+v", v.Units[0])
	}
	// Telemetry resumes: Unknown → Running.
	send(t, m, hostID, "monitor_state", stateAt(clk.now()))
	m.IngestServices(ctx, hostID, ptr(units("active", "running")), true)
	e.c.do("GET", "/api/v1/hosts/"+hostID+"/monitoring/services/jellyfin.service/history", nil).json(t, &hist)
	var seq []string
	for i := len(hist.Events) - 1; i >= 0; i-- {
		seq = append(seq, hist.Events[i].From+"→"+hist.Events[i].To)
	}
	want := "active/running→failed/failed failed/failed→active/running active/running→inactive/dead inactive/dead→active/running active/running→unknown unknown→active/running"
	if strings.Join(seq, " ") != want {
		t.Fatalf("transitions:\n got %s\nwant %s", strings.Join(seq, " "), want)
	}
	if _, ok := e.issues(t, hostID)["stale|"]; ok {
		t.Fatal("stale not resolved by fresh data")
	}
}

func ptr[T any](v T) *T { return &v }

func TestMonitoringStorageAndEvidence(t *testing.T) {
	e := newFwEnv(t)
	hostID, _ := e.sshFwHost(t, "nas1")
	m := e.h.mon
	ctx := context.Background()
	pend := int64(0)
	dev := func(passed bool, pending int64, ro bool) storage.Report {
		d := storage.Device{Key: "wwn-0x5000", Name: "sda", Kind: "hdd", Transport: "sata", Model: "WD Red", Capability: storage.CapFull,
			Partitions: []storage.Partition{}, ReadOnly: ro, SMART: &storage.SMART{Passed: &passed, Pending: &pending, Reallocated: &pend}}
		sd := storage.Device{Key: "name:mmcblk0", Name: "mmcblk0", Kind: "sd", Transport: "mmc", Capability: storage.CapLimited, Partitions: []storage.Partition{}}
		return storage.Report{Devices: []storage.Device{d, sd}, Smart: true}
	}
	send(t, m, hostID, "monitor_state", stateAt(time.Now()))
	send(t, m, hostID, "storage", dev(true, 0, false))
	var sv monsvc.StorageView
	e.c.do("GET", "/api/v1/hosts/"+hostID+"/monitoring/storage", nil).json(t, &sv)
	if len(sv.Devices) != 2 || sv.Devices[0].Health.State != storage.StateOK || sv.Devices[1].Health.State != storage.StateNoIndicators ||
		sv.Devices[1].Health.Summary != "No failure indicators detected" {
		t.Fatalf("storage: %+v", sv.Devices)
	}
	// New pending sectors: a warning and a "new media errors" issue.
	send(t, m, hostID, "storage", dev(true, 3, false))
	is := e.issues(t, hostID)
	if is["storage_health|wwn-0x5000"].Severity != "warning" || is["media_errors|wwn-0x5000"].Severity != "warning" {
		t.Fatalf("increasing pending sectors: %+v", is)
	}
	// SMART failure: critical. Block device flips read-only: critical.
	send(t, m, hostID, "storage", dev(false, 3, true))
	is = e.issues(t, hostID)
	if is["storage_health|wwn-0x5000"].Severity != "critical" || is["device_readonly|wwn-0x5000"].Severity != "critical" {
		t.Fatalf("failing disk: %+v", is)
	}
	// Kernel evidence, deduplicated across an agent restart (same boot, same kernel timestamp).
	kev := func(us int64, text string) agentmon.Event {
		ev, _ := storage.Event(us, text)
		ev.Disk = "mmcblk0"
		d, _ := json.Marshal(ev)
		return agentmon.Event{T: time.Now().Unix(), Kind: agentmon.EventKernel, Subject: "mmcblk0", Severity: "warning", Message: text, Data: d, BootID: "b1"}
	}
	evs := []agentmon.Event{kev(1000, "mmcblk0: error -110 sending stop command, original cmd response 0x900, card status 0xd00"),
		kev(2000, "Buffer I/O error on dev mmcblk0p2, logical block 0, lost async page write")}
	send(t, m, hostID, "events", map[string]any{"events": evs})
	send(t, m, hostID, "events", map[string]any{"events": evs}) // the agent restarted and re-read its kernel buffer
	var ev struct{ Events []monsvc.Event }
	e.c.do("GET", "/api/v1/hosts/"+hostID+"/monitoring/events?kind=kernel", nil).json(t, &ev)
	if len(ev.Events) != 2 {
		t.Fatalf("kernel events must be recorded once: %d", len(ev.Events))
	}
	if i := e.issues(t, hostID)["kernel_storage|mmcblk0"]; i.Severity != "warning" {
		t.Fatalf("SD errors: %+v", e.issues(t, hostID))
	}
	// Probe failure is critical; a passing probe resolves it.
	probe := func(ok bool) {
		r := agentmon.ProbeResult{Mount: "/", At: time.Now().Unix(), OK: ok}
		if !ok {
			r.Stage, r.Error = "verify", "read back 512 bytes that differ from what was written"
		}
		d, _ := json.Marshal(r)
		send(t, m, hostID, "events", map[string]any{"events": []agentmon.Event{{T: time.Now().Unix(), Kind: agentmon.EventProbe, Subject: "/", Data: d}}})
	}
	probe(false)
	if i := e.issues(t, hostID)["probe|/"]; i.Severity != "critical" {
		t.Fatalf("probe failure: %+v", e.issues(t, hostID))
	}
	probe(true)
	if _, ok := e.issues(t, hostID)["probe|/"]; ok {
		t.Fatal("probe success must resolve")
	}
	o := e.overview(t, hostID)
	if !o.Probes["/"].OK {
		t.Fatalf("latest probe: %+v", o.Probes)
	}
	for _, tl := range o.Tiles {
		if tl.Key == "devices" && tl.Status != "critical" {
			t.Fatalf("devices tile: %+v", tl)
		}
	}
	_ = ctx
}

func TestMonitoringHistoryRollupsAndPruning(t *testing.T) {
	e := newFwEnv(t)
	hostID, _ := e.sshFwHost(t, "db1")
	m := e.h.mon
	ctx := context.Background()
	clk := &monClock{t: time.Now().Truncate(time.Hour).Add(-20 * time.Hour)}
	m.SetClock(clk.now)
	send(t, m, hostID, "monitor_state", stateAt(clk.now()))
	// 20 hours of minutes (agent), with a 2-hour hole.
	var ps []monitor.Point
	for i := 0; i < 20*60; i++ {
		ts := clk.now().Add(time.Duration(i) * time.Minute)
		if i >= 300 && i < 420 {
			continue
		}
		ps = append(ps, pointAt(ts, map[string]float64{"cpu": float64(i % 100), "fs:/": 40, "net.rx:eth0": 1000}))
	}
	clk.add(20 * time.Hour)
	for i := 0; i < len(ps); i += 120 {
		send(t, m, hostID, "metrics", monitor.Batch{Points: ps[i:min(len(ps), i+120)]})
	}
	if err := m.Rollups(ctx); err != nil {
		t.Fatal(err)
	}
	var h monsvc.History
	e.c.do("GET", "/api/v1/hosts/"+hostID+"/monitoring/history?range=1h&series=cpu", nil).json(t, &h)
	if h.StepS != 60 || len(h.Series) != 1 || len(h.Series[0].Points) < 55 {
		t.Fatalf("1h: step %d %+v", h.StepS, len(h.Series))
	}
	e.c.do("GET", "/api/v1/hosts/"+hostID+"/monitoring/history?range=24h&series=cpu&series=fs:", nil).json(t, &h)
	if h.StepS != 300 || len(h.Series) != 2 {
		t.Fatalf("24h uses 5-minute windows: %d %v", h.StepS, h.Series)
	}
	n := len(h.Series[0].Points)
	if n < 200 || n > 216 {
		t.Fatalf("24h points: %d (20 h minus a 2 h hole is 216 windows)", n)
	}
	for _, p := range h.Series[0].Points {
		if p[2] < p[1] {
			t.Fatalf("max below avg: %v", p)
		}
	}
	// Missing windows are gaps, not zeros.
	for i := 1; i < n; i++ {
		if h.Series[0].Points[i][0]-h.Series[0].Points[i-1][0] > 3600 {
			goto hole
		}
	}
	t.Fatal("the 2-hour hole should be visible as missing windows")
hole:
	e.c.do("GET", "/api/v1/hosts/"+hostID+"/monitoring/history?range=7d&series=cpu", nil).json(t, &h)
	if h.StepS != 3600 || len(h.Series[0].Points) < 17 {
		t.Fatalf("7d uses hourly windows: %d %d", h.StepS, len(h.Series[0].Points))
	}
	// Late data (an agent's buffer after an outage) is rolled up again.
	late := pointAt(clk.now().Add(-14*time.Hour-30*time.Minute), map[string]float64{"cpu": 100})
	send(t, m, hostID, "metrics", monitor.Batch{Points: []monitor.Point{late}})
	m.Rollups(ctx)
	e.c.do("GET", "/api/v1/hosts/"+hostID+"/monitoring/history?range=24h&series=cpu", nil).json(t, &h)
	if len(h.Series[0].Points) != n+1 {
		t.Fatalf("late window not rolled up: %d → %d", n, len(h.Series[0].Points))
	}
	// Bounded requests.
	if r := e.c.do("GET", "/api/v1/hosts/"+hostID+"/monitoring/history?range=1y", nil); r.status != 422 {
		t.Fatalf("unbounded range accepted: %d", r.status)
	}
	var many []string
	for i := 0; i < 40; i++ {
		many = append(many, fmt.Sprintf("series=k%d", i))
	}
	if r := e.c.do("GET", "/api/v1/hosts/"+hostID+"/monitoring/history?"+strings.Join(many, "&"), nil); r.status != 422 {
		t.Fatalf("too many series accepted: %d", r.status)
	}
	// Gaps reported by the agent are recorded and shown.
	send(t, m, hostID, "metrics", monitor.Batch{Gaps: []monitor.Gap{{From: clk.now().Add(-3 * time.Hour).Unix(), To: clk.now().Add(-2 * time.Hour).Unix(), Reason: "the agent's offline buffer was full"}}})
	e.c.do("GET", "/api/v1/hosts/"+hostID+"/monitoring/history?range=6h&series=cpu", nil).json(t, &h)
	if len(h.Gaps) != 1 {
		t.Fatalf("gap: %+v", h.Gaps)
	}
	// Pruning: two days later only the tiers' horizons remain.
	clk.add(50 * time.Hour)
	r := m.Prune(ctx)
	if r.Points == 0 {
		t.Fatal("nothing pruned")
	}
	var raw, five, hour int
	e.h.st.Reader.QueryRow(`SELECT COUNT(*) FROM mon_points WHERE host_id = ? AND tier = 0`, hostID).Scan(&raw)
	e.h.st.Reader.QueryRow(`SELECT COUNT(*) FROM mon_points WHERE host_id = ? AND tier = 1`, hostID).Scan(&five)
	e.h.st.Reader.QueryRow(`SELECT COUNT(*) FROM mon_points WHERE host_id = ? AND tier = 2`, hostID).Scan(&hour)
	if raw != 0 || five == 0 || hour == 0 {
		t.Fatalf("after 2 days: raw %d five %d hour %d", raw, five, hour)
	}
	// Invalid points are refused before storage.
	bad := []monitor.Point{{T: clk.now().Add(time.Hour).Unix(), S: 60, V: map[string][2]float64{"cpu": {1, 1}}}, {T: clk.now().Unix(), S: 0, V: map[string][2]float64{"cpu": {1, 1}}}}
	if err := m.IngestPoints(ctx, hostID, bad, nil); err != nil {
		t.Fatal(err)
	}
	var future int
	e.h.st.Reader.QueryRow(`SELECT COUNT(*) FROM mon_points WHERE host_id = ? AND ts > ?`, hostID, clk.now().Unix()+60).Scan(&future)
	if future != 0 {
		t.Fatal("a point from the future was stored")
	}
}

func TestMonitoringPermissionsSettingsAndFleet(t *testing.T) {
	e := newFwEnv(t)
	hostID, _ := e.sshFwHost(t, "web9")
	m := e.h.mon
	send(t, m, hostID, "monitor_state", stateAt(time.Now()))
	e.h.mkUser("vera", "viewer")
	e.h.mkUser("otto", "operator")
	viewer, op := e.h.login("vera"), e.h.login("otto")
	if r := viewer.do("GET", "/api/v1/hosts/"+hostID+"/monitoring", nil); r.status != 200 {
		t.Fatalf("viewers read monitoring: %d", r.status)
	}
	if r := viewer.do("PUT", "/api/v1/hosts/"+hostID+"/monitoring/settings", map[string]any{"watched_services": []string{"a"}}); r.status != 403 {
		t.Fatalf("viewer changed settings: %d", r.status)
	}
	if r := op.do("PUT", "/api/v1/hosts/"+hostID+"/monitoring/settings", map[string]any{"watched_services": []string{"nginx"}, "probe_mounts": []string{"/"}, "probe_s": 600}); r.status != 200 {
		t.Fatalf("operator watches a service: %d %s", r.status, r.body)
	} else {
		var out struct{ Warning string }
		r.json(t, &out)
		if !strings.Contains(out.Warning, "Once per hour is recommended") {
			t.Fatalf("a short probe interval warns (but is allowed): %q", out.Warning)
		}
	}
	if r := op.do("PUT", "/api/v1/hosts/"+hostID+"/monitoring/settings", map[string]any{"watched_services": []string{}, "limits": map[string]any{"cpu_quota_percent": 50}}); r.status != 403 {
		t.Fatalf("an operator changed an agent's resource budget: %d", r.status)
	}
	if r := op.do("PUT", "/api/v1/monitoring/settings", monsvc.DefaultSettings()); r.status != 403 {
		t.Fatalf("operator changed fleet settings: %d", r.status)
	}
	bad := monsvc.DefaultSettings()
	bad.Profile, bad.Custom.DiskWarning, bad.Custom.DiskCritical = "custom", 96, 90
	if r := e.c.do("PUT", "/api/v1/monitoring/settings", bad); r.status != 422 {
		t.Fatalf("warning above critical accepted: %d", r.status)
	}
	custom := monsvc.DefaultSettings()
	custom.Profile, custom.Custom.DiskWarning = "custom", 70
	if r := e.c.do("PUT", "/api/v1/monitoring/settings", custom); r.status != 200 {
		t.Fatalf("custom thresholds: %d %s", r.status, r.body)
	}
	var audit struct{ Items []struct{ Action string } }
	e.c.do("GET", "/api/v1/audit?limit=50", nil).json(t, &audit)
	found := map[string]bool{}
	for _, a := range audit.Items {
		found[a.Action] = true
	}
	if !found["monitoring.settings.update"] || !found["monitoring.host_settings.update"] {
		t.Fatalf("settings changes are audited: %v", found)
	}
	// Fleet page: categories, filters, paging.
	other, _ := e.sshFwHost(t, "db9")
	st := stateAt(time.Now(), monitor.FS{Mount: "/", TotalBytes: 100, UsedBytes: 75, AvailBytes: 25, UsedPercent: 75})
	send(t, m, other, "monitor_state", st)
	send(t, m, other, "monitor_state", st) // two observations over the custom 70%
	var f monsvc.FleetSummary
	e.c.do("GET", "/api/v1/monitoring/summary", nil).json(t, &f)
	if f.Hosts != 2 || f.Monitored != 2 || f.Counts["disk"] != 1 || f.Rows[0].Name != "db9" {
		t.Fatalf("fleet: %+v", f)
	}
	e.c.do("GET", "/api/v1/monitoring/summary?category=disk", nil).json(t, &f)
	if f.Total != 1 || f.Rows[0].HostID != other {
		t.Fatalf("filter: %+v", f.Rows)
	}
	e.c.do("GET", "/api/v1/monitoring/summary?sort=name&limit=1&offset=1", nil).json(t, &f)
	if len(f.Rows) != 1 || f.Rows[0].Name != "web9" || f.Total != 2 {
		t.Fatalf("paging: %+v", f.Rows)
	}
	if r := e.c.do("GET", "/api/v1/monitoring/summary?category=bogus", nil); r.status != 422 {
		t.Fatalf("bad category: %d", r.status)
	}
}

func TestMonitoringHelpersApprovalAndInstall(t *testing.T) {
	e := newFwEnv(t)
	hostID, _ := e.sshFwHost(t, "pi1")
	m := e.h.mon
	ctx := context.Background()
	e.h.st.W.UpsertHostFacts(ctx, factsWithPkg(hostID, "apt"))
	st := stateAt(time.Now())
	st.Helpers = []monitor.Helper{{Package: "smartmontools", Tool: "smartctl", Useful: true}, {Package: "nvme-cli", Tool: "nvme", Useful: true},
		{Package: "lm-sensors", Tool: "sensors", Installed: true}}
	send(t, m, hostID, "monitor_state", st)
	var pv monsvc.HelperPreview
	if r := e.c.do("POST", "/api/v1/monitoring/helpers/preview", map[string]any{"host_ids": []string{hostID}}); r.status != 200 {
		t.Fatalf("preview: %d %s", r.status, r.body)
	} else {
		r.json(t, &pv)
	}
	if pv.Hosts != 1 || pv.Counts["smartmontools"] != 1 || pv.Counts["nvme-cli"] != 1 || pv.Counts["lm-sensors"] != 0 || pv.Installs != 1 {
		t.Fatalf("preview: %+v", pv)
	}
	if r := e.c.do("POST", "/api/v1/monitoring/helpers/preview", map[string]any{"host_ids": []string{hostID}, "packages": []string{"nvidia-driver"}}); r.status != 422 {
		t.Fatalf("a package outside the helper allowlist (GPU drivers!) was accepted: %d", r.status)
	}
	// Viewers can't install; operators can (it's a package job).
	e.h.mkUser("vic", "viewer")
	if r := e.h.login("vic").do("POST", "/api/v1/monitoring/helpers/install", map[string]any{"host_ids": []string{hostID}}); r.status != 403 {
		t.Fatalf("viewer installed packages: %d", r.status)
	}
	var res monsvc.InstallResult
	if r := e.c.do("POST", "/api/v1/monitoring/helpers/install", map[string]any{"host_ids": []string{hostID}}); r.status != 202 {
		t.Fatalf("install: %d %s", r.status, r.body)
	} else {
		r.json(t, &res)
	}
	if len(res.Jobs) != 1 {
		t.Fatalf("jobs: %+v", res)
	}
	var job struct {
		Name string
		Spec struct {
			Steps []struct {
				Type   string
				Action string
				Names  []string
			}
		}
	}
	e.c.do("GET", "/api/v1/jobs/"+res.Jobs[0], nil).json(t, &job)
	if len(job.Spec.Steps) != 1 || job.Spec.Steps[0].Type != "package" || job.Spec.Steps[0].Action != "install" || strings.Join(job.Spec.Steps[0].Names, " ") != "nvme-cli smartmontools" {
		t.Fatalf("an ordinary package job: %+v", job)
	}
	// The request is tracked; the job runs through the normal engine.
	o := e.overview(t, hostID)
	_ = o
	var hs []monsvc.HelperStatus
	var ov struct{ Helpers []monsvc.HelperStatus }
	e.c.do("GET", "/api/v1/hosts/"+hostID+"/monitoring", nil).json(t, &ov)
	hs = ov.Helpers
	if hs[0].Requested == "" || hs[0].JobID != res.Jobs[0] {
		t.Fatalf("request tracked: %+v", hs)
	}
	// Standing approvals: admin only, allowlist only, audited.
	if r := e.c.do("POST", "/api/v1/monitoring/approvals", map[string]any{"scope": "global", "package": "gpu-driver"}); r.status != 422 {
		t.Fatalf("approval outside the allowlist: %d", r.status)
	}
	if r := e.c.do("POST", "/api/v1/monitoring/approvals", map[string]any{"scope": "global", "package": "lm-sensors"}); r.status != 201 {
		t.Fatalf("approval: %d %s", r.status, r.body)
	}
	e.h.mkUser("opie", "operator")
	if r := e.h.login("opie").do("POST", "/api/v1/monitoring/approvals", map[string]any{"scope": "global", "package": "smartmontools"}); r.status != 403 {
		t.Fatalf("operator created a standing approval: %d", r.status)
	}
	// A second host that needs lm-sensors gets it automatically, once.
	other, _ := e.sshFwHost(t, "pi2")
	e.h.st.W.UpsertHostFacts(ctx, factsWithPkg(other, "dnf"))
	st2 := stateAt(time.Now())
	st2.Helpers = []monitor.Helper{{Package: "lm-sensors", Tool: "sensors", Useful: true}}
	send(t, m, other, "monitor_state", st2)
	m.AutoInstallHelpers(ctx)
	m.AutoInstallHelpers(ctx)
	var n int
	var names string
	e.h.st.Reader.QueryRow(`SELECT COUNT(*) FROM jobs WHERE name LIKE 'Install monitoring helpers%'`).Scan(&n)
	e.h.st.Reader.QueryRow(`SELECT name FROM jobs WHERE name LIKE '%lm_sensors%'`).Scan(&names)
	if n != 2 || names == "" {
		t.Fatalf("automatic installation from a standing approval (dnf name lm_sensors), once: %d %q", n, names)
	}
}

func TestMonitoringSSHPoll(t *testing.T) {
	e := newFwEnv(t)
	hostID, _ := e.sshFwHost(t, "ssh1")
	m := e.h.mon
	ctx := context.Background()
	root, bin := fixtureMachine(t)
	// The SSH path: the generated scripts, run by sh against the fixture
	// machine (the same scripts the poller runs over the pinned connection).
	m.SetPollRoot(root)
	run := func(_ context.Context, script string) ([]byte, error) {
		c := exec.Command("/bin/sh", "-c", script)
		c.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		return c.Output()
	}
	clk := &monClock{t: time.Now().Add(-time.Hour)}
	m.SetClock(clk.now)
	if err := m.PollSSH(ctx, hostID, run, nil); err != nil {
		t.Fatal(err)
	}
	// The next poll: CPU now busier, the service failed.
	os.WriteFile(filepath.Join(root, "proc/stat"), []byte("cpu  700 0 50 900 50 0 0 0 0 0\ncpu0 700 0 50 900 50 0 0 0 0 0\nbtime 1700000000\n"), 0o644)
	os.WriteFile(filepath.Join(root, "svc"), []byte("failed failed"), 0o644)
	clk.add(5 * time.Minute)
	if err := m.PollSSH(ctx, hostID, run, nil); err != nil {
		t.Fatal(err)
	}
	o := e.overview(t, hostID)
	if !o.HasData || o.Source != "ssh" || o.State.Fidelity.Mode != "polled" || o.State.Fidelity.SampleS != 300 {
		t.Fatalf("SSH hosts are polled, never continuous: %+v %+v", o.Source, o.State.Fidelity)
	}
	// CPU between the two polls: (600 busy of 700 ticks) — a 5-minute average.
	if o.State.CPU == nil || o.State.CPU.Percent == nil || *o.State.CPU.Percent < 85 || *o.State.CPU.Percent > 86 {
		t.Fatalf("cpu over the poll interval: %+v", o.State.CPU)
	}
	var hist struct{ Events []monsvc.ServiceEvent }
	e.c.do("GET", "/api/v1/hosts/"+hostID+"/monitoring/service-events", nil).json(t, &hist)
	if len(hist.Events) != 1 || hist.Events[0].To != "failed/failed" {
		t.Fatalf("SSH transitions come from the poll diff: %+v", hist.Events)
	}
	var h monsvc.History
	e.c.do("GET", "/api/v1/hosts/"+hostID+"/monitoring/history?range=1h&series=cpu&series=fs:", nil).json(t, &h)
	if h.StepS != 300 || len(h.Series) != 2 {
		t.Fatalf("one point per poll: %+v", h)
	}
}

func factsWithPkg(hostID, mgr string) db.UpsertHostFactsParams {
	return db.UpsertHostFactsParams{HostID: hostID, CollectedAt: time.Now().UnixMilli(), OsFamily: "debian", Distro: "debian", Arch: "x86_64",
		PkgManager: mgr, InitSystem: "systemd", FactsJson: "{}"}
}

var _ = agent.Features

// outageProxy sits between agents and the agent endpoint; Down drops
// every connection and refuses new ones until Up.
type outageProxy struct {
	t      *testing.T
	addr   string
	target string
	mu     sync.Mutex
	ln     net.Listener
	conns  []net.Conn
}

func newOutageProxy(t *testing.T, target string) *outageProxy {
	p := &outageProxy{t: t, target: target}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p.addr = ln.Addr().String()
	p.serve(ln)
	t.Cleanup(p.Down)
	return p
}

func (p *outageProxy) serve(ln net.Listener) {
	p.mu.Lock()
	p.ln = ln
	p.mu.Unlock()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			u, err := net.Dial("tcp", p.target)
			if err != nil {
				c.Close()
				continue
			}
			p.mu.Lock()
			p.conns = append(p.conns, c, u)
			p.mu.Unlock()
			go func() { io.Copy(u, c); u.Close() }()
			go func() { io.Copy(c, u); c.Close() }()
		}
	}()
}

func (p *outageProxy) Down() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ln != nil {
		p.ln.Close()
		p.ln = nil
	}
	for _, c := range p.conns {
		c.Close()
	}
	p.conns = nil
}

func (p *outageProxy) Up() {
	ln, err := net.Listen("tcp", p.addr)
	if err != nil {
		p.t.Fatal(err)
	}
	p.serve(ln)
}

func TestMonitoringAgentEndToEnd(t *testing.T) {
	e := newFwEnv(t)
	proxy := newOutageProxy(t, e.addr)
	e.h.agents.SetAdvertise([]string{proxy.addr})
	mon := fixtureMonitor(t)
	hostID, ag, _, _ := e.agentFwHost(t, "agentmon1", fullCaps(""), func(o *agent.Options) { o.Monitor = mon })
	m := e.h.mon
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	// The controller configures the agent (monitor-1); data streams in.
	eventually(t, 15*time.Second, "agent monitoring data", func() bool {
		o := e.overview(t, hostID)
		return o.HasData && o.Source == "agent" && o.State.Memory != nil
	})
	o := e.overview(t, hostID)
	if o.State.Fidelity.Mode != "continuous" || o.Agent == nil || !o.Agent.Monitor1 || !o.Agent.Connected {
		t.Fatalf("agent fidelity: %+v %+v", o.State.Fidelity, o.Agent)
	}
	if o.State.Agent == nil || o.State.Agent.Enforced {
		t.Fatalf("no cgroup in the fixture: the budget is reported as not enforced: %+v", o.State.Agent)
	}
	var v monsvc.ServicesView
	eventually(t, 10*time.Second, "services", func() bool {
		e.c.do("GET", "/api/v1/hosts/"+hostID+"/monitoring/services", nil).json(t, &v)
		return len(v.Units) == 1 && v.Units[0].Name == "app.service" && v.Units[0].State == "running"
	})
	countPoints := func() int {
		var n int
		e.h.st.Reader.QueryRow(`SELECT COUNT(*) FROM mon_points WHERE host_id = ? AND tier = 0`, hostID).Scan(&n)
		return n
	}
	eventually(t, 10*time.Second, "points", func() bool { return countPoints() >= 2 })

	// Controller outage: the agent keeps sampling into its bounded memory
	// buffer, and the service fails meanwhile.
	proxy.Down()
	eventually(t, 10*time.Second, "agent disconnected", func() bool { return !e.connected(hostID) })
	before := countPoints()
	root := mon.Source.Root
	os.WriteFile(filepath.Join(root, "svc"), []byte("failed failed"), 0o644)
	time.Sleep(4 * time.Second) // four "minutes" on the agent's fast clock
	if countPoints() != before {
		t.Fatal("points arrived during the outage")
	}
	if used, _, dropped := ag.a.Monitor().Outbox().Bytes(); used == 0 || dropped != 0 {
		t.Fatalf("buffered in memory during the outage: %d bytes, %d dropped", used, dropped)
	}
	proxy.Up()
	eventually(t, 20*time.Second, "buffered minutes delivered", func() bool { return countPoints() >= before+3 })
	var hist struct{ Events []monsvc.ServiceEvent }
	eventually(t, 10*time.Second, "the transition from the outage", func() bool {
		e.c.do("GET", "/api/v1/hosts/"+hostID+"/monitoring/services/app.service/history", nil).json(t, &hist)
		return len(hist.Events) > 0 && hist.Events[0].To == "failed/failed"
	})
	// The agent never wrote telemetry to disk: its state directory holds
	// only identity/config files (monitor.json is the pushed config).
	ents, _ := os.ReadDir(agentPathsOf(ag).State)
	for _, en := range ents {
		if strings.Contains(en.Name(), "metric") || strings.Contains(en.Name(), "telemetry") || strings.Contains(en.Name(), "buffer") {
			t.Fatalf("telemetry on disk: %s", en.Name())
		}
	}
	// A refresh asks the agent for fresh data; diagnostics come from its memory ring.
	if r := e.c.do("POST", "/api/v1/hosts/"+hostID+"/monitoring/refresh", nil); r.status != 202 {
		t.Fatalf("refresh: %d", r.status)
	}
	if r := e.c.do("POST", "/api/v1/hosts/"+hostID+"/monitoring/diagnostics", nil); r.status != 202 {
		t.Fatalf("diagnostics: %d %s", r.status, r.body)
	}
	eventually(t, 10*time.Second, "diagnostics", func() bool {
		var d struct{ Entries []agent.DiagEntry }
		e.c.do("GET", "/api/v1/hosts/"+hostID+"/monitoring/diagnostics", nil).json(t, &d)
		return len(d.Entries) > 0
	})
	// Turning monitoring off fleet-wide stops the agent's engine.
	off := monsvc.DefaultSettings()
	off.Enabled = false
	if r := e.c.do("PUT", "/api/v1/monitoring/settings", off); r.status != 200 {
		t.Fatalf("off: %d %s", r.status, r.body)
	}
	eventually(t, 10*time.Second, "monitoring stopped", func() bool { return ag.a.Monitor() == nil })
}

// fakeLinuxHost emulates an SSH host for the agent installation: nothing
// runs on the developer's machine. install decides what `drool-agent
// install` does with the token it receives on stdin.
type fakeLinuxHost struct {
	mu      sync.Mutex
	cmds    []string
	stdins  []string
	install func(token string) (string, int)
}

func (f *fakeLinuxHost) exec(cmd string, stdin []byte) (string, string, int) {
	f.mu.Lock()
	f.cmds = append(f.cmds, cmd)
	f.stdins = append(f.stdins, string(stdin))
	f.mu.Unlock()
	switch {
	case strings.Contains(cmd, "uname -s; uname -m"):
		return "Linux\nx86_64\n", "", 0
	case strings.Contains(cmd, "drool-agent-install.XXXXXXXX"):
		sum := sha256.Sum256(stdin)
		return hex.EncodeToString(sum[:]) + "  /tmp/drool-agent-install.abc/drool-agent\n/tmp/drool-agent-install.abc\n", "", 0
	case strings.Contains(cmd, "install --token-file -"):
		out, code := f.install(strings.TrimSpace(string(stdin)))
		return out, "", code
	}
	return "", "", 0
}

func (f *fakeLinuxHost) ran(sub string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.cmds {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

func TestAgentBulkConversion(t *testing.T) {
	e := newFwEnv(t)
	bins := t.TempDir()
	os.WriteFile(filepath.Join(bins, "drool-agent-linux-amd64"), []byte("#!/bin/sh\necho fake agent\n"), 0o755)
	e.h.agents.SetBinariesDir(bins)
	ctx := context.Background()
	mk := func(name, user, become string, f *fakeLinuxHost) string {
		var ck controllerKeyResponse
		e.c.do("GET", "/api/v1/secrets/controller-ssh-key", nil).json(t, &ck)
		key := sshtest.NewEd25519Signer(t)
		srv := sshtest.Start(t, key)
		srv.SetExec(f.exec)
		authorizeCred(t, srv, ck.PublicKey)
		addr, port := srv.Host()
		var host hostJSON
		e.c.do("POST", "/api/v1/hosts", map[string]any{"name": name, "address": addr, "port": port, "user": user, "become_method": become}).json(t, &host)
		e.c.do("POST", "/api/v1/hosts/"+host.ID+"/hostkeys/scan", nil)
		e.c.do("POST", "/api/v1/hosts/"+host.ID+"/hostkeys/approve", map[string]string{"fingerprint": ssh.FingerprintSHA256(key.PublicKey())})
		e.h.st.W.UpsertHostFacts(ctx, factsWithPkg(host.ID, "apt"))
		e.h.st.Writer.Exec(`UPDATE hosts SET status = 'ready' WHERE id = ?`, host.ID)
		return host.ID
	}
	var started []*runningAgent
	good := &fakeLinuxHost{}
	good.install = func(tok string) (string, int) {
		// What the real `drool-agent install` does: create the identity key
		// locally, enroll with the single-use token, start the service.
		p := agentPaths(t, fullCaps(""))
		if _, err := agent.Enroll(context.Background(), p, tok, "v1.0.0"); err != nil {
			return err.Error(), 1
		}
		started = append(started, startAgent(t, p, "v1.0.0"))
		return "Enrolled.\n", 0
	}
	bad := &fakeLinuxHost{install: func(string) (string, int) { return "drool-agent: could not reach the controller\n", 1 }}
	goodID := mk("conv-a", "root", "none", good)
	badID := mk("conv-b", "root", "none", bad)
	noRoot := mk("conv-c", "deploy", "none", &fakeLinuxHost{})
	agentID, _, _, _ := e.agentFwHost(t, "agentconv9", fullCaps(""))

	var pv struct {
		Selected int
		Counts   map[string]int
		Hosts    []struct{ HostID, Status, Reason string } `json:"hosts"`
	}
	if r := e.c.do("POST", "/api/v1/agents/conversions/preview", map[string]any{"host_ids": []string{goodID, badID, noRoot, agentID}}); r.status != 200 {
		t.Fatalf("preview: %d %s", r.status, r.body)
	} else {
		r.json(t, &pv)
	}
	if pv.Selected != 4 || pv.Counts["compatible"] != 2 || pv.Counts["agent"] != 1 || pv.Counts["unsupported"] != 1 {
		t.Fatalf("preview: %+v", pv)
	}
	// Canary first: the good host converts; the failing one pauses the rollout.
	var start struct {
		RolloutID string `json:"rollout_id"`
	}
	if r := e.c.do("POST", "/api/v1/agents/conversions", map[string]any{"host_ids": []string{goodID, badID, noRoot, agentID}, "preset": "managed"}); r.status != 201 {
		t.Fatalf("start: %d %s", r.status, r.body)
	} else {
		r.json(t, &start)
	}
	type rh struct{ HostID, State, Detail string }
	hostState := func() map[string]rh {
		out := map[string]rh{}
		rows, _ := e.h.st.Reader.Query(`SELECT host_id, state, detail FROM agent_rollout_hosts WHERE rollout_id = ?`, start.RolloutID)
		defer rows.Close()
		for rows.Next() {
			var x rh
			rows.Scan(&x.HostID, &x.State, &x.Detail)
			out[x.HostID] = x
		}
		return out
	}
	eventually(t, 60*time.Second, "conversion finished", func() bool {
		s := hostState()
		return (s[goodID].State == "healthy" || s[goodID].State == "failed") && s[badID].State == "failed"
	})
	s := hostState()
	if len(s) != 2 || s[goodID].State != "healthy" {
		t.Fatalf("only compatible hosts take part; the good one converted: %+v", s)
	}
	if !e.connected(goodID) {
		t.Fatal("conversion is declared successful only once the agent connected over mTLS")
	}
	var status string
	e.h.st.Reader.QueryRow(`SELECT status FROM agent_rollouts WHERE id = ?`, start.RolloutID).Scan(&status)
	if status != "paused" || !strings.Contains(s[badID].Detail, "drool-agent install exited with status 1") {
		t.Fatalf("a failure pauses the rollout (max 1 failure): %s %q", status, s[badID].Detail)
	}
	if s[badID].State == "failed" {
		// The failed host stays SSH-managed: no identity, the agent stopped.
		if _, err := e.h.st.R.GetLiveIdentityForHost(ctx, badID); err == nil {
			t.Fatal("the failed host kept an agent identity")
		}
		if !bad.ran("systemctl disable --now drool-agent.service") {
			t.Fatal("a half-installed agent is stopped again")
		}
		if !bad.ran("rm -rf -- '/tmp/drool-agent-install.abc'") {
			t.Fatal("bootstrap material is cleaned up")
		}
	}
	// The token travelled on stdin, never on a command line.
	for _, f := range []*fakeLinuxHost{good, bad} {
		for i, c := range f.cmds {
			if strings.Contains(f.stdins[i], "drool_aet_") || strings.Contains(f.stdins[i], "aet_") {
				if strings.Contains(c, strings.TrimSpace(f.stdins[i])) {
					t.Fatal("the enrollment token appeared in a command line")
				}
			}
		}
	}
	// SSH configuration is retained as the fallback.
	h, _ := e.h.st.R.GetHost(ctx, goodID)
	if h.SshConfigured == 0 {
		t.Fatal("SSH configuration was dropped")
	}
	// Audited.
	var n int
	e.h.st.Reader.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action IN ('agent.convert.start', 'agent.install')`).Scan(&n)
	if n < 2 {
		t.Fatalf("conversion audited: %d", n)
	}
	_ = started
}
