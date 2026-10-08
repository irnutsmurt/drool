package monitor

import (
	"bufio"
	"bytes"
	"sort"
	"strconv"
	"strings"

	"drool/internal/monitor/source"
)

// SystemSpec is what the system collectors read.
var SystemSpec = source.Spec{
	Files: []string{"/proc/stat", "/proc/meminfo", "/proc/loadavg", "/proc/uptime",
		"/proc/sys/kernel/random/boot_id", "/proc/pressure/cpu", "/proc/pressure/memory", "/proc/pressure/io",
		"/proc/net/dev", "/proc/diskstats",
		"/sys/class/net/*/operstate", "/sys/class/net/*/carrier", "/sys/class/net/*/speed", "/sys/class/net/*/mtu",
		"/sys/class/net/*/ifindex", "/sys/class/net/*/type",
		"/sys/block/*/dev", "/sys/block/*/queue/hw_sector_size"},
	Links: []string{"/sys/class/net/*", "/sys/class/net/*/device", "/sys/block/*"},
}

// CPUTimes are one /proc/stat cpu line, in clock ticks.
type CPUTimes struct {
	User, Nice, System, Idle, IOWait, IRQ, SoftIRQ, Steal uint64
}

func (c CPUTimes) total() uint64 {
	return c.User + c.Nice + c.System + c.Idle + c.IOWait + c.IRQ + c.SoftIRQ + c.Steal
}

// NetCounters are an interface's cumulative counters.
type NetCounters struct {
	Ifindex                        int
	RxBytes, TxBytes               uint64
	RxErrs, TxErrs, RxDrop, TxDrop uint64
	SpeedMbps                      int
}

// DiskCounters are a disk's cumulative counters (/proc/diskstats).
type DiskCounters struct {
	DevNum                      string
	ReadSectors, WrittenSectors uint64 // always 512-byte units
	IOTicksMs                   uint64
}

// Raw is one instant's cumulative counters and gauges. Rates come from
// two Raws (Rates); they are computed on whichever side has both: the
// agent between its samples, the controller between SSH polls.
type Raw struct {
	AtMs      int64
	BootID    string
	BootTime  int64      // btime, unix seconds
	CPU       []CPUTimes // [0] all CPUs, then each core
	CoreIDs   []string   // "cpu0", ...
	Mem       map[string]int64
	Load      []float64
	Uptime    *float64
	PSI       map[string][2]float64 // cpu|memory|io → some avg10, avg60
	Net       map[string]NetCounters
	Disk      map[string]DiskCounters
	NetInfo   []Interface // states (without rates)
	CPUCount  int
	Is32bit   bool
	hasMemAvl bool
}

// ReadRaw collects one sample. Missing files leave fields empty.
func ReadRaw(src source.Source, atMs int64) *Raw {
	r := &Raw{AtMs: atMs, Mem: map[string]int64{}, Net: map[string]NetCounters{}, Disk: map[string]DiskCounters{}, PSI: map[string][2]float64{}}
	if b, err := src.ReadFile("/proc/sys/kernel/random/boot_id"); err == nil {
		r.BootID = strings.TrimSpace(string(b))
	}
	if b, err := src.ReadFile("/proc/stat"); err == nil {
		r.parseStat(b)
	}
	if b, err := src.ReadFile("/proc/meminfo"); err == nil {
		r.parseMeminfo(b)
	}
	if b, err := src.ReadFile("/proc/loadavg"); err == nil {
		f := strings.Fields(string(b))
		if len(f) >= 3 {
			for _, x := range f[:3] {
				v, err := strconv.ParseFloat(x, 64)
				if err != nil {
					r.Load = nil
					break
				}
				r.Load = append(r.Load, v)
			}
		}
	}
	if b, err := src.ReadFile("/proc/uptime"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			if v, err := strconv.ParseFloat(f[0], 64); err == nil {
				r.Uptime = &v
			}
		}
	}
	for _, k := range []string{"cpu", "memory", "io"} {
		if b, err := src.ReadFile("/proc/pressure/" + k); err == nil {
			if v, ok := parsePSI(b); ok {
				r.PSI[k] = v
			}
		}
	}
	if b, err := src.ReadFile("/proc/net/dev"); err == nil {
		r.parseNetDev(b)
	}
	r.readInterfaces(src)
	if b, err := src.ReadFile("/proc/diskstats"); err == nil {
		r.parseDiskstats(b, physicalDisks(src))
	}
	return r
}

func (r *Raw) parseStat(b []byte) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		switch {
		case f[0] == "btime" && len(f) > 1:
			r.BootTime, _ = strconv.ParseInt(f[1], 10, 64)
		case strings.HasPrefix(f[0], "cpu") && len(f) >= 5:
			var v [8]uint64
			ok := true
			for i := 0; i < 8 && i+1 < len(f); i++ {
				n, err := strconv.ParseUint(f[i+1], 10, 64)
				if err != nil {
					ok = false
					break
				}
				v[i] = n
			}
			if !ok {
				continue
			}
			t := CPUTimes{v[0], v[1], v[2], v[3], v[4], v[5], v[6], v[7]}
			if f[0] == "cpu" {
				r.CPU = append([]CPUTimes{t}, r.CPU...)
				r.CoreIDs = append([]string{"cpu"}, r.CoreIDs...)
			} else {
				r.CPU = append(r.CPU, t)
				r.CoreIDs = append(r.CoreIDs, f[0])
				r.CPUCount++
			}
		}
	}
	if len(r.CoreIDs) > 0 && r.CoreIDs[0] != "cpu" {
		r.CPU, r.CoreIDs = nil, nil // no aggregate line: unusable
	}
}

func (r *Raw) parseMeminfo(b []byte) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		f := strings.Fields(v)
		if len(f) == 0 {
			continue
		}
		n, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil {
			continue
		}
		if len(f) > 1 && f[1] == "kB" {
			n *= 1024
		}
		r.Mem[k] = n
	}
	_, r.hasMemAvl = r.Mem["MemAvailable"]
}

func parsePSI(b []byte) ([2]float64, bool) {
	for _, ln := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(ln, "some ") {
			continue
		}
		var out [2]float64
		n := 0
		for _, kv := range strings.Fields(ln)[1:] {
			k, v, _ := strings.Cut(kv, "=")
			x, err := strconv.ParseFloat(v, 64)
			if err != nil {
				continue
			}
			switch k {
			case "avg10":
				out[0] = x
				n++
			case "avg60":
				out[1] = x
				n++
			}
		}
		return out, n == 2
	}
	return [2]float64{}, false
}

func (r *Raw) parseNetDev(b []byte) {
	for _, ln := range strings.Split(string(b), "\n") {
		name, rest, ok := strings.Cut(ln, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		f := strings.Fields(rest)
		if len(f) < 16 || name == "" {
			continue
		}
		var v [16]uint64
		bad := false
		for i := range v {
			n, err := strconv.ParseUint(f[i], 10, 64)
			if err != nil {
				bad = true
				break
			}
			v[i] = n
		}
		if bad {
			continue
		}
		r.Net[name] = NetCounters{RxBytes: v[0], RxErrs: v[2], RxDrop: v[3], TxBytes: v[8], TxErrs: v[10], TxDrop: v[11]}
	}
}

// MaxInterfaces bounds the interfaces reported (hosts with hundreds of
// container veths): physical first, then the rest by name.
const MaxInterfaces = 64

func (r *Raw) readInterfaces(src source.Source) {
	for name, c := range r.Net {
		base := "/sys/class/net/" + name
		in := Interface{Name: name, State: "unknown"}
		if b, err := src.ReadFile(base + "/operstate"); err == nil {
			in.State = strings.TrimSpace(string(b))
		}
		if b, err := src.ReadFile(base + "/carrier"); err == nil {
			v := strings.TrimSpace(string(b)) == "1"
			in.Carrier = &v
		}
		if b, err := src.ReadFile(base + "/speed"); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && n > 0 && n < 10_000_000 {
				in.SpeedMbps = &n
				c.SpeedMbps = n
			}
		}
		if b, err := src.ReadFile(base + "/mtu"); err == nil {
			in.MTU, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		if b, err := src.ReadFile(base + "/ifindex"); err == nil {
			c.Ifindex, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		typ := ""
		if b, err := src.ReadFile(base + "/type"); err == nil {
			typ = strings.TrimSpace(string(b))
		}
		switch {
		case name == "lo" || typ == "772":
			in.Kind = "loopback"
		case src.Link(base+"/device") != "":
			in.Kind = "physical"
		case strings.Contains(src.Link(base), "/virtual/"):
			in.Kind = "virtual"
		default:
			in.Kind = "virtual"
		}
		in.RxErrors, in.TxErrors, in.RxDrops, in.TxDrops = c.RxErrs, c.TxErrs, c.RxDrop, c.TxDrop
		r.Net[name] = c
		r.NetInfo = append(r.NetInfo, in)
	}
	sortInterfaces(r.NetInfo)
	if len(r.NetInfo) > MaxInterfaces {
		for _, in := range r.NetInfo[MaxInterfaces:] {
			delete(r.Net, in.Name)
		}
		r.NetInfo = r.NetInfo[:MaxInterfaces]
	}
}

func sortInterfaces(s []Interface) {
	rank := map[string]int{"physical": 0, "virtual": 1, "loopback": 2}
	sort.Slice(s, func(i, j int) bool {
		if rank[s[i].Kind] != rank[s[j].Kind] {
			return rank[s[i].Kind] < rank[s[j].Kind]
		}
		return s[i].Name < s[j].Name
	})
}

// physicalDisks are the whole disks worth reporting IO for: not
// partitions, loop, ram, zram or device-mapper (logical) devices.
func physicalDisks(src source.Source) map[string]string {
	out := map[string]string{}
	for _, p := range src.Glob("/sys/block/*") {
		name := p[len("/sys/block/"):]
		if !PhysicalDiskName(name) {
			continue
		}
		dev := ""
		if b, err := src.ReadFile(p + "/dev"); err == nil {
			dev = strings.TrimSpace(string(b))
		}
		out[name] = dev
	}
	return out
}

// PhysicalDiskName filters block device names.
func PhysicalDiskName(name string) bool {
	for _, pre := range []string{"loop", "ram", "zram", "dm-", "md", "sr", "fd", "nbd", "zd", "drbd"} {
		if strings.HasPrefix(name, pre) {
			return false
		}
	}
	return name != ""
}

// MaxDisks bounds the disks reported.
const MaxDisks = 64

func (r *Raw) parseDiskstats(b []byte, disks map[string]string) {
	for _, ln := range strings.Split(string(b), "\n") {
		f := strings.Fields(ln)
		if len(f) < 14 {
			continue
		}
		name := f[2]
		dev, ok := disks[name]
		if !ok || len(r.Disk) >= MaxDisks {
			continue
		}
		rs, e1 := strconv.ParseUint(f[5], 10, 64)
		ws, e2 := strconv.ParseUint(f[9], 10, 64)
		ticks, e3 := strconv.ParseUint(f[12], 10, 64)
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		if dev == "" {
			dev = f[0] + ":" + f[1]
		}
		r.Disk[name] = DiskCounters{DevNum: dev, ReadSectors: rs, WrittenSectors: ws, IOTicksMs: ticks}
	}
}

// Plausibility bounds: a rate beyond these is a counter reset that wasn't
// otherwise detectable, never real traffic.
const (
	maxNetBps  = 400e9 / 8 // 400 Gbit/s when the link speed is unknown
	maxDiskBps = 100e9     // 100 GB/s
)

// Sample is one instant's values: series plus per-core and per-interface
// detail for the current picture.
type Sample struct {
	V      map[string]float64
	Steal  *float64
	Cores  []*float64
	Ifaces map[string]Interface
	Disks  map[string]DiskIO
}

// Rates derives a Sample from two Raws of the same boot. Anything that
// went backwards (a reset, a wrap, a replaced device) yields no value
// for that interval instead of a spike.
func Rates(prev, cur *Raw) *Sample {
	s := &Sample{V: map[string]float64{}, Ifaces: map[string]Interface{}, Disks: map[string]DiskIO{}}
	cur.gauges(s)
	if prev == nil || prev.BootID != cur.BootID || cur.AtMs <= prev.AtMs {
		for _, in := range cur.NetInfo {
			s.Ifaces[in.Name] = in
		}
		return s
	}
	dt := float64(cur.AtMs-prev.AtMs) / 1000
	// CPU: only when the same CPUs are present in both samples.
	if len(prev.CPU) > 0 && len(cur.CPU) > 0 {
		if busy, iow, steal, ok := cpuPercent(prev.CPU[0], cur.CPU[0]); ok {
			s.V[KeyCPU], s.V[KeyIOWait] = busy, iow
			s.Steal = &steal
		}
		if len(prev.CPU) == len(cur.CPU) && slicesEqual(prev.CoreIDs, cur.CoreIDs) {
			for i := 1; i < len(cur.CPU); i++ {
				if busy, _, _, ok := cpuPercent(prev.CPU[i], cur.CPU[i]); ok {
					v := busy
					s.Cores = append(s.Cores, &v)
				} else {
					s.Cores = append(s.Cores, nil)
				}
			}
		}
	}
	for _, in := range cur.NetInfo {
		c := cur.Net[in.Name]
		p, ok := prev.Net[in.Name]
		if ok && p.Ifindex == c.Ifindex {
			limit := maxNetBps
			if c.SpeedMbps > 0 {
				limit = float64(c.SpeedMbps) * 1e6 / 8 * 1.1
			}
			if rx, ok := counterRate(p.RxBytes, c.RxBytes, dt, limit); ok {
				in.RxBps = &rx
				s.V[PrefixNetRx+in.Name] = rx
			}
			if tx, ok := counterRate(p.TxBytes, c.TxBytes, dt, limit); ok {
				in.TxBps = &tx
				s.V[PrefixNetTx+in.Name] = tx
			}
			e1, ok1 := counterRate(p.RxErrs, c.RxErrs, dt, 1e9)
			e2, ok2 := counterRate(p.TxErrs, c.TxErrs, dt, 1e9)
			if ok1 && ok2 {
				e := e1 + e2
				in.ErrPS = &e
				s.V[PrefixNetErr+in.Name] = e
			}
			d1, ok1 := counterRate(p.RxDrop, c.RxDrop, dt, 1e9)
			d2, ok2 := counterRate(p.TxDrop, c.TxDrop, dt, 1e9)
			if ok1 && ok2 {
				d := d1 + d2
				in.DropPS = &d
				s.V[PrefixNetDrop+in.Name] = d
			}
		}
		s.Ifaces[in.Name] = in
	}
	for name, c := range cur.Disk {
		p, ok := prev.Disk[name]
		if !ok || p.DevNum != c.DevNum {
			continue // new or replaced device: baseline only
		}
		d := DiskIO{Name: name}
		if v, ok := counterRate(p.ReadSectors, c.ReadSectors, dt, maxDiskBps/512); ok {
			v *= 512
			d.ReadBps = &v
			s.V[PrefixDiskR+name] = v
		}
		if v, ok := counterRate(p.WrittenSectors, c.WrittenSectors, dt, maxDiskBps/512); ok {
			v *= 512
			d.WriteBps = &v
			s.V[PrefixDiskW+name] = v
		}
		if v, ok := counterRate(p.IOTicksMs, c.IOTicksMs, dt, 1000*1.05); ok {
			v = min(v/10, 100) // ms busy per s → %
			d.BusyPct = &v
			s.V[PrefixDiskBusy+name] = v
		}
		s.Disks[name] = d
	}
	return s
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func cpuPercent(p, c CPUTimes) (busy, iowait, steal float64, ok bool) {
	pt, ct := p.total(), c.total()
	pi, ci := p.Idle+p.IOWait, c.Idle+c.IOWait
	if ct <= pt || ci < pi {
		return 0, 0, 0, false
	}
	dt := float64(ct - pt)
	busy = clamp(100 * (dt - float64(ci-pi)) / dt)
	// iowait alone can go backwards on tickless kernels: no value then.
	if c.IOWait >= p.IOWait {
		iowait = clamp(100 * float64(c.IOWait-p.IOWait) / dt)
	}
	if c.Steal >= p.Steal {
		steal = clamp(100 * float64(c.Steal-p.Steal) / dt)
	}
	return busy, iowait, steal, true
}

func clamp(v float64) float64 { return max(0, min(100, v)) }

// counterRate is (cur−prev)/dt, or false when the counter went backwards
// (reset or wrap: unknowable, so no value) or the rate is implausible.
func counterRate(prev, cur uint64, dt, limit float64) (float64, bool) {
	if cur < prev || dt <= 0 {
		return 0, false
	}
	v := float64(cur-prev) / dt
	if v > limit {
		return 0, false
	}
	return v, true
}

// gauges are the values that need only one sample.
func (r *Raw) gauges(s *Sample) {
	if t, ok := r.Mem["MemTotal"]; ok && t > 0 {
		avail, ok := r.Mem["MemAvailable"]
		if !ok {
			avail = r.Mem["MemFree"] + r.Mem["Buffers"] + r.Mem["Cached"]
		}
		s.V[KeyMem] = clamp(100 * float64(t-avail) / float64(t))
	}
	if t := r.Mem["SwapTotal"]; t > 0 {
		s.V[KeySwap] = clamp(100 * float64(t-r.Mem["SwapFree"]) / float64(t))
	}
	if len(r.Load) == 3 {
		s.V[KeyLoad1], s.V[KeyLoad5], s.V[KeyLoad15] = r.Load[0], r.Load[1], r.Load[2]
	}
	if v, ok := r.PSI["cpu"]; ok {
		s.V[KeyPSICPU] = v[0]
	}
	if v, ok := r.PSI["memory"]; ok {
		s.V[KeyPSIMem] = v[0]
	}
	if v, ok := r.PSI["io"]; ok {
		s.V[KeyPSIIO] = v[0]
	}
}

// Memory builds the memory picture.
func (r *Raw) Memory() *Memory {
	t, ok := r.Mem["MemTotal"]
	if !ok || t <= 0 {
		return nil
	}
	m := &Memory{TotalBytes: t}
	if a, ok := r.Mem["MemAvailable"]; ok {
		m.AvailableBytes = a
	} else {
		m.AvailableBytes = r.Mem["MemFree"] + r.Mem["Buffers"] + r.Mem["Cached"]
		m.Estimated = true
	}
	m.UsedPercent = clamp(100 * float64(t-m.AvailableBytes) / float64(t))
	m.SwapTotalBytes, m.SwapFreeBytes = r.Mem["SwapTotal"], r.Mem["SwapFree"]
	if m.SwapTotalBytes > 0 {
		v := clamp(100 * float64(m.SwapTotalBytes-m.SwapFreeBytes) / float64(m.SwapTotalBytes))
		m.SwapPercent = &v
	}
	return m
}

// PressurePicture builds the PSI picture.
func (r *Raw) PressurePicture() *Pressure {
	if len(r.PSI) == 0 {
		return nil
	}
	p := &Pressure{}
	f := func(k string, a, b **float64) {
		if v, ok := r.PSI[k]; ok {
			x, y := v[0], v[1]
			*a, *b = &x, &y
		}
	}
	f("cpu", &p.CPU10, &p.CPU60)
	f("memory", &p.Memory10, &p.Memory60)
	f("io", &p.IO10, &p.IO60)
	return p
}
