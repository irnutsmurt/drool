package agentmon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"drool/internal/monitor"
	"drool/internal/monitor/source"
)

func pt(t int64) monitor.Point {
	return monitor.Point{T: t, S: 60, N: 6, V: map[string][2]float64{"cpu": {12.5, 40}, "mem": {33, 34}, "load1": {0.2, 0.3}}}
}

func TestOutboxBoundedWithGaps(t *testing.T) {
	o := NewOutbox(4000)
	one, _ := json.Marshal(pt(0))
	for i := int64(0); i < 1000; i++ { // a long controller outage: 1000 minutes
		o.AddPoint(pt(i * 60))
	}
	used, limit, dropped := o.Bytes()
	if used > limit || used < limit-len(one) || dropped == 0 {
		t.Fatalf("used %d limit %d dropped %d", used, limit, dropped)
	}
	for i := 0; i < MaxEvents+50; i++ {
		o.AddEvent(Event{T: int64(i), Kind: EventService, Message: "x"})
	}
	o.SetLatest(KindState, []byte(`{"a":1}`))
	o.SetLatest(KindState, []byte(`{"a":2}`))
	b := o.Drain(10000)
	if len(b.Metrics.Gaps) != 1 || b.Metrics.Gaps[0].From != 0 || b.Metrics.Gaps[0].To != b.Metrics.Points[0].T {
		t.Fatalf("one gap from the start to the first kept point: %+v first=%d", b.Metrics.Gaps, b.Metrics.Points[0].T)
	}
	if last := b.Metrics.Points[len(b.Metrics.Points)-1]; last.T != 999*60 {
		t.Fatalf("the newest points are kept: %d", last.T)
	}
	if len(b.Events) != MaxEvents || b.LostEv != 50 || b.Events[0].T != 50 {
		t.Fatalf("events: %d lost %d first %d", len(b.Events), b.LostEv, b.Events[0].T)
	}
	if string(b.Latest[KindState]) != `{"a":2}` || len(b.Order) != 1 {
		t.Fatalf("latest: %s", b.Latest[KindState])
	}
	if used, _, _ := o.Bytes(); used != 0 {
		t.Fatal("drained")
	}
	// The session dropped mid-delivery: everything goes back in front.
	o.AddPoint(pt(1000 * 60))
	o.Requeue(b)
	again := o.Drain(10000)
	ps := again.Metrics.Points
	for i := 1; i < len(ps); i++ {
		if ps[i].T <= ps[i-1].T {
			t.Fatal("requeued points stay in order, before newer ones")
		}
	}
	if ps[len(ps)-1].T != 1000*60 || len(again.Metrics.Gaps) == 0 {
		t.Fatal("the newest point survives; what didn't fit is a gap")
	}
	if used, limit, _ := o.Bytes(); used > limit {
		t.Fatal("requeue respects the bound")
	}
}

func TestGovernorDegradesAndRecovers(t *testing.T) {
	q := 25.0
	mx := int64(160 << 20)
	g := &Governor{HeapSoft: 48 << 20}
	now := time.Unix(1000, 0)
	u := Usage{ok: true, CPUQuotaPct: &q, MemMax: &mx, CPUUsageUs: 0}
	if g.Check(now, u, HostPressure{}, 1<<20) || g.Level != LevelNormal {
		t.Fatal("first sample has nothing to compare")
	}
	// 30 s later the agent used 9 s of CPU: 30% of one CPU against a 25% quota.
	now = now.Add(30 * time.Second)
	u.CPUUsageUs, u.Throttled = 9_000_000, 3
	if !g.Check(now, u, HostPressure{}, 1<<20) || g.Level != LevelReduced || !strings.Contains(strings.Join(g.Reasons, ";"), "CPU") {
		t.Fatalf("level %d %v", g.Level, g.Reasons)
	}
	if g.Scale(10*time.Second, "fast") != 30*time.Second || !g.Defer("smart") || g.Defer("storage") {
		t.Fatal("reduced: slower sampling, SMART deferred")
	}
	// The host itself is starved: the workload comes first.
	now = now.Add(30 * time.Second)
	starved := 70.0
	g.Check(now, u, HostPressure{Mem10: &starved}, 1<<20)
	if g.Level != LevelMinimal || !g.Defer("storage") {
		t.Fatalf("level %d", g.Level)
	}
	// Calm: one level back per calm period, not at once.
	for i := 0; i < 9; i++ {
		now = now.Add(30 * time.Second)
		g.Check(now, u, HostPressure{}, 1<<20)
	}
	if g.Level != LevelMinimal {
		t.Fatalf("recovered too early: %d", g.Level)
	}
	for i := 0; i < 2; i++ {
		now = now.Add(30 * time.Second)
		g.Check(now, u, HostPressure{}, 1<<20)
	}
	if g.Level != LevelReduced {
		t.Fatalf("one level after 5 calm minutes: %d", g.Level)
	}
	for i := 0; i < 10; i++ {
		now = now.Add(30 * time.Second)
		g.Check(now, u, HostPressure{}, 1<<20)
	}
	if g.Level != LevelNormal || g.Reasons != nil {
		t.Fatalf("back to normal: %d %v", g.Level, g.Reasons)
	}
	// The internal heap budget is a hard trigger too.
	now = now.Add(30 * time.Second)
	g.Check(now, u, HostPressure{}, 64<<20)
	if g.Level != LevelMinimal {
		t.Fatalf("heap over budget: %d", g.Level)
	}
	if g.Defer("probe") {
		t.Fatal("the flash probe is never deferred")
	}
}

func TestGovernorThrottling(t *testing.T) {
	q := 25.0
	g := &Governor{}
	now := time.Unix(1000, 0)
	u := Usage{ok: true, CPUQuotaPct: &q}
	g.Check(now, u, HostPressure{}, 0)
	// A slow CPU: helper bursts throttled in 40 periods, 0.6 s in total
	// over 30 s (2%), 1.5 s of CPU (5%): the quota doing its job.
	now = now.Add(30 * time.Second)
	u.CPUUsageUs, u.Throttled, u.ThrottledUs = 1_500_000, 40, 600_000
	if g.Check(now, u, HostPressure{}, 0) || g.Level != LevelNormal {
		t.Fatalf("burst throttling must not reduce fidelity: %d %v", g.Level, g.Reasons)
	}
	// Held back 6 s of 30 (20%): the work doesn't fit the budget.
	now = now.Add(30 * time.Second)
	u.CPUUsageUs, u.Throttled, u.ThrottledUs = 3_000_000, 300, 6_600_000
	if !g.Check(now, u, HostPressure{}, 0) || g.Level != LevelReduced || !strings.Contains(g.Reasons[0], "20% of the time") {
		t.Fatalf("sustained throttling reduces fidelity: %d %v", g.Level, g.Reasons)
	}
}

func TestReadUsage(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "proc/self"), 0o755)
	os.WriteFile(filepath.Join(root, "proc/self/cgroup"), []byte("0::/system.slice/drool-agent.service\n"), 0o644)
	cg := filepath.Join(root, "sys/fs/cgroup/system.slice/drool-agent.service")
	os.MkdirAll(cg, 0o755)
	for f, v := range map[string]string{"cpu.max": "25000 100000", "cpu.weight": "20", "memory.high": "100663296", "memory.max": "167772160",
		"memory.current": "20000000", "memory.peak": "31000000", "cpu.stat": "usage_usec 123\nnr_periods 9\nnr_throttled 2\nthrottled_usec 4500\n",
		"memory.events": "low 0\nhigh 1\nmax 0\noom 0\noom_kill 0\n", "cgroup.procs": "1\n"} {
		os.WriteFile(filepath.Join(cg, f), []byte(v+"\n"), 0o644)
	}
	u := ReadUsage(root)
	if !u.Enforced || *u.CPUQuotaPct != 25 || *u.CPUWeight != 20 || *u.MemHigh != 96<<20 || *u.MemMax != 160<<20 ||
		u.Throttled != 2 || u.ThrottledUs != 4500 || u.HighEvents != 1 || *u.MemPeak != 31000000 {
		t.Fatalf("%+v", u)
	}
	os.WriteFile(filepath.Join(cg, "cpu.max"), []byte("max 100000\n"), 0o644)
	os.WriteFile(filepath.Join(cg, "memory.max"), []byte("max\n"), 0o644)
	if u := ReadUsage(root); u.Enforced || u.Note == "" || u.CPUQuotaPct != nil {
		t.Fatalf("unlimited must not look enforced: %+v", u)
	}
}

func TestProbe(t *testing.T) {
	mount := t.TempDir()
	p := NewProber(t.TempDir()) // the agent's state dir: maybe the same filesystem
	p.StateDir = ""             // use <mount>/.drool-agent-probe
	r := p.Probe(mount, time.Now())
	if !r.OK || r.Location != filepath.Join(mount, ".drool-agent-probe") {
		t.Fatalf("%+v", r)
	}
	fi, _ := os.Stat(filepath.Join(mount, ".drool-agent-probe", "probe"))
	if fi.Size() != ProbeSize {
		t.Fatalf("the probe is tiny: %d bytes", fi.Size())
	}
	first, _ := os.ReadFile(filepath.Join(mount, ".drool-agent-probe", "probe"))
	if r := p.Probe(mount, time.Now()); !r.OK {
		t.Fatal(r)
	}
	second, _ := os.ReadFile(filepath.Join(mount, ".drool-agent-probe", "probe"))
	if string(first) == string(second) {
		t.Fatal("each probe writes a new unpredictable value")
	}
	entries, _ := os.ReadDir(mount)
	if len(entries) != 1 {
		t.Fatalf("the probe reuses its one file: %v", entries)
	}

	t.Run("fsync failure", func(t *testing.T) {
		q := NewProber("")
		q.ops.sync = func(*os.File) error { return errors.New("EIO") }
		if r := q.Probe(mount, time.Now()); r.OK || r.Stage != "sync" {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("readback mismatch", func(t *testing.T) {
		q := NewProber("")
		q.ops.read = func(string) ([]byte, bool, error) { return make([]byte, ProbeSize), true, nil }
		if r := q.Probe(mount, time.Now()); r.OK || r.Stage != "verify" {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("write failure", func(t *testing.T) {
		q := NewProber("")
		q.ops.write = func(*os.File, []byte) error { return errors.New("EROFS") }
		if r := q.Probe(mount, time.Now()); r.OK || r.Stage != "write" {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("hung device", func(t *testing.T) {
		q := NewProber("")
		q.Timeout = 100 * time.Millisecond
		release := make(chan struct{})
		var once sync.Once
		q.ops.sync = func(*os.File) error { <-release; return nil }
		if r := q.Probe(mount, time.Now()); r.OK || r.Stage != "timeout" {
			t.Fatalf("%+v", r)
		}
		if r := q.Probe(mount, time.Now()); r.OK || !strings.Contains(r.Error, "never finished") {
			t.Fatalf("a second probe must not pile up behind the hung one: %+v", r)
		}
		once.Do(func() { close(release) })
	})
	t.Run("never writes through a planted symlink", func(t *testing.T) {
		m := t.TempDir()
		victim := filepath.Join(t.TempDir(), "important")
		os.WriteFile(victim, []byte("user data"), 0o644)
		os.Symlink(filepath.Dir(victim), filepath.Join(m, ".drool-agent-probe"))
		if r := NewProber("").Probe(m, time.Now()); r.OK || r.Stage != "location" {
			t.Fatalf("%+v", r)
		}
		if b, _ := os.ReadFile(victim); string(b) != "user data" {
			t.Fatal("user data touched")
		}
	})
	t.Run("refuses a directory it doesn't own", func(t *testing.T) {
		m := t.TempDir()
		os.Mkdir(filepath.Join(m, ".drool-agent-probe"), 0o755)
		if r := NewProber("").Probe(m, time.Now()); r.OK || r.Stage != "location" {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("probe file replaced by a symlink", func(t *testing.T) {
		m := t.TempDir()
		q := NewProber("")
		if r := q.Probe(m, time.Now()); !r.OK {
			t.Fatal(r)
		}
		victim := filepath.Join(t.TempDir(), "important")
		os.WriteFile(victim, []byte("user data"), 0o644)
		pf := filepath.Join(m, ".drool-agent-probe", "probe")
		os.Remove(pf)
		os.Symlink(victim, pf)
		if r := q.Probe(m, time.Now()); r.OK {
			t.Fatal("followed a symlink")
		}
		if b, _ := os.ReadFile(victim); string(b) != "user data" {
			t.Fatal("user data touched")
		}
	})
}

// fixtureHost is a minimal machine for the engine.
func fixtureHost(t *testing.T) (string, string) {
	root, bin := t.TempDir(), t.TempDir()
	w := func(p, s string) {
		os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte(s), 0o644)
	}
	w("/proc/stat", "cpu  100 0 50 800 50 0 0 0 0 0\ncpu0 100 0 50 800 50 0 0 0 0 0\nbtime 1700000000\n")
	w("/proc/meminfo", "MemTotal: 4000000 kB\nMemAvailable: 3000000 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n")
	w("/proc/loadavg", "0.10 0.20 0.30 1/100 42\n")
	w("/proc/sys/kernel/random/boot_id", "boot-1\n")
	w("/proc/self/mountinfo", "22 1 8:1 / / rw,relatime - ext4 /dev/sda1 rw\n")
	os.WriteFile(filepath.Join(bin, "systemctl"), []byte(`#!/bin/sh
case "$1" in
show) printf 'Id=app.service\nLoadState=loaded\nActiveState=%s\nSubState=%s\nUnitFileState=enabled\nNRestarts=0\n' "$(cat `+root+`/state)" "$(cat `+root+`/sub)";;
list-unit-files) echo 'app.service enabled enabled';;
esac
`), 0o755)
	os.WriteFile(filepath.Join(root, "state"), []byte("active"), 0o644)
	os.WriteFile(filepath.Join(root, "sub"), []byte("running"), 0o644)
	return root, bin
}

func TestEngineProducesPointsStateAndServiceEvents(t *testing.T) {
	root, bin := fixtureHost(t)
	var mu sync.Mutex
	clock := time.Unix(1_700_000_000, 0)
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	e := New(Options{Source: &source.Local{Root: root, Path: bin + ":" + os.Getenv("PATH")}, Now: now, KmsgPath: "-", Tick: 5 * time.Millisecond,
		CgroupRoot: root})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	advance := func(d time.Duration) {
		for i := time.Duration(0); i < d; i += 10 * time.Second {
			mu.Lock()
			clock = clock.Add(10 * time.Second)
			mu.Unlock()
			time.Sleep(15 * time.Millisecond)
		}
	}
	advance(90 * time.Second)
	os.WriteFile(filepath.Join(root, "state"), []byte("failed"), 0o644)
	os.WriteFile(filepath.Join(root, "sub"), []byte("failed"), 0o644)
	// Keep the clock moving until the engine has seen the failure and
	// closed a few minutes (robust on a busy machine).
	var b Batch
	deadline := time.Now().Add(20 * time.Second)
	sawFailure := false
	for !sawFailure || b.Metrics == nil || len(b.Metrics.Points) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("engine output: failure event %v, metrics %+v", sawFailure, b.Metrics)
		}
		advance(30 * time.Second)
		got := e.Outbox().Drain(100)
		if got.Metrics != nil {
			if b.Metrics == nil {
				b.Metrics = got.Metrics
			} else {
				b.Metrics.Points = append(b.Metrics.Points, got.Metrics.Points...)
			}
		}
		for _, ev := range got.Events {
			if ev.Kind == EventService && ev.Subject == "app.service" && strings.Contains(ev.Message, "failed") {
				sawFailure = true
			}
		}
		if got.Latest[KindState] != nil {
			b.Latest = got.Latest
		}
	}
	cancel()
	p := b.Metrics.Points[0]
	if p.S != 60 || p.T%60 != 0 || p.N < 1 {
		t.Fatalf("one-minute windows: %+v", p)
	}
	if _, ok := p.V[monitor.KeyMem]; !ok {
		t.Fatalf("memory sampled: %v", SortedKeys(p.V))
	}
	if _, ok := p.V[monitor.PrefixFS+"/"]; !ok {
		t.Fatalf("filesystem capacity sampled: %v", SortedKeys(p.V))
	}
	var st monitor.State
	if json.Unmarshal(b.Latest[KindState], &st) != nil || st.Source != "agent" || st.Fidelity.Mode != "continuous" || st.Memory == nil {
		t.Fatalf("state: %s", b.Latest[KindState])
	}
	if st.Agent == nil || st.Agent.Enforced {
		t.Fatalf("no cgroup in the fixture: limits are not enforced and say so: %+v", st.Agent)
	}
}

// The controller relies on a connected agent's service list being
// current: a unit's state or boot state (enabled/disabled) changing is
// sent at once; a stopped unit's boot state comes from the unit files,
// re-read every 10 minutes; and a requested report (proof after a change)
// re-reads them and is sent even when nothing changed.
func TestServicesReportedWhenAskedAndOnBootStateChanges(t *testing.T) {
	root, bin := fixtureHost(t)
	os.WriteFile(filepath.Join(root, "appfs"), []byte("enabled"), 0o644)
	os.WriteFile(filepath.Join(root, "idlefs"), []byte("enabled"), 0o644)
	os.WriteFile(filepath.Join(bin, "systemctl"), []byte(`#!/bin/sh
case "$1" in
show) printf 'Id=app.service\nLoadState=loaded\nActiveState=%s\nSubState=%s\nUnitFileState=%s\nNRestarts=0\n' "$(cat `+root+`/state)" "$(cat `+root+`/sub)" "$(cat `+root+`/appfs)";;
list-unit-files) echo "app.service $(cat `+root+`/appfs) enabled"; echo "idle.service $(cat `+root+`/idlefs) enabled";;
esac
`), 0o755)
	var mu sync.Mutex
	clock := time.Unix(1_700_000_000, 0)
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	e := New(Options{Source: &source.Local{Root: root, Path: bin + ":" + os.Getenv("PATH")}, Now: now, KmsgPath: "-", Tick: 5 * time.Millisecond,
		CgroupRoot: root})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	advance := func(d time.Duration) {
		for i := time.Duration(0); i < d; i += 10 * time.Second {
			mu.Lock()
			clock = clock.Add(10 * time.Second)
			mu.Unlock()
			time.Sleep(15 * time.Millisecond)
		}
	}
	// report moves the clock by up to d (d = 0: until the first report,
	// bounded in real time) and returns the boot states of the services
	// report sent meanwhile, or nil if there was none.
	report := func(d time.Duration) map[string]string {
		var got []byte
		drain := func() {
			if g := e.Outbox().Drain(1000).Latest[KindServices]; g != nil {
				got = g
			}
		}
		if d == 0 {
			for deadline := time.Now().Add(10 * time.Second); got == nil && time.Now().Before(deadline); {
				advance(10 * time.Second)
				drain()
			}
		}
		for moved := time.Duration(0); got == nil && moved < d; moved += 10 * time.Second {
			advance(10 * time.Second)
			drain()
		}
		// On a busy machine the engine may lag behind the clock: let it
		// catch up (the clock stands still meanwhile).
		for end := time.Now().Add(time.Second); got == nil && time.Now().Before(end); {
			time.Sleep(20 * time.Millisecond)
			drain()
		}
		if got == nil {
			return nil
		}
		var r struct {
			Units []struct {
				Name      string `json:"name"`
				FileState string `json:"file_state"`
			} `json:"units"`
		}
		if err := json.Unmarshal(got, &r); err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, u := range r.Units {
			out[u.Name] = u.FileState
		}
		return out
	}
	if r := report(0); r["app.service"] != "enabled" || r["idle.service"] != "enabled" {
		t.Fatalf("first report: %v", r)
	}
	if r := report(2 * time.Minute); r != nil {
		t.Fatalf("an unchanged list was re-sent: %v", r)
	}
	// A loaded unit is disabled (no state transition): sent at once.
	os.WriteFile(filepath.Join(root, "appfs"), []byte("disabled"), 0o644)
	if r := report(time.Minute); r["app.service"] != "disabled" {
		t.Fatalf("boot state change of a loaded unit: %v", r)
	}
	// A stopped (unloaded) unit is disabled: its unit file is read again
	// later; asking for a report reads it now, and the list is sent.
	os.WriteFile(filepath.Join(root, "idlefs"), []byte("disabled"), 0o644)
	if r := report(time.Minute); r != nil {
		t.Fatalf("unit files are cached between refreshes: %v", r)
	}
	e.Request("services")
	if r := report(time.Minute); r["idle.service"] != "disabled" {
		t.Fatalf("a requested report: %v", r)
	}
	e.Request("services")
	if r := report(time.Minute); r == nil {
		t.Fatal("a requested report is sent even when nothing changed")
	}
}
