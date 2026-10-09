package agentmon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime/metrics"
	"sort"
	"sync"
	"syscall"
	"time"

	"drool/internal/monitor"
	"drool/internal/monitor/collect"
	"drool/internal/monitor/services"
	"drool/internal/monitor/source"
	"drool/internal/monitor/storage"
)

// Telemetry kinds the engine produces.
const (
	KindMetrics  = "metrics"       // monitor.Batch
	KindState    = "monitor_state" // monitor.State
	KindServices = "services"      // services.Report
	KindStorage  = "storage"       // storage.Report
	KindEvents   = "events"        // []Event
)

// Options configure an engine.
type Options struct {
	Source   *source.Local
	StateDir string
	Now      func() time.Time
	Log      *slog.Logger
	// CgroupRoot redirects cgroup reads (tests); KmsgPath is /dev/kmsg
	// ("-" disables the kernel watcher).
	CgroupRoot string
	KmsgPath   string
	// HeapSoft is the internal memory budget (default 48 MB).
	HeapSoft uint64
	// Tick is the scheduler resolution (default 1s; tests shorten it).
	Tick time.Duration
	// Extra lets test builds add a collector (the runaway test).
	Extra func(ctx context.Context)
}

// Engine is the agent's monitoring runtime.
type Engine struct {
	opt    Options
	src    *source.Local
	out    *Outbox
	gov    *Governor
	prober *Prober
	log    *slog.Logger

	mu    sync.Mutex
	cfg   Config
	wake  chan string
	state monitor.State // the slow parts (filesystems, sensors, GPUs, helpers)

	// Sampling state (Run's goroutine only).
	prevRaw    *monitor.Raw
	sample     *monitor.Sample
	lastRaw    *monitor.Raw
	win        int64
	acc        map[string]*accum
	nSamples   int
	svcPrev    []services.Unit
	svcFiles   map[string]string
	svcSent    time.Time
	svcFilesAt time.Time // when unit-file states were last read
	svcForce   bool      // a report was asked for: re-read unit files, send even if unchanged
	svcNote    string
	svcSupport bool
	devs       []storage.Device
	stoJSON    []byte
	failures   map[string]int
	kernNote   string // guarded by mu
	boot       string // guarded by mu
}

type accum struct {
	sum, max float64
	n        int
}

// New makes an engine.
func New(opt Options) *Engine {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	if opt.HeapSoft == 0 {
		opt.HeapSoft = 48 << 20
	}
	if opt.Tick <= 0 {
		opt.Tick = time.Second
	}
	if opt.KmsgPath == "" {
		opt.KmsgPath = "/dev/kmsg"
	}
	e := &Engine{opt: opt, src: opt.Source, log: opt.Log, out: NewOutbox(DefaultBufferKB << 10),
		gov: &Governor{HeapSoft: opt.HeapSoft}, prober: NewProber(opt.StateDir), wake: make(chan string, 8),
		acc: map[string]*accum{}, failures: map[string]int{}, cfg: Config{Enabled: true}.WithDefaults()}
	return e
}

// Source is what the engine reads.
func (e *Engine) Source() source.Source { return e.src }

// Outbox is where the agent's sender takes telemetry from.
func (e *Engine) Outbox() *Outbox { return e.out }

// SetConfig applies a controller config.
func (e *Engine) SetConfig(c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	c = c.WithDefaults()
	e.mu.Lock()
	e.cfg = c
	e.mu.Unlock()
	e.out.SetLimit(c.BufferKB << 10)
	return nil
}

// Config returns the current config.
func (e *Engine) Config() Config {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg
}

// Request asks for an immediate collection ("services", "storage",
// "smart", "monitor_state").
func (e *Engine) Request(kind string) {
	select {
	case e.wake <- kind:
	default:
	}
}

// Governor exposes the fidelity state.
func (e *Engine) Governor() *Governor { return e.gov }

// Run samples until ctx ends.
func (e *Engine) Run(ctx context.Context) {
	if e.opt.KmsgPath != "-" {
		go e.watchKernel(ctx)
	}
	if e.opt.Extra != nil {
		go e.opt.Extra(ctx)
	}
	t := time.NewTicker(e.opt.Tick)
	defer t.Stop()
	var next = map[string]time.Time{}
	now := e.opt.Now()
	for _, k := range []string{"fast", "slow", "services", "storage", "smart", "gov", "probe"} {
		next[k] = now
	}
	next["smart"] = now.Add(20 * time.Second) // let the first state go out first
	next["probe"] = now.Add(2 * time.Minute)
	for {
		select {
		case <-ctx.Done():
			return
		case k := <-e.wake:
			switch k {
			case "services", "storage", "smart", "slow":
				next[k] = time.Time{}
				if k == "services" {
					e.svcForce = true
				}
			case KindState:
				e.flushState(e.opt.Now())
			}
		case <-t.C:
		}
		now := e.opt.Now()
		cfg := e.Config()
		if !cfg.Enabled {
			continue
		}
		due := func(k string, every time.Duration, kind string) bool {
			if now.Before(next[k]) {
				return false
			}
			next[k] = now.Add(e.gov.Scale(every, kind))
			return true
		}
		if due("gov", 30*time.Second, "") {
			e.govern(now)
		}
		if due("fast", time.Duration(cfg.SampleS)*time.Second, "fast") {
			e.fast(now)
		}
		if due("slow", time.Duration(cfg.SlowS)*time.Second, "slow") {
			e.slow(ctx, now)
		}
		if due("services", time.Duration(cfg.ServicesS)*time.Second, "services") {
			e.services(ctx, now)
		}
		smart := !now.Before(next["smart"]) && !e.gov.Defer("smart")
		if smart {
			next["smart"] = now.Add(time.Duration(cfg.SmartS) * time.Second)
			next["storage"] = now.Add(time.Duration(cfg.StorageS) * time.Second)
			e.storage(ctx, now, true)
		} else if !e.gov.Defer("storage") && due("storage", time.Duration(cfg.StorageS)*time.Second, "storage") {
			e.storage(ctx, now, false)
		}
		if len(cfg.ProbeMounts) > 0 && !e.gov.Defer("probe") && due("probe", time.Duration(cfg.ProbeS)*time.Second, "") {
			go e.probe(cfg.ProbeMounts, now, e.bootID())
		}
		// Close the minute's window.
		if w := now.Unix() / 60 * 60; e.win != 0 && w > e.win {
			e.emit(now)
			e.win = w
		} else if e.win == 0 {
			e.win = w
		}
	}
}

func (e *Engine) add(v map[string]float64) {
	for k, x := range v {
		a := e.acc[k]
		if a == nil {
			if len(e.acc) >= 512 {
				continue // bounded: a host with absurdly many series keeps the first 512
			}
			a = &accum{max: x}
			e.acc[k] = a
		}
		a.sum += x
		a.n++
		a.max = max(a.max, x)
	}
}

// fast samples CPU, memory, load, network and disk IO.
func (e *Engine) fast(now time.Time) {
	raw := monitor.ReadRaw(e.src, now.UnixMilli())
	if len(raw.CPU) == 0 {
		e.fail("system", fmt.Errorf("/proc/stat is unreadable"))
		return
	}
	e.ok("system")
	s := monitor.Rates(e.prevRaw, raw)
	e.prevRaw, e.lastRaw, e.sample = raw, raw, s
	e.mu.Lock()
	e.boot = raw.BootID
	e.mu.Unlock()
	e.add(s.V)
	e.nSamples++
}

// slow samples filesystems, sensors and GPUs.
func (e *Engine) slow(ctx context.Context, now time.Time) {
	e.mu.Lock()
	prevFS := e.state.Filesystems
	prevGPU, prevNote := e.state.GPUs, e.state.GPUNote
	e.mu.Unlock()
	fs, err := monitor.Filesystems(ctx, e.src, prevFS)
	if err != nil {
		e.fail("filesystems", err)
	} else {
		e.ok("filesystems")
	}
	sensors := monitor.Sensors(ctx, e.src)
	gpus, note := prevGPU, prevNote
	if !e.gov.Defer("gpu-helper") || !e.src.Has("nvidia-smi") {
		gpus, note = monitor.GPUs(ctx, e.src)
	}
	e.mu.Lock()
	e.state.Filesystems, e.state.Sensors, e.state.GPUs, e.state.GPUNote = nonNil(fs), nonNil(sensors), nonNil(gpus), note
	st := e.state
	e.mu.Unlock()
	e.add(collect.Values(&st))
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// emit closes a minute: one point, plus the current state.
func (e *Engine) emit(now time.Time) {
	if len(e.acc) > 0 {
		p := monitor.Point{T: e.win, S: 60, N: e.nSamples, V: map[string][2]float64{}}
		for k, a := range e.acc {
			p.V[k] = [2]float64{round(a.sum / float64(a.n)), round(a.max)}
		}
		e.out.AddPoint(p)
	}
	e.acc, e.nSamples = map[string]*accum{}, 0
	e.flushState(now)
}

func round(v float64) float64 {
	if v > 1000 || v < -1000 {
		return float64(int64(v))
	}
	return float64(int64(v*100)) / 100
}

// flushState queues the current picture (replacing an unsent one).
func (e *Engine) flushState(now time.Time) {
	b, _ := json.Marshal(e.State(now))
	e.out.SetLatest(KindState, b)
}

// State builds the current picture.
func (e *Engine) State(now time.Time) *monitor.State {
	e.mu.Lock()
	st := e.state
	cfg := e.cfg
	e.mu.Unlock()
	st.Source, st.At = "agent", now.UnixMilli()
	st.Fidelity = monitor.Fidelity{Mode: "continuous", SampleS: int(e.gov.Scale(time.Duration(cfg.SampleS)*time.Second, "fast") / time.Second),
		Reduced: e.gov.Level > LevelNormal, Reasons: e.gov.Reasons}
	if st.Fidelity.Reduced {
		st.Fidelity.Since = e.gov.Since.Unix()
	}
	if e.lastRaw != nil && e.sample != nil {
		collect.FillState(&st, e.lastRaw, e.sample)
	}
	st.Collectors = e.collectors()
	e.mu.Lock()
	devs := e.devs
	e.mu.Unlock()
	st.Helpers = collect.HelperStatus(e.src, devs, st.Sensors)
	u := ReadUsage(e.opt.CgroupRoot)
	used, limit, dropped := e.out.Bytes()
	ar := &monitor.AgentResources{Enforced: u.Enforced, Note: u.Note, CPUQuotaPct: u.CPUQuotaPct, CPUWeight: u.CPUWeight,
		MemoryHigh: u.MemHigh, MemoryMax: u.MemMax, MemoryBytes: u.MemCurrent, MemoryPeak: u.MemPeak, CPUPercent: e.gov.CPUPercent,
		Throttled: e.gov.HeldBack, OOMKills: u.OOMKills, BufferBytes: used, BufferLimit: limit, DroppedTotal: dropped}
	st.Agent = ar
	return &st
}

func (e *Engine) collectors() []monitor.CollectorStatus {
	var out []monitor.CollectorStatus
	names := []string{"system", "filesystems", "services", "storage", "kernel"}
	for _, n := range names {
		c := monitor.CollectorStatus{Name: n, State: monitor.CollectorOK}
		if f := e.failures[n]; f > 0 {
			c.State, c.Note = monitor.CollectorFailed, fmt.Sprintf("failed %d time(s) in a row", f)
		}
		if n == "services" && !e.svcSupport && e.svcNote != "" {
			c.State, c.Note = monitor.CollectorUnavailable, e.svcNote
		}
		if (n == "storage") && e.gov.Defer("smart") {
			c.State, c.Note = monitor.CollectorDeferred, "SMART queries deferred while the agent saves resources"
		}
		if n == "kernel" {
			e.mu.Lock()
			if e.kernNote != "" {
				c.State, c.Note = monitor.CollectorUnavailable, e.kernNote
			}
			e.mu.Unlock()
		}
		out = append(out, c)
	}
	return out
}

// fail and ok track collector health; failures are logged once, then
// every hour, never on every attempt (no routine log writes).
func (e *Engine) fail(name string, err error) {
	e.failures[name]++
	if n := e.failures[name]; n == 1 || n%360 == 0 {
		e.log.Warn("monitoring collector failing", "collector", name, "times", n, "err", err)
	}
}

func (e *Engine) ok(name string) {
	if e.failures[name] > 0 {
		e.log.Info("monitoring collector recovered", "collector", name, "after", e.failures[name])
		e.failures[name] = 0
	}
}

// bootStateChanged reports whether a unit was enabled or disabled (no
// state transition, but the controller's list must say so).
func bootStateChanged(prev, cur []services.Unit) bool {
	old := make(map[string]string, len(prev))
	for _, u := range prev {
		old[u.Name] = u.FileState
	}
	for _, u := range cur {
		if fs, ok := old[u.Name]; ok && fs != u.FileState {
			return true
		}
	}
	return false
}

// ServicesResend is how often the full service list is sent even when
// nothing changed (changes are sent at once). The controller relies on
// it: while the agent is connected, a list this old is still current.
const ServicesResend = 30 * time.Minute

// services collects service states; changes become events (kept even
// when routine telemetry is dropped) and the full list is re-sent. A
// requested collection (the controller needs proof of the current state,
// e.g. after a change) re-reads unit files and is always sent.
func (e *Engine) services(ctx context.Context, now time.Time) {
	btime := int64(0)
	if e.lastRaw != nil {
		btime = e.lastRaw.BootTime
	}
	force := e.svcForce
	e.svcForce = false
	if force || now.Sub(e.svcFilesAt) > 10*time.Minute {
		e.svcFiles = nil // re-read unit-file states every 10 minutes, and when asked
	}
	reread := e.svcFiles == nil
	rep, files, err := services.Collect(ctx, e.src, btime, e.svcFiles)
	if err != nil || rep == nil {
		e.fail("services", err)
		return
	}
	e.ok("services")
	if reread {
		e.svcFilesAt = now
	}
	e.svcFiles, e.svcNote, e.svcSupport = files, rep.Note, rep.Supported
	rep.At = now.UnixMilli()
	changes := services.Diff(e.svcPrev, rep.Units)
	if e.svcPrev != nil {
		for _, c := range changes {
			if c.FromActive == "" || c.ToActive == "" {
				continue // appeared or vanished: the full list says so
			}
			d, _ := json.Marshal(c)
			e.out.AddEvent(Event{T: now.Unix(), Kind: EventService, Subject: c.Unit, Message: c.FromActive + "/" + c.From + " → " + c.ToActive + "/" + c.To, Data: d,
				BootID: e.bootID()})
		}
	}
	if force || len(changes) > 0 || e.svcPrev == nil || bootStateChanged(e.svcPrev, rep.Units) || now.Sub(e.svcSent) > ServicesResend {
		b, _ := json.Marshal(rep)
		e.out.SetLatest(KindServices, b)
		e.svcSent = now
	}
	e.svcPrev = rep.Units
}

func (e *Engine) bootID() string {
	if e.lastRaw != nil {
		return e.lastRaw.BootID
	}
	return ""
}

// storage inventories devices; with smart, queries their health.
func (e *Engine) storage(ctx context.Context, now time.Time, smart bool) {
	rep := collect.Storage(ctx, e.src, smart, e.devs)
	rep.At = now.UnixMilli()
	e.mu.Lock()
	e.devs = rep.Devices
	e.mu.Unlock()
	e.ok("storage")
	// Only changes (or a SMART scan) go out.
	b, _ := json.Marshal(rep.Devices)
	if smart || !bytes.Equal(b, e.stoJSON) {
		e.stoJSON = b
		full, _ := json.Marshal(rep)
		e.out.SetLatest(KindStorage, full)
	}
}

func (e *Engine) probe(mounts []string, now time.Time, boot string) {
	e.mu.Lock()
	fs := e.state.Filesystems
	e.mu.Unlock()
	known := map[string]monitor.FS{}
	for _, f := range fs {
		known[f.Mount] = f
	}
	for _, m := range mounts {
		f, ok := known[m]
		var r ProbeResult
		switch {
		case !ok:
			r = ProbeResult{Mount: m, At: now.Unix(), Stage: "location", Error: "not a mounted local filesystem"}
		case f.ReadOnly:
			r = ProbeResult{Mount: m, At: now.Unix(), Stage: "write", Error: "the filesystem is mounted read-only"}
		default:
			r = e.prober.Probe(m, now)
		}
		sev, msg := "info", "write/read probe passed"
		if !r.OK {
			sev, msg = "critical", "write/read probe failed at "+r.Stage+": "+r.Error
			e.log.Warn("storage probe failed", "mount", m, "stage", r.Stage, "err", r.Error)
		}
		d, _ := json.Marshal(r)
		e.out.AddEvent(Event{T: now.Unix(), Kind: EventProbe, Subject: m, Severity: sev, Message: msg, Data: d, BootID: boot})
	}
}

// govern re-evaluates the fidelity level.
func (e *Engine) govern(now time.Time) {
	u := ReadUsage(e.opt.CgroupRoot)
	var hp HostPressure
	if e.lastRaw != nil {
		if v, ok := e.lastRaw.PSI["cpu"]; ok {
			x := v[0]
			hp.CPU10 = &x
		}
		if v, ok := e.lastRaw.PSI["memory"]; ok {
			x := v[0]
			hp.Mem10 = &x
		}
	}
	if e.gov.Check(now, u, hp, heapBytes()) {
		msg := "monitoring fidelity back to normal"
		sev := "info"
		if e.gov.Level > LevelNormal {
			msg, sev = "monitoring fidelity reduced: "+joinReasons(e.gov.Reasons), "warning"
			e.log.Warn("monitoring fidelity reduced to save resources", "level", e.gov.Level, "reasons", e.gov.Reasons)
		} else {
			e.log.Info(msg)
		}
		d, _ := json.Marshal(map[string]any{"level": e.gov.Level, "reasons": e.gov.Reasons})
		e.out.AddEvent(Event{T: now.Unix(), Kind: EventFidelity, Severity: sev, Message: msg, Data: d, BootID: e.bootID()})
		e.flushState(now)
	}
}

func joinReasons(r []string) string {
	s := ""
	for i, x := range r {
		if i > 0 {
			s += "; "
		}
		s += x
	}
	return s
}

var heapSample = []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}

func heapBytes() uint64 {
	s := []metrics.Sample{heapSample[0]}
	metrics.Read(s)
	if s[0].Value.Kind() == metrics.KindUint64 {
		return s[0].Value.Uint64()
	}
	return 0
}

// --- kernel messages ---

// MaxKernelPerMinute bounds kernel events: a failing disk can log
// thousands of errors; beyond this they are counted, not queued.
const MaxKernelPerMinute = 60

func (e *Engine) watchKernel(ctx context.Context) {
	f, err := os.Open(e.opt.KmsgPath)
	if err != nil {
		e.mu.Lock()
		e.kernNote = "kernel messages are not readable here: storage evidence comes from counters only"
		e.mu.Unlock()
		e.log.Info("kernel messages unavailable; storage evidence comes from counters only", "err", err)
		return
	}
	go func() {
		<-ctx.Done()
		f.Close()
	}()
	buf := make([]byte, 8192)
	minute, n, suppressed := int64(0), 0, 0
	for {
		k, err := f.Read(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// EPIPE: messages were overwritten before we read them; go on.
			if errors.Is(err, syscall.EPIPE) {
				continue
			}
			e.log.Warn("reading kernel messages failed", "err", err)
			return
		}
		us, msg, ok := storage.ParseKmsg(string(buf[:k]))
		if !ok {
			continue
		}
		ev, ok := storage.Event(us, msg)
		if !ok {
			continue
		}
		now := e.opt.Now()
		if m := now.Unix() / 60; m != minute {
			if suppressed > 0 {
				e.out.AddEvent(Event{T: now.Unix(), Kind: EventKernel, Severity: "warning",
					Message: fmt.Sprintf("%d more kernel storage messages in the last minute (not sent individually)", suppressed)})
			}
			minute, n, suppressed = m, 0, 0
		}
		if n >= MaxKernelPerMinute {
			suppressed++
			continue
		}
		n++
		e.mu.Lock()
		devs, boot := e.devs, e.boot
		e.mu.Unlock()
		ev.Disk = storage.DiskFor(ev.Device, devs, e.src.Link)
		d, _ := json.Marshal(ev)
		e.out.AddEvent(Event{T: now.Unix(), Kind: EventKernel, Subject: ev.Disk, Severity: "warning", Message: ev.Text, Data: d, BootID: boot})
	}
}

// SortedKeys is a test helper.
func SortedKeys(m map[string][2]float64) []string {
	var k []string
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}
