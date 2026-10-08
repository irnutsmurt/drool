package agentmon

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Usage is the agent's own cgroup: its limits and consumption.
type Usage struct {
	Enforced    bool
	Note        string
	CPUQuotaPct *float64 // of one CPU; nil = no quota
	CPUWeight   *int
	MemHigh     *int64 // nil = no limit
	MemMax      *int64
	MemCurrent  *int64
	MemPeak     *int64
	CPUUsageUs  uint64
	Throttled   uint64 // periods throttled
	ThrottledUs uint64 // time throttled
	HighEvents  int64
	OOMKills    int64
	ok          bool
}

// ReadUsage reads the agent's cgroup (v2) under root ("" for /).
func ReadUsage(root string) Usage {
	u := Usage{}
	b, err := os.ReadFile(filepath.Join(root, "/proc/self/cgroup"))
	if err != nil {
		u.Note = "cgroup information unavailable"
		return u
	}
	path := ""
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(ln, "0::") {
			path = strings.TrimPrefix(ln, "0::")
		}
	}
	if path == "" {
		u.Note = "cgroup v1 host: systemd applies the limits but DROOL can't read them back"
		return u
	}
	dir := filepath.Join(root, "/sys/fs/cgroup", path)
	read := func(f string) string {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
	if read("cgroup.procs") == "" && read("cpu.stat") == "" {
		u.Note = "the agent's cgroup is not readable"
		return u
	}
	u.ok = true
	if f := strings.Fields(read("cpu.max")); len(f) == 2 && f[0] != "max" {
		q, e1 := strconv.ParseFloat(f[0], 64)
		p, e2 := strconv.ParseFloat(f[1], 64)
		if e1 == nil && e2 == nil && p > 0 {
			v := 100 * q / p
			u.CPUQuotaPct = &v
		}
	}
	if w, err := strconv.Atoi(read("cpu.weight")); err == nil {
		u.CPUWeight = &w
	}
	num := func(s string) *int64 {
		if s == "" || s == "max" {
			return nil
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil
		}
		return &n
	}
	u.MemHigh, u.MemMax, u.MemCurrent, u.MemPeak = num(read("memory.high")), num(read("memory.max")), num(read("memory.current")), num(read("memory.peak"))
	for _, ln := range strings.Split(read("cpu.stat"), "\n") {
		f := strings.Fields(ln)
		if len(f) != 2 {
			continue
		}
		n, _ := strconv.ParseUint(f[1], 10, 64)
		switch f[0] {
		case "usage_usec":
			u.CPUUsageUs = n
		case "nr_throttled":
			u.Throttled = n
		case "throttled_usec":
			u.ThrottledUs = n
		}
	}
	for _, ln := range strings.Split(read("memory.events"), "\n") {
		f := strings.Fields(ln)
		if len(f) != 2 {
			continue
		}
		n, _ := strconv.ParseInt(f[1], 10, 64)
		switch f[0] {
		case "high":
			u.HighEvents = n
		case "oom_kill":
			u.OOMKills = n
		}
	}
	u.Enforced = u.CPUQuotaPct != nil && u.MemMax != nil
	if !u.Enforced {
		u.Note = "no CPU quota or memory limit is set on the agent's service; internal bounds still apply"
	}
	return u
}

// Levels of fidelity.
const (
	LevelNormal  = 0
	LevelReduced = 1 // slower sampling, expensive collectors deferred
	LevelMinimal = 2 // the least that keeps the host visible
)

// Governor decides the fidelity level from the agent's own consumption
// and the host's pressure. The managed workload wins: when the host is
// starved or the agent nears its budget, monitoring backs off; it
// recovers one level after a calm period.
type Governor struct {
	Level   int
	Reasons []string
	Since   time.Time

	prev     Usage
	prevAt   time.Time
	calm     time.Time
	HeapSoft uint64 // internal heap budget (bytes)
	// CPUPercent is the agent's recent CPU use (of one CPU).
	CPUPercent *float64
	// HeldBack: the CPU quota held the agent back for more than
	// ThrottledShare of the last interval.
	HeldBack bool
}

// ThrottledShare is the share of time throttled by the CPU quota that
// reduces fidelity.
const ThrottledShare = 0.10

// CalmPeriod is how long conditions must stay normal before a level is
// regained.
const CalmPeriod = 5 * time.Minute

// HostPressure is what the governor needs from the system PSI.
type HostPressure struct {
	CPU10, Mem10 *float64
}

// Check updates the level; it reports whether it changed.
func (g *Governor) Check(now time.Time, u Usage, host HostPressure, heap uint64) bool {
	var reasons []string
	severe := false
	if !g.prevAt.IsZero() && u.ok && g.prev.ok {
		dt := now.Sub(g.prevAt).Seconds()
		if dt > 0 && u.CPUUsageUs >= g.prev.CPUUsageUs {
			pct := float64(u.CPUUsageUs-g.prev.CPUUsageUs) / 1e6 / dt * 100
			g.CPUPercent = &pct
			if u.CPUQuotaPct != nil && pct > 0.7**u.CPUQuotaPct {
				reasons = append(reasons, fmt.Sprintf("the agent used %.0f%% of its %.0f%% CPU budget", pct, *u.CPUQuotaPct))
			}
		}
		// A burst held back for part of a 100 ms period is the quota doing
		// its job (one helper process on a slow CPU does that); being held
		// back for a tenth of the time means the work doesn't fit.
		g.HeldBack = dt > 0 && u.ThrottledUs > g.prev.ThrottledUs && float64(u.ThrottledUs-g.prev.ThrottledUs)/1e6/dt > ThrottledShare
		if g.HeldBack {
			reasons = append(reasons, fmt.Sprintf("the agent was held back by its CPU quota %.0f%% of the time", float64(u.ThrottledUs-g.prev.ThrottledUs)/1e4/dt))
		}
		if u.HighEvents > g.prev.HighEvents {
			reasons = append(reasons, "the agent reached its memory high-water mark")
			severe = true
		}
	}
	if u.MemCurrent != nil {
		lim := u.MemHigh
		if lim == nil {
			lim = u.MemMax
		}
		if lim != nil && float64(*u.MemCurrent) > 0.85*float64(*lim) {
			reasons = append(reasons, fmt.Sprintf("the agent uses %d MB of its %d MB memory budget", *u.MemCurrent>>20, *lim>>20))
		}
	}
	if g.HeapSoft > 0 && heap > g.HeapSoft {
		reasons = append(reasons, fmt.Sprintf("the agent's internal memory budget (%d MB) was reached", g.HeapSoft>>20))
		severe = true
	}
	if host.CPU10 != nil && *host.CPU10 > 50 {
		reasons = append(reasons, fmt.Sprintf("the host is CPU-starved (pressure %.0f%%): the workload comes first", *host.CPU10))
	}
	if host.Mem10 != nil && *host.Mem10 > 20 {
		reasons = append(reasons, fmt.Sprintf("the host is short of memory (pressure %.0f%%): the workload comes first", *host.Mem10))
		severe = true
	}
	g.prev, g.prevAt = u, now
	old := g.Level
	switch {
	case len(reasons) > 0:
		want := LevelReduced
		if severe || g.Level >= LevelReduced {
			want = LevelMinimal
		}
		if want > g.Level {
			g.Level, g.Since = want, now
		}
		g.Reasons, g.calm = reasons, time.Time{}
	case g.Level > LevelNormal:
		if g.calm.IsZero() {
			g.calm = now
		} else if now.Sub(g.calm) >= CalmPeriod {
			g.Level--
			g.calm = now
			if g.Level == LevelNormal {
				g.Reasons = nil
			}
		}
	}
	return g.Level != old
}

// Scale stretches an interval for the current level.
func (g *Governor) Scale(d time.Duration, kind string) time.Duration {
	f := map[string][3]int{"fast": {1, 3, 6}, "slow": {1, 2, 5}, "services": {1, 2, 4}, "storage": {1, 2, 6}}[kind]
	if f[0] == 0 {
		return d
	}
	return d * time.Duration(f[g.Level])
}

// Defer reports whether an expensive collector waits at this level. The
// flash probe never waits: it is one 512-byte write and read, and on a
// host that is always busy it would otherwise never run.
func (g *Governor) Defer(kind string) bool {
	switch kind {
	case "smart", "gpu-helper":
		return g.Level >= LevelReduced
	case "storage":
		return g.Level >= LevelMinimal
	}
	return false
}
