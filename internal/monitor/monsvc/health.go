package monsvc

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"drool/internal/ids"
	"drool/internal/monitor"
	"drool/internal/monitor/agentmon"
	"drool/internal/monitor/services"
	"drool/internal/monitor/storage"
)

// Health checks. Each is evaluated per subject (a mount, a unit, a disk).
const (
	CheckCPU            = "cpu"
	CheckMemory         = "memory"
	CheckFilesystem     = "filesystem"
	CheckInodes         = "inodes"
	CheckFSReadOnly     = "filesystem_readonly"
	CheckDeviceReadOnly = "device_readonly"
	CheckServiceFailed  = "service_failed"
	CheckServiceStopped = "service_stopped"
	CheckInterfaceDown  = "interface_down"
	CheckTemperature    = "temperature"
	CheckStorageHealth  = "storage_health"
	CheckMediaErrors    = "media_errors"
	CheckKernelStorage  = "kernel_storage"
	CheckProbe          = "probe"
	CheckStale          = "stale"
	CheckFidelity       = "fidelity"
)

var allChecks = []string{CheckCPU, CheckMemory, CheckFilesystem, CheckInodes, CheckFSReadOnly, CheckDeviceReadOnly,
	CheckServiceFailed, CheckServiceStopped, CheckInterfaceDown, CheckTemperature, CheckStorageHealth, CheckMediaErrors,
	CheckKernelStorage, CheckProbe, CheckStale, CheckFidelity}

// AllChecks lists the check ids.
func AllChecks() []string { return append([]string(nil), allChecks...) }

// KnownCheck reports whether a check id exists.
func KnownCheck(c string) bool {
	for _, x := range allChecks {
		if x == c {
			return true
		}
	}
	return false
}

// Decay periods for evidence-based issues (no "recovered" signal exists
// for a kernel error: it simply stops happening).
const (
	kernelQuiet = 24 * time.Hour
	mediaQuiet  = 7 * 24 * time.Hour
	resetWindow = time.Hour
	resetCount  = 3
)

// cond tracks one condition with duration and hysteresis.
type cond struct {
	since, clearSince, lastAt time.Time
	firing                    bool
	hits                      int
}

type hostEval struct {
	conds      map[string]*cond
	boot       string
	rwSeen     map[string]bool // mount → seen read-write this boot
	devRW      map[string]bool // device → seen writable this boot
	ifUp       map[string]bool // interface → seen up this boot
	kernelLast map[string]time.Time
	mediaLast  map[string]time.Time
	resets     map[string][]time.Time
}

type issueRef struct {
	id, severity, title string
}

type evaluator struct {
	s      *Service
	mu     sync.Mutex
	hosts  map[string]*hostEval
	active map[string]map[string]issueRef // host → check|subject
}

func newEvaluator(s *Service) *evaluator {
	return &evaluator{s: s, hosts: map[string]*hostEval{}, active: map[string]map[string]issueRef{}}
}

func (e *evaluator) host(id string) *hostEval {
	h := e.hosts[id]
	if h == nil {
		h = &hostEval{conds: map[string]*cond{}, rwSeen: map[string]bool{}, devRW: map[string]bool{}, ifUp: map[string]bool{},
			kernelLast: map[string]time.Time{}, mediaLast: map[string]time.Time{}, resets: map[string][]time.Time{}}
		e.hosts[id] = h
	}
	return h
}

func ikey(check, subject string) string { return check + "|" + subject }

// load reads the active issues after a restart.
func (e *evaluator) load(ctx context.Context) {
	rows, err := e.s.d.Store.Reader.QueryContext(ctx, `SELECT id, host_id, check_id, subject, severity, title, updated_at FROM mon_issues WHERE state = 'active'`)
	if err != nil {
		return
	}
	defer rows.Close()
	e.mu.Lock()
	defer e.mu.Unlock()
	for rows.Next() {
		var id, h, c, sub, sev, title string
		var at int64
		if rows.Scan(&id, &h, &c, &sub, &sev, &title, &at) != nil {
			continue
		}
		if e.active[h] == nil {
			e.active[h] = map[string]issueRef{}
		}
		e.active[h][ikey(c, sub)] = issueRef{id: id, severity: sev, title: title}
		he := e.host(h)
		// Evidence issues keep decaying from when they were last updated.
		switch c {
		case CheckKernelStorage:
			he.kernelLast[sub] = time.UnixMilli(at)
		case CheckMediaErrors:
			he.mediaLast[sub] = time.UnixMilli(at)
		}
		he.conds[ikey(c, sub)] = &cond{firing: true}
	}
}

// track advances a condition: holds for at least dur fires it; it clears
// only after clears held for clearDur. Between the two it stays as is.
func track(c *cond, holds, clears bool, start, end time.Time, maxGap, dur, clearDur time.Duration) (fire, resolve bool) {
	if !c.lastAt.IsZero() && start.Sub(c.lastAt) > maxGap {
		c.since, c.clearSince = time.Time{}, time.Time{} // a gap in the data restarts the clock
	}
	c.lastAt = end
	switch {
	case holds:
		c.clearSince = time.Time{}
		if c.since.IsZero() {
			c.since = start
		}
		if !c.firing && end.Sub(c.since) >= dur {
			c.firing = true
			return true, false
		}
	case clears:
		c.since = time.Time{}
		if c.firing {
			if c.clearSince.IsZero() {
				c.clearSince = start
			}
			if end.Sub(c.clearSince) >= clearDur {
				c.firing = false
				return false, true
			}
		}
	default:
		c.since, c.clearSince = time.Time{}, time.Time{}
	}
	return false, false
}

func (h *hostEval) cond(key string) *cond {
	c := h.conds[key]
	if c == nil {
		c = &cond{}
		h.conds[key] = c
	}
	return c
}

// --- issue persistence ---

func (e *evaluator) open(ctx context.Context, hostID, check, subject, sev, title, detail string, value *float64) {
	e.mu.Lock()
	ref, ok := e.active[hostID][ikey(check, subject)]
	e.mu.Unlock()
	now := e.s.now().UnixMilli()
	db := e.s.d.Store.Writer
	if ok {
		if ref.severity == sev && ref.title == title {
			return
		}
		db.ExecContext(ctx, `UPDATE mon_issues SET severity = ?, title = ?, detail = ?, value = ?, updated_at = ? WHERE id = ?`,
			sev, trunc(title, 300), trunc(detail, 1000), value, now, ref.id)
	} else {
		ref.id = ids.New(ids.MonIssue)
		if _, err := db.ExecContext(ctx, `INSERT INTO mon_issues (id, host_id, check_id, subject, severity, state, title, detail, value, started_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 'active', ?, ?, ?, ?, ?)`, ref.id, hostID, check, trunc(subject, 255), sev, trunc(title, 300), trunc(detail, 1000), value, now, now); err != nil {
			e.s.log.Warn("record health issue", "host", hostID, "check", check, "err", err)
			return
		}
	}
	ref.severity, ref.title = sev, title
	e.mu.Lock()
	if e.active[hostID] == nil {
		e.active[hostID] = map[string]issueRef{}
	}
	e.active[hostID][ikey(check, subject)] = ref
	e.mu.Unlock()
}

// touch refreshes an evidence issue's time (it decays from there).
func (e *evaluator) touch(ctx context.Context, hostID, check, subject string) {
	e.mu.Lock()
	ref, ok := e.active[hostID][ikey(check, subject)]
	e.mu.Unlock()
	if ok {
		e.s.d.Store.Writer.ExecContext(ctx, `UPDATE mon_issues SET updated_at = ? WHERE id = ?`, e.s.now().UnixMilli(), ref.id)
	}
}

func (e *evaluator) resolve(ctx context.Context, hostID, check, subject string) {
	e.mu.Lock()
	ref, ok := e.active[hostID][ikey(check, subject)]
	if ok {
		delete(e.active[hostID], ikey(check, subject))
	}
	e.mu.Unlock()
	if !ok {
		return
	}
	now := e.s.now().UnixMilli()
	e.s.d.Store.Writer.ExecContext(ctx, `UPDATE mon_issues SET state = 'resolved', resolved_at = ?, updated_at = ? WHERE id = ?`, now, now, ref.id)
}

func (e *evaluator) activeKeys(hostID, check string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for k := range e.active[hostID] {
		if c, sub, _ := strings.Cut(k, "|"); c == check {
			out = append(out, sub)
		}
	}
	return out
}

func (e *evaluator) settings(ctx context.Context, hostID string) (Thresholds, HostSettings) {
	return e.s.Settings(ctx).Thresholds(), e.s.HostSettings(ctx, hostID)
}

func mins(n int) time.Duration { return time.Duration(n) * time.Minute }

func f64(v float64) *float64 { return &v }

// --- points: CPU and memory ---

func (e *evaluator) onPoints(ctx context.Context, hostID string, ps []monitor.Point) {
	t, hs := e.settings(ctx, hostID)
	type outcome struct {
		check, sev, title, detail string
		fire, resolve             bool
		v                         float64
	}
	var outs []outcome
	e.mu.Lock()
	h := e.host(hostID)
	for _, p := range ps {
		start, end := time.Unix(p.T, 0), time.Unix(p.T+int64(p.S), 0)
		gap := 2*time.Duration(p.S)*time.Second + time.Minute
		if v, ok := p.V[monitor.KeyCPU]; ok && !hs.Disabled(CheckCPU) {
			c := h.cond(ikey(CheckCPU, ""))
			fire, res := track(c, v[0] >= t.CPUPercent, v[0] < t.CPUPercent-10, start, end, gap, mins(t.CPUMinutes), 3*time.Minute)
			outs = append(outs, outcome{CheckCPU, "warning", fmt.Sprintf("CPU at %.0f%% or more for %d minutes", t.CPUPercent, t.CPUMinutes),
				"Sustained CPU saturation; momentary spikes are ignored.", fire, res, v[0]})
		}
		if v, ok := p.V[monitor.KeyMem]; ok && !hs.Disabled(CheckMemory) {
			c := h.cond(ikey(CheckMemory, ""))
			fire, res := track(c, v[0] >= t.MemoryPercent, v[0] < t.MemoryPercent-5, start, end, gap, mins(t.MemoryMinutes), 3*time.Minute)
			sev, title := "warning", fmt.Sprintf("Memory over %.0f%% in use for %d minutes", t.MemoryPercent, t.MemoryMinutes)
			if v[0] >= t.MemoryCritical {
				sev, title = "critical", fmt.Sprintf("Memory over %.0f%% in use: the host is close to running out", t.MemoryCritical)
			}
			if c.firing && !fire {
				fire = true // severity may have changed: open() writes only real changes
			}
			outs = append(outs, outcome{CheckMemory, sev, title, "Used memory excludes reclaimable cache (MemAvailable).", fire, res, v[0]})
		}
	}
	e.mu.Unlock()
	for _, o := range outs {
		switch {
		case o.fire:
			e.open(ctx, hostID, o.check, "", o.sev, o.title, o.detail, f64(o.v))
		case o.resolve:
			e.resolve(ctx, hostID, o.check, "")
		}
	}
}

// --- state: filesystems, interfaces, temperatures, fidelity ---

func (e *evaluator) onState(ctx context.Context, hostID string, prev, st *monitor.State) {
	t, hs := e.settings(ctx, hostID)
	e.resolve(ctx, hostID, CheckStale, "")
	now := time.UnixMilli(st.At)
	type act struct {
		open                   bool
		check, sub, sev, title string
		detail                 string
		v                      *float64
	}
	var acts []act
	e.mu.Lock()
	h := e.host(hostID)
	if st.BootID != "" && st.BootID != h.boot {
		h.boot = st.BootID
		h.rwSeen, h.ifUp, h.devRW = map[string]bool{}, map[string]bool{}, map[string]bool{}
	}
	mounts := map[string]bool{}
	for _, f := range st.Filesystems {
		mounts[f.Mount] = true
		if f.Unresponsive {
			continue
		}
		if !hs.Disabled(CheckFilesystem) && f.TotalBytes > 0 {
			c := h.cond(ikey(CheckFilesystem, f.Mount))
			holds := f.UsedPercent >= t.DiskWarning
			if holds {
				c.hits++
			} else {
				c.hits = 0
			}
			switch {
			case holds && c.hits >= 2: // two observations in a row: not a passing spike
				c.firing = true
				sev, title := "warning", fmt.Sprintf("%s is over %.0f%% full", f.Mount, t.DiskWarning)
				if f.UsedPercent >= t.DiskCritical {
					sev, title = "critical", fmt.Sprintf("%s is over %.0f%% full", f.Mount, t.DiskCritical)
				}
				acts = append(acts, act{true, CheckFilesystem, f.Mount, sev, title, fmt.Sprintf("%s free of %s.", bytesText(f.AvailBytes), bytesText(f.TotalBytes)), f64(f.UsedPercent)})
			case c.firing && f.UsedPercent < t.DiskWarning-2:
				c.firing = false
				acts = append(acts, act{open: false, check: CheckFilesystem, sub: f.Mount})
			}
		}
		if f.InodesPercent != nil && !hs.Disabled(CheckInodes) {
			c := h.cond(ikey(CheckInodes, f.Mount))
			v := *f.InodesPercent
			if v >= t.InodeWarning {
				c.hits++
			} else {
				c.hits = 0
			}
			switch {
			case c.hits >= 2:
				c.firing = true
				sev := "warning"
				if v >= t.InodeCritical {
					sev = "critical"
				}
				acts = append(acts, act{true, CheckInodes, f.Mount, sev, fmt.Sprintf("%s is running out of inodes", f.Mount), "New files can't be created when inodes run out, even with free space.", f64(v)})
			case c.firing && v < t.InodeWarning-2:
				c.firing = false
				acts = append(acts, act{open: false, check: CheckInodes, sub: f.Mount})
			}
		}
		if !hs.Disabled(CheckFSReadOnly) {
			switch {
			case !f.ReadOnly:
				h.rwSeen[f.Mount] = true
				acts = append(acts, act{open: false, check: CheckFSReadOnly, sub: f.Mount})
			case h.rwSeen[f.Mount]:
				acts = append(acts, act{true, CheckFSReadOnly, f.Mount, "critical", f.Mount + " became read-only",
					"It was writable earlier in this boot. Filesystems switch to read-only after IO errors; check the storage device.", nil})
			}
		}
	}
	for _, k := range e.activeKeysLocked(hostID, CheckFilesystem) {
		if !mounts[k] {
			acts = append(acts, act{open: false, check: CheckFilesystem, sub: k})
		}
	}
	if !hs.Disabled(CheckInterfaceDown) {
		for _, in := range st.Interfaces {
			if in.Kind != "physical" {
				continue
			}
			up := in.State == "up" || in.State == "unknown" && in.Carrier != nil && *in.Carrier
			if up {
				h.ifUp[in.Name] = true
			}
			if !h.ifUp[in.Name] {
				continue // never up this boot: unused ports aren't problems
			}
			c := h.cond(ikey(CheckInterfaceDown, in.Name))
			fire, res := track(c, !up, up, now, now, 10*time.Minute, mins(t.InterfaceMinutes), 0)
			if fire || c.firing {
				acts = append(acts, act{true, CheckInterfaceDown, in.Name, "warning", "Network interface " + in.Name + " is down",
					"It was up earlier in this boot (state now: " + in.State + ").", nil})
			} else if res || up {
				acts = append(acts, act{open: false, check: CheckInterfaceDown, sub: in.Name})
			}
		}
	}
	if !hs.Disabled(CheckTemperature) {
		for _, sn := range st.Sensors {
			if sn.High == nil && sn.Critical == nil {
				continue // no hardware threshold: DROOL doesn't invent one
			}
			limit := sn.High
			if limit == nil {
				limit = sn.Critical
			}
			c := h.cond(ikey(CheckTemperature, sn.ID))
			fire, res := track(c, sn.Celsius >= *limit, sn.Celsius < *limit-3, now, now, 10*time.Minute, mins(t.TempMinutes), 2*time.Minute)
			if fire || c.firing {
				sev, which := "warning", "high"
				if sn.Critical != nil && sn.Celsius >= *sn.Critical {
					sev, which = "critical", "critical"
				}
				acts = append(acts, act{true, CheckTemperature, sn.ID, sev, fmt.Sprintf("%s is above its %s temperature", sensorName(sn), which),
					fmt.Sprintf("At or above the hardware's own %s threshold (%.0f °C).", which, *limit), f64(sn.Celsius)})
			} else if res {
				acts = append(acts, act{open: false, check: CheckTemperature, sub: sn.ID})
			}
		}
	}
	if st.Fidelity.Reduced && !hs.Disabled(CheckFidelity) {
		acts = append(acts, act{true, CheckFidelity, "", "info", "Monitoring fidelity reduced to save resources",
			strings.Join(st.Fidelity.Reasons, "; "), nil})
	} else {
		acts = append(acts, act{open: false, check: CheckFidelity})
	}
	e.mu.Unlock()
	for _, a := range acts {
		if a.open {
			e.open(ctx, hostID, a.check, a.sub, a.sev, a.title, a.detail, a.v)
		} else {
			e.resolve(ctx, hostID, a.check, a.sub)
		}
	}
}

func (e *evaluator) activeKeysLocked(hostID, check string) []string {
	var out []string
	for k := range e.active[hostID] {
		if c, sub, _ := strings.Cut(k, "|"); c == check {
			out = append(out, sub)
		}
	}
	return out
}

func sensorName(s monitor.Sensor) string {
	switch s.Kind {
	case "cpu":
		return "CPU (" + s.Label + ")"
	case "storage":
		if s.Device != "" {
			return s.Device + " (" + s.Label + ")"
		}
	case "gpu":
		return "GPU (" + s.Label + ")"
	}
	return s.Chip + " " + s.Label
}

func bytesText(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTP"[exp])
}

// --- storage devices ---

func devLabel(d storage.Device) string {
	l := d.Name
	if d.Model != "" {
		l += " (" + d.Model + ")"
	}
	return l
}

func counters(d storage.Device) map[string]int64 {
	out := map[string]int64{}
	if s := d.SMART; s != nil {
		for k, v := range map[string]*int64{"reallocated sectors": s.Reallocated, "pending sectors": s.Pending,
			"offline uncorrectable sectors": s.OfflineUncorrect, "grown defects": s.GrownDefects, "uncorrected errors": s.UncorrectedErrors} {
			if v != nil {
				out[k] = *v
			}
		}
	}
	if n := d.NVMe; n != nil && n.MediaErrors != nil {
		out["media errors"] = *n.MediaErrors
	}
	return out
}

func (e *evaluator) onStorage(ctx context.Context, hostID string, prev, r *storage.Report) {
	_, hs := e.settings(ctx, hostID)
	old := map[string]storage.Device{}
	if prev != nil {
		for _, d := range prev.Devices {
			old[d.Key] = d
		}
	}
	now := e.s.now()
	seen := map[string]bool{}
	for _, d := range r.Devices {
		seen[d.Key] = true
		if !hs.Disabled(CheckStorageHealth) {
			switch d.Health.State {
			case storage.StateFailing:
				e.open(ctx, hostID, CheckStorageHealth, d.Key, "critical", devLabel(d)+": "+d.Health.Summary, d.CapabilityNote, nil)
			case storage.StateWarning:
				e.open(ctx, hostID, CheckStorageHealth, d.Key, "warning", devLabel(d)+": "+d.Health.Summary, d.CapabilityNote, nil)
			default:
				e.resolve(ctx, hostID, CheckStorageHealth, d.Key)
			}
		}
		o, had := old[d.Key]
		if had && !hs.Disabled(CheckMediaErrors) {
			var grew []string
			oc := counters(o)
			for k, v := range counters(d) {
				if p, ok := oc[k]; ok && v > p {
					grew = append(grew, fmt.Sprintf("%s %d → %d", k, p, v))
				}
			}
			if len(grew) > 0 {
				e.mu.Lock()
				e.host(hostID).mediaLast[d.Key] = now
				e.mu.Unlock()
				e.open(ctx, hostID, CheckMediaErrors, d.Key, "warning", devLabel(d)+": new media errors", strings.Join(grew, "; "), nil)
				e.touch(ctx, hostID, CheckMediaErrors, d.Key)
			}
		}
		if !hs.Disabled(CheckDeviceReadOnly) {
			e.mu.Lock()
			he := e.host(hostID)
			if !d.ReadOnly {
				he.devRW[d.Key] = true
			}
			wasRW := he.devRW[d.Key] || had && !o.ReadOnly
			e.mu.Unlock()
			switch {
			case !d.ReadOnly:
				e.resolve(ctx, hostID, CheckDeviceReadOnly, d.Key)
			case wasRW:
				e.open(ctx, hostID, CheckDeviceReadOnly, d.Key, "critical", devLabel(d)+" became read-only",
					"The kernel or the device switched it to read-only (SD cards and eMMC do this when they detect wear-out or errors).", nil)
			}
		}
	}
	for _, k := range e.activeKeys(hostID, CheckStorageHealth) {
		if !seen[k] {
			e.resolve(ctx, hostID, CheckStorageHealth, k) // the device is gone
		}
	}
}

// --- events: kernel evidence and probes ---

var kernelIssue = map[string]string{
	storage.KernIOError: "IO errors", storage.KernMedium: "medium errors", storage.KernTimeout: "command timeouts or link resets",
	storage.KernMMC: "SD/MMC controller errors", storage.KernFSError: "filesystem errors", storage.KernCRC: "interface CRC errors",
}

func (e *evaluator) onEvents(ctx context.Context, hostID string, evs []agentmon.Event, kernel []storage.KernelEvent, probes []agentmon.ProbeResult) {
	_, hs := e.settings(ctx, hostID)
	now := e.s.now()
	for _, k := range kernel {
		subject := k.Disk
		if subject == "" {
			subject = k.Device
		}
		switch k.Kind {
		case storage.KernFSReadOnly:
			if !hs.Disabled(CheckFSReadOnly) {
				e.open(ctx, hostID, CheckFSReadOnly, "dev:"+k.Device, "critical", "The kernel remounted a filesystem on "+k.Device+" read-only", k.Text, nil)
			}
		case storage.KernUSBReset, storage.KernUSBGone:
			e.mu.Lock()
			he := e.host(hostID)
			rs := append(he.resets[subject], now)
			for len(rs) > 0 && now.Sub(rs[0]) > resetWindow {
				rs = rs[1:]
			}
			if len(rs) > 20 {
				rs = rs[len(rs)-20:]
			}
			he.resets[subject] = rs
			n := len(rs)
			if n >= resetCount {
				he.kernelLast[subject] = now
			}
			e.mu.Unlock()
			if n >= resetCount && !hs.Disabled(CheckKernelStorage) {
				e.open(ctx, hostID, CheckKernelStorage, subject, "warning", fmt.Sprintf("%s: %d USB resets or disconnects in the last hour", subject, n),
					"Repeated USB resets usually mean a failing device, cable or power supply. Last: "+k.Text, nil)
				e.touch(ctx, hostID, CheckKernelStorage, subject)
			}
		default:
			what, ok := kernelIssue[k.Kind]
			if !ok || hs.Disabled(CheckKernelStorage) {
				continue
			}
			e.mu.Lock()
			e.host(hostID).kernelLast[subject] = now
			e.mu.Unlock()
			e.open(ctx, hostID, CheckKernelStorage, subject, "warning", subject+": kernel reports "+what, "Last: "+k.Text, nil)
			e.touch(ctx, hostID, CheckKernelStorage, subject)
		}
	}
	for _, p := range probes {
		if hs.Disabled(CheckProbe) {
			break
		}
		if p.OK {
			e.resolve(ctx, hostID, CheckProbe, p.Mount)
		} else {
			e.open(ctx, hostID, CheckProbe, p.Mount, "critical", "Write/read probe of "+p.Mount+" failed ("+p.Stage+")", p.Error, nil)
		}
	}
}

// --- time-based checks ---

// tick runs checks that depend on elapsed time: stale hosts, services
// that stayed failed or stopped, and evidence that decays.
func (e *evaluator) tick(ctx context.Context, now time.Time) {
	set := e.s.Settings(ctx)
	t := set.Thresholds()
	if !set.Enabled {
		return
	}
	e.s.mu.Lock()
	type hc struct {
		id, source string
		at         time.Time
	}
	var hosts []hc
	for id, c := range e.s.cur {
		if c.state != nil {
			hosts = append(hosts, hc{id, c.source, c.at})
		}
	}
	e.s.mu.Unlock()
	live := e.liveHosts(ctx)
	for _, h := range hosts {
		if !live[h.id] {
			e.s.mu.Lock()
			delete(e.s.cur, h.id)
			e.s.mu.Unlock()
			continue
		}
		hs := e.s.HostSettings(ctx, h.id)
		interval := time.Minute
		if h.source == "ssh" {
			interval = time.Duration(set.SSHPollS) * time.Second
		}
		stale := now.Sub(h.at) > time.Duration(t.StaleIntervals)*interval+time.Minute
		if stale && !hs.Disabled(CheckStale) {
			e.open(ctx, h.id, CheckStale, "", "warning", "No monitoring data since "+h.at.UTC().Format("2006-01-02 15:04")+" UTC",
				"The host is unreachable, or its agent or SSH polling stopped. Values shown are from before.", nil)
			e.markServicesUnknown(ctx, h.id, now)
		}
		if !stale {
			e.services(ctx, h.id, hs, t, now)
		}
		e.mu.Lock()
		he := e.host(h.id)
		var quiet, mediaOK []string
		for sub, at := range he.kernelLast {
			if now.Sub(at) > kernelQuiet {
				quiet = append(quiet, sub)
				delete(he.kernelLast, sub)
			}
		}
		for sub, at := range he.mediaLast {
			if now.Sub(at) > mediaQuiet {
				mediaOK = append(mediaOK, sub)
				delete(he.mediaLast, sub)
			}
		}
		e.mu.Unlock()
		for _, sub := range quiet {
			e.resolve(ctx, h.id, CheckKernelStorage, sub)
		}
		for _, sub := range mediaOK {
			e.resolve(ctx, h.id, CheckMediaErrors, sub)
		}
	}
}

func (e *evaluator) liveHosts(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	rows, err := e.s.d.Store.Reader.QueryContext(ctx, `SELECT id FROM hosts`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		rows.Scan(&id)
		out[id] = true
	}
	return out
}

// markServicesUnknown records Running → Unknown for every known unit of
// a host whose telemetry stopped (once).
func (e *evaluator) markServicesUnknown(ctx context.Context, hostID string, now time.Time) {
	e.s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO mon_service_events (host_id, unit, ts, from_state, to_state)
			SELECT host_id, unit, ?, active_state || '/' || sub_state, 'unknown' FROM mon_services
			WHERE host_id = ? AND known = 1 AND (active_state != 'inactive' OR file_state IN ('enabled','enabled-runtime'))`, now.UnixMilli(), hostID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE mon_services SET known = 0 WHERE host_id = ? AND known = 1`, hostID)
		return err
	})
}

// services opens and resolves service issues for one host.
func (e *evaluator) services(ctx context.Context, hostID string, hs HostSettings, t Thresholds, now time.Time) {
	units, err := e.s.Services(ctx, hostID)
	if err != nil {
		return
	}
	failed, stopped := map[string]bool{}, map[string]bool{}
	present := map[string]ServiceRow{}
	for _, u := range units {
		present[u.Name] = u
		if !u.Known {
			continue
		}
		su := services.Unit{Name: u.Name, Active: u.Active, Sub: u.Sub, FileState: u.FileState, Load: u.Load, Transient: u.Transient}
		since := now.Sub(time.UnixMilli(u.ChangedAt))
		watched := hs.Watched(u.Name)
		switch services.State(su) {
		case "failed":
			if (watched || services.Notable(su)) && since >= mins(t.ServiceMinutes) && !hs.Disabled(CheckServiceFailed) {
				failed[u.Name] = true
				sev := "warning"
				if watched {
					sev = "critical"
				}
				detail := "The unit failed"
				if u.Result != "" && u.Result != "success" {
					detail += " (" + u.Result + ")"
				}
				detail += "."
				e.open(ctx, hostID, CheckServiceFailed, u.Name, sev, u.Name+" failed", detail, nil)
			}
		case "stopped":
			if watched && since >= mins(t.WatchedMinutes) && !hs.Disabled(CheckServiceStopped) {
				stopped[u.Name] = true
				e.open(ctx, hostID, CheckServiceStopped, u.Name, "critical", u.Name+" is not running",
					"A watched service ("+u.Active+"/"+u.Sub+").", nil)
			}
		}
	}
	for _, w := range hs.WatchedServices {
		if _, ok := present[w]; !ok && len(units) > 0 && !hs.Disabled(CheckServiceStopped) {
			stopped[w] = true
			e.open(ctx, hostID, CheckServiceStopped, w, "warning", w+" is not installed", "It is on this host's watch list but systemd doesn't know it.", nil)
		}
	}
	for _, k := range e.activeKeys(hostID, CheckServiceFailed) {
		if !failed[k] {
			e.resolve(ctx, hostID, CheckServiceFailed, k)
		}
	}
	for _, k := range e.activeKeys(hostID, CheckServiceStopped) {
		if !stopped[k] {
			e.resolve(ctx, hostID, CheckServiceStopped, k)
		}
	}
}

// onServices re-evaluates services right after a report (failures show
// within a tick rather than waiting for it).
func (e *evaluator) onServices(ctx context.Context, hostID string) {
	set := e.s.Settings(ctx)
	e.services(ctx, hostID, e.s.HostSettings(ctx, hostID), set.Thresholds(), e.s.now())
}
