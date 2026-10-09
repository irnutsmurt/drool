package desired

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"drool/internal/files"
	"drool/internal/model"
	"drool/internal/store/db"
)

// A check observes one host's managed packages and files (read-only: a
// package database query, stat and SHA-256 on the host) and, when proof
// is needed (a refresh, before and after a correction), asks for fresh
// service data. Services are otherwise judged from monitoring's own
// reports as they arrive.
type checkReq struct {
	hostID string
	proof  bool
}

// checkResult is what a check found.
type checkResult struct {
	obs map[string]Observation
	// Set when that kind was checked (even if some resources failed).
	pkgChecked, filesChecked bool
	pkgNote, filesNote       string
	// proof: fresh service data was requested; servicesFresh whether it came.
	proof         bool
	servicesFresh bool
	servicesNote  string
	at            int64
}

// dispatchChecks hands due checks to the workers, never more than the
// pool can take, earliest first.
func (s *Service) dispatchChecks(ctx context.Context) {
	s.mu.Lock()
	now := s.now()
	type due struct {
		id string
		at time.Time
	}
	var ready []due
	for id, at := range s.due {
		if !at.After(now) && !s.inflight[id] {
			ready = append(ready, due{id, at})
		}
	}
	sort.Slice(ready, func(i, j int) bool {
		if !ready[i].at.Equal(ready[j].at) {
			return ready[i].at.Before(ready[j].at)
		}
		return ready[i].id < ready[j].id
	})
	free := 2*s.cfg.Workers - len(s.inflight)
	var reqs []checkReq
	for _, d := range ready {
		if len(reqs) >= free {
			break
		}
		delete(s.due, d.id)
		s.inflight[d.id] = true
		reqs = append(reqs, checkReq{hostID: d.id, proof: s.urgent[d.id]})
		delete(s.urgent, d.id)
	}
	s.mu.Unlock()
	for _, r := range reqs {
		select {
		case s.checks <- r:
		case <-ctx.Done():
			return
		}
	}
}

// worker runs checks from the queue (a fixed pool started by Run).
func (s *Service) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case r := <-s.checks:
			s.checkAndEvaluate(ctx, r.hostID, r.proof, 0)
			s.mu.Lock()
			delete(s.inflight, r.hostID)
			if hs, ok := s.eff[r.hostID]; ok && needsCheck(hs) {
				next := s.now().Add(s.cfg.CheckEvery + jitter(r.hostID, s.cfg.CheckEvery/10))
				if cur, ok := s.due[r.hostID]; !ok || next.Before(cur) {
					s.due[r.hostID] = next
				}
			}
			s.mu.Unlock()
		}
	}
}

func jitter(hostID string, d time.Duration) time.Duration {
	return spread(hostID+"#", d)
}

// checkAndEvaluate checks a host and evaluates it with what was found.
func (s *Service) checkAndEvaluate(ctx context.Context, hostID string, proof bool, since int64) checkResult {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	res := s.runCheck(cctx, hostID, proof)
	if err := s.evaluate(ctx, hostID, evalInput{check: &res, since: since}); err != nil {
		s.log.Error("desired state: evaluate after check", "host", hostID, "err", err)
	}
	return res
}

// CheckNow checks a host now, with fresh service data, and evaluates it
// (tests and tools; the service checks hosts on its own schedule).
func (s *Service) CheckNow(ctx context.Context, hostID string) {
	s.checkAndEvaluate(ctx, hostID, true, 0)
}

// checkHosts checks several hosts now with bounded concurrency (restore
// previews, settling corrections) and waits for all of them.
func (s *Service) checkHosts(ctx context.Context, hostIDs []string, proof bool, since int64) {
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, id := range hostIDs {
		wg.Add(1)
		sem <- struct{}{}
		go func(id string) {
			defer func() { <-sem; wg.Done() }()
			s.checkAndEvaluate(ctx, id, proof, since)
		}(id)
	}
	wg.Wait()
}

func errText(err error) string {
	var fe *files.Error
	if errors.As(err, &fe) {
		return fe.Message
	}
	oe := model.AsOpError(err)
	if oe.Message != "" {
		return oe.Message
	}
	return err.Error()
}

// runCheck observes a host's managed packages and files.
func (s *Service) runCheck(ctx context.Context, hostID string, proof bool) checkResult {
	res := checkResult{obs: map[string]Observation{}, proof: proof}
	hs, ok := s.effective(hostID)
	if !ok {
		return res
	}
	h, err := s.d.Store.R.GetHost(ctx, hostID)
	if err != nil {
		return res
	}
	var pkgs []string
	pkgKeys := map[string]string{}
	type fileQ struct {
		key, path string
	}
	byElev := map[bool][]fileQ{}
	hasService := false
	for k, e := range hs.Resources {
		switch e.Kind {
		case KindPackage:
			n := KeyName(k)
			pkgs = append(pkgs, n)
			pkgKeys[n] = k
		case KindFile:
			elev := e.Resource != nil && e.Resource.Elevated || e.Conflict && conflictElevated(e)
			byElev[elev] = append(byElev[elev], fileQ{k, KeyName(k)})
		case KindService:
			hasService = true
		}
	}
	if proof && hasService {
		res.servicesFresh, res.servicesNote = s.freshServices(ctx, h)
	}
	if len(pkgs) > 0 && s.d.Prober != nil {
		sort.Strings(pkgs)
		pctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		mgr, vs, err := s.d.Prober.PackageVersions(pctx, hostID, pkgs)
		cancel()
		res.pkgChecked = true
		if err != nil {
			res.pkgNote = errText(err)
		} else {
			now := s.nowMs()
			for n, k := range pkgKeys {
				v := vs[n]
				res.obs[k] = Observation{Package: &PackageObs{Installed: v != "", Version: v, Manager: mgr}, Source: "check", At: now}
			}
		}
	}
	if len(byElev) > 0 && s.d.Prober != nil {
		res.filesChecked = true
		for _, elev := range []bool{false, true} {
			qs := byElev[elev]
			if len(qs) == 0 {
				continue
			}
			sort.Slice(qs, func(i, j int) bool { return qs[i].path < qs[j].path })
			fctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			fsys, err := s.d.Prober.OpenFS(fctx, hostID, elev)
			if err != nil {
				// The host couldn't be reached (or no session opened): keep
				// the last observations; they turn unknown once stale.
				cancel()
				if res.filesNote == "" {
					res.filesNote = errText(err)
				}
				continue
			}
			for _, q := range qs {
				fo, err := ObserveFile(fctx, fsys, q.path)
				if err != nil {
					res.obs[q.key] = Observation{Error: errText(err), Source: "check", At: s.nowMs()}
					continue
				}
				res.obs[q.key] = Observation{File: &fo, Source: "check", At: s.nowMs()}
			}
			fsys.Close()
			cancel()
		}
	}
	res.at = s.nowMs()
	return res
}

// conflictElevated: a conflicting file is read as root when any of its
// declarations deploys it as root.
func conflictElevated(e Effective) bool { return e.Source.Elevated }

// servicesStamp is when monitoring last stored the host's service list
// (controller clock), 0 if never.
func (s *Service) servicesStamp(ctx context.Context, hostID string) int64 {
	var at int64
	s.d.Store.Reader.QueryRowContext(ctx, `SELECT services_at FROM mon_current WHERE host_id = ?`, hostID).Scan(&at)
	return at
}

// freshServices asks for service data collected now: the agent reports
// at once; an SSH host is polled. It reports whether new data arrived.
// Data from before the request never counts (M13's verification rule).
func (s *Service) freshServices(ctx context.Context, h db.Host) (bool, string) {
	if s.d.Monitor == nil || !s.d.Monitor.Settings(ctx).Enabled {
		return false, "monitoring is turned off (Settings → Monitoring); desired service state is read from monitoring data"
	}
	base := s.servicesStamp(ctx, h.ID)
	info := PathInfo{}
	if s.d.Path != nil {
		info = s.d.Path(ctx, h)
	}
	if info.AgentConnected {
		s.d.Monitor.RequestRefresh(ctx, h.ID, "services")
		end := time.Now().Add(s.cfg.ProofWait)
		for time.Now().Before(end) {
			if s.servicesStamp(ctx, h.ID) != base {
				return true, ""
			}
			select {
			case <-time.After(250 * time.Millisecond):
			case <-ctx.Done():
				return false, "cancelled"
			}
		}
		return false, "the agent didn't report its services in time (agents older than this controller only report changes: update the agent if this keeps happening)"
	}
	if h.SshConfigured != 0 && s.d.PollSSH != nil {
		if err := s.d.PollSSH(ctx, h); err != nil {
			return false, "collecting service data over SSH failed: " + errText(err)
		}
		return s.servicesStamp(ctx, h.ID) != base, ""
	}
	if info.Reason != "" {
		return false, info.Reason
	}
	return false, "no way to reach the host right now"
}
