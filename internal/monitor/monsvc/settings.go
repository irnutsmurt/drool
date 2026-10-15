package monsvc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"drool/internal/monitor/agentmon"
	"drool/internal/store/db"
)

// Profiles (M11). Since M12.1 the health profile is always Recommended
// plus sparse overrides (policy.go); a stored M11 "custom" profile is
// turned into global overrides once at start (migrateLegacy).
const (
	ProfileRecommended = "recommended"
	ProfileCustom      = "custom"
)

// Thresholds is M11's complete custom profile, kept only to read and
// migrate stored settings.
type Thresholds struct {
	CPUPercent       float64 `json:"cpu_percent"`
	CPUMinutes       int     `json:"cpu_minutes"`
	MemoryPercent    float64 `json:"memory_percent"`
	MemoryCritical   float64 `json:"memory_critical_percent"`
	MemoryMinutes    int     `json:"memory_minutes"`
	DiskWarning      float64 `json:"disk_warning_percent"`
	DiskCritical     float64 `json:"disk_critical_percent"`
	InodeWarning     float64 `json:"inode_warning_percent"`
	InodeCritical    float64 `json:"inode_critical_percent"`
	ServiceMinutes   int     `json:"service_failed_minutes"`
	WatchedMinutes   int     `json:"watched_stopped_minutes"`
	InterfaceMinutes int     `json:"interface_down_minutes"`
	TempMinutes      int     `json:"temperature_minutes"`
	StaleIntervals   int     `json:"stale_intervals"`
}

// Recommended is M11's prebuilt profile as Thresholds (the same values as
// the Recommended health configuration).
func Recommended() Thresholds {
	return Thresholds{CPUPercent: 95, CPUMinutes: 5, MemoryPercent: 90, MemoryCritical: 97, MemoryMinutes: 5,
		DiskWarning: 85, DiskCritical: 95, InodeWarning: 90, InodeCritical: 97, ServiceMinutes: 1, WatchedMinutes: 2,
		InterfaceMinutes: 1, TempMinutes: 2, StaleIntervals: 3}
}

// Validate checks thresholds.
func (t Thresholds) Validate() error {
	pct := func(v float64, name string) error {
		if v < 1 || v > 100 {
			return fmt.Errorf("%s must be 1–100%%", name)
		}
		return nil
	}
	mins := func(v int, name string) error {
		if v < 0 || v > 1440 {
			return fmt.Errorf("%s must be 0–1440 minutes", name)
		}
		return nil
	}
	return errors.Join(pct(t.CPUPercent, "CPU"), pct(t.MemoryPercent, "memory warning"), pct(t.MemoryCritical, "memory critical"),
		pct(t.DiskWarning, "disk warning"), pct(t.DiskCritical, "disk critical"), pct(t.InodeWarning, "inode warning"),
		pct(t.InodeCritical, "inode critical"), mins(t.CPUMinutes, "CPU duration"), mins(t.MemoryMinutes, "memory duration"),
		mins(t.ServiceMinutes, "service duration"), mins(t.WatchedMinutes, "watched service duration"),
		mins(t.InterfaceMinutes, "interface duration"), mins(t.TempMinutes, "temperature duration"),
		func() error {
			if t.DiskWarning > t.DiskCritical || t.InodeWarning > t.InodeCritical || t.MemoryPercent > t.MemoryCritical {
				return errors.New("warning levels must not exceed critical levels")
			}
			if t.StaleIntervals < 2 || t.StaleIntervals > 20 {
				return errors.New("stale after must be 2–20 missed intervals")
			}
			return nil
		}())
}

// Settings is the fleet's monitoring configuration (audited when changed).
type Settings struct {
	Enabled bool `json:"enabled"`
	// Profile and Custom are M11's (read once and migrated; never stored
	// again).
	Profile       string      `json:"profile,omitempty"`
	Custom        *Thresholds `json:"custom,omitempty"`
	RetentionDays int         `json:"retention_days"`
	// Agent sampling (agentmon.Config) and the fleet's default agent budget.
	SampleS   int              `json:"sample_s"`
	SlowS     int              `json:"slow_s"`
	ServicesS int              `json:"services_s"`
	StorageS  int              `json:"storage_s"`
	SmartS    int              `json:"smart_s"`
	BufferKB  int              `json:"buffer_kb"`
	Limits    *agentmon.Limits `json:"limits,omitempty"`
	// SSH polling (never continuous).
	SSHPollS  int  `json:"ssh_poll_s"`
	SSHSmartS int  `json:"ssh_smart_s"`
	SSHSudo   bool `json:"ssh_sudo"` // SMART and kernel messages over SSH with sudo when allowed
}

// Default retention and SSH cadence.
const (
	DefaultRetentionDays = 30
	DefaultSSHPollS      = 300
	DefaultSSHSmartS     = 3600
)

// DefaultSettings: monitoring on, recommended thresholds.
func DefaultSettings() Settings {
	return Settings{Enabled: true, RetentionDays: DefaultRetentionDays,
		SampleS: agentmon.DefaultSampleS, SlowS: agentmon.DefaultSlowS, ServicesS: agentmon.DefaultServicesS,
		StorageS: agentmon.DefaultStorageS, SmartS: agentmon.DefaultSmartS, BufferKB: agentmon.DefaultBufferKB,
		SSHPollS: DefaultSSHPollS, SSHSmartS: DefaultSSHSmartS, SSHSudo: true}
}

// Validate checks the settings.
func (s Settings) Validate() error {
	if s.Profile != "" && s.Profile != ProfileRecommended && s.Profile != ProfileCustom {
		return errors.New("profile must be recommended")
	}
	if s.RetentionDays < 7 || s.RetentionDays > 400 {
		return errors.New("retention must be 7–400 days")
	}
	if s.SSHPollS < 60 || s.SSHPollS > 3600 {
		return errors.New("the SSH poll interval must be 1–60 minutes")
	}
	if s.SSHSmartS < 600 || s.SSHSmartS > 7*86400 {
		return errors.New("the SSH storage health interval must be 10 minutes to 7 days")
	}
	if s.Profile == ProfileCustom {
		if s.Custom == nil {
			return errors.New("a custom profile needs its thresholds")
		}
		if err := s.Custom.Validate(); err != nil {
			return err
		}
	}
	return s.AgentConfig(HostSettings{}).Validate()
}

// AgentConfig is what a host's agent receives.
func (s Settings) AgentConfig(h HostSettings) agentmon.Config {
	c := agentmon.Config{Enabled: s.Enabled, SampleS: s.SampleS, SlowS: s.SlowS, ServicesS: s.ServicesS, StorageS: s.StorageS,
		SmartS: s.SmartS, BufferKB: s.BufferKB, Limits: s.Limits, ProbeMounts: h.ProbeMounts, ProbeS: h.ProbeS}
	if h.Limits != nil {
		c.Limits = h.Limits
	}
	return c
}

const settingKey = "monitoring"

// LoadSettings reads the settings (defaults for anything unset).
func LoadSettings(ctx context.Context, q *db.Queries) Settings {
	s := DefaultSettings()
	if row, err := q.GetSetting(ctx, settingKey); err == nil {
		json.Unmarshal([]byte(row.ValueJson), &s)
	}
	if s.Validate() != nil {
		return DefaultSettings()
	}
	return s
}

// SaveSettingsTx stores the settings. Health thresholds are not part of
// them any more: they are overrides (a custom profile is refused).
func SaveSettingsTx(ctx context.Context, q *db.Queries, s Settings, by string, now int64) error {
	if s.Profile == ProfileCustom {
		return errors.New("health thresholds are configured as overrides (Settings → Monitoring → Health checks), not as a custom profile")
	}
	s.Profile, s.Custom = "", nil
	if err := s.Validate(); err != nil {
		return err
	}
	b, _ := json.Marshal(s)
	return q.UpsertSetting(ctx, db.UpsertSettingParams{Key: settingKey, ValueJson: string(b),
		UpdatedBy: sql.NullString{String: by, Valid: by != ""}, UpdatedAt: now})
}

// HostSettings are one host's monitoring choices.
type HostSettings struct {
	// WatchedServices are the units this host's own settings expect to run
	// (expected state "running", a health setting since the final After-M17
	// batch): reading lists them, writing sets or clears that setting.
	WatchedServices []string `json:"watched_services"`
	// ProbeMounts get the write/read flash probe every ProbeS seconds
	// (default hourly; agent hosts only).
	ProbeMounts []string         `json:"probe_mounts"`
	ProbeS      int              `json:"probe_s,omitempty"`
	Limits      *agentmon.Limits `json:"limits,omitempty"`
	// Turning a check off for a host is a health override since M12.1
	// (M11's disabled_checks are migrated at start).
}

// ProbeWarning is shown when the probe runs more often than hourly.
const ProbeWarning = "Frequent active storage probes create additional writes to flash media. Once per hour is recommended for SD cards. Shorter intervals may contribute unnecessary wear."

// Validate checks a host's settings; warning explains a short probe
// interval (allowed, but not recommended).
func (h *HostSettings) Validate() (warning string, err error) {
	if len(h.WatchedServices) > 200 {
		return "", errors.New("at most 200 watched services")
	}
	for i, u := range h.WatchedServices {
		u = strings.TrimSpace(u)
		if u != "" && !strings.Contains(u, ".") {
			u += ".service"
		}
		if u == "" || len(u) > 255 || strings.ContainsAny(u, " /\\\x00") {
			return "", fmt.Errorf("%q is not a service name", h.WatchedServices[i])
		}
		h.WatchedServices[i] = u
	}
	sort.Strings(h.WatchedServices)
	if len(h.ProbeMounts) > 4 {
		return "", errors.New("at most 4 filesystems can be probed")
	}
	for _, m := range h.ProbeMounts {
		if !strings.HasPrefix(m, "/") || strings.Contains(m, "..") {
			return "", fmt.Errorf("%q is not a mount point", m)
		}
	}
	if h.ProbeS == 0 {
		h.ProbeS = agentmon.DefaultProbeS
	}
	if h.ProbeS < 60 || h.ProbeS > 7*86400 {
		return "", errors.New("the probe interval must be 1 minute to 7 days")
	}
	if err := h.Limits.Validate(); err != nil {
		return "", err
	}
	if len(h.ProbeMounts) > 0 && h.ProbeS < 3000 {
		warning = ProbeWarning
	}
	return warning, nil
}

// Watched reports whether a unit is watched.
func (h HostSettings) Watched(unit string) bool {
	for _, u := range h.WatchedServices {
		if u == unit {
			return true
		}
	}
	return false
}
