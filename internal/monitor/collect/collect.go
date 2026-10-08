// Package collect assembles the monitoring collectors into what each
// management path reports: the agent calls the pieces on its own
// schedule (internal/agent); the SSH path polls everything at once with
// Poll. Both use the same collectors (internal/monitor/source).
package collect

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"

	"drool/internal/monitor"
	"drool/internal/monitor/services"
	"drool/internal/monitor/source"
	"drool/internal/monitor/storage"
)

// HostSpec is everything the SSH first pass dumps.
var HostSpec = source.Merge(monitor.SystemSpec, monitor.FSSpec, monitor.HWSpec, storage.Spec, services.Spec,
	source.Spec{Files: []string{"/sys/class/dmi/id/sys_vendor", "/sys/class/dmi/id/product_name", "/sys/hypervisor/type",
		"/.dockerenv", "/run/.containerenv"}, Commands: [][]string{storage.JournalCommand}, Tools: []string{"journalctl"}})

// Script is the SSH first-pass script.
func Script() string { return HostSpec.Script("") }

// Helper packages DROOL may suggest. Distro-neutral names; the package
// manager's names come from PackageName.
const (
	HelperSmart   = "smartmontools"
	HelperNVMe    = "nvme-cli"
	HelperSensors = "lm-sensors"
)

// Helpers is the allowlist of monitoring helpers DROOL ever offers to
// install. GPU drivers are deliberately not in it.
var Helpers = []string{HelperSmart, HelperNVMe, HelperSensors}

// PackageName maps a helper to the package manager's name.
func PackageName(helper, pkgManager string) string {
	if helper == HelperSensors && (pkgManager == "dnf" || pkgManager == "yum") {
		return "lm_sensors"
	}
	return helper
}

// HelperPurpose says what installing a helper provides.
var HelperPurpose = map[string]string{
	HelperSmart:   "SMART health for SATA/SAS disks and SSDs (overall result, reallocated and pending sectors, temperature, power-on hours, self-test results), USB bridges that pass it through, and NVMe health",
	HelperNVMe:    "NVMe health: critical warnings, endurance used, available spare, media errors and the error log",
	HelperSensors: "sensors-detect, which can identify motherboard sensor chips the kernel didn't load automatically (run it yourself; DROOL never probes hardware buses). DROOL reads the sensors through the kernel afterwards",
}

// InContainer reports a container: /sys shows the container host's block
// devices, which the container can neither query nor is responsible for.
func InContainer(src source.Source) bool {
	if _, err := src.ReadFile("/.dockerenv"); err == nil {
		return true
	}
	if _, err := src.ReadFile("/run/.containerenv"); err == nil {
		return true
	}
	if b, err := src.ReadFile("/proc/self/mountinfo"); err == nil {
		for _, ln := range strings.Split(string(b), "\n") {
			if f := strings.Fields(ln); len(f) > 4 && f[4] == "/" && strings.Contains(ln, " - overlay ") {
				return true
			}
		}
	}
	return false
}

// ContainerStorageNote explains why a container reports no devices.
const ContainerStorageNote = "This host is a container: physical storage health belongs to the machine it runs on."

// Storage collects storage health, or nothing inside a container.
func Storage(ctx context.Context, src source.Source, smart bool, prev []storage.Device) *storage.Report {
	if InContainer(src) {
		return &storage.Report{Devices: []storage.Device{}, Note: ContainerStorageNote, Smart: smart}
	}
	return storage.Collect(ctx, src, smart, prev)
}

// Virtualized reports a VM or container, where hardware helpers are moot.
func Virtualized(src source.Source) bool {
	if InContainer(src) {
		return true
	}
	if _, err := src.ReadFile("/.dockerenv"); err == nil {
		return true
	}
	if _, err := src.ReadFile("/run/.containerenv"); err == nil {
		return true
	}
	if b, _ := src.ReadFile("/sys/hypervisor/type"); len(b) > 0 {
		return true
	}
	if b, err := src.ReadFile("/proc/self/mountinfo"); err == nil {
		for _, ln := range strings.Split(string(b), "\n") {
			if f := strings.Fields(ln); len(f) > 4 && f[4] == "/" && strings.Contains(ln, " - overlay ") {
				return true // a container's root
			}
		}
	}
	v, _ := src.ReadFile("/sys/class/dmi/id/sys_vendor")
	p, _ := src.ReadFile("/sys/class/dmi/id/product_name")
	s := strings.ToLower(string(v) + " " + string(p))
	for _, h := range []string{"qemu", "kvm", "vmware", "virtualbox", "innotek", "xen", "bochs", "microsoft corporation virtual", "parallels", "amazon ec2", "google compute", "openstack", "digitalocean", "hetzner vserver"} {
		if strings.Contains(s, h) {
			return true
		}
	}
	return false
}

// HelperStatus decides which helpers are installed and which would be
// useful on this host, from what the collectors found.
func HelperStatus(src source.Source, devs []storage.Device, sensors []monitor.Sensor) []monitor.Helper {
	want := map[string]bool{}
	for _, d := range devs {
		if d.Helper != "" {
			want[d.Helper] = true
		}
	}
	if len(sensors) == 0 && !Virtualized(src) && !src.Has("sensors") {
		want[HelperSensors] = true
	}
	tools := map[string]string{HelperSmart: "smartctl", HelperNVMe: "nvme", HelperSensors: "sensors"}
	var out []monitor.Helper
	for _, h := range Helpers {
		has := src.Has(tools[h])
		out = append(out, monitor.Helper{Package: h, Tool: tools[h], Installed: has, Useful: want[h] && !has, Purpose: HelperPurpose[h]})
	}
	return out
}

// Result is one complete SSH poll.
type Result struct {
	Raw      *monitor.Raw
	State    *monitor.State
	Services *services.Report
	Storage  *storage.Report
	Kernel   []storage.KernelEvent
	// FileStates are the unit-file states (kept between polls).
	FileStates map[string]string
}

// Runner runs a script on the host and returns its output.
type Runner func(ctx context.Context, script string) ([]byte, error)

// Poll runs the SSH collection: the first pass dumps files as the login
// user, then up to two more passes run what the collectors asked for
// (capacity checks, SMART queries, a SAT retry for an unknown USB
// bridge). With smart and a privileged runner, those later passes run
// as root (SMART and the kernel log need it); routine polls never do,
// so the host's auth log doesn't fill with DROOL's sudo calls.
func Poll(ctx context.Context, run, runRoot Runner, nowMs int64, prev *monitor.State, prevDevs []storage.Device, smart bool) (*Result, error) {
	return poll(ctx, run, runRoot, "", nowMs, prev, prevDevs, smart)
}

// PollRoot is Poll with every path under root (tests: a fixture machine).
func PollRoot(ctx context.Context, run, runRoot Runner, root string, nowMs int64, prev *monitor.State, prevDevs []storage.Device, smart bool) (*Result, error) {
	return poll(ctx, run, runRoot, root, nowMs, prev, prevDevs, smart)
}

// poll is Poll with every path under root (tests: a fixture tree).
func poll(ctx context.Context, run, runRoot Runner, root string, nowMs int64, prev *monitor.State, prevDevs []storage.Device, smart bool) (*Result, error) {
	later := run
	if smart && runRoot != nil {
		later = runRoot
	}
	out, err := run(ctx, HostSpec.Script(root))
	if err != nil {
		return nil, err
	}
	d, err := source.ParseDump(out, root)
	if err != nil {
		return nil, err
	}
	if smart && runRoot != nil {
		d.Forget(storage.JournalCommand[0], storage.JournalCommand[1:]...) // read the kernel log as root
	}
	var res *Result
	for pass := 0; pass < 3; pass++ {
		if pass == 2 {
			d.Final()
		}
		d.Begin()
		res = collectAll(ctx, d, nowMs, prev, prevDevs, smart)
		if len(d.Wanted) == 0 && len(d.WantedFS) == 0 {
			break
		}
		script, err := source.SecondPass(dedupe(d.Wanted), d.WantedFS, root)
		if err != nil {
			return nil, err
		}
		more, err := later(ctx, script)
		if err != nil {
			return nil, err
		}
		if err := d.AddPass(more); err != nil {
			return nil, err
		}
	}
	return res, nil
}

func dedupe(c [][]string) [][]string {
	seen := map[string]bool{}
	var out [][]string
	for _, x := range c {
		k := strings.Join(x, "\x00")
		if !seen[k] {
			seen[k] = true
			out = append(out, x)
		}
	}
	return out
}

func collectAll(ctx context.Context, src source.Source, nowMs int64, prev *monitor.State, prevDevs []storage.Device, smart bool) *Result {
	raw := monitor.ReadRaw(src, nowMs)
	st := &monitor.State{Source: "ssh", At: nowMs, BootID: raw.BootID, Fidelity: monitor.Fidelity{Mode: "polled"}}
	var prevFS []monitor.FS
	if prev != nil {
		prevFS = prev.Filesystems
	}
	var cols []monitor.CollectorStatus
	fs, err := monitor.Filesystems(ctx, src, prevFS)
	st.Filesystems = nonNil(fs)
	cols = append(cols, status("filesystems", err))
	st.Sensors = nonNil(monitor.Sensors(ctx, src))
	if len(st.Sensors) == 0 {
		cols = append(cols, monitor.CollectorStatus{Name: "sensors", State: monitor.CollectorUnavailable, Note: "No temperature sensors are exposed on this host."})
	} else {
		cols = append(cols, monitor.CollectorStatus{Name: "sensors", State: monitor.CollectorOK})
	}
	gpus, note := monitor.GPUs(ctx, src)
	st.GPUs, st.GPUNote = nonNil(gpus), note
	res := &Result{Raw: raw, State: st}
	svc, fileStates, err := services.Collect(ctx, src, raw.BootTime, nil)
	if err == nil {
		res.Services, res.FileStates = svc, fileStates
	}
	res.Storage = Storage(ctx, src, smart, prevDevs)
	res.Storage.At = nowMs
	res.Kernel = KernelFromJournal(ctx, src, res.Storage.Devices)
	st.Helpers = HelperStatus(src, res.Storage.Devices, st.Sensors)
	st.Collectors = cols
	return res
}

func status(name string, err error) monitor.CollectorStatus {
	if err == nil || errors.Is(err, source.ErrDeferred) {
		return monitor.CollectorStatus{Name: name, State: monitor.CollectorOK}
	}
	return monitor.CollectorStatus{Name: name, State: monitor.CollectorFailed, Note: err.Error()}
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// KernelFromJournal reads recent matching kernel messages (SSH hosts).
func KernelFromJournal(ctx context.Context, src source.Source, devs []storage.Device) []storage.KernelEvent {
	if !src.Has("journalctl") {
		return nil
	}
	c := storage.JournalCommand
	r, err := src.Run(ctx, c[0], c[1:]...)
	if err != nil || r == nil {
		return nil
	}
	var out []storage.KernelEvent
	for _, ln := range strings.Split(string(r.Stdout), "\n") {
		us, msg, ok := storage.ParseJournalLine(ln)
		if !ok {
			continue
		}
		if ev, ok := storage.Event(us, msg); ok {
			ev.Disk = storage.DiskFor(ev.Device, devs, src.Link)
			out = append(out, ev)
			if len(out) >= 500 {
				break
			}
		}
	}
	return out
}

// FillState completes a State from a raw sample and its rates (both
// paths): CPU, memory, load, uptime, pressure, interfaces, disk IO.
func FillState(st *monitor.State, raw *monitor.Raw, s *monitor.Sample) {
	st.BootID = raw.BootID
	cpu := &monitor.CPU{Count: raw.CPUCount, Cores: s.Cores, Steal: s.Steal}
	if v, ok := s.V[monitor.KeyCPU]; ok {
		cpu.Percent = &v
	}
	if v, ok := s.V[monitor.KeyIOWait]; ok {
		cpu.IOWait = &v
	}
	st.CPU = cpu
	if len(raw.Load) == 3 {
		st.Load = raw.Load
	}
	st.Memory = raw.Memory()
	st.UptimeS = raw.Uptime
	st.Pressure = raw.PressurePicture()
	st.Interfaces = []monitor.Interface{}
	for _, in := range raw.NetInfo {
		if x, ok := s.Ifaces[in.Name]; ok {
			in = x
		}
		st.Interfaces = append(st.Interfaces, in)
	}
	st.Disks = []monitor.DiskIO{}
	names := make([]string, 0, len(raw.Disk))
	for n := range raw.Disk {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		d, ok := s.Disks[n]
		if !ok {
			d = monitor.DiskIO{Name: n}
		}
		st.Disks = append(st.Disks, d)
	}
}

// Values are the series of a State's slow collectors (filesystems,
// sensors, GPUs) for the history.
func Values(st *monitor.State) map[string]float64 {
	v := map[string]float64{}
	for _, f := range st.Filesystems {
		if f.Unresponsive || f.TotalBytes == 0 {
			continue
		}
		v[monitor.PrefixFS+f.Mount] = f.UsedPercent
		if f.InodesPercent != nil {
			v[monitor.PrefixInodes+f.Mount] = *f.InodesPercent
		}
	}
	for _, s := range st.Sensors {
		v[monitor.PrefixTemp+s.ID] = s.Celsius
	}
	for _, g := range st.GPUs {
		k := strconv.Itoa(g.Index)
		if g.UtilPercent != nil {
			v[monitor.PrefixGPUUtil+k] = *g.UtilPercent
		}
		if g.MemUsed != nil && g.MemTotal != nil && *g.MemTotal > 0 {
			v[monitor.PrefixGPUMem+k] = 100 * float64(*g.MemUsed) / float64(*g.MemTotal)
		}
		if g.Celsius != nil {
			v[monitor.PrefixGPUTemp+k] = *g.Celsius
		}
		if g.PowerW != nil {
			v[monitor.PrefixGPUPower+k] = *g.PowerW
		}
	}
	return v
}
