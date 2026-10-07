// Package poller keeps inventory and status fresh in the background
// (ARCHITECTURE-ALPHA §11.8, decision D6).
//
// It runs on its own small worker budget, yields to user jobs (no new polls
// while job workers are saturated), and never waits for a host lock: a
// locked host is simply checked later, and the UI shows its data as stale.
package poller

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"drool/internal/engine/lock"
	"drool/internal/inventory"
	"drool/internal/model"
	"drool/internal/store"
	"drool/internal/store/db"
)

// Task kinds, in priority order.
const (
	taskFacts   = "facts"
	taskStatus  = "status"
	taskUpdates = "updates"
	taskRefresh = "refresh"
)

// Config tunes polling.
type Config struct {
	StatusInterval  time.Duration // default 5m
	UpdatesInterval time.Duration // default 6h
	FactsInterval   time.Duration // default 24h
	RefreshInterval time.Duration // default 24h (package metadata, needs root)
	Workers         int           // default 4
	Tick            time.Duration // scheduler tick; default 15s
}

func (c *Config) defaults() {
	if c.StatusInterval <= 0 {
		c.StatusInterval = 5 * time.Minute
	}
	if c.UpdatesInterval <= 0 {
		c.UpdatesInterval = 6 * time.Hour
	}
	if c.FactsInterval <= 0 {
		c.FactsInterval = 24 * time.Hour
	}
	if c.RefreshInterval <= 0 {
		c.RefreshInterval = 24 * time.Hour
	}
	if c.Workers <= 0 {
		c.Workers = 4
	}
	if c.Tick <= 0 {
		c.Tick = 15 * time.Second
	}
}

// StaleAfter is how old a status check may be before the UI marks it stale.
func (c Config) StaleAfter() time.Duration {
	c.defaults()
	return 3 * c.StatusInterval
}

// Poller schedules background checks.
type Poller struct {
	st    *store.Store
	inv   *inventory.Service
	locks *lock.Manager
	busy  func() bool // true while user jobs saturate the engine
	log   *slog.Logger
	cfg   Config
	now   func() time.Time

	slots chan struct{}
	wake  chan struct{} // a worker finished: dispatch again without waiting for the tick
	exec  func(ctx context.Context, h db.Host, task string)
	mu    sync.Mutex
	due   map[string]map[string]time.Time // host -> task -> next run
	inFly map[string]bool                 // host -> a task is running

	// AgentManaged reports hosts whose inventory comes from their agent's
	// telemetry; they are not polled over SSH (ARCHITECTURE-ALPHA §23.7).
	AgentManaged func(ctx context.Context, h db.Host) bool
}

func New(st *store.Store, inv *inventory.Service, locks *lock.Manager, busy func() bool, log *slog.Logger, cfg Config) *Poller {
	cfg.defaults()
	p := &Poller{st: st, inv: inv, locks: locks, busy: busy, log: log, cfg: cfg, now: time.Now,
		slots: make(chan struct{}, cfg.Workers), wake: make(chan struct{}, 1), due: map[string]map[string]time.Time{}, inFly: map[string]bool{}}
	p.exec = p.run
	return p
}

// Run schedules checks until ctx ends.
func (p *Poller) Run(ctx context.Context) {
	t := time.NewTicker(p.cfg.Tick)
	defer t.Stop()
	for {
		p.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-p.wake:
		}
	}
}

// jitter spreads d by ±10%.
func jitter(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (0.9 + 0.2*rand.Float64()))
}

func (p *Poller) interval(task string) time.Duration {
	switch task {
	case taskStatus:
		return p.cfg.StatusInterval
	case taskUpdates:
		return p.cfg.UpdatesInterval
	case taskFacts:
		return p.cfg.FactsInterval
	}
	return p.cfg.RefreshInterval
}

// seed derives a host's first schedule from stored timestamps, so a
// controller restart doesn't re-poll everything at once.
func (p *Poller) seed(ctx context.Context, hostID string, now time.Time) map[string]time.Time {
	soon := func(within time.Duration) time.Time { return now.Add(time.Duration(rand.Int64N(int64(within)))) }
	next := func(lastMs int64, valid bool, task string, missing time.Time) time.Time {
		if !valid || lastMs == 0 {
			return missing
		}
		t := time.UnixMilli(lastMs).Add(jitter(p.interval(task)))
		if t.Before(now) {
			return soon(time.Minute)
		}
		return t
	}
	d := map[string]time.Time{}
	f, ferr := p.st.R.GetHostFacts(ctx, hostID)
	d[taskFacts] = next(f.CollectedAt, ferr == nil, taskFacts, now) // missing facts: now
	st, serr := p.st.R.GetHostStatus(ctx, hostID)
	d[taskStatus] = next(st.CheckedAt, serr == nil, taskStatus, now) // never checked (new host): now
	d[taskUpdates] = next(st.UpdatesCheckedAt.Int64, serr == nil && st.UpdatesCheckedAt.Valid, taskUpdates, soon(5*time.Minute))
	d[taskRefresh] = next(st.MetadataRefreshedAt.Int64, serr == nil && st.MetadataRefreshedAt.Valid, taskRefresh, soon(30*time.Minute))
	if serr == nil && st.UpdatesCheckedAt.Valid && !st.UpdatesAvailable.Valid {
		// Update count unknown (no package cache): refresh metadata soon.
		d[taskRefresh] = soon(2 * time.Minute)
	}
	return d
}

func eligible(h db.Host) bool {
	if h.JumpOnly != 0 {
		return false // not managed; may not even have a shell
	}
	switch h.Status {
	case inventory.StatusReady, inventory.StatusUnreachable, inventory.StatusUntested,
		inventory.StatusAuthFailed, inventory.StatusUnsupported:
		return true
	}
	return false // pending or changed host key: nothing to trust yet
}

func (p *Poller) tick(ctx context.Context) {
	if p.busy() {
		return // user jobs first; data goes stale instead
	}
	hosts, err := p.st.R.ListHosts(ctx)
	if err != nil {
		p.log.Error("poller: list hosts", "err", err)
		return
	}
	now := p.now()
	seen := map[string]bool{}
	for _, h := range hosts {
		seen[h.ID] = true
		if !eligible(h) || (p.AgentManaged != nil && p.AgentManaged(ctx, h)) {
			continue
		}
		p.mu.Lock()
		d, ok := p.due[h.ID]
		if !ok {
			p.mu.Unlock()
			d = p.seed(ctx, h.ID, now)
			p.mu.Lock()
			p.due[h.ID] = d
		}
		task := ""
		for _, t := range []string{taskFacts, taskStatus, taskUpdates, taskRefresh} {
			if !d[t].After(now) {
				task = t
				break
			}
		}
		if task == "" || p.inFly[h.ID] {
			p.mu.Unlock()
			continue
		}
		select {
		case p.slots <- struct{}{}:
		default:
			p.mu.Unlock()
			return // budget exhausted; the rest waits for the next tick
		}
		p.inFly[h.ID] = true
		p.mu.Unlock()
		go func(h db.Host, task string) {
			defer func() {
				<-p.slots
				p.mu.Lock()
				delete(p.inFly, h.ID)
				p.mu.Unlock()
				select {
				case p.wake <- struct{}{}:
				default:
				}
			}()
			p.exec(ctx, h, task)
		}(h, task)
	}
	// Forget deleted hosts.
	p.mu.Lock()
	for id := range p.due {
		if !seen[id] {
			delete(p.due, id)
		}
	}
	p.mu.Unlock()
}

func (p *Poller) reschedule(hostID, task string, after time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if d := p.due[hostID]; d != nil {
		d[task] = p.now().Add(after)
	}
}

// RefreshSoon schedules a package metadata refresh within a minute (used
// when an update check found no package cache).
func (p *Poller) RefreshSoon(hostID string) {
	p.reschedule(hostID, taskRefresh, time.Minute)
}

// RunNow makes a host's facts, status and update checks due immediately
// (e.g. after a successful connection test).
func (p *Poller) RunNow(hostID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if d := p.due[hostID]; d != nil {
		now := p.now()
		d[taskFacts], d[taskStatus], d[taskUpdates] = now, now, now
	}
}

func (p *Poller) run(ctx context.Context, h db.Host, task string) {
	release, ok := p.locks.TryAcquire(h.ID, "poller", model.LockShared)
	if !ok {
		p.reschedule(h.ID, task, time.Minute) // a job holds the host; try later
		return
	}
	defer release()
	log := p.log.With("host_id", h.ID, "host", h.Name, "task", task)
	switch task {
	case taskFacts:
		f, err := p.inv.Discover(ctx, h.ID)
		if err != nil {
			log.Info("poller: discovery failed", "err", err)
			p.recordStatusFailure(ctx, h, err)
			p.reschedule(h.ID, task, jitter(p.cfg.StatusInterval))
			return
		}
		if err := p.st.InTx(ctx, func(q *db.Queries) error { return p.inv.StoreFactsTx(ctx, q, h.ID, f) }); err != nil {
			log.Error("poller: store facts", "err", err)
			p.reschedule(h.ID, task, time.Minute)
			return
		}
		p.reschedule(h.ID, task, jitter(p.cfg.FactsInterval))
	case taskStatus:
		st, err := p.inv.CheckStatus(ctx, h.ID)
		var out inventory.StatusOutcome
		txErr := p.st.InTx(ctx, func(q *db.Queries) error {
			var e error
			out, e = p.inv.RecordStatusTx(ctx, q, h.ID, st, err)
			return e
		})
		if txErr != nil {
			log.Error("poller: store status", "err", txErr)
		}
		switch {
		case out.Rebooted:
			log.Info("host rebooted (boot id changed)")
		case out.BecameReady:
			log.Info("host reachable again")
		case out.NewStatus != "":
			log.Warn("host status changed by poll", "status", out.NewStatus, "err", err)
		}
		p.reschedule(h.ID, task, jitter(p.cfg.StatusInterval))
	case taskUpdates:
		if _, err := p.st.R.GetHostStatus(ctx, h.ID); err != nil {
			// Update counts are stored with the host's status: check that first.
			p.reschedule(h.ID, taskStatus, 0)
			p.reschedule(h.ID, task, 30*time.Second)
			return
		}
		f, err := p.st.R.GetHostFacts(ctx, h.ID)
		if err != nil || (f.PkgManager != "apt" && f.PkgManager != "dnf" && f.PkgManager != "yum") {
			p.reschedule(h.ID, task, jitter(p.cfg.UpdatesInterval))
			return
		}
		u, err := p.inv.CheckUpdates(ctx, h.ID, f.PkgManager)
		if err != nil {
			log.Info("poller: update check failed", "err", err)
			p.reschedule(h.ID, task, jitter(p.cfg.StatusInterval))
			return
		}
		if err := p.st.InTx(ctx, func(q *db.Queries) error { return p.inv.RecordUpdatesTx(ctx, q, h.ID, u) }); err != nil {
			log.Error("poller: store updates", "err", err)
			p.reschedule(h.ID, task, time.Minute)
			return
		}
		p.reschedule(h.ID, task, jitter(p.cfg.UpdatesInterval))
		if u.Available == nil && inventory.CanBecomeRoot(h) && p.refreshedLongAgo(ctx, h.ID) {
			// No package cache yet (typical for a new host): refresh metadata
			// soon instead of waiting for the daily refresh.
			p.reschedule(h.ID, taskRefresh, time.Minute)
		}
	case taskRefresh:
		f, err := p.st.R.GetHostFacts(ctx, h.ID)
		if err != nil || !inventory.CanBecomeRoot(h) || (f.PkgManager != "apt" && f.PkgManager != "dnf" && f.PkgManager != "yum") {
			p.reschedule(h.ID, task, jitter(p.cfg.RefreshInterval))
			return
		}
		if err := p.inv.RefreshMetadata(ctx, h.ID, f.PkgManager); err != nil {
			log.Info("poller: metadata refresh failed", "err", err)
			p.reschedule(h.ID, task, jitter(time.Hour))
			return
		}
		p.reschedule(h.ID, task, jitter(p.cfg.RefreshInterval))
		p.reschedule(h.ID, taskUpdates, 0) // count updates against fresh metadata
	}
}

// refreshedLongAgo reports whether package metadata wasn't refreshed by
// DROOL within the last hour (avoids refresh loops when a cache can't be built).
func (p *Poller) refreshedLongAgo(ctx context.Context, hostID string) bool {
	st, err := p.st.R.GetHostStatus(ctx, hostID)
	if err != nil || !st.MetadataRefreshedAt.Valid {
		return true
	}
	return p.now().Sub(time.UnixMilli(st.MetadataRefreshedAt.Int64)) > time.Hour
}

// recordStatusFailure records a failed connection from any task as a
// failed status check, so reachability stays accurate.
func (p *Poller) recordStatusFailure(ctx context.Context, h db.Host, err error) {
	_ = p.st.InTx(ctx, func(q *db.Queries) error {
		_, e := p.inv.RecordStatusTx(ctx, q, h.ID, nil, err)
		return e
	})
}
