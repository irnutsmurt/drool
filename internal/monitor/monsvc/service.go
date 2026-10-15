// Package monsvc is the controller's system monitoring (M11): it stores
// what agents stream and what SSH polls collect, keeps metric history in
// downsampled tiers, records service state changes, judges storage
// health evidence, and evaluates host health with the Recommended profile
// and its sparse overrides (M12.1: global, group, host and subject scope).
// M12's alerting consumes its issue transitions (SetIssueHook); nothing
// here notifies anyone.
package monsvc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"drool/internal/agentsvc"
	"drool/internal/audit"
	"drool/internal/engine"
	"drool/internal/inventory"
	"drool/internal/monitor"
	"drool/internal/monitor/agentmon"
	"drool/internal/monitor/collect"
	"drool/internal/monitor/services"
	"drool/internal/monitor/storage"
	"drool/internal/store"
)

// Deps are the service's collaborators.
type Deps struct {
	Store     *store.Store
	Agents    *agentsvc.Service // nil when agents are disabled
	Inventory *inventory.Service
	Engine    *engine.Engine
	Audit     *audit.Writer
	Log       *slog.Logger
}

// Service is system monitoring.
type Service struct {
	d       Deps
	log     *slog.Logger
	now     func() time.Time
	layouts *layouts
	eval    *evaluator
	hook    IssueHook
	// svcHook is told after a host's service list was stored, and
	// whether any unit changed (desired state, M14).
	svcHook func(hostID string, changed bool)

	mu         sync.Mutex
	watermarks map[string]map[int]int64
	late       map[string]map[int]int64
	cur        map[string]*hostCache
	lastPrune  PruneResult
	queue      chan ingestItem
	dropped    int64
	diag       map[string][]byte // latest agent diagnostics per host (on request)
	pollRoot   string            // tests: SSH polls read a fixture machine
	testLoaded map[string]bool   // TEST MODE: hosts whose fixture events were recorded
}

// hostCache is the latest of everything for one host (bounded: one entry
// per host, each bounded by the collectors' limits).
type hostCache struct {
	state      *monitor.State
	at         time.Time // when the state was collected
	source     string
	storage    *storage.Report
	probes     map[string]agentmon.ProbeResult
	kernel     kernelSeen
	sshRaw     *monitor.Raw
	devPrev    map[string]storage.Device
	features   []string
	forceSmart bool // the next SSH poll reads storage health ("Collect now")
}

type kernelSeen struct {
	Boot   string `json:"boot"`
	LastUs int64  `json:"last_us"`
}

type ingestItem struct {
	host, kind string
	payload    []byte
}

// New creates the service.
func New(d Deps) *Service {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	s := &Service{d: d, log: d.Log, now: time.Now, layouts: newLayouts(), watermarks: map[string]map[int]int64{},
		late: map[string]map[int]int64{}, cur: map[string]*hostCache{}, queue: make(chan ingestItem, 2000), diag: map[string][]byte{},
		testLoaded: map[string]bool{}}
	s.eval = newEvaluator(s)
	return s
}

// SetPollRoot makes SSH polls read a fixture machine (tests).
func (s *Service) SetPollRoot(root string) { s.pollRoot = root }

// SetClock replaces the clock (tests).
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// Tick runs the time-based health checks once (tests; Run does it every 30 s).
func (s *Service) Tick(ctx context.Context) { s.eval.tick(ctx, s.now()) }

// Load restores state from the database (tests; Run does it at start).
func (s *Service) Load(ctx context.Context) { s.load(ctx) }

// Settings returns the fleet settings.
func (s *Service) Settings(ctx context.Context) Settings { return LoadSettings(ctx, s.d.Store.R) }

// LastPrune reports the most recent prune.
func (s *Service) LastPrune() PruneResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastPrune
}

// Run ingests, rolls up, prunes and evaluates until ctx ends.
func (s *Service) Run(ctx context.Context) {
	s.load(ctx)
	go s.ingestLoop(ctx)
	if f := os.Getenv("DROOL_TEST_MONITOR_FILE"); f != "" {
		s.log.Warn("TEST MODE: loading synthetic monitoring data; never use this in production", "file", f)
		go s.RunTestFile(ctx, f)
	}
	roll := time.NewTicker(time.Minute)
	defer roll.Stop()
	prune := time.NewTicker(10 * time.Minute)
	defer prune.Stop()
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	helpers := time.NewTicker(5 * time.Minute)
	defer helpers.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-roll.C:
			if err := s.Rollups(ctx); err != nil && ctx.Err() == nil {
				s.log.Warn("monitoring rollup", "err", err)
			}
		case <-prune.C:
			r := s.Prune(ctx)
			if r.Points+r.Events+r.ServiceEvents > 0 {
				s.log.Info("monitoring history pruned", "points", r.Points, "events", r.Events, "service_events", r.ServiceEvents, "ms", r.Millis)
			}
		case <-tick.C:
			s.eval.tick(ctx, s.now())
		case <-helpers.C:
			s.AutoInstallHelpers(ctx)
		}
	}
}

// load restores the per-host cache and active issues after a restart.
func (s *Service) load(ctx context.Context) {
	s.migrateLegacy(ctx)
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT host_id, source, updated_at, state_json, raw_json, storage_json, probes_json, kernel_json FROM mon_current`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var h, src, sj, rj, stj, pj, kj string
		var at int64
		if rows.Scan(&h, &src, &at, &sj, &rj, &stj, &pj, &kj) != nil {
			continue
		}
		c := &hostCache{source: src, at: time.UnixMilli(at), probes: map[string]agentmon.ProbeResult{}}
		var st monitor.State
		if json.Unmarshal([]byte(sj), &st) == nil {
			c.state = &st
		}
		if rj != "" {
			var r monitor.Raw
			if json.Unmarshal([]byte(rj), &r) == nil {
				c.sshRaw = &r
			}
		}
		if stj != "" {
			var r storage.Report
			if json.Unmarshal([]byte(stj), &r) == nil {
				c.storage = &r
			}
		}
		json.Unmarshal([]byte(pj), &c.probes)
		json.Unmarshal([]byte(kj), &c.kernel)
		s.cur[h] = c
	}
	s.eval.load(ctx)
}

func (s *Service) cache(hostID string) *hostCache {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.cur[hostID]
	if c == nil {
		c = &hostCache{probes: map[string]agentmon.ProbeResult{}}
		s.cur[hostID] = c
	}
	return c
}

// --- agent sessions ---

// OnSession pushes the monitoring config to a monitor-1 agent.
func (s *Service) OnSession(hostID string, features []string, effectiveJSON []byte) {
	c := s.cache(hostID)
	s.mu.Lock()
	c.features = features
	s.mu.Unlock()
	if !hasFeature(features, "monitor-1") {
		return
	}
	s.PushConfig(context.Background(), hostID)
}

func hasFeature(fs []string, f string) bool {
	for _, x := range fs {
		if x == f {
			return true
		}
	}
	return false
}

// PushConfig sends a host's agent its monitoring config (if connected).
func (s *Service) PushConfig(ctx context.Context, hostID string) bool {
	if s.d.Agents == nil {
		return false
	}
	if fs, ok := s.d.Agents.SessionFeatures(hostID); !ok || !hasFeature(fs, "monitor-1") {
		return false
	}
	cfg := s.Settings(ctx).AgentConfig(s.HostSettings(ctx, hostID))
	b, _ := json.Marshal(cfg)
	return s.d.Agents.SendMonitorConfig(hostID, b)
}

// PushAll re-sends configs after a settings change.
func (s *Service) PushAll(ctx context.Context) {
	s.mu.Lock()
	var ids []string
	for h := range s.cur {
		ids = append(ids, h)
	}
	s.mu.Unlock()
	for _, h := range ids {
		s.PushConfig(ctx, h)
	}
}

// OnTelemetry queues agent telemetry. The agent's stream never waits for
// the database: the queue is bounded and overflow is counted (the agent
// sends the current state again within a minute).
func (s *Service) OnTelemetry(hostID, kind string, payload []byte) {
	if len(payload) > 8<<20 {
		s.log.Warn("monitoring telemetry too large; dropped", "host", hostID, "kind", kind, "bytes", len(payload))
		return
	}
	select {
	case s.queue <- ingestItem{host: hostID, kind: kind, payload: payload}:
	default:
		s.mu.Lock()
		s.dropped++
		n := s.dropped
		s.mu.Unlock()
		if n == 1 || n%1000 == 0 {
			s.log.Warn("monitoring ingestion is behind; telemetry dropped", "dropped_total", n)
		}
	}
}

func (s *Service) ingestLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case it := <-s.queue:
			if err := s.ingest(ctx, it); err != nil && ctx.Err() == nil {
				s.log.Warn("store monitoring telemetry", "host", it.host, "kind", it.kind, "err", err)
			}
		}
	}
}

// Drain processes everything queued (tests).
func (s *Service) Drain(ctx context.Context) {
	for {
		select {
		case it := <-s.queue:
			if err := s.ingest(ctx, it); err != nil {
				s.log.Warn("store monitoring telemetry", "host", it.host, "kind", it.kind, "err", err)
			}
		default:
			return
		}
	}
}

func (s *Service) ingest(ctx context.Context, it ingestItem) error {
	switch it.kind {
	case agentmon.KindMetrics:
		var b monitor.Batch
		if err := json.Unmarshal(it.payload, &b); err != nil {
			return err
		}
		return s.IngestPoints(ctx, it.host, b.Points, b.Gaps)
	case agentmon.KindState:
		var st monitor.State
		if err := json.Unmarshal(it.payload, &st); err != nil {
			return err
		}
		st.Source = "agent"
		return s.IngestState(ctx, it.host, &st, nil)
	case agentmon.KindServices:
		var r services.Report
		if err := json.Unmarshal(it.payload, &r); err != nil {
			return err
		}
		return s.IngestServices(ctx, it.host, &r, false)
	case agentmon.KindStorage:
		var r storage.Report
		if err := json.Unmarshal(it.payload, &r); err != nil {
			return err
		}
		return s.IngestStorage(ctx, it.host, &r)
	case agentmon.KindEvents:
		var e struct {
			Events []agentmon.Event `json:"events"`
			Lost   int              `json:"lost"`
		}
		if err := json.Unmarshal(it.payload, &e); err != nil {
			return err
		}
		if e.Lost > 0 {
			e.Events = append(e.Events, agentmon.Event{T: s.now().Unix(), Kind: agentmon.EventGap, Severity: "warning",
				Message: "the agent's event buffer overflowed during a long outage; older events were discarded"})
		}
		return s.IngestEvents(ctx, it.host, e.Events)
	case "diagnostics":
		s.mu.Lock()
		s.diag[it.host] = it.payload
		s.mu.Unlock()
	}
	return nil
}

// Diagnostics returns the agent's last diagnostic ring (nil if none).
func (s *Service) Diagnostics(hostID string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.diag[hostID]
}

// --- ingestion (both paths) ---

// IngestPoints stores raw points and records gaps.
func (s *Service) IngestPoints(ctx context.Context, hostID string, ps []monitor.Point, gaps []monitor.Gap) error {
	now := s.now()
	var ok []monitor.Point
	for _, p := range ps {
		if validPoint(p, now) {
			ok = append(ok, p)
		}
	}
	if len(ok) == 0 && len(gaps) == 0 {
		return nil
	}
	err := s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
		if err := s.storePointsTx(ctx, tx, hostID, ok); err != nil {
			return err
		}
		for _, g := range gaps {
			if g.To <= g.From || g.To-g.From > 400*86400 {
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO mon_gaps (host_id, from_ts, to_ts, reason, created_at) VALUES (?, ?, ?, ?, ?)`,
				hostID, g.From, g.To, trunc(g.Reason, 300), now.UnixMilli()); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO mon_events (host_id, ts, kind, subject, severity, message) VALUES (?, ?, 'gap', '', 'warning', ?)`,
				hostID, now.UnixMilli(), "Telemetry gap "+time.Unix(g.From, 0).UTC().Format("2006-01-02 15:04")+" – "+time.Unix(g.To, 0).UTC().Format("15:04")+" UTC: "+trunc(g.Reason, 200)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.layouts.reset()
		return err
	}
	for _, p := range ok {
		s.markLate(hostID, Tier5m, p.T)
	}
	s.eval.onPoints(ctx, hostID, ok)
	return nil
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// IngestState stores a host's current picture. raw (SSH) is kept for
// the next poll's rates.
func (s *Service) IngestState(ctx context.Context, hostID string, st *monitor.State, raw *monitor.Raw) error {
	if st.At <= 0 {
		st.At = s.now().UnixMilli()
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	rj := ""
	if raw != nil {
		rb, _ := json.Marshal(raw)
		rj = string(rb)
	}
	_, err = s.d.Store.Writer.ExecContext(ctx, `INSERT INTO mon_current (host_id, source, updated_at, state_json, raw_json) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(host_id) DO UPDATE SET source = excluded.source, updated_at = excluded.updated_at, state_json = excluded.state_json,
		raw_json = CASE WHEN excluded.raw_json != '' THEN excluded.raw_json ELSE mon_current.raw_json END`,
		hostID, st.Source, st.At, string(b), rj)
	if err != nil {
		return err
	}
	c := s.cache(hostID)
	s.mu.Lock()
	prev := c.state
	c.state, c.at, c.source = st, time.UnixMilli(st.At), st.Source
	if raw != nil {
		c.sshRaw = raw
	}
	s.mu.Unlock()
	if prev != nil && prev.BootID != "" && st.BootID != "" && prev.BootID != st.BootID {
		s.event(ctx, hostID, "reboot", "", "info", "The host rebooted (new boot id)", nil)
	}
	s.eval.onState(ctx, hostID, prev, st)
	return nil
}

// event records one monitoring event.
func (s *Service) event(ctx context.Context, hostID, kind, subject, sev, msg string, data []byte) {
	s.d.Store.Writer.ExecContext(ctx, `INSERT INTO mon_events (host_id, ts, kind, subject, severity, message, data_json) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		hostID, s.now().UnixMilli(), kind, trunc(subject, 200), sev, trunc(msg, 500), string(data))
}

// IngestServices applies a full service list. Only changes are written:
// identical reports cost one read. Transitions come from the diff (SSH)
// or from the agent's own events (which keep exact times); fromSSH says
// which.
func (s *Service) IngestServices(ctx context.Context, hostID string, r *services.Report, fromSSH bool) error {
	now := s.now().UnixMilli()
	prev, err := s.Services(ctx, hostID)
	if err != nil {
		return err
	}
	old := map[string]ServiceRow{}
	for _, u := range prev {
		old[u.Name] = u
	}
	if len(r.Units) > services.MaxUnits {
		r.Units = r.Units[:services.MaxUnits]
	}
	anyChange := len(prev) == 0
	err = s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
		if err := ensureCurrentTx(ctx, tx, hostID, fromSSH); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE mon_current SET services_note = ?, services_supported = ?, services_at = ? WHERE host_id = ?`,
			trunc(r.Note, 300), boolInt(r.Supported), now, hostID); err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, u := range r.Units {
			if u.Name == "" || len(u.Name) > 255 {
				continue
			}
			seen[u.Name] = true
			o, existed := old[u.Name]
			changed := !existed || o.Active != u.Active || o.Sub != u.Sub || !o.Known
			same := existed && !changed && o.Description == trunc(u.Description, 200) && o.Load == u.Load && o.FileState == u.FileState &&
				o.Since == u.Since && eqIntPtr(o.Restarts, u.Restarts) && o.Result == u.Result
			if same {
				continue
			}
			anyChange = true
			changedAt := o.ChangedAt
			if changed {
				changedAt = now
				if u.Since > 0 && (!existed || !o.Known) {
					// Systemd knows when the state began.
					changedAt = u.Since * 1000
				}
				if existed {
					from := o.Active + "/" + o.Sub
					if !o.Known {
						from = "unknown"
					}
					if fromSSH || !o.Known {
						if _, err := tx.ExecContext(ctx, `INSERT INTO mon_service_events (host_id, unit, ts, from_state, to_state, result) VALUES (?, ?, ?, ?, ?, ?)`,
							hostID, u.Name, now, from, u.Active+"/"+u.Sub, u.Result); err != nil {
							return err
						}
					}
				}
			}
			var restarts any
			if u.Restarts != nil {
				restarts = *u.Restarts
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO mon_services (host_id, unit, description, load_state, active_state, sub_state, file_state, since, restarts, result, transient, known, changed_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?)
				ON CONFLICT(host_id, unit) DO UPDATE SET description = excluded.description, load_state = excluded.load_state,
				active_state = excluded.active_state, sub_state = excluded.sub_state, file_state = excluded.file_state, since = excluded.since,
				restarts = excluded.restarts, result = excluded.result, transient = excluded.transient, known = 1, changed_at = excluded.changed_at`,
				hostID, u.Name, trunc(u.Description, 200), u.Load, u.Active, u.Sub, u.FileState, u.Since, restarts, u.Result, boolInt(u.Transient), changedAt); err != nil {
				return err
			}
		}
		for name := range old {
			if !seen[name] {
				anyChange = true
				if _, err := tx.ExecContext(ctx, `DELETE FROM mon_services WHERE host_id = ? AND unit = ?`, hostID, name); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.eval.onServices(ctx, hostID)
	if fn := s.svcHook; fn != nil {
		fn(hostID, anyChange)
	}
	return nil
}

// SetServicesHook installs fn, told after every stored service list
// (desired state judges services from monitoring's data).
func (s *Service) SetServicesHook(fn func(hostID string, changed bool)) { s.svcHook = fn }

// ensureCurrentTx makes sure a host has its mon_current row (reports can
// arrive before the first state).
func ensureCurrentTx(ctx context.Context, tx *sql.Tx, hostID string, ssh bool) error {
	src := "agent"
	if ssh {
		src = "ssh"
	}
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO mon_current (host_id, source, updated_at, state_json) VALUES (?, ?, 0, '{}')`, hostID, src)
	return err
}

func eqIntPtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// IngestStorage stores a storage report and judges what changed.
func (s *Service) IngestStorage(ctx context.Context, hostID string, r *storage.Report) error {
	if len(r.Devices) > storage.MaxDevices {
		r.Devices = r.Devices[:storage.MaxDevices]
	}
	for i := range r.Devices {
		storage.Judge(&r.Devices[i]) // the controller's own judgement, same code
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
		if err := ensureCurrentTx(ctx, tx, hostID, false); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE mon_current SET storage_json = ?, storage_at = ? WHERE host_id = ?`, string(b), s.now().UnixMilli(), hostID)
		return err
	}); err != nil {
		return err
	}
	c := s.cache(hostID)
	s.mu.Lock()
	prev := c.storage
	c.storage = r
	s.mu.Unlock()
	s.eval.onStorage(ctx, hostID, prev, r)
	return nil
}

// IngestEvents stores agent events: service transitions, kernel evidence
// (deduplicated by boot and kernel timestamp), probe results, fidelity
// changes and gaps.
func (s *Service) IngestEvents(ctx context.Context, hostID string, evs []agentmon.Event) error {
	if len(evs) > agentmon.MaxEvents+10 {
		evs = evs[len(evs)-agentmon.MaxEvents-10:]
	}
	c := s.cache(hostID)
	var kernel []storage.KernelEvent
	var probes []agentmon.ProbeResult
	err := s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
		if err := ensureCurrentTx(ctx, tx, hostID, false); err != nil {
			return err
		}
		for _, e := range evs {
			ts := e.T * 1000
			if ts <= 0 || ts > s.now().Add(5*time.Minute).UnixMilli() {
				ts = s.now().UnixMilli()
			}
			switch e.Kind {
			case agentmon.EventService:
				var ch services.Change
				if json.Unmarshal(e.Data, &ch) != nil || ch.Unit == "" {
					continue
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO mon_service_events (host_id, unit, ts, from_state, to_state, result) VALUES (?, ?, ?, ?, ?, ?)`,
					hostID, trunc(ch.Unit, 255), ts, ch.FromActive+"/"+ch.From, ch.ToActive+"/"+ch.To, trunc(ch.Result, 50)); err != nil {
					return err
				}
				continue
			case agentmon.EventKernel:
				var k storage.KernelEvent
				if json.Unmarshal(e.Data, &k) == nil && k.Kind != "" {
					s.mu.Lock()
					seen := c.kernel
					if e.BootID != "" && e.BootID == seen.Boot && k.MonoUs <= seen.LastUs {
						s.mu.Unlock()
						continue // already recorded (the agent restarted and re-read the kernel buffer)
					}
					if e.BootID != "" {
						c.kernel = kernelSeen{Boot: e.BootID, LastUs: max(k.MonoUs, func() int64 {
							if seen.Boot == e.BootID {
								return seen.LastUs
							}
							return 0
						}())}
					}
					s.mu.Unlock()
					kernel = append(kernel, k)
				}
			case agentmon.EventProbe:
				var p agentmon.ProbeResult
				if json.Unmarshal(e.Data, &p) == nil && p.Mount != "" {
					probes = append(probes, p)
				}
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO mon_events (host_id, ts, kind, subject, severity, message, data_json) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				hostID, ts, trunc(e.Kind, 30), trunc(e.Subject, 200), sevOf(e.Severity), trunc(e.Message, 500), trunc(string(e.Data), 4000)); err != nil {
				return err
			}
		}
		s.mu.Lock()
		for _, p := range probes {
			c.probes[p.Mount] = p
		}
		pj, _ := json.Marshal(c.probes)
		kj, _ := json.Marshal(c.kernel)
		s.mu.Unlock()
		_, err := tx.ExecContext(ctx, `UPDATE mon_current SET probes_json = ?, kernel_json = ? WHERE host_id = ?`, string(pj), string(kj), hostID)
		return err
	})
	if err != nil {
		return err
	}
	s.eval.onEvents(ctx, hostID, evs, kernel, probes)
	return nil
}

func sevOf(s string) string {
	switch s {
	case "warning", "critical":
		return s
	}
	return "info"
}

// --- SSH polling (never continuous) ---

// PollSSH collects one SSH host's monitoring data. run executes a script
// over the host's pinned connection as the login user; runRoot (nil when
// the host has no root access, or SSH sudo is off) as root, used only for
// the hourly storage-health pass.
func (s *Service) PollSSH(ctx context.Context, hostID string, run, runRoot collect.Runner) error {
	set := s.Settings(ctx)
	if !set.Enabled {
		return nil
	}
	c := s.cache(hostID)
	s.mu.Lock()
	prev, prevRaw := c.state, c.sshRaw
	var prevDevs []storage.Device
	if c.storage != nil {
		prevDevs = c.storage.Devices
	}
	smart := c.storage == nil || c.forceSmart || s.now().Sub(time.UnixMilli(c.storage.At)) >= time.Duration(set.SSHSmartS)*time.Second
	c.forceSmart = false
	s.mu.Unlock()
	now := s.now()
	if !set.SSHSudo {
		runRoot = nil
	}
	res, err := collect.PollRoot(ctx, run, runRoot, s.pollRoot, now.UnixMilli(), prev, prevDevs, smart)
	if err != nil {
		return err
	}
	// Rates between this poll and the last (a reset or reboot yields none).
	sample := monitor.Rates(prevRaw, res.Raw)
	st := res.State
	collect.FillState(st, res.Raw, sample)
	st.Fidelity = monitor.Fidelity{Mode: "polled", SampleS: set.SSHPollS}
	v := sample.V
	for k, x := range collect.Values(st) {
		v[k] = x
	}
	span := set.SSHPollS
	if prevRaw != nil && prevRaw.BootID == res.Raw.BootID {
		if d := int((res.Raw.AtMs - prevRaw.AtMs) / 1000); d >= 10 && d <= 3600 {
			span = d
		}
	}
	p := monitor.Point{T: now.Unix() - int64(span), S: span, N: 1, V: map[string][2]float64{}}
	for k, x := range v {
		p.V[k] = [2]float64{x, x}
	}
	if err := s.IngestState(ctx, hostID, st, res.Raw); err != nil {
		return err
	}
	if err := s.IngestPoints(ctx, hostID, []monitor.Point{p}, nil); err != nil {
		return err
	}
	if res.Services != nil && !(res.Services.Limited && s.servicesReadRecently(ctx, hostID, now, 2*time.Duration(set.SSHSmartS)*time.Second)) {
		res.Services.At = now.UnixMilli()
		if err := s.IngestServices(ctx, hostID, res.Services, true); err != nil {
			return err
		}
	}
	if res.Storage != nil {
		if !smart {
			res.Storage.Smart = false
		}
		if err := s.IngestStorage(ctx, hostID, res.Storage); err != nil {
			return err
		}
	}
	var evs []agentmon.Event
	for _, k := range res.Kernel {
		d, _ := json.Marshal(k)
		evs = append(evs, agentmon.Event{T: now.Unix(), Kind: agentmon.EventKernel, Subject: k.Disk, Severity: "warning",
			Message: k.Text, Data: d, BootID: res.Raw.BootID})
	}
	if len(evs) > 0 {
		return s.IngestEvents(ctx, hostID, evs)
	}
	return nil
}

// servicesReadRecently reports whether the host has a service list read
// within window: a routine poll whose account can't ask systemd keeps the
// one the hourly root pass read rather than replacing it with "unknown".
func (s *Service) servicesReadRecently(ctx context.Context, hostID string, now time.Time, window time.Duration) bool {
	var sup int
	var at int64
	if err := s.d.Store.Reader.QueryRowContext(ctx, `SELECT services_supported, services_at FROM mon_current WHERE host_id = ?`, hostID).Scan(&sup, &at); err != nil {
		return false
	}
	return sup == 1 && now.Sub(time.UnixMilli(at)) < window
}

// ForceStorage makes the host's next SSH poll include storage health.
func (s *Service) ForceStorage(hostID string) {
	c := s.cache(hostID)
	s.mu.Lock()
	c.forceSmart = true
	s.mu.Unlock()
}

// RequestRefresh asks for fresh data now: an agent collects immediately;
// an SSH host is polled on its next scheduler pass.
func (s *Service) RequestRefresh(ctx context.Context, hostID string, kinds ...string) bool {
	if s.d.Agents == nil {
		return false
	}
	ok := false
	for _, k := range kinds {
		ok = s.d.Agents.RequestTelemetry(hostID, k) || ok
	}
	return ok
}

// HostLabel is a host's name for messages.
func (s *Service) hostName(ctx context.Context, id string) string {
	if h, err := s.d.Store.R.GetHost(ctx, id); err == nil {
		return h.Name
	}
	return id
}

var errNotFound = errors.New("not found")

func notFound(err error) bool { return errors.Is(err, sql.ErrNoRows) || errors.Is(err, errNotFound) }

func lower(s string) string { return strings.ToLower(s) }
