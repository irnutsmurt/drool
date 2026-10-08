package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"drool/internal/monitor/source"
)

// HWSpec is what the sensor and GPU collectors read.
var HWSpec = source.Spec{
	Files: []string{"/sys/class/hwmon/hwmon*/name", "/sys/class/hwmon/hwmon*/temp*_input", "/sys/class/hwmon/hwmon*/temp*_label",
		"/sys/class/hwmon/hwmon*/temp*_max", "/sys/class/hwmon/hwmon*/temp*_crit", "/sys/class/hwmon/hwmon*/temp*_emergency",
		"/sys/class/thermal/thermal_zone*/type", "/sys/class/thermal/thermal_zone*/temp",
		"/sys/class/thermal/thermal_zone*/trip_point_*_type", "/sys/class/thermal/thermal_zone*/trip_point_*_temp",
		"/sys/bus/pci/devices/*/class", "/sys/bus/pci/devices/*/vendor", "/sys/bus/pci/devices/*/device",
		"/sys/bus/pci/devices/*/gpu_busy_percent", "/sys/bus/pci/devices/*/mem_info_vram_used", "/sys/bus/pci/devices/*/mem_info_vram_total",
		"/sys/bus/pci/devices/*/hwmon/hwmon*/temp1_input", "/sys/bus/pci/devices/*/hwmon/hwmon*/power1_average",
		"/sys/bus/pci/devices/*/hwmon/hwmon*/power1_input", "/sys/bus/pci/devices/*/power_state"},
	Links: []string{"/sys/class/hwmon/hwmon*/device", "/sys/bus/pci/devices/*/driver"},
	Tools: []string{"nvidia-smi", "sensors", "lspci"},
	Commands: [][]string{
		{"nvidia-smi", "--query-gpu=index,name,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw,pstate", "--format=csv,noheader,nounits"},
		{"lspci", "-mm"},
	},
}

// MaxSensors bounds the sensors reported.
const MaxSensors = 128

func readMilli(src source.Source, p string) (float64, bool) {
	b, err := src.ReadFile(p)
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0, false
	}
	return float64(n) / 1000, true
}

func readText(src source.Source, p string) string {
	b, err := src.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func sensorKind(chip string) string {
	chip = strings.ReplaceAll(strings.ToLower(chip), "-", "_")
	switch {
	case chip == "coretemp" || chip == "k10temp" || chip == "zenpower" || strings.Contains(chip, "cpu_thermal") ||
		strings.Contains(chip, "soc_thermal") || chip == "cpu" || strings.HasPrefix(chip, "x86_pkg"):
		return "cpu"
	case chip == "nvme" || chip == "drivetemp":
		return "storage"
	case chip == "amdgpu" || chip == "nouveau" || chip == "i915" || chip == "xe" || chip == "radeon":
		return "gpu"
	case chip == "acpitz" || strings.HasPrefix(chip, "nct") || strings.HasPrefix(chip, "it87") || strings.HasPrefix(chip, "pch_") ||
		strings.HasPrefix(chip, "w83") || strings.HasPrefix(chip, "f71") || chip == "asus_wmi_sensors" || chip == "dell_smm":
		return "board"
	}
	return "other"
}

// blockForDevice finds the block device below a sysfs device path (a
// storage sensor's disk): /sys/block/X resolves into the device's tree.
func blockForDevice(src source.Source, dev string) string {
	if dev == "" {
		return ""
	}
	for _, b := range src.Glob("/sys/block/*") {
		if t := src.Link(b); strings.HasPrefix(t, dev+"/") {
			return path.Base(b)
		}
	}
	return ""
}

// Sensors reads hwmon temperatures with the thresholds the hardware
// reports; thermal zones only when hwmon has none; `sensors -j` only when
// neither does. Missing sensors are normal: an empty list.
func Sensors(ctx context.Context, src source.Source) []Sensor {
	var out []Sensor
	dirs := src.Glob("/sys/class/hwmon/hwmon*/name")
	sort.Slice(dirs, func(i, j int) bool { return hwmonNum(dirs[i]) < hwmonNum(dirs[j]) })
	seenChip := map[string]int{}
	for _, nameFile := range dirs {
		dir := path.Dir(nameFile)
		chip := readText(src, nameFile)
		if chip == "" {
			continue
		}
		kind := sensorKind(chip)
		inst := chip
		if n := seenChip[chip]; n > 0 {
			inst = fmt.Sprintf("%s.%d", chip, n)
		}
		seenChip[chip]++
		blk := ""
		if kind == "storage" {
			blk = blockForDevice(src, src.Link(dir+"/device"))
			if blk != "" {
				inst = chip + ":" + blk
			}
		}
		inputs := src.Glob(dir + "/temp*_input")
		sort.Strings(inputs)
		for _, in := range inputs {
			v, ok := readMilli(src, in)
			if !ok || v < -60 || v > 250 {
				continue // absent or a sensor reporting garbage
			}
			base := strings.TrimSuffix(in, "_input")
			label := readText(src, base+"_label")
			if label == "" {
				label = path.Base(base)
			}
			s := Sensor{ID: inst + "/" + label, Chip: chip, Label: label, Kind: kind, Device: blk, Celsius: v}
			if x, ok := readMilli(src, base+"_max"); ok && x > 0 && x < 250 {
				s.High = &x
			}
			if x, ok := readMilli(src, base+"_crit"); ok && x > 0 && x < 250 {
				s.Critical = &x
			}
			if x, ok := readMilli(src, base+"_emergency"); ok && x > 0 && x < 250 {
				s.Emerg = &x
			}
			out = append(out, s)
			if len(out) >= MaxSensors {
				return out
			}
		}
	}
	if len(out) > 0 {
		return out
	}
	for _, tf := range src.Glob("/sys/class/thermal/thermal_zone*/type") {
		dir := path.Dir(tf)
		typ := readText(src, tf)
		v, ok := readMilli(src, dir+"/temp")
		if !ok || v < -60 || v > 250 {
			continue
		}
		s := Sensor{ID: "thermal/" + path.Base(dir) + "/" + typ, Chip: typ, Label: typ, Kind: sensorKind(typ), Celsius: v}
		for _, tp := range src.Glob(dir + "/trip_point_*_type") {
			t := readText(src, tp)
			x, ok := readMilli(src, strings.TrimSuffix(tp, "_type")+"_temp")
			if !ok || x <= 0 || x >= 250 {
				continue
			}
			switch t {
			case "critical":
				s.Critical = &x
			case "hot":
				s.High = &x
			}
		}
		out = append(out, s)
	}
	if len(out) > 0 || !src.Has("sensors") {
		return out
	}
	return sensorsJSON(ctx, src)
}

func hwmonNum(p string) int {
	d := path.Base(path.Dir(p))
	n, _ := strconv.Atoi(strings.TrimPrefix(d, "hwmon"))
	return n
}

// sensorsJSON parses `sensors -j` (lm-sensors), the last resort.
func sensorsJSON(ctx context.Context, src source.Source) []Sensor {
	r, err := src.Run(ctx, "sensors", "-j")
	if err != nil || r == nil || r.Exit != 0 {
		return nil
	}
	var chips map[string]map[string]json.RawMessage
	if json.Unmarshal(r.Stdout, &chips) != nil {
		return nil
	}
	var out []Sensor
	names := make([]string, 0, len(chips))
	for n := range chips {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, chip := range names {
		short, _, _ := strings.Cut(chip, "-")
		for label, raw := range chips[chip] {
			var vals map[string]float64
			if json.Unmarshal(raw, &vals) != nil {
				continue
			}
			for k, v := range vals {
				if !strings.HasPrefix(k, "temp") || !strings.HasSuffix(k, "_input") || v < -60 || v > 250 {
					continue
				}
				s := Sensor{ID: chip + "/" + label, Chip: short, Label: label, Kind: sensorKind(short), Celsius: v}
				pre := strings.TrimSuffix(k, "_input")
				if x, ok := vals[pre+"_max"]; ok && x > 0 && x < 250 {
					s.High = &x
				}
				if x, ok := vals[pre+"_crit"]; ok && x > 0 && x < 250 {
					s.Critical = &x
				}
				out = append(out, s)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > MaxSensors {
		out = out[:MaxSensors]
	}
	return out
}

// --- GPUs: one collector per vendor behind one function ---

type pciGPU struct {
	slot, vendor, device, driver string
}

func vendorName(v string) string {
	switch v {
	case "0x10de":
		return "nvidia"
	case "0x1002":
		return "amd"
	case "0x8086":
		return "intel"
	}
	return ""
}

// MaxGPUs bounds the GPUs reported.
const MaxGPUs = 16

func pciGPUs(src source.Source) []pciGPU {
	var out []pciGPU
	for _, cf := range src.Glob("/sys/bus/pci/devices/*/class") {
		c := readText(src, cf)
		if !strings.HasPrefix(c, "0x03") { // display controllers
			continue
		}
		dir := path.Dir(cf)
		g := pciGPU{slot: path.Base(dir), vendor: readText(src, dir+"/vendor"), device: readText(src, dir+"/device"),
			driver: path.Base(src.Link(dir + "/driver"))}
		if vendorName(g.vendor) == "" {
			continue // virtual display adapters and BMCs: nothing to monitor
		}
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].slot < out[j].slot })
	if len(out) > MaxGPUs {
		out = out[:MaxGPUs]
	}
	return out
}

// lspciNames maps PCI slots to "Vendor Device" names from `lspci -mm`.
func lspciNames(ctx context.Context, src source.Source) map[string]string {
	out := map[string]string{}
	if !src.Has("lspci") {
		return out
	}
	r, err := src.Run(ctx, "lspci", "-mm")
	if err != nil || r == nil {
		return out
	}
	for _, ln := range strings.Split(string(r.Stdout), "\n") {
		f := splitQuoted(ln)
		if len(f) >= 4 {
			slot := f[0]
			if strings.Count(slot, ":") == 1 {
				slot = "0000:" + slot
			}
			out[slot] = strings.TrimSpace(f[2] + " " + f[3])
		}
	}
	return out
}

func splitQuoted(s string) []string {
	var out []string
	for s = strings.TrimSpace(s); s != ""; s = strings.TrimSpace(s) {
		if s[0] == '"' {
			end := strings.IndexByte(s[1:], '"')
			if end < 0 {
				return out
			}
			out = append(out, s[1:1+end])
			s = s[2+end:]
			continue
		}
		f, rest, _ := strings.Cut(s, " ")
		out = append(out, f)
		s = rest
	}
	return out
}

// GPUs collects best-effort GPU telemetry. It returns the GPUs and a note
// for what is unavailable; no GPU at all is (nil, "").
func GPUs(ctx context.Context, src source.Source) ([]GPU, string) {
	devs := pciGPUs(src)
	if len(devs) == 0 {
		return nil, ""
	}
	names := lspciNames(ctx, src)
	var out []GPU
	var notes []string
	nv := 0
	for _, d := range devs {
		if vendorName(d.vendor) == "nvidia" {
			nv++
		}
	}
	var nvidia []GPU
	if nv > 0 {
		var err error
		nvidia, err = nvidiaSMI(ctx, src)
		if err != nil {
			notes = append(notes, err.Error())
		}
	}
	nvi := 0
	for i, d := range devs {
		model := names[d.slot]
		if model == "" {
			model = fmt.Sprintf("%s GPU [%s:%s]", strings.ToUpper(vendorName(d.vendor)[:1])+vendorName(d.vendor)[1:],
				strings.TrimPrefix(d.vendor, "0x"), strings.TrimPrefix(d.device, "0x"))
		}
		switch vendorName(d.vendor) {
		case "nvidia":
			if nvi < len(nvidia) {
				g := nvidia[nvi]
				g.Index = i
				out = append(out, g)
			} else {
				out = append(out, GPU{Index: i, Vendor: "nvidia", Model: model, Source: "none",
					Note: "GPU telemetry unavailable: nvidia-smi (installed with the NVIDIA driver) is not available. DROOL never installs GPU drivers."})
			}
			nvi++
		case "amd":
			out = append(out, sysfsGPU(src, d, i, "amd", model))
		case "intel":
			g := sysfsGPU(src, d, i, "intel", model)
			if g.UtilPercent == nil {
				g.Note = "Intel GPUs don't report utilization through sysfs; temperature and power only where the driver provides them."
			}
			out = append(out, g)
		}
	}
	note := ""
	if len(notes) > 0 {
		note = strings.Join(notes, " ")
	}
	return out, note
}

func sysfsGPU(src source.Source, d pciGPU, idx int, vendor, model string) GPU {
	dir := "/sys/bus/pci/devices/" + d.slot
	g := GPU{Index: idx, Vendor: vendor, Model: model, Source: "sysfs", PowerState: readText(src, dir+"/power_state")}
	if b := readText(src, dir+"/gpu_busy_percent"); b != "" {
		if v, err := strconv.ParseFloat(b, 64); err == nil && v >= 0 && v <= 100 {
			g.UtilPercent = &v
		}
	}
	used, e1 := strconv.ParseInt(readText(src, dir+"/mem_info_vram_used"), 10, 64)
	tot, e2 := strconv.ParseInt(readText(src, dir+"/mem_info_vram_total"), 10, 64)
	if e1 == nil && e2 == nil && tot > 0 && used >= 0 && used <= tot {
		g.MemUsed, g.MemTotal = &used, &tot
	}
	hw := src.Glob(dir + "/hwmon/hwmon*/temp1_input")
	if len(hw) > 0 {
		if v, ok := readMilli(src, hw[0]); ok && v > -60 && v < 250 {
			g.Celsius = &v
		}
		hd := path.Dir(hw[0])
		for _, pf := range []string{"power1_average", "power1_input"} {
			if b := readText(src, hd+"/"+pf); b != "" {
				if n, err := strconv.ParseFloat(b, 64); err == nil && n >= 0 && n < 2e9 {
					w := n / 1e6 // µW
					g.PowerW = &w
					break
				}
			}
		}
	}
	if d.driver == "" || d.driver == "." {
		g.Note = "No kernel driver is bound to this GPU: telemetry unavailable."
		g.UtilPercent, g.MemUsed, g.MemTotal, g.Celsius, g.PowerW = nil, nil, nil, nil, nil
	} else if vendor == "amd" && g.UtilPercent == nil && g.Celsius == nil {
		g.Note = "GPU telemetry unavailable from the " + d.driver + " driver."
	}
	return g
}

func nvidiaSMI(ctx context.Context, src source.Source) ([]GPU, error) {
	if !src.Has("nvidia-smi") {
		return nil, nil
	}
	cmd := HWSpec.Commands[0]
	r, err := src.Run(ctx, cmd[0], cmd[1:]...)
	if err != nil || r == nil {
		if err == source.ErrDeferred {
			return nil, nil
		}
		return nil, fmt.Errorf("nvidia-smi failed: GPU telemetry unavailable.")
	}
	if r.Exit != 0 {
		return nil, fmt.Errorf("nvidia-smi failed (exit %d; is the driver loaded?): GPU telemetry unavailable.", r.Exit)
	}
	return ParseNvidiaSMI(r.Stdout)
}

// ParseNvidiaSMI parses the CSV query output. "[N/A]" and "[Not
// Supported]" are missing values, never zero.
func ParseNvidiaSMI(b []byte) ([]GPU, error) {
	var out []GPU
	for _, ln := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if ln == "" {
			continue
		}
		f := strings.Split(ln, ",")
		if len(f) != 8 {
			return out, fmt.Errorf("nvidia-smi output not understood: GPU telemetry unavailable.")
		}
		for i := range f {
			f[i] = strings.TrimSpace(f[i])
		}
		num := func(s string) *float64 {
			if strings.HasPrefix(s, "[") || s == "" {
				return nil
			}
			v, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return nil
			}
			return &v
		}
		idx, _ := strconv.Atoi(f[0])
		g := GPU{Index: idx, Vendor: "nvidia", Model: f[1], Source: "nvidia-smi", PowerState: strings.Trim(f[7], "[]")}
		if g.PowerState == "N/A" || g.PowerState == "Not Supported" {
			g.PowerState = ""
		}
		if v := num(f[2]); v != nil && *v >= 0 && *v <= 100 {
			g.UtilPercent = v
		}
		used, tot := num(f[3]), num(f[4])
		if used != nil && tot != nil && *tot > 0 && *used <= *tot {
			u, t := int64(*used*(1<<20)), int64(*tot*(1<<20)) // MiB
			g.MemUsed, g.MemTotal = &u, &t
		}
		if v := num(f[5]); v != nil && *v > -60 && *v < 250 {
			g.Celsius = v
		}
		if v := num(f[6]); v != nil && *v >= 0 && *v < 5000 {
			g.PowerW = v
		}
		out = append(out, g)
		if len(out) >= MaxGPUs {
			break
		}
	}
	return out, nil
}
