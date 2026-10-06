package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"drool/internal/audit"
	"drool/internal/model"
	"drool/internal/store/db"
)

// factsSummaryJSON is the normalized part of a host's facts.
type factsSummaryJSON struct {
	CollectedAt       string   `json:"collected_at"`
	OSFamily          string   `json:"os_family"`
	Distro            string   `json:"distro"`
	DistroVersion     string   `json:"distro_version"`
	DistroName        string   `json:"distro_name"`
	Kernel            string   `json:"kernel"`
	Arch              string   `json:"arch"`
	Hostname          string   `json:"hostname"`
	CPUCount          *int64   `json:"cpu_count"`
	MemBytes          *int64   `json:"mem_bytes"`
	PkgManager        string   `json:"pkg_manager"`
	InitSystem        string   `json:"init_system"`
	ContainerRuntimes []string `json:"container_runtimes"`
}

// healthJSON is the latest polled status.
type healthJSON struct {
	CheckedAt        string   `json:"checked_at"`
	Stale            bool     `json:"stale"`
	Reachable        bool     `json:"reachable"`
	UptimeS          *int64   `json:"uptime_s"`
	Load1            *float64 `json:"load1"`
	MemUsedPct       *float64 `json:"mem_used_pct"`
	MaxFSUsedPct     *float64 `json:"max_fs_used_pct"`
	MaxFSMount       string   `json:"max_fs_mount"`
	FailedUnits      *int64   `json:"failed_units"`
	FailedUnitNames  []string `json:"failed_unit_names"`
	RebootRequired   *bool    `json:"reboot_required"`
	RebootReason     string   `json:"reboot_reason"`
	UpdatesAvailable *int64   `json:"updates_available"`
	SecurityUpdates  *int64   `json:"security_updates"`
	UpdatesCheckedAt *string  `json:"updates_checked_at"`
	LastError        string   `json:"last_error"`
	LastErrorClass   string   `json:"last_error_class"`
}

func i64(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}

func f64(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	return &v.Float64
}

func splitList(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Split(s, ",")
}

func toFactsSummary(f *db.HostFact) *factsSummaryJSON {
	if f == nil {
		return nil
	}
	return &factsSummaryJSON{CollectedAt: msTime(f.CollectedAt), OSFamily: f.OsFamily, Distro: f.Distro,
		DistroVersion: f.DistroVersion, DistroName: f.DistroName, Kernel: f.Kernel, Arch: f.Arch,
		Hostname: f.Hostname, CPUCount: i64(f.CpuCount), MemBytes: i64(f.MemBytes), PkgManager: f.PkgManager,
		InitSystem: f.InitSystem, ContainerRuntimes: splitList(f.ContainerRuntimes)}
}

func (s *Server) toHealth(h *db.HostStatus) *healthJSON {
	if h == nil {
		return nil
	}
	out := &healthJSON{CheckedAt: msTime(h.CheckedAt), Reachable: h.Reachable != 0,
		Stale:   time.Since(time.UnixMilli(h.CheckedAt)) > s.d.StaleAfter,
		UptimeS: i64(h.UptimeS), Load1: f64(h.Load1), MemUsedPct: f64(h.MemUsedPct),
		MaxFSUsedPct: f64(h.MaxFsUsedPct), MaxFSMount: h.MaxFsMount, FailedUnits: i64(h.FailedUnits),
		FailedUnitNames: splitList(h.FailedUnitNames), RebootReason: h.RebootReason,
		UpdatesAvailable: i64(h.UpdatesAvailable), SecurityUpdates: i64(h.SecurityUpdates),
		UpdatesCheckedAt: nullMsTime(h.UpdatesCheckedAt), LastError: h.LastError, LastErrorClass: h.LastErrorClass}
	if h.RebootRequired.Valid {
		b := h.RebootRequired.Int64 != 0
		out.RebootRequired = &b
	}
	return out
}

type addressJSON struct {
	Address     string `json:"address"`
	Family      int64  `json:"family"`
	Interface   string `json:"interface"`
	Source      string `json:"source"`
	FirstSeenAt string `json:"first_seen_at"`
	LastSeenAt  string `json:"last_seen_at"`
}

type factsResponse struct {
	CollectedAt *string         `json:"collected_at"`
	Facts       json.RawMessage `json:"facts"` // full discovery output; null before the first discovery
	Addresses   []addressJSON   `json:"addresses"`
}

func (s *Server) handleHostFacts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if _, err := s.d.Inventory.GetHost(ctx, id); err != nil {
		writeProblem(w, r, s.inventoryError(err))
		return
	}
	out := factsResponse{Facts: json.RawMessage("null"), Addresses: []addressJSON{}}
	if f, err := s.d.Store.R.GetHostFacts(ctx, id); err == nil {
		t := msTime(f.CollectedAt)
		out.CollectedAt, out.Facts = &t, json.RawMessage(f.FactsJson)
	} else if !errors.Is(err, sql.ErrNoRows) {
		writeProblem(w, r, errInternal)
		return
	}
	addrs, err := s.d.Store.R.ListHostAddresses(ctx, id)
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	for _, a := range addrs {
		out.Addresses = append(out.Addresses, addressJSON{Address: a.Address, Family: a.Family, Interface: a.Interface,
			Source: a.Source, FirstSeenAt: msTime(a.FirstSeenAt), LastSeenAt: msTime(a.LastSeenAt)})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRefreshHost runs discovery, a status check and an update check now
// (no package metadata refresh). It doesn't wait for a busy host.
func (s *Server) handleRefreshHost(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	v, err := s.d.Inventory.GetHost(ctx, id)
	if err != nil {
		writeProblem(w, r, s.inventoryError(err))
		return
	}
	if v.PinnedKey == nil {
		writeProblem(w, r, errConflict("no_host_key", "approve the host key first"))
		return
	}
	release, ok := s.d.Engine.Locks().TryAcquire(id, "refresh", model.LockShared)
	if !ok {
		writeProblem(w, r, errConflict("host_busy", "a job is changing this host; try again when it finishes"))
		return
	}
	defer release()

	facts, derr := s.d.Inventory.Discover(ctx, id)
	st, serr := s.d.Inventory.CheckStatus(ctx, id)
	var warnings []string
	err = s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		if derr == nil {
			if err := s.d.Inventory.StoreFactsTx(ctx, q, id, facts); err != nil {
				return audit.Event{}, err
			}
			warnings = facts.Warnings
		}
		if _, err := s.d.Inventory.RecordStatusTx(ctx, q, id, st, serr); err != nil {
			return audit.Event{}, err
		}
		result := audit.Success
		if derr != nil || serr != nil {
			result = audit.Failure
		}
		return audit.Event{Action: "host.refresh", Result: result, TargetType: "host", TargetID: id,
			Details: map[string]any{"discovery_error": errString(derr), "status_error": errString(serr)}}, nil
	})
	if err != nil {
		writeProblem(w, r, s.inventoryError(err))
		return
	}
	if derr == nil && (facts.PkgManager == "apt" || facts.PkgManager == "dnf" || facts.PkgManager == "yum") {
		if u, err := s.d.Inventory.CheckUpdates(ctx, id, facts.PkgManager); err == nil {
			_ = s.d.Store.InTx(ctx, func(q *db.Queries) error { return s.d.Inventory.RecordUpdatesTx(ctx, q, id, u) })
		}
	}
	if derr != nil && serr != nil {
		writeProblem(w, r, s.inventoryError(serr))
		return
	}
	v, err = s.d.Inventory.GetHost(ctx, id)
	if err != nil {
		writeProblem(w, r, s.inventoryError(err))
		return
	}
	if warnings == nil {
		warnings = []string{}
	}
	writeJSON(w, http.StatusOK, refreshResponse{Host: s.toHostJSON(*v), Warnings: warnings})
}

type refreshResponse struct {
	Host     hostJSON `json:"host"`
	Warnings []string `json:"warnings"`
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
