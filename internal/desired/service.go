package desired

import (
	"context"
	"database/sql"
	"hash/fnv"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"drool/internal/alerts"
	"drool/internal/artifacts"
	"drool/internal/audit"
	"drool/internal/engine"
	"drool/internal/monitor/monsvc"
	"drool/internal/store"
	"drool/internal/store/db"
)

// Tuning sets the service's cadence. Zero values mean the defaults.
type Tuning struct {
	// Tick: how often the loop looks for work (1 s).
	Tick time.Duration
	// CheckEvery: how often each host's packages and files are checked
	// (15 min), spread across the interval per host.
	CheckEvery time.Duration
	// Workers: concurrent host checks (4).
	Workers int
	// AutoEvery: how often automatic restore looks for drift (30 s).
	AutoEvery time.Duration
	// MaintenanceGrace: how long after a host leaves maintenance before
	// automatic restore may correct it (5 min).
	MaintenanceGrace time.Duration
	// ProofWait: how long to wait for fresh service data from an agent
	// after a correction or before a restore (45 s: an agent collects at
	// once when asked, and delivers telemetry every 15 s).
	ProofWait time.Duration
	// HistoryDays and HistoryPerResource bound history (180 days, 100).
	HistoryDays        int
	HistoryPerResource int
}

func (t *Tuning) defaults() {
	if t.Tick <= 0 {
		t.Tick = time.Second
	}
	if t.CheckEvery <= 0 {
		t.CheckEvery = 15 * time.Minute
	}
	if t.Workers <= 0 {
		t.Workers = 4
	}
	if t.AutoEvery <= 0 {
		t.AutoEvery = 30 * time.Second
	}
	if t.MaintenanceGrace <= 0 {
		t.MaintenanceGrace = 5 * time.Minute
	}
	if t.ProofWait <= 0 {
		t.ProofWait = 45 * time.Second
	}
	if t.HistoryDays <= 0 {
		t.HistoryDays = 180
	}
	if t.HistoryPerResource <= 0 {
		t.HistoryPerResource = 100
	}
}

// Deps are the service's collaborators.
type Deps struct {
	Store     *store.Store
	Engine    *engine.Engine
	Monitor   *monsvc.Service  // service data (M11); nil: services are unknown
	Alerts    *alerts.Service  // maintenance and the event timeline; may be nil
	Prober    Prober           // packages and files (mgmt.Router)
	Artifacts *artifacts.Store // managed file content
	Audit     *audit.Writer
	Log       *slog.Logger
	// Path reports how DROOL reaches a host right now (mgmt.Router.Decide):
	// whether its agent is connected, and why a host can't be changed
	// (a quarantined or revoked identity, another controller epoch).
	Path func(ctx context.Context, h db.Host) PathInfo
	// PollSSH collects one monitoring poll over SSH now (fresh service data).
	PollSSH func(ctx context.Context, h db.Host) error
	// Removals reports which other packages removing pkg would also remove
	// (a read-only package simulation); nil: not shown in previews.
	Removals func(ctx context.Context, hostID, pkg string) ([]string, error)
	Tuning   Tuning
}

// PathInfo is the management path's state for a host.
type PathInfo struct {
	AgentConnected bool `json:"agent_connected"`
	// AgentState: connected, not_connected, epoch_mismatch, quarantined, ...
	// ("" for SSH hosts).
	AgentState string `json:"agent_state,omitempty"`
	// Blocked: no path may be used until an administrator decides.
	Blocked bool   `json:"blocked"`
	Reason  string `json:"reason,omitempty"`
}

// Service is desired state.
type Service struct {
	d   Deps
	cfg Tuning
	log *slog.Logger
	now func() time.Time

	mu       sync.Mutex
	snap     *Snapshot
	eff      map[string]HostState // host → effective desired state
	dirty    map[string]bool      // hosts to evaluate
	due      map[string]time.Time // host → next package/file check
	urgent   map[string]bool      // checks asked for now (refresh, after a job)
	inflight map[string]bool      // checks running
	post     map[string]bool      // finished corrective jobs to settle (reconcile ids)
	events   *eventBatch
	mons     monCache // monitoring settings, read briefly cached

	wake        chan struct{}
	checks      chan checkReq
	settling    sync.Map // reconcile id → true while being settled
	lastGC      time.Time
	lastAut     time.Time
	started     bool
	autoRunning atomic.Bool
}

// AutoRestoreNow runs one pass of automatic restore synchronously (tests),
// after any pass already running: passes never overlap.
func (s *Service) AutoRestoreNow(ctx context.Context) {
	for !s.autoRunning.CompareAndSwap(false, true) {
		time.Sleep(10 * time.Millisecond)
	}
	defer s.autoRunning.Store(false)
	s.autoRestore(ctx)
}

// New creates the service. Call Run to start it.
func New(d Deps) *Service {
	d.Tuning.defaults()
	if d.Log == nil {
		d.Log = slog.Default()
	}
	s := &Service{d: d, cfg: d.Tuning, log: d.Log, now: time.Now, eff: map[string]HostState{}, dirty: map[string]bool{},
		due: map[string]time.Time{}, urgent: map[string]bool{}, inflight: map[string]bool{}, post: map[string]bool{},
		events: &eventBatch{}, wake: make(chan struct{}, 1), checks: make(chan checkReq, 256)}
	return s
}

// SetClock replaces the clock (tests).
func (s *Service) SetClock(now func() time.Time) { s.now = now }

func (s *Service) nowMs() int64 { return s.now().UnixMilli() }

func (s *Service) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run evaluates, checks hosts, settles corrections and restores
// automatically (when enabled) until ctx ends. One loop and a fixed pool
// of check workers; nothing is started per host, baseline or resource.
func (s *Service) Run(ctx context.Context) {
	for range s.cfg.Workers {
		go s.worker(ctx)
	}
	if err := s.Start(ctx); err != nil {
		s.log.Error("desired state: start", "err", err)
	}
	t := time.NewTicker(s.cfg.Tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.wake:
		}
		s.Step(ctx)
	}
}

// Start loads the configuration, settles corrections that finished while
// the controller was down (interrupted jobs are never re-run), and
// spreads the first host checks over the check interval.
func (s *Service) Start(ctx context.Context) error {
	if err := s.reload(ctx, false); err != nil {
		return err
	}
	s.mu.Lock()
	now := s.now()
	for id, hs := range s.eff {
		s.dirty[id] = true
		if needsCheck(hs) {
			s.due[id] = now.Add(spread(id, s.cfg.CheckEvery))
		}
	}
	s.started = true
	s.mu.Unlock()
	// Corrections whose jobs ended (or were interrupted) while DROOL was
	// down: settle them from what the hosts look like now.
	ids, err := s.activeReconciles(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	for _, id := range ids {
		s.post[id] = true
	}
	s.mu.Unlock()
	return nil
}

// Step does one round of work (Run calls it every tick; tests call it).
func (s *Service) Step(ctx context.Context) {
	if gen, err := generation(ctx, s.d.Store.Reader); err == nil && (s.snap == nil || gen != s.snapGen()) {
		if err := s.reload(ctx, true); err != nil {
			s.log.Error("desired state: reload", "err", err)
		}
	}
	s.evaluateDirty(ctx)
	s.dispatchChecks(ctx)
	s.settle(ctx)
	now := s.now()
	if now.Sub(s.lastAut) >= s.cfg.AutoEvery {
		s.lastAut = now
		s.staleSweep(ctx)
		// Automatic restore checks hosts before it corrects them, which
		// takes a while: never on the loop itself, never twice at once.
		if s.autoRunning.CompareAndSwap(false, true) {
			go func() {
				defer s.autoRunning.Store(false)
				s.autoRestore(ctx)
			}()
		}
	}
	if now.Sub(s.lastGC) >= time.Hour {
		s.lastGC = now
		s.prune(ctx)
	}
	s.flushEvents(ctx)
}

func (s *Service) snapGen() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snap == nil {
		return -1
	}
	return s.snap.Gen
}

// reload reads a new configuration snapshot and marks hosts whose
// effective desired state changed for evaluation and a prompt check.
func (s *Service) reload(ctx context.Context, changed bool) error {
	snap, err := LoadSnapshot(ctx, s.d.Store.Reader)
	if err != nil {
		return err
	}
	eff := make(map[string]HostState, len(snap.Hosts))
	for id := range snap.Hosts {
		eff[id] = snap.Resolve(id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.eff
	s.snap, s.eff = snap, eff
	if !changed {
		return nil
	}
	now := s.now()
	n := 0
	for id, hs := range eff {
		if o, ok := old[id]; ok && o.Hash == hs.Hash {
			continue
		}
		s.dirty[id] = true
		if needsCheck(hs) && newKeys(old[id], hs) {
			// Spread checks of a broad change over a minute.
			s.due[id] = now.Add(spread(id, time.Minute))
			n++
		}
	}
	for id := range old {
		if _, ok := eff[id]; !ok {
			s.dirty[id] = true // removed or no longer managed: clean up
			delete(s.due, id)
		}
	}
	return nil
}

// needsCheck reports whether a host has packages or files to check.
func needsCheck(hs HostState) bool {
	for _, e := range hs.Resources {
		if e.Kind != KindService {
			return true
		}
	}
	return false
}

// newKeys reports whether hs manages packages or files the old state
// didn't, or declares them differently (worth checking promptly).
func newKeys(old, hs HostState) bool {
	for k, e := range hs.Resources {
		if e.Kind == KindService {
			continue
		}
		o, ok := old.Resources[k]
		if !ok || o.Conflict != e.Conflict || (o.Resource != nil && e.Resource != nil && !o.Resource.Same(*e.Resource)) {
			return true
		}
	}
	return false
}

// spread maps a host to a stable offset inside an interval, so checks of
// many hosts never start together.
func spread(hostID string, every time.Duration) time.Duration {
	if every <= 0 {
		return 0
	}
	h := fnv.New64a()
	h.Write([]byte(hostID))
	return time.Duration(h.Sum64() % uint64(every))
}

// effective returns a host's effective desired state.
func (s *Service) effective(hostID string) (HostState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hs, ok := s.eff[hostID]
	return hs, ok
}

// Snapshot returns the current configuration snapshot.
func (s *Service) Snapshot() *Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snap
}

// fresh makes sure the snapshot reflects the latest configuration (a
// host added, a group changed) before answering a request.
func (s *Service) fresh(ctx context.Context) {
	if g, err := generation(ctx, s.d.Store.Reader); err == nil && g != s.snapGen() {
		if err := s.reload(ctx, true); err != nil {
			s.log.Error("desired state: reload", "err", err)
		}
		s.poke()
	}
}

// markDirty asks for a host's evaluation.
func (s *Service) markDirty(hostIDs ...string) {
	s.mu.Lock()
	for _, id := range hostIDs {
		s.dirty[id] = true
	}
	s.mu.Unlock()
	s.poke()
}

// OnServices is called after monitoring stored a host's service list.
// Unchanged lists need no evaluation (staleness and recovery from
// unknown are handled by the periodic sweep).
func (s *Service) OnServices(hostID string, changed bool) {
	if !changed {
		return
	}
	s.mu.Lock()
	hs, ok := s.eff[hostID]
	s.mu.Unlock()
	if !ok {
		return
	}
	for _, e := range hs.Resources {
		if e.Kind == KindService {
			s.markDirty(hostID)
			return
		}
	}
}

// OnHostConnected is called when a host's agent (re)connects: its checks
// are brought forward (spread over a minute, as many may reconnect at
// once after a controller restart).
func (s *Service) OnHostConnected(hostID string) {
	s.mu.Lock()
	if hs, ok := s.eff[hostID]; ok && needsCheck(hs) {
		at := s.now().Add(spread(hostID, time.Minute))
		if cur, ok := s.due[hostID]; !ok || at.Before(cur) {
			s.due[hostID] = at
		}
	}
	s.mu.Unlock()
}

// OnJobDone is the engine's notification that a job finished.
func (s *Service) OnJobDone(jobID, _ string) {
	var rid sql.NullString
	if err := s.d.Store.Reader.QueryRow(`SELECT reconcile_id FROM jobs WHERE id = ?`, jobID).Scan(&rid); err != nil || !rid.Valid {
		return
	}
	s.mu.Lock()
	s.post[rid.String] = true
	s.mu.Unlock()
	s.poke()
}

// evaluateDirty evaluates the hosts marked dirty (bounded per step).
func (s *Service) evaluateDirty(ctx context.Context) {
	s.mu.Lock()
	ids := make([]string, 0, len(s.dirty))
	for id := range s.dirty {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) > 500 {
		ids = ids[:500]
	}
	for _, id := range ids {
		delete(s.dirty, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		if err := s.evaluate(ctx, id, evalInput{}); err != nil {
			s.log.Error("desired state: evaluate", "host", id, "err", err)
		}
	}
}

// Refresh checks hosts now (packages, files, and fresh service data).
func (s *Service) Refresh(hostIDs []string) {
	s.mu.Lock()
	now := s.now()
	for i, id := range hostIDs {
		if _, ok := s.eff[id]; !ok {
			continue
		}
		s.urgent[id] = true
		// Large refreshes are spread a little.
		s.due[id] = now.Add(time.Duration(i/20) * time.Second)
	}
	s.mu.Unlock()
	s.poke()
}
