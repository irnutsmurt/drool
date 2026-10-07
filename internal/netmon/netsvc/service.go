// Package netsvc is the controller's network connectivity monitoring
// (M10): what hosts actually talk to, correlated with the firewall policy
// and enriched with local GeoIP/ASN data. It reads the observations the
// firewall service stores (one store, one evaluator): the firewall
// compiler decides policy status, never this package or the browser.
//
// It is visibility, not enforcement: nothing here blocks, scores or
// alerts, and GeoIP/ASN values are context that may be wrong or stale.
package netsvc

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"time"

	"drool/internal/firewall/fwsvc"
	"drool/internal/netmon"
	"drool/internal/netmon/geoip"
	"drool/internal/store"
	"drool/internal/store/db"
)

// Deps are the service's collaborators.
type Deps struct {
	Store    *store.Store
	Firewall *fwsvc.Service
	// GeoIPDir holds the operator's MMDB files (default
	// <data_dir>/geoip). Missing is fine: enrichment is optional.
	GeoIPDir string
	Log      *slog.Logger
}

// Service is network monitoring.
type Service struct {
	d   Deps
	log *slog.Logger
	now func() time.Time
	geo *geoip.Enricher

	mu        sync.Mutex
	lastPrune PruneResult
	kick      chan struct{}
}

// CacheSize bounds the enrichment cache (addresses).
const CacheSize = 50000

// New creates the service and loads the enrichment databases.
func New(d Deps) *Service {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	s := &Service{d: d, log: d.Log, now: time.Now, geo: geoip.NewEnricher(d.GeoIPDir, CacheSize), kick: make(chan struct{}, 1)}
	s.geo.Refresh()
	return s
}

// Enricher returns the GeoIP/ASN enricher.
func (s *Service) Enricher() *geoip.Enricher { return s.geo }

// Settings returns the monitoring settings.
func (s *Service) Settings(ctx context.Context) netmon.Settings {
	return netmon.LoadSettings(ctx, s.d.Store.R)
}

// SaveSettingsTx stores the settings (the caller audits).
func (s *Service) SaveSettingsTx(ctx context.Context, q *db.Queries, in netmon.Settings, by string) error {
	if err := in.Validate(); err != nil {
		return fwsvc.ValidationError("%v", err)
	}
	return netmon.SaveSettingsTx(ctx, q, in, by, s.now().UnixMilli())
}

// SettingsChanged runs after new settings were committed: agents learn
// whether to collect, and retention applies right away.
func (s *Service) SettingsChanged(ctx context.Context) {
	if s.d.Firewall != nil {
		s.d.Firewall.PushCollection(ctx)
	}
	s.Kick()
}

// Kick runs a maintenance pass soon.
func (s *Service) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// PruneResult is the last maintenance pass.
type PruneResult struct {
	At      string `json:"at,omitempty"`
	Expired int64  `json:"expired"` // rows past retention
	Trimmed int64  `json:"trimmed"` // rows over a host's limit
	Rows    int64  `json:"rows"`    // rows kept
	Error   string `json:"error,omitempty"`
}

// LastPrune returns the last maintenance pass.
func (s *Service) LastPrune() PruneResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastPrune
}

// pruneBatch keeps each delete short: collection (inserts from agent
// reports) interleaves with pruning instead of waiting behind it.
const pruneBatch = 2000

// Prune deletes observations past retention, then the least recently seen
// rows of hosts over their limit, in small batches.
func (s *Service) Prune(ctx context.Context) PruneResult {
	set := s.Settings(ctx)
	res := PruneResult{At: s.now().UTC().Format(time.RFC3339)}
	cutoff := s.now().Add(-time.Duration(set.RetentionDays) * 24 * time.Hour).Unix()
	for ctx.Err() == nil {
		n, err := s.d.Store.W.PruneFwObservations(ctx, db.PruneFwObservationsParams{LastSeen: cutoff, Limit: pruneBatch})
		if err != nil {
			res.Error = err.Error()
			break
		}
		res.Expired += n
		if n < pruneBatch {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if per, err := s.d.Store.R.CountObservationsByHost(ctx); err == nil {
		for _, h := range per {
			excess := h.N - int64(set.MaxFlowsPerHost)
			for excess > 0 && ctx.Err() == nil {
				n, err := s.d.Store.W.TrimFwObservations(ctx, db.TrimFwObservationsParams{HostID: h.HostID, Limit: min(excess, pruneBatch)})
				if err != nil || n == 0 {
					break
				}
				res.Trimmed += n
				excess -= n
				time.Sleep(10 * time.Millisecond)
			}
		}
	}
	res.Rows, _ = s.d.Store.R.CountFwObservations(ctx)
	if res.Expired+res.Trimmed > 0 {
		s.log.Info("pruned network observations", "expired", res.Expired, "trimmed", res.Trimmed, "kept", res.Rows)
	}
	s.mu.Lock()
	s.lastPrune = res
	s.mu.Unlock()
	return res
}

// Run maintains observations until ctx ends: scopes after a restart (and
// internal_ranges changes), pruning every 10 minutes, and enrichment
// database reloads when the files change.
func (s *Service) Run(ctx context.Context) {
	if f := os.Getenv("DROOL_TEST_NETFLOWS_FILE"); f != "" && s.d.Firewall != nil {
		go s.runTestFlows(ctx, f)
	}
	if s.d.Firewall != nil {
		if err := s.d.Firewall.Rescope(ctx); err != nil {
			s.log.Warn("could not reclassify observation scopes", "err", err)
		}
	}
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	var lastPrune time.Time
	for {
		if s.geo.Refresh() {
			st := s.geo.Status()
			s.log.Info("GeoIP/ASN databases loaded", "dir", st.Directory, "country", st.Country, "asn", st.ASN, "files", len(st.Files))
		}
		if s.now().Sub(lastPrune) >= 10*time.Minute {
			lastPrune = s.now()
			s.Prune(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-s.kick:
			lastPrune = time.Time{}
		}
	}
}
