// Package monitor is DROOL's system monitoring model (M11): the typed
// telemetry both management paths produce and the controller stores.
//
// One implementation collects it. The agent runs the collectors against
// the local machine continuously; the SSH path runs a generated script
// that dumps the same /proc and sysfs files and helper outputs, and the
// controller runs the very same collectors against that dump
// (internal/monitor/source). Both therefore produce identical values;
// they differ only in how often they sample, which every report states
// (Fidelity).
//
// Unknown is never zero. A value that could not be read is absent: a nil
// pointer, a missing series key, an empty list with a note.
package monitor

// Series keys: a metric name, optionally ":" and a label (a mount point,
// an interface, a disk, a sensor id, a GPU index). Values are averaged
// over a point's window, with the window's maximum kept beside them.
const (
	KeyCPU    = "cpu"        // % of all CPUs busy
	KeyIOWait = "cpu.iowait" // % of CPU time waiting for IO
	KeyMem    = "mem"        // % of memory in use (MemTotal − MemAvailable)
	KeySwap   = "swap"       // % of swap in use
	KeyLoad1  = "load1"
	KeyLoad5  = "load5"
	KeyLoad15 = "load15"
	KeyPSICPU = "psi.cpu" // pressure stall: % of time some tasks waited (avg10)
	KeyPSIMem = "psi.mem"
	KeyPSIIO  = "psi.io"

	PrefixFS       = "fs:"        // + mount point: % used
	PrefixInodes   = "inodes:"    // + mount point: % of inodes used
	PrefixNetRx    = "net.rx:"    // + interface: bytes/s received
	PrefixNetTx    = "net.tx:"    // + interface: bytes/s sent
	PrefixNetErr   = "net.err:"   // + interface: errors/s (rx+tx)
	PrefixNetDrop  = "net.drop:"  // + interface: drops/s (rx+tx)
	PrefixDiskR    = "disk.r:"    // + disk: bytes/s read
	PrefixDiskW    = "disk.w:"    // + disk: bytes/s written
	PrefixDiskBusy = "disk.busy:" // + disk: % of time busy
	PrefixTemp     = "temp:"      // + sensor id: °C
	PrefixGPUUtil  = "gpu.util:"  // + index: %
	PrefixGPUMem   = "gpu.mem:"   // + index: % VRAM used
	PrefixGPUTemp  = "gpu.temp:"  // + index: °C
	PrefixGPUPower = "gpu.power:" // + index: W
)

// Point is one window of samples: average and maximum per series.
type Point struct {
	T int64                 `json:"t"` // window start, unix seconds
	S int                   `json:"s"` // window length, seconds
	N int                   `json:"n"` // samples aggregated
	V map[string][2]float64 `json:"v"` // key → [avg, max]
}

// Gap is a period whose routine telemetry was lost (an agent's offline
// buffer overflowed, or it was reducing fidelity).
type Gap struct {
	From   int64  `json:"from"` // unix seconds
	To     int64  `json:"to"`
	Reason string `json:"reason"`
}

// Batch is the agent's "metrics" telemetry: points and any gaps.
type Batch struct {
	Points []Point `json:"points"`
	Gaps   []Gap   `json:"gaps,omitempty"`
}

// State is a host's current monitoring picture: the latest values and
// everything that isn't a time series. Agents send it with every batch;
// the SSH path builds it on every poll.
type State struct {
	Source   string   `json:"source"` // agent | ssh
	At       int64    `json:"at"`     // unix milliseconds
	BootID   string   `json:"boot_id,omitempty"`
	Fidelity Fidelity `json:"fidelity"`

	CPU         *CPU        `json:"cpu,omitempty"`
	Load        []float64   `json:"load,omitempty"` // 1, 5, 15 minutes
	Memory      *Memory     `json:"memory,omitempty"`
	UptimeS     *float64    `json:"uptime_s,omitempty"`
	Pressure    *Pressure   `json:"pressure,omitempty"`
	Filesystems []FS        `json:"filesystems"`
	Interfaces  []Interface `json:"interfaces"`
	Disks       []DiskIO    `json:"disks"`
	Sensors     []Sensor    `json:"sensors"`
	GPUs        []GPU       `json:"gpus"`
	// GPUNote explains missing GPU telemetry ("" when there is no GPU or
	// telemetry works).
	GPUNote    string            `json:"gpu_note,omitempty"`
	Collectors []CollectorStatus `json:"collectors"`
	Helpers    []Helper          `json:"helpers"`
	Agent      *AgentResources   `json:"agent,omitempty"`
}

// Fidelity says how a host is observed. SSH polling is never continuous.
type Fidelity struct {
	Mode    string   `json:"mode"`     // continuous | polled
	SampleS int      `json:"sample_s"` // seconds between samples
	Reduced bool     `json:"reduced"`  // the agent is saving resources
	Reasons []string `json:"reasons,omitempty"`
	Since   int64    `json:"since,omitempty"` // reduced since (unix seconds)
}

// CPU is the CPU picture.
type CPU struct {
	Percent *float64   `json:"percent,omitempty"`
	IOWait  *float64   `json:"iowait,omitempty"`
	Steal   *float64   `json:"steal,omitempty"`
	Cores   []*float64 `json:"cores,omitempty"` // per core %, nil while unknown
	Count   int        `json:"count"`
}

// Memory uses MemAvailable: reclaimable cache is available memory.
type Memory struct {
	TotalBytes     int64    `json:"total_bytes"`
	AvailableBytes int64    `json:"available_bytes"`
	UsedPercent    float64  `json:"used_percent"`
	Estimated      bool     `json:"estimated,omitempty"` // no MemAvailable (very old kernel)
	SwapTotalBytes int64    `json:"swap_total_bytes"`
	SwapFreeBytes  int64    `json:"swap_free_bytes"`
	SwapPercent    *float64 `json:"swap_percent,omitempty"`
}

// Pressure is Linux PSI ("some" avg10 and avg60, %), when the kernel has it.
type Pressure struct {
	CPU10    *float64 `json:"cpu10,omitempty"`
	CPU60    *float64 `json:"cpu60,omitempty"`
	Memory10 *float64 `json:"memory10,omitempty"`
	Memory60 *float64 `json:"memory60,omitempty"`
	IO10     *float64 `json:"io10,omitempty"`
	IO60     *float64 `json:"io60,omitempty"`
}

// FS is a mounted filesystem.
type FS struct {
	Mount         string   `json:"mount"`
	Device        string   `json:"device"` // mount source, e.g. /dev/sda1
	DevNum        string   `json:"devnum"` // major:minor
	Type          string   `json:"type"`
	TotalBytes    int64    `json:"total_bytes"`
	UsedBytes     int64    `json:"used_bytes"`
	AvailBytes    int64    `json:"avail_bytes"`
	UsedPercent   float64  `json:"used_percent"`
	InodesPercent *float64 `json:"inodes_percent,omitempty"`
	ReadOnly      bool     `json:"read_only"`
	// Unresponsive: the last capacity check did not return (a hung
	// device); values are from before.
	Unresponsive bool `json:"unresponsive,omitempty"`
}

// Interface is a network interface.
type Interface struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`  // physical | virtual | loopback
	State     string   `json:"state"` // up | down | lowerlayerdown | dormant | unknown
	Carrier   *bool    `json:"carrier,omitempty"`
	SpeedMbps *int     `json:"speed_mbps,omitempty"`
	MTU       int      `json:"mtu,omitempty"`
	RxBps     *float64 `json:"rx_bps,omitempty"` // bytes/s
	TxBps     *float64 `json:"tx_bps,omitempty"`
	ErrPS     *float64 `json:"err_ps,omitempty"`
	DropPS    *float64 `json:"drop_ps,omitempty"`
	RxErrors  uint64   `json:"rx_errors"` // totals since the interface appeared
	TxErrors  uint64   `json:"tx_errors"`
	RxDrops   uint64   `json:"rx_drops"`
	TxDrops   uint64   `json:"tx_drops"`
}

// DiskIO is a physical disk's throughput.
type DiskIO struct {
	Name     string   `json:"name"`
	ReadBps  *float64 `json:"read_bps,omitempty"`
	WriteBps *float64 `json:"write_bps,omitempty"`
	BusyPct  *float64 `json:"busy_percent,omitempty"`
}

// Sensor is a temperature reading with the thresholds the hardware itself
// provides. There is no universal danger temperature.
type Sensor struct {
	ID     string `json:"id"`   // chip/label, stable across samples
	Chip   string `json:"chip"` // hwmon driver name, e.g. coretemp
	Label  string `json:"label"`
	Kind   string `json:"kind"`             // cpu | board | storage | gpu | other
	Device string `json:"device,omitempty"` // block device for storage sensors
	// HW is the sysfs device the sensor belongs to (a PCI address such as
	// 0000:00:18.3, or a platform device such as coretemp.0): with Chip
	// and Label it identifies the sensor across reboots, unlike ID.
	HW       string   `json:"hw,omitempty"`
	Celsius  float64  `json:"celsius"`
	High     *float64 `json:"high,omitempty"`     // tempN_max
	Critical *float64 `json:"critical,omitempty"` // tempN_crit
	Emerg    *float64 `json:"emergency,omitempty"`
}

// GPU is one GPU's telemetry; every value may be missing.
type GPU struct {
	Index  int    `json:"index"`
	Vendor string `json:"vendor"` // nvidia | amd | intel | other
	// UUID is the GPU's own identity when the driver reports one (NVIDIA
	// GPU UUID, AMD unique_id); PCI its PCI address. Index is only the
	// position in this report.
	UUID        string   `json:"uuid,omitempty"`
	PCI         string   `json:"pci,omitempty"`
	Model       string   `json:"model"`
	Source      string   `json:"source"` // nvidia-smi | sysfs
	UtilPercent *float64 `json:"util_percent,omitempty"`
	MemUsed     *int64   `json:"mem_used_bytes,omitempty"`
	MemTotal    *int64   `json:"mem_total_bytes,omitempty"`
	Celsius     *float64 `json:"celsius,omitempty"`
	PowerW      *float64 `json:"power_w,omitempty"`
	PowerState  string   `json:"power_state,omitempty"`
	Note        string   `json:"note,omitempty"` // what isn't available and why
}

// Collector states.
const (
	CollectorOK          = "ok"
	CollectorUnavailable = "unavailable" // not on this host (no sensors, no systemd)
	CollectorFailed      = "failed"
	CollectorDeferred    = "deferred" // skipped to save resources
)

// CollectorStatus is one collector's health.
type CollectorStatus struct {
	Name  string `json:"name"`
	State string `json:"state"`
	Note  string `json:"note,omitempty"`
}

// Helper is an optional monitoring tool: whether it is installed and
// what installing it would provide on this host.
type Helper struct {
	Package   string `json:"package"` // distro-neutral name: smartmontools, nvme-cli, lm-sensors
	Tool      string `json:"tool"`
	Installed bool   `json:"installed"`
	// Useful: installing it would provide something on this host.
	Useful  bool   `json:"useful"`
	Purpose string `json:"purpose"`
}

// AgentResources is the agent's own resource budget and use.
type AgentResources struct {
	Enforced     bool     `json:"enforced"` // limits applied by systemd/cgroups
	Note         string   `json:"note,omitempty"`
	CPUQuotaPct  *float64 `json:"cpu_quota_percent,omitempty"` // of one CPU
	CPUWeight    *int     `json:"cpu_weight,omitempty"`
	MemoryHigh   *int64   `json:"memory_high_bytes,omitempty"`
	MemoryMax    *int64   `json:"memory_max_bytes,omitempty"`
	CPUPercent   *float64 `json:"cpu_percent,omitempty"` // recent use, of one CPU
	MemoryBytes  *int64   `json:"memory_bytes,omitempty"`
	MemoryPeak   *int64   `json:"memory_peak_bytes,omitempty"`
	Throttled    bool     `json:"throttled,omitempty"` // held back by the CPU quota >10% of the last interval
	OOMKills     int64    `json:"oom_kills,omitempty"`
	BufferBytes  int      `json:"buffer_bytes"` // offline buffer in use
	BufferLimit  int      `json:"buffer_limit"`
	DroppedTotal int      `json:"dropped_points"` // routine points discarded since start
}
