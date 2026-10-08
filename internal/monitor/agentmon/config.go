// Package agentmon is the agent's monitoring runtime (M11): it samples
// the host on a schedule, aggregates samples into one-minute points,
// keeps a bounded in-memory outbox while the controller is away, and
// degrades its own fidelity under resource pressure.
//
// Nothing here writes to disk except the optional flash probe, which
// writes one small file in a DROOL-owned directory on the filesystem it
// checks. History lives on the controller; the agent is not a database.
package agentmon

import "fmt"

// Config is what the controller pushes (MonitorConfig.config_json).
// Zero values mean "default".
type Config struct {
	Enabled   bool `json:"enabled"`
	SampleS   int  `json:"sample_s,omitempty"`   // CPU, memory, network, disk IO
	SlowS     int  `json:"slow_s,omitempty"`     // filesystems, sensors, GPUs
	ServicesS int  `json:"services_s,omitempty"` // service states
	StorageS  int  `json:"storage_s,omitempty"`  // block device inventory
	SmartS    int  `json:"smart_s,omitempty"`    // SMART/NVMe queries
	// Probe: mount points to write/read check, and how often.
	ProbeMounts []string `json:"probe_mounts,omitempty"`
	ProbeS      int      `json:"probe_s,omitempty"`
	BufferKB    int      `json:"buffer_kb,omitempty"`
	Limits      *Limits  `json:"limits,omitempty"`
}

// Limits are the agent's resource budget (systemd properties of
// drool-agent.service). Nil fields keep the defaults.
type Limits struct {
	CPUQuotaPct  *int `json:"cpu_quota_percent,omitempty"` // of one CPU
	CPUWeight    *int `json:"cpu_weight,omitempty"`
	MemoryHighMB *int `json:"memory_high_mb,omitempty"`
	MemoryMaxMB  *int `json:"memory_max_mb,omitempty"`
}

// Defaults. The resource defaults come from measuring the agent (see
// ARCHITECTURE-ALPHA §27): they leave several times the measured peak
// as headroom while bounding a runaway agent to a small share of a
// Raspberry Pi.
const (
	DefaultSampleS   = 10
	DefaultSlowS     = 60
	DefaultServicesS = 30
	DefaultStorageS  = 300
	DefaultSmartS    = 1800
	DefaultProbeS    = 3600
	DefaultBufferKB  = 2048

	DefaultCPUQuotaPct  = 25
	DefaultCPUWeight    = 20
	DefaultMemoryHighMB = 96
	DefaultMemoryMaxMB  = 160
)

// WithDefaults fills unset values.
func (c Config) WithDefaults() Config {
	def := func(v *int, d int) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&c.SampleS, DefaultSampleS)
	def(&c.SlowS, DefaultSlowS)
	def(&c.ServicesS, DefaultServicesS)
	def(&c.StorageS, DefaultStorageS)
	def(&c.SmartS, DefaultSmartS)
	def(&c.ProbeS, DefaultProbeS)
	def(&c.BufferKB, DefaultBufferKB)
	return c
}

// Validate checks ranges (the controller validates before pushing; the
// agent checks again and ignores a bad config).
func (c Config) Validate() error {
	c = c.WithDefaults()
	switch {
	case c.SampleS < 5 || c.SampleS > 300:
		return fmt.Errorf("sample interval must be 5–300 s")
	case c.SlowS < 15 || c.SlowS > 3600:
		return fmt.Errorf("slow interval must be 15–3600 s")
	case c.ServicesS < 10 || c.ServicesS > 3600:
		return fmt.Errorf("service interval must be 10–3600 s")
	case c.StorageS < 60 || c.StorageS > 86400:
		return fmt.Errorf("storage interval must be 60–86400 s")
	case c.SmartS < 300 || c.SmartS > 7*86400:
		return fmt.Errorf("SMART interval must be 5 minutes to 7 days")
	case c.ProbeS < 60 || c.ProbeS > 7*86400:
		return fmt.Errorf("probe interval must be 1 minute to 7 days")
	case c.BufferKB < 64 || c.BufferKB > 65536:
		return fmt.Errorf("buffer must be 64 KiB–64 MiB")
	case len(c.ProbeMounts) > 4:
		return fmt.Errorf("at most 4 filesystems can be probed")
	case c.BufferKB > c.Limits.Effective().MemoryHighMB*1024/4:
		// The buffer must fit well inside the budget (the agent's memory
		// guard restarts an agent whose heap stays beyond it).
		return fmt.Errorf("the offline buffer must be at most a quarter of the agent's memory high-water mark (%d MB)", c.Limits.Effective().MemoryHighMB)
	}
	return c.Limits.Validate()
}

// Validate checks a resource budget.
func (l *Limits) Validate() error {
	if l == nil {
		return nil
	}
	if v := l.CPUQuotaPct; v != nil && (*v < 5 || *v > 800) {
		return fmt.Errorf("CPU quota must be 5–800%% of one CPU")
	}
	if v := l.CPUWeight; v != nil && (*v < 1 || *v > 10000) {
		return fmt.Errorf("CPU weight must be 1–10000")
	}
	if v := l.MemoryMaxMB; v != nil && (*v < 64 || *v > 4096) {
		return fmt.Errorf("memory limit must be 64–4096 MB")
	}
	if v := l.MemoryHighMB; v != nil && (*v < 48 || *v > 4096) {
		return fmt.Errorf("memory high-water mark must be 48–4096 MB")
	}
	e := l.Effective()
	if e.MemoryHighMB > e.MemoryMaxMB {
		return fmt.Errorf("the memory high-water mark must not exceed the memory limit")
	}
	return nil
}

// EffectiveLimits are concrete values.
type EffectiveLimits struct {
	CPUQuotaPct, CPUWeight, MemoryHighMB, MemoryMaxMB int
}

// Effective resolves defaults.
func (l *Limits) Effective() EffectiveLimits {
	e := EffectiveLimits{DefaultCPUQuotaPct, DefaultCPUWeight, DefaultMemoryHighMB, DefaultMemoryMaxMB}
	if l == nil {
		return e
	}
	if l.CPUQuotaPct != nil {
		e.CPUQuotaPct = *l.CPUQuotaPct
	}
	if l.CPUWeight != nil {
		e.CPUWeight = *l.CPUWeight
	}
	if l.MemoryHighMB != nil {
		e.MemoryHighMB = *l.MemoryHighMB
	}
	if l.MemoryMaxMB != nil {
		e.MemoryMaxMB = *l.MemoryMaxMB
	}
	return e
}
