package storage

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// SmartctlResult is smartctl -j output, interpreted.
type SmartctlResult struct {
	raw           smartJSON
	Exit          int
	Parsed        bool
	Standby       bool
	Denied        bool
	UnknownBridge bool
	NoSupport     bool // the device (or bridge) doesn't support SMART
	Message       string
}

// Usable reports whether smartctl actually read health data.
func (r SmartctlResult) Usable() bool {
	return r.Parsed && !r.UnknownBridge && !r.Denied && !r.NoSupport && !r.Standby &&
		(r.raw.SmartStatus != nil || r.raw.NVMeLog != nil || len(r.raw.ATAAttrs.Table) > 0)
}

type smartJSON struct {
	Smartctl struct {
		ExitStatus int `json:"exit_status"`
		Messages   []struct {
			String   string `json:"string"`
			Severity string `json:"severity"`
		} `json:"messages"`
	} `json:"smartctl"`
	Device struct {
		Type     string `json:"type"`
		Protocol string `json:"protocol"`
	} `json:"device"`
	ModelName       string `json:"model_name"`
	SerialNumber    string `json:"serial_number"`
	FirmwareVersion string `json:"firmware_version"`
	WWN             *struct {
		NAA uint64 `json:"naa"`
		OUI uint64 `json:"oui"`
		ID  uint64 `json:"id"`
	} `json:"wwn"`
	LogicalBlockSize int64 `json:"logical_block_size"`
	RotationRate     *int  `json:"rotation_rate"`
	SmartSupport     *struct {
		Available bool `json:"available"`
		Enabled   bool `json:"enabled"`
	} `json:"smart_support"`
	SmartStatus *struct {
		Passed bool `json:"passed"`
	} `json:"smart_status"`
	Temperature *struct {
		Current *int `json:"current"`
	} `json:"temperature"`
	PowerOnTime *struct {
		Hours *int64 `json:"hours"`
	} `json:"power_on_time"`
	PowerCycleCount *int64 `json:"power_cycle_count"`
	ATAAttrs        struct {
		Table []struct {
			ID         int    `json:"id"`
			Name       string `json:"name"`
			Value      int    `json:"value"`
			Worst      int    `json:"worst"`
			Thresh     int    `json:"thresh"`
			WhenFailed string `json:"when_failed"`
			Raw        struct {
				Value  int64  `json:"value"`
				String string `json:"string"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
	ATAStats *struct {
		Pages []struct {
			Name  string `json:"name"`
			Table []struct {
				Name  string `json:"name"`
				Value *int64 `json:"value"`
				Flags struct {
					Valid bool `json:"valid"`
				} `json:"flags"`
			} `json:"table"`
		} `json:"pages"`
	} `json:"ata_device_statistics"`
	ATAErrorLog *struct {
		Summary *struct {
			Count *int64 `json:"count"`
		} `json:"summary"`
	} `json:"ata_smart_error_log"`
	ATASelfTest *struct {
		Standard *struct {
			Table []struct {
				Type   struct{ String string } `json:"type"`
				Status struct {
					String string `json:"string"`
					Passed *bool  `json:"passed"`
				} `json:"status"`
			} `json:"table"`
		} `json:"standard"`
	} `json:"ata_smart_self_test_log"`
	NVMeLog *struct {
		CriticalWarning *int   `json:"critical_warning"`
		Temperature     *int   `json:"temperature"`
		AvailableSpare  *int   `json:"available_spare"`
		SpareThreshold  *int   `json:"available_spare_threshold"`
		PercentageUsed  *int   `json:"percentage_used"`
		DataUnitsRead   *int64 `json:"data_units_read"`
		DataUnitsWrite  *int64 `json:"data_units_written"`
		PowerCycles     *int64 `json:"power_cycles"`
		PowerOnHours    *int64 `json:"power_on_hours"`
		UnsafeShutdowns *int64 `json:"unsafe_shutdowns"`
		MediaErrors     *int64 `json:"media_errors"`
		ErrLogEntries   *int64 `json:"num_err_log_entries"`
	} `json:"nvme_smart_health_information_log"`
	NVMeSelfTest *struct {
		Table []struct {
			Result struct{ String string } `json:"self_test_result"`
		} `json:"table"`
	} `json:"nvme_self_test_log"`
	SCSIDefects *int64 `json:"scsi_grown_defect_list"`
	SCSIErrors  *struct {
		Read *struct {
			Uncorrected *int64 `json:"total_uncorrected_errors"`
		} `json:"read"`
		Write *struct {
			Uncorrected *int64 `json:"total_uncorrected_errors"`
		} `json:"write"`
	} `json:"scsi_error_counter_log"`
}

// ParseSmartctl interprets smartctl -j -a output. exit is smartctl's
// exit status (a bit mask; the JSON repeats it when present).
func ParseSmartctl(b []byte, exit int) SmartctlResult {
	r := SmartctlResult{Exit: exit}
	if err := json.Unmarshal(b, &r.raw); err != nil {
		r.Message = "smartctl output not understood"
		return r
	}
	r.Parsed = true
	if r.raw.Smartctl.ExitStatus != 0 {
		r.Exit = r.raw.Smartctl.ExitStatus
	}
	var msgs []string
	for _, m := range r.raw.Smartctl.Messages {
		s := m.String
		msgs = append(msgs, s)
		l := strings.ToLower(s)
		switch {
		case strings.Contains(l, "standby") || strings.Contains(l, "sleep mode"):
			r.Standby = true
		case strings.Contains(l, "permission denied") || strings.Contains(l, "operation not permitted"):
			r.Denied = true
		case strings.Contains(l, "unknown usb bridge"):
			r.UnknownBridge = true
		case strings.Contains(l, "unsupported field in scsi command") || strings.Contains(l, "does not support smart") ||
			strings.Contains(l, "device lacks smart capability") || strings.Contains(l, "smart support is: unavailable"):
			r.NoSupport = true
		}
	}
	if r.raw.SmartSupport != nil && !r.raw.SmartSupport.Available && r.raw.NVMeLog == nil {
		r.NoSupport = true
	}
	r.Message = strings.Join(msgs, "; ")
	return r
}

func p64(v int64) *int64 { return &v }
func pint(v int) *int    { return &v }

// Attribute names whose meaning is standardized enough to normalize.
// smartctl names attributes from its drive database, so a vendor's
// reuse of an ID under another meaning shows up under another name and
// is left alone (it stays visible in the raw table).
var (
	attrReallocated = map[string]bool{"Reallocated_Sector_Ct": true}
	attrPending     = map[string]bool{"Current_Pending_Sector": true, "Current_Pending_Ecc_Cnt": true}
	attrOffline     = map[string]bool{"Offline_Uncorrectable": true}
	attrReported    = map[string]bool{"Reported_Uncorrect": true}
	attrCRC         = map[string]bool{"UDMA_CRC_Error_Count": true, "CRC_Error_Count": true}
	// Vendor wear attributes: normalized value counts down from 100.
	attrWearRemaining = map[string]bool{"Wear_Leveling_Count": true, "Percent_Lifetime_Remain": true,
		"Media_Wearout_Indicator": true, "SSD_Life_Left": true, "Remaining_Lifetime_Perc": true, "Percent_Life_Remaining": true}
)

// rawCount is the leading count of a raw value string ("12 (Min/Max 0/3)").
func rawCount(s string, v int64) int64 {
	if f := strings.Fields(s); len(f) > 0 {
		if n, err := strconv.ParseInt(f[0], 10, 64); err == nil {
			return n
		}
	}
	return v & 0xFFFFFFFF
}

func applySmartctl(d *Device, r SmartctlResult) {
	d.Source = "smartctl"
	j := r.raw
	switch {
	case !r.Parsed:
		d.Capability, d.CapabilityNote = CapUnavailable, "smartctl output could not be read."
		return
	case r.Standby:
		d.Standby = true
		d.Capability, d.CapabilityNote = CapPartial, "The disk is in standby; DROOL doesn't wake it to read SMART. Health from the last reading."
		return
	case r.Denied:
		d.Capability, d.CapabilityNote = CapUnavailable, "smartctl needs root to read this disk (over SSH: install the agent, or allow DROOL to use sudo)."
		return
	case r.UnknownBridge:
		d.Kind = "usb-flash"
		d.Capability, d.CapabilityNote = CapLimited, "This USB bridge doesn't pass SMART data through. DROOL watches IO errors, USB resets and read-only changes."
		return
	case r.NoSupport && j.NVMeLog == nil:
		if d.Transport == "usb" {
			d.Kind = "usb-flash"
		}
		d.Capability, d.CapabilityNote = CapLimited, "This device doesn't support SMART. DROOL watches IO errors, resets and read-only changes."
		return
	}
	if d.Model == "" || len(j.ModelName) > len(d.Model) {
		d.Model = j.ModelName
	}
	if j.SerialNumber != "" {
		d.Serial = j.SerialNumber
	}
	if j.FirmwareVersion != "" {
		d.Firmware = j.FirmwareVersion
	}
	if j.WWN != nil && !hasID(d.IDs, "wwn-") {
		d.Key = fmt.Sprintf("wwn-0x%x%06x%09x", j.WWN.NAA, j.WWN.OUI, j.WWN.ID)
	} else if d.Serial != "" && strings.HasPrefix(d.Key, "name:") {
		d.Key = "serial:" + strings.ReplaceAll(d.Model, " ", "_") + "_" + d.Serial
	}
	if j.NVMeLog != nil {
		d.NVMe = nvmeFromSmartctl(j)
		d.Capability = CapFull
		if d.Transport == "usb" {
			d.Kind, d.NVMe.ViaUSB = "usb-nvme", true
			d.CapabilityNote = "NVMe health passed through the USB enclosure (" + j.Device.Type + ")."
		}
		return
	}
	s := &SMART{Protocol: j.Device.Protocol}
	if d.Transport == "usb" {
		s.BridgeType = j.Device.Type
		d.Kind = "usb-sata"
	}
	if j.SmartStatus != nil {
		v := j.SmartStatus.Passed
		s.Passed = &v
	}
	if j.Temperature != nil && j.Temperature.Current != nil && *j.Temperature.Current > -40 && *j.Temperature.Current < 150 {
		s.Celsius = j.Temperature.Current
	}
	if j.PowerOnTime != nil {
		s.PowerOnHours = j.PowerOnTime.Hours
	}
	s.PowerCycles = j.PowerCycleCount
	std := 0
	for _, a := range j.ATAAttrs.Table {
		d.Raw = append(d.Raw, Attribute{ID: a.ID, Name: a.Name, Value: a.Value, Worst: a.Worst, Thresh: a.Thresh,
			Raw: a.Raw.String, WhenFailed: a.WhenFailed})
		n := rawCount(a.Raw.String, a.Raw.Value)
		switch {
		case attrReallocated[a.Name]:
			s.Reallocated, std = p64(n), std+1
		case attrPending[a.Name]:
			s.Pending, std = p64(n), std+1
		case attrOffline[a.Name]:
			s.OfflineUncorrect, std = p64(n), std+1
		case attrReported[a.Name]:
			s.ReportedUncorrect = p64(n)
		case attrCRC[a.Name]:
			s.CRCErrors = p64(n)
		case attrWearRemaining[a.Name] && a.Value >= 0 && a.Value <= 100 && s.EnduranceUsed == nil:
			s.EnduranceUsed, s.EnduranceVendor = pint(100-a.Value), true
		}
	}
	if len(d.Raw) > 64 {
		d.Raw = d.Raw[:64]
	}
	// ACS device statistics are standardized: prefer them.
	if j.ATAStats != nil {
		bs := j.LogicalBlockSize
		if bs <= 0 {
			bs = 512
		}
		for _, p := range j.ATAStats.Pages {
			for _, e := range p.Table {
				if e.Value == nil || !e.Flags.Valid {
					continue
				}
				switch e.Name {
				case "Percentage Used Endurance Indicator":
					s.EnduranceUsed, s.EnduranceVendor = pint(int(*e.Value)), false
				case "Logical Sectors Written":
					s.BytesWritten = p64(*e.Value * bs)
				case "Logical Sectors Read":
					s.BytesRead = p64(*e.Value * bs)
				}
			}
		}
	}
	if j.ATAErrorLog != nil && j.ATAErrorLog.Summary != nil {
		s.ErrorLogCount = j.ATAErrorLog.Summary.Count
	}
	if j.ATASelfTest != nil && j.ATASelfTest.Standard != nil && len(j.ATASelfTest.Standard.Table) > 0 {
		t := j.ATASelfTest.Standard.Table[0]
		st := strings.TrimSpace(t.Type.String + ": " + t.Status.String)
		s.SelfTest, s.SelfTestPassed = &st, t.Status.Passed
	}
	if j.SCSIDefects != nil {
		s.GrownDefects = j.SCSIDefects
		std++
	}
	if j.SCSIErrors != nil {
		var n int64
		ok := false
		if j.SCSIErrors.Read != nil && j.SCSIErrors.Read.Uncorrected != nil {
			n, ok = n+*j.SCSIErrors.Read.Uncorrected, true
		}
		if j.SCSIErrors.Write != nil && j.SCSIErrors.Write.Uncorrected != nil {
			n, ok = n+*j.SCSIErrors.Write.Uncorrected, true
		}
		if ok {
			s.UncorrectedErrors = &n
		}
	}
	d.SMART = s
	switch {
	case s.Passed != nil && std > 0:
		d.Capability = CapFull
	case s.Passed != nil:
		d.Capability, d.CapabilityNote = CapPartial, "Only the overall SMART result is available; no standardized attributes."
	case len(d.Raw) > 0:
		d.Capability, d.CapabilityNote = CapVendor, "Only vendor-specific SMART attributes are available; their meaning varies by manufacturer."
	default:
		d.Capability, d.CapabilityNote = CapLimited, "smartctl returned no health data for this device."
	}
	if d.Transport == "usb" && d.Capability != CapLimited {
		d.CapabilityNote = strings.TrimSpace("SMART passed through the USB bridge (" + j.Device.Type + "). " + d.CapabilityNote)
	}
}

func hasID(ids []string, pre string) bool {
	for _, id := range ids {
		if strings.HasPrefix(id, pre) {
			return true
		}
	}
	return false
}

// NVMe data units are 1000 × 512 bytes.
const nvmeUnit = 512000

func nvmeFromSmartctl(j smartJSON) *NVMe {
	l := j.NVMeLog
	n := &NVMe{CriticalWarning: l.CriticalWarning, AvailableSpare: l.AvailableSpare, SpareThreshold: l.SpareThreshold,
		PercentUsed: l.PercentageUsed, PowerCycles: l.PowerCycles, PowerOnHours: l.PowerOnHours,
		UnsafeShutdowns: l.UnsafeShutdowns, MediaErrors: l.MediaErrors, ErrorLogEntries: l.ErrLogEntries}
	if l.Temperature != nil && *l.Temperature > -40 && *l.Temperature < 150 {
		n.Celsius = l.Temperature
	}
	if l.DataUnitsRead != nil {
		n.BytesRead = p64(*l.DataUnitsRead * nvmeUnit)
	}
	if l.DataUnitsWrite != nil {
		n.BytesWritten = p64(*l.DataUnitsWrite * nvmeUnit)
	}
	if j.NVMeSelfTest != nil && len(j.NVMeSelfTest.Table) > 0 {
		s := j.NVMeSelfTest.Table[0].Result.String
		n.SelfTest = &s
	}
	n.Warnings = decodeCritical(n.CriticalWarning)
	return n
}

// decodeCritical names the NVMe critical-warning bits.
func decodeCritical(cw *int) []string {
	if cw == nil || *cw == 0 {
		return nil
	}
	names := []string{"available spare below threshold", "temperature outside the allowed range",
		"reliability degraded by media or internal errors", "media placed in read-only mode",
		"volatile memory backup failed", "persistent memory region read-only"}
	var out []string
	for i, n := range names {
		if *cw&(1<<i) != 0 {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		out = append(out, fmt.Sprintf("critical warning 0x%x", *cw))
	}
	return out
}

// ParseNVMeCLI parses `nvme smart-log -o json`. Field names and value
// formats differ between nvme-cli versions (numbers or numeric strings,
// temperature in Kelvin); anything not understood is left out.
func ParseNVMeCLI(b []byte) (*NVMe, bool) {
	var m map[string]any
	if json.Unmarshal(b, &m) != nil || len(m) == 0 {
		return nil, false
	}
	get := func(keys ...string) (int64, bool) {
		for _, k := range keys {
			v, ok := m[k]
			if !ok {
				continue
			}
			switch x := v.(type) {
			case float64:
				return int64(x), true
			case string:
				x = strings.TrimSpace(strings.TrimSuffix(strings.Fields(x + " ")[0], "%"))
				x = strings.ReplaceAll(x, ",", "")
				if n, err := strconv.ParseInt(x, 0, 64); err == nil {
					return n, true
				}
			case map[string]any: // nvme-cli 2.x: {"value": ...}
				if f, ok := x["value"].(float64); ok {
					return int64(f), true
				}
			}
		}
		return 0, false
	}
	n := &NVMe{}
	found := 0
	if v, ok := get("critical_warning"); ok {
		n.CriticalWarning, found = pint(int(v)), found+1
	}
	if v, ok := get("temperature"); ok {
		c := int(v)
		if c > 200 { // Kelvin
			c -= 273
		}
		if c > -40 && c < 150 {
			n.Celsius = &c
		}
	}
	if v, ok := get("avail_spare", "available_spare"); ok {
		n.AvailableSpare, found = pint(int(v)), found+1
	}
	if v, ok := get("spare_thresh", "available_spare_threshold"); ok {
		n.SpareThreshold = pint(int(v))
	}
	if v, ok := get("percent_used", "percentage_used"); ok {
		n.PercentUsed, found = pint(int(v)), found+1
	}
	if v, ok := get("data_units_read"); ok {
		n.BytesRead = p64(v * nvmeUnit)
	}
	if v, ok := get("data_units_written"); ok {
		n.BytesWritten = p64(v * nvmeUnit)
	}
	if v, ok := get("power_cycles"); ok {
		n.PowerCycles = p64(v)
	}
	if v, ok := get("power_on_hours"); ok {
		n.PowerOnHours = p64(v)
	}
	if v, ok := get("unsafe_shutdowns"); ok {
		n.UnsafeShutdowns = p64(v)
	}
	if v, ok := get("media_errors"); ok {
		n.MediaErrors, found = p64(v), found+1
	}
	if v, ok := get("num_err_log_entries"); ok {
		n.ErrorLogEntries = p64(v)
	}
	n.Warnings = decodeCritical(n.CriticalWarning)
	return n, found >= 2
}
