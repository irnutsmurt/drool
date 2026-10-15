package monsvc

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"time"

	"drool/internal/monitor"
	"drool/internal/monitor/agentmon"
	"drool/internal/monitor/services"
	"drool/internal/monitor/storage"
)

// TEST MODE (DROOL_TEST_MONITOR_FILE): synthetic monitoring data for
// browser tests and screenshots. No isolated test environment has SD
// cards, failing NVMe drives and GPUs, so the file describes them. The
// controller logs TEST MODE loudly when it is used.

// TestHost is one host's synthetic data, by host name.
type TestHost struct {
	State    *monitor.State      `json:"state"`
	Series   map[string]TestWave `json:"series"`
	Services *services.Report    `json:"services"`
	Storage  *storage.Report     `json:"storage"`
	Events   []agentmon.Event    `json:"events"` // T <= 0: seconds before now
	// StatesTwice ingests the state twice (debounced checks fire).
	StatesTwice bool `json:"states_twice"`
	// Sustained: these values for the last eight minutes, through the
	// health evaluation (sustained-condition issues fire).
	Sustained map[string]float64 `json:"sustained"`
	Settings  *HostSettings      `json:"settings"`
}

// TestWave generates a series: base + amp·sin(2πt/period) + a bump.
type TestWave struct {
	Base     float64 `json:"base"`
	Amp      float64 `json:"amp"`
	PeriodH  float64 `json:"period_h"`
	SpikeAtH float64 `json:"spike_at_h"` // hours ago, 0 = none
	Spike    float64 `json:"spike"`
	GapFromH float64 `json:"gap_from_h"` // hours ago: no data in [gap_to_h, gap_from_h]
	GapToH   float64 `json:"gap_to_h"`
}

func (w TestWave) at(hoursAgo float64) (float64, bool) {
	if w.GapFromH > 0 && hoursAgo <= w.GapFromH && hoursAgo >= w.GapToH {
		return 0, false
	}
	v := w.Base
	if w.PeriodH > 0 {
		v += w.Amp * math.Sin(2*math.Pi*hoursAgo/w.PeriodH)
	}
	if w.SpikeAtH > 0 && math.Abs(hoursAgo-w.SpikeAtH) < 0.15 {
		v += w.Spike
	}
	return math.Max(0, v), true
}

// LoadTestFile ingests a TEST MODE file.
func (s *Service) LoadTestFile(ctx context.Context, path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var file struct {
		Hosts map[string]TestHost `json:"hosts"`
	}
	if err := json.Unmarshal(b, &file); err != nil {
		return err
	}
	hosts, err := s.d.Store.R.ListHosts(ctx)
	if err != nil {
		return err
	}
	byName := map[string]string{}
	for _, h := range hosts {
		byName[h.Name] = h.ID
	}
	for name, th := range file.Hosts {
		id, ok := byName[name]
		if !ok {
			continue
		}
		if err := s.loadTestHost(ctx, id, th); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func (s *Service) loadTestHost(ctx context.Context, id string, th TestHost) error {
	now := s.now()
	if len(th.Series) > 0 {
		// Write every tier directly: 26 h of minutes, 8 days of five
		// minutes, 30 days of hours.
		err := s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
			for _, tier := range []struct {
				t     int
				span  int64
				hours float64
			}{{TierRaw, 60, 26}, {Tier5m, 300, 8 * 24}, {Tier1h, 3600, 30 * 24}} {
				end := now.Unix() / tier.span * tier.span
				for ts := end - int64(tier.hours*3600); ts < end; ts += tier.span {
					v := map[string][2]float64{}
					for k, w := range th.Series {
						if x, ok := w.at(float64(end-ts) / 3600); ok {
							v[k] = [2]float64{x, x * 1.15}
						}
					}
					if len(v) == 0 {
						continue
					}
					keys, data := encode(v)
					lid, err := s.layouts.id(ctx, tx, keys)
					if err != nil {
						return err
					}
					if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO mon_points (host_id, tier, ts, span, n, layout, data) VALUES (?, ?, ?, ?, ?, ?, ?)`,
						id, tier.t, ts, tier.span, tier.span/10, lid, data); err != nil {
						return err
					}
				}
			}
			return nil
		})
		if err != nil {
			s.layouts.reset()
			return err
		}
		s.setWatermark(id, Tier5m, now.Unix()/300*300)
		s.setWatermark(id, Tier1h, now.Unix()/3600*3600)
	}
	if th.Settings != nil {
		// watched_services are expected-running services on the host now.
		if err := s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
			if err := SaveHostSettingsTx(ctx, tx, id, *th.Settings, "", now.UnixMilli()); err != nil {
				return err
			}
			_, err := s.SetWatchedTx(ctx, tx, id, th.Settings.WatchedServices, "", now.UnixMilli())
			return err
		}); err != nil {
			return err
		}
	}
	if len(th.Sustained) > 0 {
		var ps []monitor.Point
		for i := 8; i >= 1; i-- {
			p := monitor.Point{T: now.Add(-time.Duration(i)*time.Minute).Unix() / 60 * 60, S: 60, N: 6, V: map[string][2]float64{}}
			for k, v := range th.Sustained {
				p.V[k] = [2]float64{v, v}
			}
			ps = append(ps, p)
		}
		if err := s.IngestPoints(ctx, id, ps, nil); err != nil {
			return err
		}
	}
	if th.State != nil {
		st := *th.State
		if st.At <= 0 {
			st.At = now.UnixMilli() + st.At*1000
		}
		if err := s.IngestState(ctx, id, &st, nil); err != nil {
			return err
		}
		if th.StatesTwice {
			if err := s.IngestState(ctx, id, &st, nil); err != nil {
				return err
			}
		}
	}
	if th.Services != nil {
		if err := s.IngestServices(ctx, id, th.Services, true); err != nil {
			return err
		}
	}
	if th.Storage != nil {
		if th.Storage.At <= 0 {
			th.Storage.At = now.UnixMilli()
		}
		if err := s.IngestStorage(ctx, id, th.Storage); err != nil {
			return err
		}
	}
	s.mu.Lock()
	first := !s.testLoaded[id]
	s.testLoaded[id] = true
	s.mu.Unlock()
	if len(th.Events) > 0 && first {
		evs := append([]agentmon.Event(nil), th.Events...)
		for i := range evs {
			if evs[i].T <= 0 {
				evs[i].T = now.Unix() + evs[i].T
			}
		}
		if err := s.IngestEvents(ctx, id, evs); err != nil {
			return err
		}
	}
	return nil
}

// RunTestFile reloads the file now and then, so states stay fresh.
func (s *Service) RunTestFile(ctx context.Context, path string) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		if err := s.LoadTestFile(ctx, path); err != nil {
			s.log.Warn("TEST MODE: monitoring test file", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// OpenIssueForTest opens (or changes) an issue through the evaluator's
// normal recording path (its transaction and the issue hook) without the
// detection in front of it (tests of what consumes issues).
func (s *Service) OpenIssueForTest(ctx context.Context, hostID, check, subject, severity, title string, value *float64, observed time.Time) {
	s.eval.openAt(ctx, hostID, check, subject, severity, title, "", value, observed)
}

// ResolveIssueForTest resolves an issue through the normal path (tests).
func (s *Service) ResolveIssueForTest(ctx context.Context, hostID, check, subject string) {
	s.eval.resolve(ctx, hostID, check, subject)
}
