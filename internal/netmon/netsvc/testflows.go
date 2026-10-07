package netsvc

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"drool/internal/firewall/observe"
)

// Test mode (DROOL_TEST_NETFLOWS_FILE): browser tests need hundreds of
// public addresses across many networks, which no isolated test network
// can produce. The controller ingests reports from a JSON file — exactly
// as if the named hosts' agents had sent them — whenever the file changes:
//
//	{"web-1": {"source": "conntrack", "flows": [{"direction": "out", ...}]}}
//
// first_seen/last_seen at or below zero are relative to now (-3600 = an
// hour ago). Never set in production; logged loudly when set.

type testReport struct {
	Source string         `json:"source"`
	Flows  []observe.Flow `json:"flows"`
}

func (s *Service) runTestFlows(ctx context.Context, path string) {
	s.log.Warn("TEST MODE: ingesting network observations from a file", "file", path)
	var applied time.Time
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		if fi, err := os.Stat(path); err == nil && fi.ModTime().After(applied) {
			applied = fi.ModTime()
			s.ingestTestFile(ctx, path)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (s *Service) ingestTestFile(ctx context.Context, path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var in map[string]testReport
	if err := json.Unmarshal(b, &in); err != nil {
		s.log.Warn("TEST MODE: unreadable flows file", "err", err)
		return
	}
	hosts, err := s.d.Store.R.ListHosts(ctx)
	if err != nil {
		return
	}
	now := s.now().Unix()
	for _, h := range hosts {
		r, ok := in[h.Name]
		if !ok {
			continue
		}
		for i := range r.Flows {
			if r.Flows[i].FirstSeen <= 0 {
				r.Flows[i].FirstSeen += now
			}
			if r.Flows[i].LastSeen <= 0 {
				r.Flows[i].LastSeen += now
			}
		}
		if r.Source == "" {
			r.Source = observe.SourceConntrack
		}
		payload, _ := json.Marshal(observe.Report{Source: r.Source, Every: 30, Flows: r.Flows})
		s.d.Firewall.OnTelemetry(h.ID, "netflows", payload)
		s.log.Warn("TEST MODE: ingested network observations", "host", h.Name, "flows", len(r.Flows))
	}
}
