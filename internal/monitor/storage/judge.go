package storage

import (
	"fmt"
	"strings"
)

func ind(name, value, status string) Indicator {
	return Indicator{Name: name, Value: value, Status: status}
}

// Judge sets d.Health from the data the device actually provided. It
// never reports "ok" for data that wasn't read.
func Judge(d *Device) {
	h := Health{Indicators: []Indicator{}}
	bad, warn := 0, 0
	add := func(i Indicator) {
		h.Indicators = append(h.Indicators, i)
		switch i.Status {
		case "critical":
			bad++
		case "warning":
			warn++
		}
	}
	countInd := func(name string, v *int64, status string) {
		if v == nil {
			return
		}
		st := "ok"
		if *v > 0 {
			st = status
		}
		add(ind(name, fmt.Sprint(*v), st))
	}
	if s := d.SMART; s != nil {
		if s.Passed != nil {
			if *s.Passed {
				add(ind("SMART overall result", "Passed", "ok"))
			} else {
				add(ind("SMART overall result", "FAILED: the drive predicts its own failure", "critical"))
			}
		}
		countInd("Reallocated sectors", s.Reallocated, "warning")
		countInd("Pending sectors", s.Pending, "warning")
		countInd("Offline uncorrectable sectors", s.OfflineUncorrect, "warning")
		countInd("Reported uncorrectable errors", s.ReportedUncorrect, "warning")
		countInd("Interface CRC errors", s.CRCErrors, "info")
		countInd("Grown defects", s.GrownDefects, "warning")
		countInd("Uncorrected read/write errors", s.UncorrectedErrors, "warning")
		if s.EnduranceUsed != nil {
			st := "ok"
			if *s.EnduranceUsed >= 100 {
				st = "warning"
			}
			v := fmt.Sprintf("%d%% used", *s.EnduranceUsed)
			if s.EnduranceVendor {
				v += " (vendor-specific attribute)"
			}
			add(ind("Endurance", v, st))
		}
		if s.SelfTest != nil {
			st := "info"
			if s.SelfTestPassed != nil {
				st = "ok"
				if !*s.SelfTestPassed {
					st = "warning"
				}
			}
			add(ind("Last self-test", *s.SelfTest, st))
		}
	}
	if n := d.NVMe; n != nil {
		if n.CriticalWarning != nil {
			if len(n.Warnings) == 0 {
				add(ind("Critical warnings", "None", "ok"))
			} else {
				add(ind("Critical warnings", strings.Join(n.Warnings, "; "), "critical"))
			}
		}
		if n.AvailableSpare != nil {
			st := "ok"
			v := fmt.Sprintf("%d%%", *n.AvailableSpare)
			if n.SpareThreshold != nil {
				v += fmt.Sprintf(" (threshold %d%%)", *n.SpareThreshold)
				if *n.AvailableSpare < *n.SpareThreshold {
					st = "critical"
				}
			}
			add(ind("Available spare", v, st))
		}
		if n.PercentUsed != nil {
			st := "ok"
			if *n.PercentUsed >= 100 {
				st = "warning"
			}
			add(ind("Endurance used", fmt.Sprintf("%d%%", *n.PercentUsed), st))
		}
		countInd("Media and data integrity errors", n.MediaErrors, "warning")
		if n.ErrorLogEntries != nil {
			add(ind("Error log entries", fmt.Sprint(*n.ErrorLogEntries), "info"))
		}
		if n.UnsafeShutdowns != nil {
			add(ind("Unsafe shutdowns", fmt.Sprint(*n.UnsafeShutdowns), "info"))
		}
	}
	if e := d.EMMC; e != nil {
		for _, lt := range []struct{ name, code, text string }{{"Life time estimate (type A)", e.LifeTimeA, e.LifeA}, {"Life time estimate (type B)", e.LifeTimeB, e.LifeB}} {
			if lt.text == "" || lt.code == "0x00" {
				continue
			}
			st := "ok"
			switch lt.code {
			case "0x0a":
				st = "warning"
			case "0x0b":
				st = "critical"
			}
			add(ind(lt.name, lt.text, st))
		}
		switch e.PreEOL {
		case "normal":
			add(ind("Reserved blocks (pre-EOL)", "Normal", "ok"))
		case "warning":
			add(ind("Reserved blocks (pre-EOL)", "Warning: 80% of reserved blocks consumed", "warning"))
		case "urgent":
			add(ind("Reserved blocks (pre-EOL)", "Urgent: reserved blocks nearly exhausted", "critical"))
		}
	}
	if d.Evid.IOErrors != nil {
		// Failed commands of any kind, including harmless ones; only
		// increases are judged (by the controller), so this is info.
		add(ind("Failed IO commands since the device appeared", fmt.Sprint(*d.Evid.IOErrors), "info"))
	}
	switch {
	case d.ReadOnly && d.Kind == "virtual":
		// The hypervisor presents it read-only (a cloud-init seed image, a
		// read-only volume): a fact, not a health problem.
		add(ind("Block device", "Read-only (provided read-only by the hypervisor)", "info"))
	case d.ReadOnly && !d.Removable:
		// Flash that locks itself read-only is failing; a device turning
		// read-only within a boot is also a separate health check.
		add(ind("Block device", "Read-only", "warning"))
	}
	switch {
	case bad > 0:
		h.State, h.Summary = StateFailing, "Failure indicators: "+firstBad(h.Indicators, "critical")
	case warn > 0:
		h.State, h.Summary = StateWarning, "Warning: "+firstBad(h.Indicators, "warning")
	case d.Capability == CapFull || d.Capability == CapPartial || d.Capability == CapVendor:
		h.State = StateOK
		switch {
		case d.Standby:
			h.Summary = "No problems at the last reading (disk now in standby)"
		case d.SMART != nil && d.SMART.Passed != nil:
			h.Summary = "Healthy: SMART passed, no problems reported"
		case d.NVMe != nil:
			h.Summary = "Healthy: no problems in the NVMe health log"
		case d.EMMC != nil:
			h.Summary = "No wear warnings reported by the eMMC"
		default:
			h.Summary = "No problems reported (vendor-specific data only)"
		}
	case d.Capability == CapLimited:
		h.State, h.Summary = StateNoIndicators, "No failure indicators detected"
	default:
		h.State, h.Summary = StateUnknown, "Health unknown"
	}
	d.Health = h
}

func firstBad(is []Indicator, status string) string {
	var out []string
	for _, i := range is {
		if i.Status == status {
			name := i.Name
			if len(name) > 1 && !(name[1] >= 'A' && name[1] <= 'Z') {
				name = strings.ToLower(name[:1]) + name[1:]
			}
			out = append(out, name+" "+i.Value)
		}
	}
	return strings.Join(out, "; ")
}
