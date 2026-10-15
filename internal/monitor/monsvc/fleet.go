package monsvc

import (
	"context"
	"sort"
	"strings"

	"drool/internal/monitor/storage"
)

// FleetHost is one row of the fleet health page.
type FleetHost struct {
	HostID      string   `json:"host_id"`
	Name        string   `json:"name"`
	Source      string   `json:"source"` // agent | ssh | "" (no data yet)
	Mode        string   `json:"mode"`   // continuous | polled | none
	UpdatedAt   int64    `json:"updated_at"`
	Stale       bool     `json:"stale"`
	CPU         *float64 `json:"cpu,omitempty"`
	Memory      *float64 `json:"memory,omitempty"`
	Disk        *float64 `json:"disk,omitempty"`
	DiskMount   string   `json:"disk_mount,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	Services    int      `json:"services"`
	// Failed counts units in the failed state, monitored or not (the
	// inventory's fact); ServiceIssues the monitored services' problems,
	// the only ones that count as unhealthy.
	Failed        int      `json:"failed_services"`
	ServiceIssues int      `json:"service_issues"`
	StorageText   string   `json:"storage"` // worst device state
	Issues        int      `json:"issues"`
	Worst         string   `json:"worst"` // ok | info | warning | critical
	Reduced       bool     `json:"reduced"`
	Helpers       []string `json:"helpers"` // helpers that would add capability
	Categories    []string `json:"categories"`
}

// FleetSummary counts what needs attention across the fleet.
type FleetSummary struct {
	Hosts       int            `json:"hosts"`
	Monitored   int            `json:"monitored"`
	Agent       int            `json:"agent"`
	SSH         int            `json:"ssh"`
	Counts      map[string]int `json:"counts"` // per category
	Rows        []FleetHost    `json:"rows"`
	Total       int            `json:"total"` // rows matching the filter
	Issues      []Issue        `json:"issues"`
	GeneratedAt int64          `json:"generated_at"`
}

// Categories of attention.
var FleetCategories = []string{"unhealthy", "stale", "cpu", "memory", "disk", "storage", "services", "network", "temperature", "fidelity", "helpers", "no_data"}

var checkCategory = map[string]string{
	CheckCPU: "cpu", CheckMemory: "memory", CheckFilesystem: "disk", CheckInodes: "disk", CheckFSReadOnly: "storage",
	CheckDeviceReadOnly: "storage", CheckStorageHealth: "storage", CheckMediaErrors: "storage", CheckKernelStorage: "storage",
	CheckProbe: "storage", CheckServiceFailed: "services", CheckServiceStopped: "services", CheckServiceUnexpected: "services", CheckInterfaceDown: "network",
	CheckTemperature: "temperature", CheckStale: "stale", CheckFidelity: "fidelity", CheckGPUUtil: "gpu",
}

// CheckCategory is the category a check belongs to (cpu, memory, disk,
// storage, services, network, temperature, gpu, stale, fidelity).
func CheckCategory(check string) string {
	if c, ok := checkCategory[check]; ok {
		return c
	}
	return "other"
}

// CheckCategories lists the categories of the checks.
var CheckCategories = []string{"cpu", "memory", "disk", "storage", "services", "network", "temperature", "gpu", "stale", "fidelity"}

// FleetFilter selects rows.
type FleetFilter struct {
	Category string
	Query    string
	Sort     string // name | cpu | memory | disk | temperature | issues
	Limit    int
	Offset   int
}

// Fleet builds the fleet page: counts over every host, rows filtered,
// sorted and paged on the server (5–200 hosts stay fast: everything comes
// from the in-memory cache plus one issues query).
func (s *Service) Fleet(ctx context.Context, f FleetFilter) (*FleetSummary, error) {
	hosts, err := s.d.Store.R.ListHosts(ctx)
	if err != nil {
		return nil, err
	}
	set := s.Settings(ctx)
	pol := s.eval.current(ctx)
	issues, err := s.Issues(ctx, IssueFilter{Limit: 500})
	if err != nil {
		return nil, err
	}
	byHost := map[string][]Issue{}
	for _, i := range issues {
		byHost[i.HostID] = append(byHost[i.HostID], i)
	}
	failed := s.failedCounts(ctx)
	sum := &FleetSummary{Counts: map[string]int{}, Rows: []FleetHost{}, GeneratedAt: s.now().UnixMilli()}
	var rows []FleetHost
	now := s.now()
	for _, h := range hosts {
		if h.JumpOnly != 0 {
			continue
		}
		sum.Hosts++
		r := FleetHost{HostID: h.ID, Name: h.Name, Mode: "none", Worst: "ok", Helpers: []string{}, Categories: []string{}}
		s.mu.Lock()
		c := s.cur[h.ID]
		if c != nil && c.state != nil && c.state.At > 0 {
			st := c.state
			r.Source, r.UpdatedAt = c.source, c.at.UnixMilli()
			r.Mode = st.Fidelity.Mode
			r.Reduced = st.Fidelity.Reduced
			if st.CPU != nil && st.CPU.Percent != nil {
				v := *st.CPU.Percent
				r.CPU = &v
			}
			if st.Memory != nil {
				v := st.Memory.UsedPercent
				r.Memory = &v
			}
			for _, fs := range st.Filesystems {
				if fs.TotalBytes > 0 && (r.Disk == nil || fs.UsedPercent > *r.Disk) {
					v := fs.UsedPercent
					r.Disk, r.DiskMount = &v, fs.Mount
				}
			}
			for _, sn := range st.Sensors {
				if sn.Kind == "cpu" && (r.Temperature == nil || sn.Celsius > *r.Temperature) {
					v := sn.Celsius
					r.Temperature = &v
				}
			}
			for _, hp := range st.Helpers {
				if hp.Useful {
					r.Helpers = append(r.Helpers, hp.Package)
				}
			}
			if c.storage != nil {
				r.StorageText = worstDevice(c.storage.Devices)
			}
			r.Stale = now.Sub(c.at) > staleAfter(pol, h.ID, c.source, set.SSHPollS)
		}
		s.mu.Unlock()
		if r.Source == "" {
			r.Categories = append(r.Categories, "no_data")
		} else {
			sum.Monitored++
			if r.Source == "agent" {
				sum.Agent++
			} else {
				sum.SSH++
			}
		}
		fc := failed[h.ID]
		r.Services, r.Failed = fc[0], fc[1]
		cats := map[string]bool{}
		for _, i := range byHost[h.ID] {
			r.Issues++
			if checkCategory[i.Check] == "services" {
				r.ServiceIssues++
			}
			r.Worst = worstStatus(r.Worst, i.Severity)
			if c := checkCategory[i.Check]; c != "" {
				cats[c] = true
			}
		}
		if r.Stale {
			cats["stale"] = true
		}
		if r.Reduced {
			cats["fidelity"] = true
		}
		if len(r.Helpers) > 0 {
			cats["helpers"] = true
		}
		if r.Worst == "warning" || r.Worst == "critical" {
			cats["unhealthy"] = true
		}
		for _, cat := range FleetCategories {
			if cats[cat] {
				r.Categories = append(r.Categories, cat)
			}
		}
		for _, cat := range r.Categories {
			sum.Counts[cat]++
		}
		rows = append(rows, r)
	}
	q := strings.ToLower(strings.TrimSpace(f.Query))
	var keep []FleetHost
	for _, r := range rows {
		if q != "" && !strings.Contains(strings.ToLower(r.Name), q) {
			continue
		}
		if f.Category != "" && f.Category != "all" && !contains(r.Categories, f.Category) {
			continue
		}
		keep = append(keep, r)
	}
	rank := map[string]int{"critical": 0, "warning": 1, "info": 2, "ok": 3}
	val := func(p *float64) float64 {
		if p == nil {
			return -1
		}
		return *p
	}
	sort.SliceStable(keep, func(i, j int) bool {
		a, b := keep[i], keep[j]
		switch f.Sort {
		case "cpu":
			return val(a.CPU) > val(b.CPU)
		case "memory":
			return val(a.Memory) > val(b.Memory)
		case "disk":
			return val(a.Disk) > val(b.Disk)
		case "temperature":
			return val(a.Temperature) > val(b.Temperature)
		case "name":
			return a.Name < b.Name
		}
		// Default: what needs attention first.
		if rank[a.Worst] != rank[b.Worst] {
			return rank[a.Worst] < rank[b.Worst]
		}
		if a.Stale != b.Stale {
			return a.Stale
		}
		if a.Issues != b.Issues {
			return a.Issues > b.Issues
		}
		return a.Name < b.Name
	})
	sum.Total = len(keep)
	limit := clampInt(f.Limit, 1, 500, 100)
	off := max(0, f.Offset)
	if off < len(keep) {
		sum.Rows = keep[off:min(len(keep), off+limit)]
	}
	if len(issues) > 50 {
		issues = issues[:50]
	}
	sum.Issues = issues
	return sum, nil
}

func contains(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}

func worstDevice(ds []storage.Device) string {
	order := []string{storage.StateFailing, storage.StateWarning, storage.StateUnknown, storage.StateNoIndicators, storage.StateOK}
	have := map[string]bool{}
	for _, d := range ds {
		have[d.Health.State] = true
	}
	for _, o := range order {
		if have[o] {
			return o
		}
	}
	return ""
}

// failedCounts returns per host [default-view units, failed units].
func (s *Service) failedCounts(ctx context.Context) map[string][2]int {
	out := map[string][2]int{}
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT host_id,
		SUM(CASE WHEN known = 1 AND (active_state = 'failed' OR sub_state = 'failed') THEN 1 ELSE 0 END),
		SUM(CASE WHEN file_state IN ('enabled','enabled-runtime','alias','indirect','linked') OR active_state IN ('active','failed') THEN 1 ELSE 0 END)
		FROM mon_services WHERE transient = 0 GROUP BY host_id`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var h string
		var f, n int
		rows.Scan(&h, &f, &n)
		out[h] = [2]int{n, f}
	}
	return out
}
