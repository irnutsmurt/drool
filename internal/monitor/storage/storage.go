// Package storage is physical storage health (M11), separate from
// filesystem capacity: "/ is 94% full" is not "the NVMe has media errors".
//
// Health is capability-aware. What DROOL can know depends on the device
// and its transport: SATA/SAS and NVMe report standardized health
// (smartctl, nvme-cli); USB bridges sometimes pass it through; SD cards
// and most USB sticks report nothing. A device is never called healthy
// because its health couldn't be read: such devices are judged only on
// the evidence Linux itself has (IO errors, resets, read-only flips, an
// optional write/read probe), and say so.
package storage

import (
	"context"
	"path"
	"sort"
	"strconv"
	"strings"

	"drool/internal/monitor"
	"drool/internal/monitor/source"
)

// Capability levels.
const (
	CapFull        = "full"        // standardized health data
	CapPartial     = "partial"     // some standardized data (e.g. eMMC lifetime, SMART status only)
	CapVendor      = "vendor"      // health only through vendor-specific attributes
	CapLimited     = "limited"     // the device exposes no health telemetry: Linux-side evidence only
	CapUnavailable = "unavailable" // could exist but can't be read (helper missing, no permission, virtual)
)

// Health states. "no_indicators" is the honest result for limited
// devices: nothing bad seen, which is not the same as healthy.
const (
	StateOK           = "ok"
	StateWarning      = "warning"
	StateFailing      = "failing"
	StateNoIndicators = "no_indicators"
	StateUnknown      = "unknown"
)

// Device is one physical block device.
type Device struct {
	Key        string      `json:"key"` // stable identity (WWN, EUI, model+serial)
	Name       string      `json:"name"`
	IDs        []string    `json:"ids,omitempty"` // /dev/disk/by-id names
	Kind       string      `json:"kind"`          // hdd ssd nvme usb-flash usb-sata usb-nvme usb sd emmc virtual unknown
	Transport  string      `json:"transport"`     // sata sas scsi nvme usb mmc virtio xen unknown
	Model      string      `json:"model,omitempty"`
	Serial     string      `json:"serial,omitempty"`
	Firmware   string      `json:"firmware,omitempty"`
	SizeBytes  int64       `json:"size_bytes"`
	Rotational *bool       `json:"rotational,omitempty"`
	Removable  bool        `json:"removable"`
	ReadOnly   bool        `json:"read_only"`
	Partitions []Partition `json:"partitions"`
	Mounts     []string    `json:"mounts,omitempty"` // filesystems directly on the disk

	Capability     string `json:"capability"`
	CapabilityNote string `json:"capability_note,omitempty"`
	Source         string `json:"source,omitempty"` // smartctl | nvme-cli | sysfs
	Standby        bool   `json:"standby,omitempty"`
	Health         Health `json:"health"`

	SMART *SMART      `json:"smart,omitempty"`
	NVMe  *NVMe       `json:"nvme,omitempty"`
	EMMC  *EMMC       `json:"emmc,omitempty"`
	Evid  Evidence    `json:"evidence"`
	Raw   []Attribute `json:"raw,omitempty"` // Advanced: vendor attribute table
	// Helper names a missing package that would provide health data.
	Helper string `json:"helper,omitempty"`
}

// Partition is a partition and where it's mounted (through LVM/LUKS too).
type Partition struct {
	Name      string   `json:"name"`
	UUID      string   `json:"uuid,omitempty"`
	SizeBytes int64    `json:"size_bytes"`
	ReadOnly  bool     `json:"read_only"`
	Mounts    []string `json:"mounts,omitempty"`
	Via       string   `json:"via,omitempty"` // dm name when mounted through device-mapper
}

// Health is the device's judged state with the indicators behind it.
type Health struct {
	State      string      `json:"state"`
	Summary    string      `json:"summary"`
	Indicators []Indicator `json:"indicators"`
}

// Indicator is one fact the judgement used.
type Indicator struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Status string `json:"status"` // ok | warning | critical | info
}

// SMART is normalized ATA/SCSI health. Missing values are nil.
type SMART struct {
	Passed            *bool   `json:"passed,omitempty"`
	Celsius           *int    `json:"celsius,omitempty"`
	PowerOnHours      *int64  `json:"power_on_hours,omitempty"`
	PowerCycles       *int64  `json:"power_cycles,omitempty"`
	Reallocated       *int64  `json:"reallocated,omitempty"`
	Pending           *int64  `json:"pending,omitempty"`
	OfflineUncorrect  *int64  `json:"offline_uncorrectable,omitempty"`
	ReportedUncorrect *int64  `json:"reported_uncorrectable,omitempty"`
	CRCErrors         *int64  `json:"crc_errors,omitempty"`
	GrownDefects      *int64  `json:"grown_defects,omitempty"` // SCSI
	UncorrectedErrors *int64  `json:"uncorrected_errors,omitempty"`
	EnduranceUsed     *int    `json:"endurance_used_percent,omitempty"`
	EnduranceVendor   bool    `json:"endurance_vendor_specific,omitempty"` // from a vendor attribute
	BytesRead         *int64  `json:"bytes_read,omitempty"`
	BytesWritten      *int64  `json:"bytes_written,omitempty"`
	SelfTest          *string `json:"last_self_test,omitempty"`
	SelfTestPassed    *bool   `json:"last_self_test_passed,omitempty"`
	ErrorLogCount     *int64  `json:"error_log_count,omitempty"`
	Protocol          string  `json:"protocol,omitempty"` // ATA | SCSI
	BridgeType        string  `json:"bridge_type,omitempty"`
}

// NVMe is the NVMe SMART/health log.
type NVMe struct {
	CriticalWarning *int     `json:"critical_warning,omitempty"`
	Warnings        []string `json:"warnings,omitempty"` // decoded bits
	Celsius         *int     `json:"celsius,omitempty"`
	AvailableSpare  *int     `json:"available_spare,omitempty"`
	SpareThreshold  *int     `json:"spare_threshold,omitempty"`
	PercentUsed     *int     `json:"percent_used,omitempty"`
	BytesRead       *int64   `json:"bytes_read,omitempty"`
	BytesWritten    *int64   `json:"bytes_written,omitempty"`
	PowerCycles     *int64   `json:"power_cycles,omitempty"`
	PowerOnHours    *int64   `json:"power_on_hours,omitempty"`
	UnsafeShutdowns *int64   `json:"unsafe_shutdowns,omitempty"`
	MediaErrors     *int64   `json:"media_errors,omitempty"`
	ErrorLogEntries *int64   `json:"error_log_entries,omitempty"`
	SelfTest        *string  `json:"last_self_test,omitempty"`
	ViaUSB          bool     `json:"via_usb,omitempty"`
}

// EMMC is what eMMC 5.0+ exposes through the kernel.
type EMMC struct {
	LifeTimeA string `json:"life_time_a,omitempty"` // "0x01".."0x0b"
	LifeTimeB string `json:"life_time_b,omitempty"`
	LifeA     string `json:"life_a,omitempty"` // "0–10% used"
	LifeB     string `json:"life_b,omitempty"`
	PreEOL    string `json:"pre_eol,omitempty"` // normal | warning | urgent
}

// Evidence is what Linux itself saw.
type Evidence struct {
	IOErrors *int64 `json:"io_errors,omitempty"` // SCSI ioerr_cnt since the device appeared
}

// Attribute is a raw SMART attribute (Advanced view).
type Attribute struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Value      int    `json:"value"`
	Worst      int    `json:"worst"`
	Thresh     int    `json:"thresh"`
	Raw        string `json:"raw"`
	WhenFailed string `json:"when_failed,omitempty"`
}

// Report is the "storage" telemetry.
type Report struct {
	At      int64    `json:"at"`
	Devices []Device `json:"devices"`
	// Smart says whether helper queries ran ("" when only sysfs was read,
	// e.g. a quick refresh between SMART scans).
	Smart bool `json:"smart"`
	// Note explains an empty report (e.g. inside a container).
	Note string `json:"note,omitempty"`
}

// Spec is what the storage collector reads.
var Spec = source.Spec{
	Files: []string{"/sys/block/*/size", "/sys/block/*/ro", "/sys/block/*/removable", "/sys/block/*/dev",
		"/sys/block/*/queue/rotational", "/sys/block/*/wwid", "/sys/block/*/device/wwid",
		"/sys/block/*/device/model", "/sys/block/*/device/vendor", "/sys/block/*/device/rev",
		"/sys/block/*/device/serial", "/sys/block/*/device/firmware_rev", "/sys/block/*/device/type",
		"/sys/block/*/device/name", "/sys/block/*/device/life_time", "/sys/block/*/device/pre_eol_info",
		"/sys/block/*/device/ioerr_cnt", "/sys/block/*/device/state",
		"/sys/block/*/*/partition", "/sys/block/*/*/dev", "/sys/block/*/*/size", "/sys/block/*/*/ro",
		"/sys/block/dm-*/dm/name", "/proc/self/mountinfo"},
	Links: []string{"/sys/block/*", "/dev/disk/by-id/*", "/dev/disk/by-uuid/*", "/sys/block/dm-*/slaves/*",
		"/sys/block/*/device/driver"},
	Tools: []string{"smartctl", "nvme"},
}

// MaxDevices bounds the devices reported.
const MaxDevices = 64

func text(src source.Source, p string) string {
	b, err := src.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func num(src source.Source, p string) (int64, bool) {
	t := text(src, p)
	if t == "" {
		return 0, false
	}
	base := 10
	if strings.HasPrefix(t, "0x") {
		t, base = t[2:], 16
	}
	n, err := strconv.ParseInt(t, base, 64)
	return n, err == nil
}

// Inventory lists physical devices with their partitions and mounts,
// from sysfs alone (cheap: run every few minutes).
func Inventory(src source.Source) []Device {
	byID := map[string][]string{}
	for _, l := range src.Glob("/dev/disk/by-id/*") {
		if t := path.Base(src.Link(l)); t != "" && t != "." {
			byID[t] = append(byID[t], path.Base(l))
		}
	}
	uuid := map[string]string{}
	for _, l := range src.Glob("/dev/disk/by-uuid/*") {
		if t := path.Base(src.Link(l)); t != "" {
			uuid[t] = path.Base(l)
		}
	}
	// Mounts by device number; device-mapper devices by number with their slaves.
	mounts := map[string][]string{}
	if b, err := src.ReadFile("/proc/self/mountinfo"); err == nil {
		for _, m := range monitor.ParseMountinfo(b) {
			mounts[m.DevNum] = append(mounts[m.DevNum], m.Point)
		}
	}
	dmOf := map[string]string{} // slave name → dm name
	for _, s := range src.Glob("/sys/block/dm-*/slaves/*") {
		dm := strings.Split(s, "/")[3]
		name := text(src, "/sys/block/"+dm+"/dm/name")
		if name == "" {
			name = dm
		}
		if mm := mounts[text(src, "/sys/block/"+dm+"/dev")]; len(mm) > 0 {
			mounts["slave:"+path.Base(s)] = append(mounts["slave:"+path.Base(s)], mm...)
		}
		dmOf[path.Base(s)] = name
	}
	var out []Device
	for _, b := range src.Glob("/sys/block/*") {
		name := path.Base(b)
		if !monitor.PhysicalDiskName(name) || strings.HasPrefix(name, "mmcblk") && strings.Contains(name, "boot") ||
			strings.Contains(name, "rpmb") {
			continue
		}
		d := Device{Name: name, Transport: "unknown", Kind: "unknown"}
		if n, ok := num(src, b+"/size"); ok {
			d.SizeBytes = n * 512
		}
		if d.SizeBytes == 0 {
			continue // empty card reader slots and the like
		}
		ro, _ := num(src, b+"/ro")
		d.ReadOnly = ro == 1
		rem, _ := num(src, b+"/removable")
		d.Removable = rem == 1
		if r, ok := num(src, b+"/queue/rotational"); ok {
			v := r == 1
			d.Rotational = &v
		}
		d.Model = strings.TrimSpace(text(src, b+"/device/model"))
		vendor := strings.TrimSpace(text(src, b+"/device/vendor"))
		d.Serial = text(src, b+"/device/serial")
		d.Firmware = text(src, b+"/device/firmware_rev")
		if d.Firmware == "" {
			d.Firmware = text(src, b+"/device/rev")
		}
		ids := byID[name]
		sort.Strings(ids)
		if len(ids) > 8 {
			ids = ids[:8]
		}
		d.IDs = ids
		classify(src, b, &d, vendor)
		if n, ok := num(src, b+"/device/ioerr_cnt"); ok {
			d.Evid.IOErrors = &n
		}
		d.Mounts = mounts[text(src, b+"/dev")]
		for _, pf := range src.Glob(b + "/" + name + "*/partition") {
			pd := path.Dir(pf)
			pn := path.Base(pd)
			p := Partition{Name: pn, UUID: uuid[pn]}
			if n, ok := num(src, pd+"/size"); ok {
				p.SizeBytes = n * 512
			}
			r, _ := num(src, pd+"/ro")
			p.ReadOnly = r == 1
			p.Mounts = append(mounts[text(src, pd+"/dev")], mounts["slave:"+pn]...)
			p.Via = dmOf[pn]
			d.Partitions = append(d.Partitions, p)
		}
		sort.Slice(d.Partitions, func(i, j int) bool { return d.Partitions[i].Name < d.Partitions[j].Name })
		d.Key = deviceKey(d)
		out = append(out, d)
		if len(out) >= MaxDevices {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// classify decides transport and kind from sysfs.
func classify(src source.Source, b string, d *Device, vendor string) {
	real := src.Link(b)
	model := strings.ToUpper(d.Model + " " + vendor)
	switch {
	case strings.HasPrefix(d.Name, "nvme"):
		d.Transport, d.Kind = "nvme", "nvme"
	case strings.HasPrefix(d.Name, "mmcblk"):
		d.Transport = "mmc"
		switch text(src, b+"/device/type") {
		case "MMC":
			d.Kind = "emmc"
		case "SDIO":
			d.Kind = "unknown"
		default:
			d.Kind = "sd"
		}
		if d.Model == "" {
			d.Model = text(src, b+"/device/name")
		}
	case strings.HasPrefix(d.Name, "vd") || strings.Contains(real, "/virtio"):
		d.Transport, d.Kind = "virtio", "virtual"
	case strings.HasPrefix(d.Name, "xvd"):
		d.Transport, d.Kind = "xen", "virtual"
	case strings.Contains(real, "/usb"):
		d.Transport, d.Kind = "usb", "usb"
	case strings.Contains(real, "/ata"):
		d.Transport = "sata"
	case strings.HasPrefix(d.Name, "sd"):
		d.Transport = "scsi"
		if strings.Contains(path.Base(src.Link(b+"/device/driver")), "sas") || strings.Contains(real, "/sas") {
			d.Transport = "sas"
		}
	}
	if strings.Contains(model, "QEMU") || strings.Contains(model, "VBOX") || strings.Contains(model, "VMWARE") ||
		strings.Contains(model, "VIRTUAL DISK") || strings.Contains(model, "MSFT") && strings.Contains(model, "VIRTUAL") {
		d.Kind = "virtual"
	}
	if d.Kind == "unknown" && (d.Transport == "sata" || d.Transport == "scsi" || d.Transport == "sas") {
		d.Kind = "ssd"
		if d.Rotational != nil && *d.Rotational {
			d.Kind = "hdd"
		}
	}
}

// deviceKey prefers identifiers that survive renaming (/dev/sda can be
// another disk after a reboot): WWN, NVMe EUI/NGUID, then model+serial.
func deviceKey(d Device) string {
	for _, pre := range []string{"wwn-", "nvme-eui.", "nvme-nvme."} {
		for _, id := range d.IDs {
			if strings.HasPrefix(id, pre) && !strings.Contains(id, "-part") {
				return id
			}
		}
	}
	if d.Serial != "" {
		return "serial:" + strings.ReplaceAll(d.Model, " ", "_") + "_" + d.Serial
	}
	for _, id := range d.IDs {
		if !strings.Contains(id, "-part") {
			return id
		}
	}
	return "name:" + d.Name
}

// Collect inventories devices and, with smart, queries their health
// through the installed helpers. prev (the last full report) carries
// health forward for disks asleep in standby (DROOL doesn't wake them).
func Collect(ctx context.Context, src source.Source, smart bool, prev []Device) *Report {
	devs := Inventory(src)
	old := map[string]Device{}
	for _, d := range prev {
		old[d.Key] = d
		// Helper data can refine the key (a serial smartctl read); a
		// disk asleep at the next scan is found by name.
		old["name:"+d.Name] = d
	}
	for i := range devs {
		d := &devs[i]
		if o, ok := old["name:"+d.Name]; ok && o.Key != d.Key && strings.HasPrefix(d.Key, "name:") {
			d.Key = o.Key
		}
		if smart {
			query(ctx, src, d)
			if o, ok := old[d.Key]; ok && d.Standby {
				// Asleep: keep the last reading rather than waking it.
				carry(d, o)
				d.Standby = true
			}
		} else if o, ok := old[d.Key]; ok {
			carry(d, o)
		} else {
			baseline(src, d)
		}
		Judge(d)
	}
	return &Report{Devices: devs, Smart: smart}
}

// carry keeps the last helper results between SMART scans.
func carry(d *Device, o Device) {
	if d.Serial == "" {
		d.Model, d.Serial, d.Firmware = o.Model, o.Serial, o.Firmware
	}
	d.Capability, d.CapabilityNote, d.Source, d.Standby, d.Helper = o.Capability, o.CapabilityNote, o.Source, o.Standby, o.Helper
	d.SMART, d.NVMe, d.Raw = o.SMART, o.NVMe, o.Raw
	if d.Kind == "usb" && o.Kind != "" {
		d.Kind = o.Kind
	}
	if d.EMMC == nil {
		d.EMMC = o.EMMC
	}
}

// baseline sets the capability from sysfs alone.
func baseline(src source.Source, d *Device) {
	switch d.Kind {
	case "virtual":
		d.Capability, d.CapabilityNote = CapUnavailable, "Virtual disk: the hypervisor owns the physical storage and its health."
	case "sd":
		d.Capability, d.CapabilityNote = CapLimited, "SD cards don't report standardized health or wear. DROOL watches IO errors, resets, read-only changes and (if enabled) a write/read probe."
	case "emmc":
		readEMMC(src, d)
	default:
		d.Capability = CapUnavailable
	}
}

// query runs the helpers for one device.
func query(ctx context.Context, src source.Source, d *Device) {
	switch d.Kind {
	case "virtual", "sd":
		baseline(src, d)
		return
	case "emmc":
		readEMMC(src, d)
		return
	}
	dev := "/dev/" + d.Name
	if src.Has("smartctl") {
		r, err := src.Run(ctx, "smartctl", "-j", "-a", "-n", "standby", dev)
		if err == source.ErrDeferred {
			return
		}
		if err == nil && r != nil {
			res := ParseSmartctl(r.Stdout, r.Exit)
			if res.UnknownBridge && d.Transport == "usb" {
				// Unknown USB bridge: try SAT passthrough, which most
				// SATA bridges speak. Only after smartctl itself gave up.
				r2, err := src.Run(ctx, "smartctl", "-j", "-a", "-n", "standby", "-d", "sat", dev)
				if err == source.ErrDeferred {
					return
				}
				if err == nil && r2 != nil {
					if res2 := ParseSmartctl(r2.Stdout, r2.Exit); res2.Usable() {
						res = res2
					}
				}
			}
			applySmartctl(d, res)
			if d.Capability != "" {
				return
			}
		} else if err != nil && err != source.ErrNotFound {
			d.Capability, d.CapabilityNote, d.Source = CapUnavailable, "smartctl failed: "+err.Error(), "smartctl"
			return
		}
	}
	if d.Kind == "nvme" && src.Has("nvme") {
		r, err := src.Run(ctx, "nvme", "smart-log", "-o", "json", dev)
		if err == source.ErrDeferred {
			return
		}
		if err == nil && r != nil && r.Exit == 0 {
			if n, ok := ParseNVMeCLI(r.Stdout); ok {
				d.NVMe, d.Source, d.Capability = n, "nvme-cli", CapFull
				return
			}
		}
		d.Capability, d.CapabilityNote, d.Source = CapUnavailable, "nvme-cli could not read the health log (it needs root).", "nvme-cli"
		return
	}
	missing(src, d)
}

// missing explains which helper would provide health data.
func missing(src source.Source, d *Device) {
	switch d.Kind {
	case "nvme":
		d.Capability, d.Helper = CapUnavailable, "nvme-cli"
		d.CapabilityNote = "NVMe health is unavailable because neither nvme-cli nor smartmontools is installed. Installing nvme-cli enables critical warnings, endurance, spare capacity and media-error information."
	case "usb":
		d.Capability, d.Helper = CapUnavailable, "smartmontools"
		d.CapabilityNote = "Whether this USB device passes health data through is unknown because smartmontools is not installed. Installing it lets DROOL check (many USB bridges for SATA and NVMe drives support it; USB sticks don't)."
	default:
		d.Capability, d.Helper = CapUnavailable, "smartmontools"
		d.CapabilityNote = "Detailed SMART health is unavailable because smartmontools is not installed. Installing it enables the overall SMART result, reallocated and pending sectors, temperature, power-on hours and self-test results."
	}
}

var lifeText = map[string]string{"0x00": "not defined", "0x01": "0–10% used", "0x02": "10–20% used", "0x03": "20–30% used",
	"0x04": "30–40% used", "0x05": "40–50% used", "0x06": "50–60% used", "0x07": "60–70% used", "0x08": "70–80% used",
	"0x09": "80–90% used", "0x0a": "90–100% used", "0x0b": "beyond its estimated life"}

// readEMMC reads the eMMC 5.0 life-time estimate and pre-EOL registers.
func readEMMC(src source.Source, d *Device) {
	b := "/sys/block/" + d.Name + "/device/"
	lt := strings.Fields(strings.ToLower(text(src, b+"life_time")))
	eol := strings.ToLower(text(src, b+"pre_eol_info"))
	e := &EMMC{}
	if len(lt) == 2 {
		e.LifeTimeA, e.LifeTimeB = norm2(lt[0]), norm2(lt[1])
		e.LifeA, e.LifeB = lifeText[e.LifeTimeA], lifeText[e.LifeTimeB]
	}
	switch norm2(eol) {
	case "0x01":
		e.PreEOL = "normal"
	case "0x02":
		e.PreEOL = "warning"
	case "0x03":
		e.PreEOL = "urgent"
	}
	if e.LifeA == "" && e.PreEOL == "" || e.LifeTimeA == "0x00" && e.LifeTimeB == "0x00" && e.PreEOL == "" {
		d.EMMC = nil
		d.Capability = CapLimited
		d.CapabilityNote = "This eMMC doesn't expose life-time or pre-EOL information. DROOL watches IO errors, resets and read-only changes."
		return
	}
	d.EMMC, d.Capability, d.Source = e, CapPartial, "sysfs"
	d.CapabilityNote = "eMMC life-time estimate and pre-EOL state from the device's registers; no SMART."
}

func norm2(s string) string {
	s = strings.TrimPrefix(strings.TrimSpace(s), "0x")
	n, err := strconv.ParseUint(s, 16, 8)
	if err != nil {
		return ""
	}
	return "0x" + strconv.FormatUint(n+0x100, 16)[1:]
}
