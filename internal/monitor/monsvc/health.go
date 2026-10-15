package monsvc

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	// CheckServiceUnexpected: a unit expected stopped is running.
	CheckServiceUnexpected = "service_unexpected"
	// CheckServiceExpected holds the expected state of a unit (running,
	// stopped, ignored): a setting the service checks follow, never an
	// issue of its own.
	CheckServiceExpected = "service_expected"
	CheckInterfaceDown   = "interface_down"
	CheckTemperature     = "temperature"
	CheckStorageHealth   = "storage_health"
	CheckMediaErrors     = "media_errors"
	CheckKernelStorage   = "kernel_storage"
	CheckProbe           = "probe"
	CheckStale           = "stale"
	CheckFidelity        = "fidelity"
)

var allChecks = []string{CheckCPU, CheckMemory, CheckFilesystem, CheckInodes, CheckFSReadOnly, CheckDeviceReadOnly,
	CheckServiceFailed, CheckServiceStopped, CheckServiceUnexpected, CheckInterfaceDown, CheckTemperature, CheckStorageHealth, CheckMediaErrors,
	CheckKernelStorage, CheckProbe, CheckStale, CheckFidelity, CheckGPUUtil}

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

// Why an issue ended (M12.1). Only "recovered" means the machine got
// better; the others are configuration or inventory facts, and M12 never
// reports them as a recovery.
const (
	ResRecovered    = "recovered"
	ResDisabled     = "disabled"     // the check was turned off for it
	ResReconfigured = "reconfigured" // the rule changed and the current state no longer qualifies
	ResGone         = "gone"         // the subject (a filesystem, a disk, a unit) disappeared
)

// cond tracks one condition with duration and hysteresis.
type cond struct {
	since, clearSince, lastAt time.Time
	// notBefore: the rule changed then; a condition can't have been
	// counting under it before (durations start fresh).
	notBefore time.Time
	firing    bool
	hits      int
	v         float64 // latest value (points-based checks)
	hasV      bool
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
	cfgAt      map[string]time.Time // check → when its rules last changed for this host
}

type issueRef struct {
	id, severity, title string
	started             int64
}

type evaluator struct {
	s      *Service
	mu     sync.Mutex
	hosts  map[string]*hostEval
	active map[string]map[string]issueRef // host → check|subject

	cfgMu sync.Mutex // one configuration reload (and re-evaluation) at a time
	pol   atomic.Pointer[policy]
}

func newEvaluator(s *Service) *evaluator {
	return &evaluator{s: s, hosts: map[string]*hostEval{}, active: map[string]map[string]issueRef{}}
}

func (e *evaluator) host(id string) *hostEval {
	h := e.hosts[id]
	if h == nil {
		h = &hostEval{conds: map[string]*cond{}, rwSeen: map[string]bool{}, devRW: map[string]bool{}, ifUp: map[string]bool{},
			kernelLast: map[string]time.Time{}, mediaLast: map[string]time.Time{}, resets: map[string][]time.Time{}, cfgAt: map[string]time.Time{}}
		e.hosts[id] = h
	}
	return h
}

func ikey(check, subject string) string { return check + "|" + subject }

// load reads the active issues after a restart.
func (e *evaluator) load(ctx context.Context) {
	rows, err := e.s.d.Store.Reader.QueryContext(ctx, `SELECT id, host_id, check_id, subject, severity, title, updated_at, started_at FROM mon_issues WHERE state = 'active'`)
	if err != nil {
		return
	}
	e.mu.Lock()
	for rows.Next() {
		var id, h, c, sub, sev, title string
		var at, started int64
		if rows.Scan(&id, &h, &c, &sub, &sev, &title, &at, &started) != nil {
			continue
		}
		if e.active[h] == nil {
			e.active[h] = map[string]issueRef{}
		}
		e.active[h][ikey(c, sub)] = issueRef{id: id, severity: sev, title: title, started: started}
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
	rows.Close()
	e.mu.Unlock()
	// Settings may have changed while the controller was down (or a
	// backup restored): judge every active issue against them now.
	e.pol.Store(nil)
	e.current(ctx)
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
			if c.since.Before(c.notBefore) {
				c.since = c.notBefore // not under a rule that didn't exist yet
			}
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

// IssueTransition is an issue becoming active, changing severity or title,
// or resolving. M12's alerting consumes it.
type IssueTransition struct {
	Kind         string // opened | changed | resolved
	IssueID      string
	HostID       string
	Check        string
	Subject      string
	Severity     string
	PrevSeverity string // changed: the severity before
	Title        string
	Detail       string
	Value        *float64
	ObservedAt   *int64 // when the condition was first seen, if known (before its duration was satisfied)
	StartedAt    int64  // when the issue became active
	At           int64
	// Resolution (resolved): recovered | disabled | reconfigured | gone,
	// and a sentence saying what happened.
	Resolution     string
	ResolutionNote string
}

// IssueHook runs inside the transaction that records an issue transition,
// so whatever it writes commits (or rolls back) together with the issue.
type IssueHook func(ctx context.Context, tx *sql.Tx, t IssueTransition) error

// SetIssueHook installs the transition hook (alerting).
func (s *Service) SetIssueHook(h IssueHook) { s.hook = h }

func (e *evaluator) inTx(ctx context.Context, fn func(tx *sql.Tx) error, t IssueTransition) error {
	return e.s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		if e.s.hook != nil {
			return e.s.hook(ctx, tx, t)
		}
		return nil
	})
}

func (e *evaluator) open(ctx context.Context, hostID, check, subject, sev, title, detail string, value *float64) {
	e.openAt(ctx, hostID, check, subject, sev, title, detail, value, time.Time{})
}

// openAt opens (or updates) an issue; observed is when the condition was
// first seen, when known.
func (e *evaluator) openAt(ctx context.Context, hostID, check, subject, sev, title, detail string, value *float64, observed time.Time) {
	e.mu.Lock()
	ref, ok := e.active[hostID][ikey(check, subject)]
	e.mu.Unlock()
	now := e.s.now().UnixMilli()
	title, detail, subject = trunc(title, 300), trunc(detail, 1000), trunc(subject, 255)
	t := IssueTransition{HostID: hostID, Check: check, Subject: subject, Severity: sev, Title: title, Detail: detail, Value: value, At: now}
	if ok {
		if ref.severity == sev && ref.title == title {
			return
		}
		t.Kind, t.IssueID, t.PrevSeverity, t.StartedAt = "changed", ref.id, ref.severity, ref.started
		if err := e.inTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE mon_issues SET severity = ?, title = ?, detail = ?, value = ?, updated_at = ? WHERE id = ?`,
				sev, title, detail, value, now, ref.id)
			return err
		}, t); err != nil {
			e.s.log.Warn("update health issue", "host", hostID, "check", check, "err", err)
			return
		}
	} else {
		ref.id = ids.New(ids.MonIssue)
		ref.started = now
		var obs *int64
		if !observed.IsZero() && observed.UnixMilli() < now {
			o := observed.UnixMilli()
			obs = &o
		}
		t.Kind, t.IssueID, t.StartedAt, t.ObservedAt = "opened", ref.id, now, obs
		if err := e.inTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO mon_issues (id, host_id, check_id, subject, severity, state, title, detail, value, started_at, updated_at, observed_at)
				VALUES (?, ?, ?, ?, ?, 'active', ?, ?, ?, ?, ?, ?)`, ref.id, hostID, check, subject, sev, title, detail, value, now, now, obs)
			return err
		}, t); err != nil {
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

// resolve ends an issue whose condition recovered.
func (e *evaluator) resolve(ctx context.Context, hostID, check, subject string) {
	e.resolveWhy(ctx, hostID, check, subject, ResRecovered, "")
}

// resolveWhy ends an issue for a reason.
func (e *evaluator) resolveWhy(ctx context.Context, hostID, check, subject, why, note string) {
	e.mu.Lock()
	ref, ok := e.active[hostID][ikey(check, subject)]
	if ok && why != ResRecovered {
		if c := e.host(hostID).conds[ikey(check, subject)]; c != nil {
			c.firing, c.since, c.clearSince, c.hits = false, time.Time{}, time.Time{}, 0
		}
	}
	e.mu.Unlock()
	if !ok {
		return
	}
	now := e.s.now().UnixMilli()
	note = trunc(note, 500)
	t := IssueTransition{Kind: "resolved", IssueID: ref.id, HostID: hostID, Check: check, Subject: subject, Severity: ref.severity,
		Title: ref.title, StartedAt: ref.started, At: now, Resolution: why, ResolutionNote: note}
	if err := e.inTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE mon_issues SET state = 'resolved', resolved_at = ?, updated_at = ?, resolution = ?, resolution_note = ? WHERE id = ?`,
			now, now, why, note, ref.id)
		return err
	}, t); err != nil {
		e.s.log.Warn("resolve health issue", "host", hostID, "check", check, "err", err)
		return // still active: the next evaluation tries again
	}
	e.mu.Lock()
	if cur, still := e.active[hostID][ikey(check, subject)]; still && cur.id == ref.id {
		delete(e.active[hostID], ikey(check, subject))
	}
	e.mu.Unlock()
}

func (e *evaluator) isActive(hostID, check, subject string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.active[hostID][ikey(check, subject)]
	return ok
}

func (e *evaluator) activeKeys(hostID, check string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.activeKeysLocked(hostID, check)
}

func (e *evaluator) activeKeysLocked(hostID, check string) []string {
	var out []string
	for k := range e.active[hostID] {
		if c, sub, _ := strings.Cut(k, "|"); c == check {
			out = append(out, sub)
		}
	}
	sort.Strings(out)
	return out
}

func mins(n int) time.Duration { return time.Duration(n) * time.Minute }

func f64(v float64) *float64 { return &v }

func durMin(d time.Duration) int { return int(d / time.Minute) }

// --- configuration ---

// current returns the health configuration, reloading it when something
// changed (an override, a group's priority or members, watched services).
// A change re-judges the affected hosts at once: see reconfigure.
func (e *evaluator) current(ctx context.Context) *policy {
	gen := readGen(ctx, e.s.d.Store.Reader)
	if p := e.pol.Load(); p != nil && p.gen == gen {
		return p
	}
	e.cfgMu.Lock()
	defer e.cfgMu.Unlock()
	old := e.pol.Load()
	if old != nil && old.gen == gen {
		return old
	}
	np, err := loadPolicy(ctx, e.s.d.Store.Reader)
	if err != nil {
		e.s.log.Warn("load health configuration", "err", err)
		if old != nil {
			return old
		}
		np = emptyPolicy()
		np.gen = -2 // retry next time
	}
	e.pol.Store(np)
	e.reconfigure(ctx, old, np, e.s.now())
	return np
}

// peek returns the loaded configuration without reloading (inside a
// write transaction, where a reload's re-evaluation can't run).
func (e *evaluator) peek(ctx context.Context) *policy {
	if p := e.pol.Load(); p != nil {
		return p
	}
	if p, err := loadPolicy(ctx, e.s.d.Store.Reader); err == nil {
		return p
	}
	return emptyPolicy()
}

// reconfigure applies a configuration change (old nil: after a start,
// every active issue). For each host and check whose rules changed:
//
//   - an active issue still failing under the new rule stays the same
//     issue (its severity and title follow the rule);
//   - one whose check is now disabled, or whose current value no longer
//     qualifies, is resolved as "disabled" or "reconfigured", never as a
//     recovery;
//   - conditions that were only pending start their duration again from
//     the change: nothing is backdated under a rule that didn't exist.
func (e *evaluator) reconfigure(ctx context.Context, old, np *policy, at time.Time) {
	type job struct{ host, check, sub string }
	var jobs []job
	var svc []string
	e.mu.Lock()
	hostSet := map[string]bool{}
	for h := range e.active {
		hostSet[h] = true
	}
	for h := range e.hosts {
		hostSet[h] = true
	}
	for h := range hostSet {
		changed := map[string]bool{}
		for _, c := range allChecks {
			if old == nil || old.sig(h, c) != np.sig(h, c) {
				changed[c] = true
			}
		}
		if len(changed) == 0 {
			continue
		}
		if old != nil {
			he := e.host(h)
			for c := range changed {
				he.cfgAt[c] = at
			}
			for k, c := range he.conds {
				chk, _, _ := strings.Cut(k, "|")
				if !changed[chk] {
					continue
				}
				if c.firing {
					c.clearSince = time.Time{}
				} else {
					c.since, c.clearSince, c.hits, c.notBefore = time.Time{}, time.Time{}, 0, at
				}
			}
			if changed[CheckServiceFailed] || changed[CheckServiceStopped] {
				svc = append(svc, h)
			}
		}
		for _, k := range sortedKeys(e.active[h]) {
			chk, sub, _ := strings.Cut(k, "|")
			if changed[chk] {
				jobs = append(jobs, job{h, chk, sub})
			}
		}
	}
	e.mu.Unlock()
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].host+"|"+jobs[i].check+"|"+jobs[i].sub < jobs[j].host+"|"+jobs[j].check+"|"+jobs[j].sub
	})
	for _, j := range jobs {
		e.requalify(ctx, np, j.host, j.check, j.sub)
	}
	sort.Strings(svc)
	for _, h := range svc {
		e.services(ctx, h, np, e.s.now())
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// snapshot is a host's latest reports.
func (e *evaluator) snapshot(hostID string) (*monitor.State, *storage.Report) {
	e.s.mu.Lock()
	defer e.s.mu.Unlock()
	if c := e.s.cur[hostID]; c != nil {
		return c.state, c.storage
	}
	return nil, nil
}

// subjectOf finds what an issue subject is and how overrides name it.
func (e *evaluator) subjectOf(hostID, check, sub string) (subjectCtx, *hwLimits) {
	st, rep := e.snapshot(hostID)
	switch check {
	case CheckFilesystem, CheckInodes, CheckFSReadOnly:
		if strings.HasPrefix(sub, "dev:") {
			return subjectCtx{}, nil // a kernel remount names a device, not a filesystem
		}
		return fsCtx(sub, mountUUIDs(rep)), nil
	case CheckStorageHealth, CheckMediaErrors, CheckDeviceReadOnly:
		label := sub
		if rep != nil {
			for _, d := range rep.Devices {
				if d.Key == sub {
					label = devLabel(d)
				}
			}
		}
		return subjectCtx{keys: []string{"disk:" + sub}, label: label}, nil
	case CheckTemperature:
		if st != nil {
			if strings.HasPrefix(sub, "gpu:") {
				for _, g := range st.GPUs {
					if gpuTempIssue(g) == sub {
						return subjectCtx{keys: []string{sub}, label: g.Model}, &hwLimits{}
					}
				}
				return subjectCtx{keys: []string{sub}}, &hwLimits{}
			}
			for _, sn := range st.Sensors {
				if sn.ID == sub {
					s := sensorSubject(sn, diskKeysByName(rep))
					return subjectCtx{keys: []string{s.Key}, label: s.Label}, &hwLimits{high: sn.High, crit: sn.Critical}
				}
			}
		}
		return subjectCtx{}, nil
	case CheckGPUUtil:
		label := sub
		if st != nil {
			for _, g := range st.GPUs {
				if gpuID(g) == sub {
					label = g.Model
				}
			}
		}
		return subjectCtx{keys: []string{"gpu:" + sub}, label: label}, nil
	case CheckInterfaceDown:
		return subjectCtx{keys: []string{"iface:" + sub}, label: sub}, nil
	case CheckServiceFailed, CheckServiceStopped:
		return unitCtx(sub), nil
	case CheckProbe:
		return subjectCtx{keys: []string{"mount:" + sub}, label: sub}, nil
	}
	return subjectCtx{}, nil
}

func fsCtx(mount string, uuids map[string]string) subjectCtx {
	sc := subjectCtx{label: mount}
	if u := uuids[mount]; u != "" {
		sc.keys = append(sc.keys, "fsuuid:"+u)
	}
	sc.keys = append(sc.keys, "mount:"+mount)
	return sc
}

func unitCtx(u string) subjectCtx { return subjectCtx{keys: []string{"unit:" + u}, label: u} }

func checkLabel(check string) string {
	if s, ok := Spec(check); ok {
		return s.Label
	}
	return check
}

func disabledNote(check string, sc subjectCtx, r rule) string {
	what := checkLabel(check)
	if sc.label != "" {
		what += " for " + sc.label
	}
	return what + " was turned off (" + r.enabledSource + "). This is a configuration change, not a recovery."
}

// requalify judges one active issue against a changed rule.
func (e *evaluator) requalify(ctx context.Context, pol *policy, hostID, check, sub string) {
	sc, hw := e.subjectOf(hostID, check, sub)
	r := pol.rule(hostID, check, sc, hw)
	if !r.on {
		e.resolveWhy(ctx, hostID, check, sub, ResDisabled, disabledNote(check, sc, r))
		return
	}
	noLonger := func(v float64, unit string) {
		lvl := "no level"
		if r.warn != nil {
			lvl = fmt.Sprintf("%.0f%s", *r.warn, unit)
		}
		e.resolveWhy(ctx, hostID, check, sub, ResReconfigured, fmt.Sprintf("The threshold changed to %s (%s); the current value %.0f%s no longer qualifies. This is a configuration change, not a recovery.",
			lvl, r.warnSource, v, unit))
	}
	st, rep := e.snapshot(hostID)
	switch check {
	case CheckCPU, CheckMemory, CheckGPUUtil:
		e.mu.Lock()
		c := e.host(hostID).conds[ikey(check, sub)]
		v, ok := 0.0, c != nil && c.hasV
		if ok {
			v = c.v
		}
		e.mu.Unlock()
		if !ok {
			return
		}
		if r.warn == nil || v < *r.warn {
			noLonger(v, "%")
			return
		}
		var sev, title, detail string
		switch check {
		case CheckCPU:
			sev, title, detail = cpuIssue(r)
		case CheckMemory:
			sev, title, detail = memIssue(r, v)
		default:
			model := sc.label
			sev, title, detail = gpuIssue(model, r)
		}
		e.open(ctx, hostID, check, sub, sev, title, detail, f64(v))
	case CheckFilesystem, CheckInodes:
		if st == nil {
			return
		}
		for _, f := range st.Filesystems {
			if f.Mount != sub || f.Unresponsive {
				continue
			}
			v := f.UsedPercent
			if check == CheckInodes {
				if f.InodesPercent == nil {
					return
				}
				v = *f.InodesPercent
			}
			if r.warn == nil || v < *r.warn {
				noLonger(v, "%")
				return
			}
			sev, title, detail := fsIssue(f, r)
			if check == CheckInodes {
				sev, title, detail = inodeIssue(f.Mount, r, v)
			}
			e.open(ctx, hostID, check, sub, sev, title, detail, f64(v))
		}
	case CheckTemperature:
		v, name, ok := tempValue(st, rep, sub)
		if !ok {
			return
		}
		limit := r.warn
		if limit == nil {
			limit = r.crit
		}
		if limit == nil || v < *limit {
			lvl := "no limit"
			if limit != nil {
				lvl = fmt.Sprintf("%.0f °C", *limit)
			}
			e.resolveWhy(ctx, hostID, check, sub, ResReconfigured, fmt.Sprintf("The temperature limit changed to %s (%s); the current %.0f °C no longer qualifies. This is a configuration change, not a recovery.",
				lvl, r.warnSource, v))
			return
		}
		sev, title, detail := tempIssue(name, r, v)
		e.open(ctx, hostID, check, sub, sev, title, detail, f64(v))
	case CheckStale:
		if !e.isStale(hostID, pol, e.s.now()) {
			e.resolveWhy(ctx, hostID, check, sub, ResReconfigured, fmt.Sprintf("The stale limit changed to %d missed reports (%s); the host is no longer considered stale. This is a configuration change, not a recovery.",
				r.intervals, pol.effective(hostID, CheckStale, subjectCtx{}, nil, levelAll)[FieldIntervals].Source.Label))
		}
	}
}

func tempValue(st *monitor.State, rep *storage.Report, sub string) (float64, string, bool) {
	if st == nil {
		return 0, "", false
	}
	for _, sn := range st.Sensors {
		if sn.ID == sub {
			return sn.Celsius, sensorName(sn), true
		}
	}
	for _, g := range st.GPUs {
		if gpuTempIssue(g) == sub && g.Celsius != nil {
			return *g.Celsius, "GPU " + g.Model, true
		}
	}
	return 0, "", false
}

// --- issue texts ---

func cpuIssue(r rule) (sev, title, detail string) {
	return "warning", fmt.Sprintf("CPU at %.0f%% or more for %d minutes", *r.warn, durMin(r.dur)), "Sustained CPU saturation; momentary spikes are ignored."
}

func memIssue(r rule, v float64) (sev, title, detail string) {
	sev, title = "warning", fmt.Sprintf("Memory over %.0f%% in use for %d minutes", *r.warn, durMin(r.dur))
	if r.crit != nil && v >= *r.crit {
		sev, title = "critical", fmt.Sprintf("Memory over %.0f%% in use: the host is close to running out", *r.crit)
	}
	return sev, title, "Used memory excludes reclaimable cache (MemAvailable)."
}

func fsIssue(f monitor.FS, r rule) (sev, title, detail string) {
	sev, title = "warning", fmt.Sprintf("%s is over %.0f%% full", f.Mount, *r.warn)
	if r.crit != nil && f.UsedPercent >= *r.crit {
		sev, title = "critical", fmt.Sprintf("%s is over %.0f%% full", f.Mount, *r.crit)
	}
	return sev, title, fmt.Sprintf("%s free of %s.", bytesText(f.AvailBytes), bytesText(f.TotalBytes))
}

func inodeIssue(mount string, r rule, v float64) (sev, title, detail string) {
	sev = "warning"
	if r.crit != nil && v >= *r.crit {
		sev = "critical"
	}
	return sev, fmt.Sprintf("%s is running out of inodes", mount), "New files can't be created when inodes run out, even with free space."
}

func gpuIssue(model string, r rule) (sev, title, detail string) {
	if model == "" {
		model = "GPU"
	}
	return "warning", fmt.Sprintf("%s at %.0f%% utilization or more for %d minutes", model, *r.warn, durMin(r.dur)),
		"Sustained GPU saturation. This check is off unless enabled: a busy GPU is usually working, not failing. GPU temperature is checked separately."
}

// tempIssue describes a temperature issue, saying which limit applies:
// the hardware's own, or a custom one somebody set.
func tempIssue(name string, r rule, v float64) (sev, title, detail string) {
	limit := r.warn
	if limit == nil {
		limit = r.crit
	}
	sev, which := "warning", "high"
	if r.crit != nil && v >= *r.crit {
		sev, which, limit = "critical", "critical", r.crit
	}
	if r.warnKind == "hardware" || r.warnKind == "unset" {
		return sev, fmt.Sprintf("%s is above its %s temperature", name, which),
			fmt.Sprintf("At or above the hardware's own %s threshold (%.0f °C).", which, *limit)
	}
	return sev, fmt.Sprintf("%s is above its custom %s limit (%.0f °C)", name, which, *limit),
		fmt.Sprintf("A custom limit (%s), not the hardware's own threshold.", r.warnSource)
}

// --- points: CPU, memory and GPU utilization ---

func (e *evaluator) onPoints(ctx context.Context, hostID string, ps []monitor.Point) {
	pol := e.current(ctx)
	st, _ := e.snapshot(hostID)
	gpus := map[string]monitor.GPU{}
	if st != nil {
		for _, g := range st.GPUs {
			gpus[strconv.Itoa(g.Index)] = g
		}
	}
	type outcome struct {
		check, sub, sev, title, detail string
		fire, resolve, off             bool
		v                              float64
		since                          time.Time
		r                              rule
		sc                             subjectCtx
	}
	var outs []outcome
	e.mu.Lock()
	h := e.host(hostID)
	for _, p := range ps {
		start, end := time.Unix(p.T, 0), time.Unix(p.T+int64(p.S), 0)
		gap := 2*time.Duration(p.S)*time.Second + time.Minute
		if v, ok := p.V[monitor.KeyCPU]; ok {
			r := pol.rule(hostID, CheckCPU, subjectCtx{}, nil)
			c := h.cond(ikey(CheckCPU, ""))
			c.v, c.hasV = v[0], true
			if !r.on || r.warn == nil {
				outs = append(outs, outcome{check: CheckCPU, off: true, r: r})
			} else {
				fire, res := track(c, v[0] >= *r.warn, v[0] < *r.clr, start, end, gap, r.dur, r.clearDur)
				sev, title, detail := cpuIssue(r)
				outs = append(outs, outcome{CheckCPU, "", sev, title, detail, fire, res, false, v[0], c.since, r, subjectCtx{}})
			}
		}
		if v, ok := p.V[monitor.KeyMem]; ok {
			r := pol.rule(hostID, CheckMemory, subjectCtx{}, nil)
			c := h.cond(ikey(CheckMemory, ""))
			c.v, c.hasV = v[0], true
			if !r.on || r.warn == nil {
				outs = append(outs, outcome{check: CheckMemory, off: true, r: r})
			} else {
				fire, res := track(c, v[0] >= *r.warn, v[0] < *r.clr, start, end, gap, r.dur, r.clearDur)
				sev, title, detail := memIssue(r, v[0])
				if c.firing && !fire {
					fire = true // severity may have changed: open() writes only real changes
				}
				outs = append(outs, outcome{CheckMemory, "", sev, title, detail, fire, res, false, v[0], c.since, r, subjectCtx{}})
			}
		}
		for k, v := range p.V {
			idx, ok := strings.CutPrefix(k, monitor.PrefixGPUUtil)
			if !ok {
				continue
			}
			g, ok := gpus[idx]
			if !ok {
				continue // the GPU's identity isn't known yet
			}
			sub := gpuID(g)
			sc := subjectCtx{keys: []string{"gpu:" + sub}, label: g.Model}
			r := pol.rule(hostID, CheckGPUUtil, sc, nil)
			c := h.cond(ikey(CheckGPUUtil, sub))
			c.v, c.hasV = v[0], true
			if !r.on || r.warn == nil {
				outs = append(outs, outcome{check: CheckGPUUtil, sub: sub, off: true, r: r, sc: sc})
				continue
			}
			fire, res := track(c, v[0] >= *r.warn, v[0] < *r.clr, start, end, gap, r.dur, r.clearDur)
			sev, title, detail := gpuIssue(g.Model, r)
			outs = append(outs, outcome{CheckGPUUtil, sub, sev, title, detail, fire, res, false, v[0], c.since, r, sc})
		}
	}
	e.mu.Unlock()
	for _, o := range outs {
		switch {
		case o.off:
			// Normally reconfigure resolved it already; this covers an
			// issue opened before the check could be judged.
			if e.isActive(hostID, o.check, o.sub) {
				e.resolveWhy(ctx, hostID, o.check, o.sub, ResDisabled, disabledNote(o.check, o.sc, o.r))
			}
		case o.fire:
			e.openAt(ctx, hostID, o.check, o.sub, o.sev, o.title, o.detail, f64(o.v), o.since)
		case o.resolve:
			e.resolve(ctx, hostID, o.check, o.sub)
		}
	}
}

// --- state: filesystems, interfaces, temperatures, fidelity ---

func (e *evaluator) onState(ctx context.Context, hostID string, prev, st *monitor.State) {
	pol := e.current(ctx)
	e.resolve(ctx, hostID, CheckStale, "")
	now := time.UnixMilli(st.At)
	_, rep := e.snapshot(hostID)
	uuids := mountUUIDs(rep)
	disks := diskKeysByName(rep)
	type act struct {
		open                   bool
		check, sub, sev, title string
		detail                 string
		v                      *float64
		since                  time.Time
		why, note              string // resolution reason (default: recovered)
	}
	var acts []act
	off := func(check, sub string, sc subjectCtx, r rule) {
		acts = append(acts, act{check: check, sub: sub, why: ResDisabled, note: disabledNote(check, sc, r)})
	}
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
		sc := fsCtx(f.Mount, uuids)
		if r := pol.rule(hostID, CheckFilesystem, sc, nil); !r.on || r.warn == nil {
			off(CheckFilesystem, f.Mount, sc, r)
		} else if f.TotalBytes > 0 {
			c := h.cond(ikey(CheckFilesystem, f.Mount))
			holds := f.UsedPercent >= *r.warn
			if holds {
				c.hits++
			} else {
				c.hits = 0
			}
			switch {
			case holds && c.hits >= 2: // two observations in a row: not a passing spike
				c.firing = true
				sev, title, detail := fsIssue(f, r)
				acts = append(acts, act{open: true, check: CheckFilesystem, sub: f.Mount, sev: sev, title: title, detail: detail, v: f64(f.UsedPercent)})
			case c.firing && f.UsedPercent < *r.clr:
				c.firing = false
				acts = append(acts, act{check: CheckFilesystem, sub: f.Mount})
			}
		}
		if f.InodesPercent != nil {
			if r := pol.rule(hostID, CheckInodes, sc, nil); !r.on || r.warn == nil {
				off(CheckInodes, f.Mount, sc, r)
			} else {
				c := h.cond(ikey(CheckInodes, f.Mount))
				v := *f.InodesPercent
				if v >= *r.warn {
					c.hits++
				} else {
					c.hits = 0
				}
				switch {
				case c.hits >= 2:
					c.firing = true
					sev, title, detail := inodeIssue(f.Mount, r, v)
					acts = append(acts, act{open: true, check: CheckInodes, sub: f.Mount, sev: sev, title: title, detail: detail, v: f64(v)})
				case c.firing && v < *r.clr:
					c.firing = false
					acts = append(acts, act{check: CheckInodes, sub: f.Mount})
				}
			}
		}
		if r := pol.rule(hostID, CheckFSReadOnly, sc, nil); !r.on {
			off(CheckFSReadOnly, f.Mount, sc, r)
		} else {
			switch {
			case !f.ReadOnly:
				h.rwSeen[f.Mount] = true
				acts = append(acts, act{check: CheckFSReadOnly, sub: f.Mount})
			case h.rwSeen[f.Mount]:
				acts = append(acts, act{open: true, check: CheckFSReadOnly, sub: f.Mount, sev: "critical", title: f.Mount + " became read-only",
					detail: "It was writable earlier in this boot. Filesystems switch to read-only after IO errors; check the storage device."})
			}
		}
	}
	for _, chk := range []string{CheckFilesystem, CheckInodes, CheckFSReadOnly} {
		for _, k := range e.activeKeysLocked(hostID, chk) {
			if !mounts[k] && !strings.HasPrefix(k, "dev:") {
				acts = append(acts, act{check: chk, sub: k, why: ResGone, note: k + " is no longer mounted on the host, so it can't be judged. This is not a recovery."})
			}
		}
	}
	for _, in := range st.Interfaces {
		if in.Kind != "physical" {
			continue
		}
		up := in.State == "up" || in.State == "unknown" && in.Carrier != nil && *in.Carrier
		if up {
			h.ifUp[in.Name] = true
		}
		sc := subjectCtx{keys: []string{"iface:" + in.Name}, label: in.Name}
		r := pol.rule(hostID, CheckInterfaceDown, sc, nil)
		if !r.on {
			off(CheckInterfaceDown, in.Name, sc, r)
			continue
		}
		if !h.ifUp[in.Name] {
			continue // never up this boot: unused ports aren't problems
		}
		c := h.cond(ikey(CheckInterfaceDown, in.Name))
		fire, res := track(c, !up, up, now, now, 10*time.Minute, r.dur, 0)
		if fire || c.firing {
			acts = append(acts, act{open: true, check: CheckInterfaceDown, sub: in.Name, sev: "warning", title: "Network interface " + in.Name + " is down",
				detail: "It was up earlier in this boot (state now: " + in.State + ").", since: c.since})
		} else if res || up {
			acts = append(acts, act{check: CheckInterfaceDown, sub: in.Name})
		}
	}
	temp := func(sub, name string, celsius float64, sc subjectCtx, hw *hwLimits) {
		r := pol.rule(hostID, CheckTemperature, sc, hw)
		if !r.on {
			off(CheckTemperature, sub, sc, r)
			return
		}
		limit := r.warn
		if limit == nil {
			limit = r.crit
		}
		if limit == nil {
			return // no hardware threshold and none set: DROOL doesn't invent one
		}
		clr := *limit - clearMargin[CheckTemperature]
		if r.clr != nil {
			clr = *r.clr
		}
		c := h.cond(ikey(CheckTemperature, sub))
		fire, res := track(c, celsius >= *limit, celsius < clr, now, now, 10*time.Minute, r.dur, r.clearDur)
		if fire || c.firing {
			sev, title, detail := tempIssue(name, r, celsius)
			acts = append(acts, act{open: true, check: CheckTemperature, sub: sub, sev: sev, title: title, detail: detail, v: f64(celsius), since: c.since})
		} else if res {
			acts = append(acts, act{check: CheckTemperature, sub: sub})
		}
	}
	for _, sn := range st.Sensors {
		s := sensorSubject(sn, disks)
		temp(sn.ID, sensorName(sn), sn.Celsius, subjectCtx{keys: []string{s.Key}, label: s.Label}, &hwLimits{high: sn.High, crit: sn.Critical})
	}
	for _, g := range st.GPUs {
		if g.Source == "nvidia-smi" && g.Celsius != nil {
			// No hwmon sensor and no limit in the report: judged only
			// against a custom limit somebody set for it.
			temp(gpuTempIssue(g), "GPU "+g.Model, *g.Celsius, subjectCtx{keys: []string{gpuTempIssue(g)}, label: g.Model}, &hwLimits{})
		}
	}
	if r := pol.rule(hostID, CheckFidelity, subjectCtx{}, nil); !r.on {
		off(CheckFidelity, "", subjectCtx{}, r)
	} else if st.Fidelity.Reduced {
		acts = append(acts, act{open: true, check: CheckFidelity, sev: "info", title: "Monitoring fidelity reduced to save resources",
			detail: strings.Join(st.Fidelity.Reasons, "; ")})
	} else {
		acts = append(acts, act{check: CheckFidelity})
	}
	e.mu.Unlock()
	for _, a := range acts {
		switch {
		case a.open:
			e.openAt(ctx, hostID, a.check, a.sub, a.sev, a.title, a.detail, a.v, a.since)
		case a.why != "":
			e.resolveWhy(ctx, hostID, a.check, a.sub, a.why, a.note)
		default:
			e.resolve(ctx, hostID, a.check, a.sub)
		}
	}
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
	pol := e.current(ctx)
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
		sc := subjectCtx{keys: []string{"disk:" + d.Key}, label: devLabel(d)}
		if rh := pol.rule(hostID, CheckStorageHealth, sc, nil); !rh.on {
			e.resolveWhy(ctx, hostID, CheckStorageHealth, d.Key, ResDisabled, disabledNote(CheckStorageHealth, sc, rh))
		} else {
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
		if rm := pol.rule(hostID, CheckMediaErrors, sc, nil); !rm.on {
			e.resolveWhy(ctx, hostID, CheckMediaErrors, d.Key, ResDisabled, disabledNote(CheckMediaErrors, sc, rm))
		} else if had {
			var grew []string
			oc := counters(o)
			for k, v := range counters(d) {
				if p, ok := oc[k]; ok && v > p {
					grew = append(grew, fmt.Sprintf("%s %d → %d", k, p, v))
				}
			}
			sort.Strings(grew)
			if len(grew) > 0 {
				e.mu.Lock()
				e.host(hostID).mediaLast[d.Key] = now
				e.mu.Unlock()
				e.open(ctx, hostID, CheckMediaErrors, d.Key, "warning", devLabel(d)+": new media errors", strings.Join(grew, "; "), nil)
				e.touch(ctx, hostID, CheckMediaErrors, d.Key)
			}
		}
		if rr := pol.rule(hostID, CheckDeviceReadOnly, sc, nil); !rr.on {
			e.resolveWhy(ctx, hostID, CheckDeviceReadOnly, d.Key, ResDisabled, disabledNote(CheckDeviceReadOnly, sc, rr))
		} else {
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
			e.resolveWhy(ctx, hostID, CheckStorageHealth, k, ResGone, "The device is no longer reported by the host, so its health can't be judged. This is not a recovery.")
		}
	}
}

// --- events: kernel evidence and probes ---

var kernelIssue = map[string]string{
	storage.KernIOError: "IO errors", storage.KernMedium: "medium errors", storage.KernTimeout: "command timeouts or link resets",
	storage.KernMMC: "SD/MMC controller errors", storage.KernFSError: "filesystem errors", storage.KernCRC: "interface CRC errors",
}

func (e *evaluator) onEvents(ctx context.Context, hostID string, evs []agentmon.Event, kernel []storage.KernelEvent, probes []agentmon.ProbeResult) {
	pol := e.current(ctx)
	kernelOn := pol.rule(hostID, CheckKernelStorage, subjectCtx{}, nil).on
	roOn := pol.rule(hostID, CheckFSReadOnly, subjectCtx{}, nil).on
	now := e.s.now()
	for _, k := range kernel {
		subject := k.Disk
		if subject == "" {
			subject = k.Device
		}
		switch k.Kind {
		case storage.KernFSReadOnly:
			if roOn {
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
			if n >= resetCount && kernelOn {
				e.open(ctx, hostID, CheckKernelStorage, subject, "warning", fmt.Sprintf("%s: %d USB resets or disconnects in the last hour", subject, n),
					"Repeated USB resets usually mean a failing device, cable or power supply. Last: "+k.Text, nil)
				e.touch(ctx, hostID, CheckKernelStorage, subject)
			}
		default:
			what, ok := kernelIssue[k.Kind]
			if !ok || !kernelOn {
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
		sc := subjectCtx{keys: []string{"mount:" + p.Mount}, label: p.Mount}
		r := pol.rule(hostID, CheckProbe, sc, nil)
		switch {
		case !r.on:
			e.resolveWhy(ctx, hostID, CheckProbe, p.Mount, ResDisabled, disabledNote(CheckProbe, sc, r))
		case p.OK:
			e.resolve(ctx, hostID, CheckProbe, p.Mount)
		default:
			e.open(ctx, hostID, CheckProbe, p.Mount, "critical", "Write/read probe of "+p.Mount+" failed ("+p.Stage+")", p.Error, nil)
		}
	}
}

// --- time-based checks ---

// staleAfter is how long a host may stay silent.
func staleAfter(pol *policy, hostID, source string, sshPollS int) time.Duration {
	interval := time.Minute
	if source == "ssh" {
		interval = time.Duration(sshPollS) * time.Second
	}
	n := pol.rule(hostID, CheckStale, subjectCtx{}, nil).intervals
	if n < 2 {
		n = 3
	}
	return time.Duration(n)*interval + time.Minute
}

func (e *evaluator) isStale(hostID string, pol *policy, now time.Time) bool {
	e.s.mu.Lock()
	c := e.s.cur[hostID]
	var at time.Time
	src := ""
	if c != nil {
		at, src = c.at, c.source
	}
	e.s.mu.Unlock()
	if at.IsZero() {
		return false
	}
	return now.Sub(at) > staleAfter(pol, hostID, src, e.s.Settings(context.Background()).SSHPollS)
}

// tick runs checks that depend on elapsed time: stale hosts, services
// that stayed failed or stopped, and evidence that decays.
func (e *evaluator) tick(ctx context.Context, now time.Time) {
	set := e.s.Settings(ctx)
	if !set.Enabled {
		return
	}
	pol := e.current(ctx)
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
		r := pol.rule(h.id, CheckStale, subjectCtx{}, nil)
		stale := now.Sub(h.at) > staleAfter(pol, h.id, h.source, set.SSHPollS)
		switch {
		case !r.on:
			if e.isActive(h.id, CheckStale, "") {
				e.resolveWhy(ctx, h.id, CheckStale, "", ResDisabled, disabledNote(CheckStale, subjectCtx{}, r))
			}
		case stale:
			e.openAt(ctx, h.id, CheckStale, "", "warning", "No monitoring data since "+h.at.UTC().Format("2006-01-02 15:04")+" UTC",
				"The host is unreachable, or its agent or SSH polling stopped. Values shown are from before.", nil, h.at)
		case e.isActive(h.id, CheckStale, ""):
			// New data resolves a stale issue when it arrives; still active
			// with no new data means the limit changed.
			e.resolveWhy(ctx, h.id, CheckStale, "", ResReconfigured, fmt.Sprintf("The stale limit changed to %d missed reports; the host is no longer considered stale. This is a configuration change, not a recovery.", r.intervals))
		}
		if stale {
			e.markServicesUnknown(ctx, h.id, now)
		} else {
			e.services(ctx, h.id, pol, now)
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
		if r := pol.rule(h.id, CheckKernelStorage, subjectCtx{}, nil); !r.on {
			for _, sub := range e.activeKeys(h.id, CheckKernelStorage) {
				e.resolveWhy(ctx, h.id, CheckKernelStorage, sub, ResDisabled, disabledNote(CheckKernelStorage, subjectCtx{}, r))
			}
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

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// services opens and resolves service issues for one host, following each
// unit's expected state (CheckServiceExpected, final After-M17 batch):
//
//   - running: stopped (or not installed) is a problem, failed is critical;
//   - stopped: running is a problem; stopped or failed is what was wanted;
//   - ignored: its state is shown, never an issue;
//   - automatic (no setting): a failure of a unit DROOL reports on its own
//     (one that starts at boot) is a problem; stopped is fine.
//
// A unit still failed, stopped or running when its check is turned off or
// its expected state changes ends as a configuration change; a unit that
// is gone ends as gone; only one back in its expected state has recovered.
// Expectations never change because someone started or stopped a unit.
func (e *evaluator) services(ctx context.Context, hostID string, pol *policy, now time.Time) {
	units, err := e.s.Services(ctx, hostID)
	if err != nil {
		return
	}
	e.mu.Lock()
	he := e.host(hostID)
	cfgF, cfgS, cfgU := he.cfgAt[CheckServiceFailed], he.cfgAt[CheckServiceStopped], he.cfgAt[CheckServiceUnexpected]
	e.mu.Unlock()
	failed, stopped, unexpected := map[string]bool{}, map[string]bool{}, map[string]bool{}
	whyF, whyS, whyU := map[string][2]string{}, map[string][2]string{}, map[string][2]string{}
	reconf := func(msg string) [2]string {
		return [2]string{ResReconfigured, msg + " This is a configuration change, not a recovery."}
	}
	present := map[string]ServiceRow{}
	for _, u := range units {
		present[u.Name] = u
		if !u.Known {
			continue
		}
		su := services.Unit{Name: u.Name, Active: u.Active, Sub: u.Sub, FileState: u.FileState, Load: u.Load, Transient: u.Transient}
		changed := time.UnixMilli(u.ChangedAt)
		exp, src := pol.expected(hostID, u.Name)
		from := ExpectSource(src)
		sc := unitCtx(u.Name)
		switch services.State(su) {
		case "failed":
			r := pol.rule(hostID, CheckServiceFailed, sc, nil)
			switch {
			case exp == ExpectIgnored:
				whyF[u.Name] = reconf(u.Name + " is ignored (" + from + "): its state is shown, never a health issue.")
			case exp == ExpectStopped:
				whyF[u.Name] = reconf(u.Name + " is expected to be stopped (" + from + "), and it isn't running.")
			case !r.on:
				whyF[u.Name] = [2]string{ResDisabled, disabledNote(CheckServiceFailed, sc, r)}
			case exp == ExpectAuto && !services.Notable(su):
				whyF[u.Name] = reconf(u.Name + " isn't expected to run, and isn't a unit DROOL reports on its own.")
			case e.isActive(hostID, CheckServiceFailed, u.Name) || now.Sub(later(changed, cfgF)) >= r.dur:
				failed[u.Name] = true
				sev, detail := "warning", "The unit failed"
				if exp == ExpectRunning {
					sev, detail = "critical", "The unit, expected to run ("+from+"), failed"
				}
				if u.Result != "" && u.Result != "success" {
					detail += " (" + u.Result + ")"
				}
				e.open(ctx, hostID, CheckServiceFailed, u.Name, sev, u.Name+" failed", detail+".", nil)
			}
		case "stopped":
			r := pol.rule(hostID, CheckServiceStopped, sc, nil)
			switch {
			case exp != ExpectRunning:
				whyS[u.Name] = reconf(u.Name + " is no longer expected to run (" + ExpectLabel(exp) + ", " + from + ").")
			case !r.on:
				whyS[u.Name] = [2]string{ResDisabled, disabledNote(CheckServiceStopped, sc, r)}
			case e.isActive(hostID, CheckServiceStopped, u.Name) || now.Sub(later(changed, cfgS)) >= r.dur:
				stopped[u.Name] = true
				e.openAt(ctx, hostID, CheckServiceStopped, u.Name, "critical", u.Name+" is not running",
					"Expected to run ("+from+"), but it is "+u.Active+"/"+u.Sub+".", nil, later(changed, cfgS))
			}
		case "running":
			r := pol.rule(hostID, CheckServiceUnexpected, sc, nil)
			switch {
			case exp != ExpectStopped:
				whyU[u.Name] = reconf(u.Name + " is no longer expected to be stopped (" + ExpectLabel(exp) + ", " + from + ").")
			case !r.on:
				whyU[u.Name] = [2]string{ResDisabled, disabledNote(CheckServiceUnexpected, sc, r)}
			case e.isActive(hostID, CheckServiceUnexpected, u.Name) || now.Sub(later(changed, cfgU)) >= r.dur:
				unexpected[u.Name] = true
				e.openAt(ctx, hostID, CheckServiceUnexpected, u.Name, "warning", u.Name+" is running",
					"Expected to be stopped ("+from+"), but it is "+u.Active+"/"+u.Sub+".", nil, later(changed, cfgU))
			}
		}
	}
	for _, w := range pol.expectedRunning(hostID) {
		if _, ok := present[w]; ok || len(units) == 0 {
			continue
		}
		sc := unitCtx(w)
		if r := pol.rule(hostID, CheckServiceStopped, sc, nil); !r.on {
			whyS[w] = [2]string{ResDisabled, disabledNote(CheckServiceStopped, sc, r)}
			continue
		}
		_, src := pol.expected(hostID, w)
		stopped[w] = true
		e.open(ctx, hostID, CheckServiceStopped, w, "warning", w+" is not installed", "It is expected to run ("+ExpectSource(src)+") but systemd doesn't know it.", nil)
	}
	end := func(check, k string, why map[string][2]string) {
		if w, ok := why[k]; ok {
			e.resolveWhy(ctx, hostID, check, k, w[0], w[1])
			return
		}
		if _, ok := present[k]; !ok && len(units) > 0 && check != CheckServiceStopped {
			e.resolveWhy(ctx, hostID, check, k, ResGone, k+" is no longer known to systemd on the host. This is not a recovery.")
			return
		}
		if _, ok := present[k]; !ok && len(units) > 0 && !pol.isWatched(hostID, k) {
			e.resolveWhy(ctx, hostID, check, k, ResReconfigured, k+" is no longer expected to run. This is a configuration change, not a recovery.")
			return
		}
		e.resolve(ctx, hostID, check, k)
	}
	for check, set := range map[string]map[string]bool{CheckServiceFailed: failed, CheckServiceStopped: stopped, CheckServiceUnexpected: unexpected} {
		why := map[string]map[string][2]string{CheckServiceFailed: whyF, CheckServiceStopped: whyS, CheckServiceUnexpected: whyU}[check]
		for _, k := range e.activeKeys(hostID, check) {
			if !set[k] {
				end(check, k, why)
			}
		}
	}
}

// ExpectLabel names an expected state for people.
func ExpectLabel(v float64) string {
	switch v {
	case ExpectRunning:
		return "expected running"
	case ExpectStopped:
		return "expected stopped"
	case ExpectIgnored:
		return "ignored"
	}
	return "automatic"
}

// ExpectSource says where a unit's expected state comes from, for
// sentences ("set on this host", "from pack media").
func ExpectSource(r Resolved) string {
	switch r.Source.Kind {
	case "subject", "host":
		return "set on this host"
	case "group":
		names := make([]string, len(r.Source.Groups))
		for i, g := range r.Source.Groups {
			names[i] = g.Name
		}
		if len(names) == 1 {
			return "from pack " + names[0]
		}
		return "from packs " + strings.Join(names, ", ")
	case "global":
		return "set for every host"
	}
	if r.Conflict != nil {
		return "packs disagree, so automatic"
	}
	return "no setting"
}

// onServices re-evaluates services right after a report (failures show
// within a tick rather than waiting for it).
func (e *evaluator) onServices(ctx context.Context, hostID string) {
	e.services(ctx, hostID, e.current(ctx), e.s.now())
}
