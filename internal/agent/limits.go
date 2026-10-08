package agent

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"runtime/debug"
	"runtime/metrics"
	"strings"
	"time"

	"drool/internal/hostexec"
	"drool/internal/monitor/agentmon"
)

// The agent's resource budget (M11): the managed workload wins.
//
// drool-agent.service carries a low CPU weight, a hard CPU quota, a
// memory high-water mark (reclaim and throttling start there) and a hard
// memory ceiling. The agent re-applies the controller's chosen budget at
// runtime (systemctl set-property --runtime: nothing is written to disk),
// and sets Go's soft memory limit below the high-water mark so the
// garbage collector works harder before the kernel has to.
//
// Operations are not monitoring: package installs, scripts, firewall
// transactions and service changes run in a transient scope outside the
// agent's budget (systemd-run --scope), exactly as fast as before M11.
// A capped agent must never OOM-kill an apt upgrade.

// opScope is the command prefix for operations, set when systemd scopes
// work on this host.
var opScope = []string{"systemd-run", "--scope", "--quiet", "--collect", "--"}

// bgScope runs background package checks at low priority.
var bgScope = []string{"systemd-run", "--scope", "--quiet", "--collect", "-p", "CPUWeight=20", "-p", "IOWeight=20", "--"}

// underSystemd reports whether this process is drool-agent.service.
func underSystemd() bool {
	if os.Getuid() != 0 {
		return false
	}
	if fi, err := os.Stat("/run/systemd/system"); err != nil || !fi.IsDir() {
		return false
	}
	b, err := os.ReadFile("/proc/self/cgroup")
	return err == nil && strings.Contains(string(b), "/drool-agent.service")
}

// setupRunners picks the runners: operations and background checks in
// their own scopes when systemd can make them, monitoring in the agent.
func (a *Agent) setupRunners() {
	if a.opt.Runner != nil {
		a.run, a.bgRun, a.monRun = a.opt.Runner, a.opt.Runner, a.opt.Runner
		return
	}
	a.monRun = hostexec.Local{}
	a.run, a.bgRun = hostexec.Local{}, hostexec.Local{}
	if !underSystemd() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "systemd-run", "--scope", "--quiet", "--collect", "--", "/bin/true").CombinedOutput(); err != nil {
		a.log.Warn("operations will run inside the agent's resource budget: systemd-run --scope failed", "err", err, "out", strings.TrimSpace(string(out)))
		return
	}
	a.run, a.bgRun = hostexec.Local{Wrap: opScope}, hostexec.Local{Wrap: bgScope}
}

// applyLimits sets the agent's systemd resource properties when they
// differ from what's in effect.
func (a *Agent) applyLimits(l *agentmon.Limits) {
	e := l.Effective()
	high := int64(e.MemoryHighMB) << 20
	// Go's soft limit: three quarters of the high-water mark.
	debug.SetMemoryLimit(high * 3 / 4)
	if !underSystemd() {
		return
	}
	u := agentmon.ReadUsage("")
	same := u.CPUQuotaPct != nil && math.Abs(*u.CPUQuotaPct-float64(e.CPUQuotaPct)) < 0.5 &&
		u.CPUWeight != nil && *u.CPUWeight == e.CPUWeight &&
		u.MemHigh != nil && *u.MemHigh == high && u.MemMax != nil && *u.MemMax == int64(e.MemoryMaxMB)<<20
	if same {
		return
	}
	args := []string{"set-property", "--runtime", "drool-agent.service",
		fmt.Sprintf("CPUQuota=%d%%", e.CPUQuotaPct), fmt.Sprintf("CPUWeight=%d", e.CPUWeight),
		fmt.Sprintf("MemoryHigh=%dM", e.MemoryHighMB), fmt.Sprintf("MemoryMax=%dM", e.MemoryMaxMB)}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput(); err != nil {
		a.log.Warn("could not apply the agent's resource budget", "err", err, "out", strings.TrimSpace(string(out)))
		return
	}
	a.log.Info("agent resource budget applied", "cpu_quota_percent", e.CPUQuotaPct, "cpu_weight", e.CPUWeight,
		"memory_high_mb", e.MemoryHighMB, "memory_max_mb", e.MemoryMaxMB)
}

// The memory guard: memory.high throttles a cgroup's allocations instead
// of killing it, so an agent whose heap grows without bound (a leak, a
// runaway collector) would sit near its high-water mark indefinitely,
// slowed to a crawl and silent, without ever reaching MemoryMax and being
// restarted. When the live heap (what is left after a garbage
// collection) stays beyond Go's soft limit for guardChecks consecutive
// checks, the garbage collector can't help: the agent logs it and exits,
// and systemd (Restart=always) starts a fresh one. A normal agent's live
// heap is about a tenth of the soft limit; the offline buffer is at most
// a third of it. The check reads runtime metrics only and allocates
// nothing, so the kernel's throttling of allocations doesn't stall it.
const (
	guardEvery  = 10 * time.Second
	guardChecks = 12
)

// heapGuard calls trip once live stays beyond limit for n checks.
func heapGuard(ctx context.Context, every time.Duration, n int, read func() (live, limit uint64), trip func(live, limit uint64)) {
	t := time.NewTicker(every)
	defer t.Stop()
	over := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		live, limit := read()
		if limit == 0 || live <= limit {
			over = 0
			continue
		}
		if over++; over >= n {
			trip(live, limit)
			return
		}
	}
}

// startMemGuard runs the guard when systemd will restart the agent.
func (a *Agent) startMemGuard(ctx context.Context) {
	if !underSystemd() {
		return
	}
	sample := []metrics.Sample{{Name: "/gc/heap/live:bytes"}}
	read := func() (uint64, uint64) {
		metrics.Read(sample)
		limit := debug.SetMemoryLimit(-1)
		if sample[0].Value.Kind() != metrics.KindUint64 || limit <= 0 || limit == math.MaxInt64 {
			return 0, 0
		}
		return sample[0].Value.Uint64(), uint64(limit)
	}
	go heapGuard(ctx, guardEvery, guardChecks, read, func(live, limit uint64) {
		a.log.Error("the agent's memory keeps growing beyond its budget; restarting it",
			"live_heap_mb", live>>20, "soft_limit_mb", limit>>20)
		os.Exit(70)
	})
}
