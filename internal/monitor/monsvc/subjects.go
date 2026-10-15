package monsvc

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"drool/internal/monitor"
	"drool/internal/monitor/storage"
)

// Subject identity (M12.1). A subject override binds to the most stable
// identity DROOL has for the thing, never to a name that can mean another
// device after a reboot (gpu0, sda, hwmon3). Where only a weaker identity
// exists it is used and labelled as such, never presented as stronger.
//
//	filesystem  fsuuid:<UUID>  (filesystem UUID; mount:<path> when the
//	            UUID is unknown or shared by several mounts)
//	disk        disk:<WWN | NVMe EUI | model+serial>  (disk:name:sda is weak)
//	GPU         gpu:<NVIDIA GPU UUID | amd:unique_id>, else gpu:pci:<address>
//	sensor      sensor:<disk identity>/<label> for drive sensors,
//	            sensor:<chip>@<sysfs device>/<label>, else sensor:id:<id>
//	interface   iface:<name>
//	service     unit:<canonical unit name>
//	probe       mount:<path>
//
// A configured subject that disappears keeps its override (shown as
// unavailable) and it is never moved to another subject; it applies again
// when the same identity returns.

// Identity strengths.
const (
	IdentityStable   = "stable"   // the device's own identity: survives renames, moves and reboots
	IdentityLocation = "location" // where it is (a PCI slot, a mount path): stable until hardware or mounts change
	IdentityName     = "name"     // a name the system assigns (an interface, a platform sensor)
	IdentityWeak     = "weak"     // order of discovery: may refer to another device after a reboot
)

// Subject is something a subject override can bind to.
type Subject struct {
	Key      string   `json:"key"`
	Kind     string   `json:"kind"`
	Label    string   `json:"label"`
	Identity string   `json:"identity"` // what the key is ("Filesystem UUID", "GPU UUID", …)
	Strength string   `json:"strength"` // stable | location | name | weak
	Present  bool     `json:"present"`  // currently reported by the host
	Checks   []string `json:"checks"`
	// Issue is the subject as health issues name it (a mount, a unit, a
	// sensor id, a GPU id).
	Issue string `json:"issue_subject,omitempty"`
	// Ref is what the host's current report calls it (a mount, a sensor
	// id, a disk key, an interface, a unit, "gpu:<index>"), so a page can
	// find the subject of one of its rows without knowing identities.
	Ref string `json:"ref,omitempty"`
	// Customized: at least one subject override exists for it.
	Customized bool `json:"customized"`
}

var subjectChecks = func() map[string][]string {
	m := map[string][]string{}
	for _, c := range checkSpecs {
		if c.Subject != "" {
			m[c.Subject] = append(m[c.Subject], c.ID)
		}
	}
	return m
}()

// mountUUIDs maps mount points to filesystem UUIDs from the storage
// report, when the filesystem sits directly on a partition (not through
// LVM or LUKS, whose partition UUID isn't the filesystem's) and the UUID
// is used by one mount only (btrfs subvolumes share theirs).
func mountUUIDs(r *storage.Report) map[string]string {
	out := map[string]string{}
	if r == nil {
		return out
	}
	uses := map[string]int{}
	for _, d := range r.Devices {
		for _, p := range d.Partitions {
			if p.UUID == "" || p.Via != "" {
				continue
			}
			for _, m := range p.Mounts {
				out[m] = p.UUID
				uses[p.UUID]++
			}
		}
	}
	for m, u := range out {
		if uses[u] > 1 {
			delete(out, m)
		}
	}
	return out
}

func fsSubject(mount string, uuids map[string]string) Subject {
	if u := uuids[mount]; u != "" {
		return Subject{Key: "fsuuid:" + u, Kind: SubjectFilesystem, Label: mount, Identity: "Filesystem UUID " + u, Strength: IdentityStable, Issue: mount}
	}
	return Subject{Key: "mount:" + mount, Kind: SubjectFilesystem, Label: mount, Identity: "Mount path (no filesystem UUID known)", Strength: IdentityLocation, Issue: mount}
}

func diskStrength(key string) (string, string) {
	switch {
	case strings.HasPrefix(key, "wwn-"):
		return "World Wide Name", IdentityStable
	case strings.HasPrefix(key, "nvme-eui.") || strings.HasPrefix(key, "nvme-nvme."):
		return "NVMe identifier", IdentityStable
	case strings.HasPrefix(key, "serial:"):
		return "Model and serial number", IdentityStable
	case strings.HasPrefix(key, "name:"):
		return "Kernel device name only (may change after a reboot)", IdentityWeak
	}
	return "Device id", IdentityLocation
}

func diskSubject(d storage.Device) Subject {
	id, st := diskStrength(d.Key)
	return Subject{Key: "disk:" + d.Key, Kind: SubjectDisk, Label: devLabel(d), Identity: id, Strength: st, Issue: d.Key}
}

// gpuID is a GPU's subject id as its health issues name it.
func gpuID(g monitor.GPU) string {
	switch {
	case g.UUID != "":
		return g.UUID
	case g.PCI != "":
		return "pci:" + g.PCI
	}
	return "index:" + strconv.Itoa(g.Index)
}

func gpuSubject(g monitor.GPU) Subject {
	s := Subject{Key: "gpu:" + gpuID(g), Kind: SubjectGPU, Label: g.Model, Issue: gpuID(g)}
	switch {
	case strings.HasPrefix(g.UUID, "GPU-"):
		s.Identity, s.Strength = "NVIDIA GPU UUID", IdentityStable
	case g.UUID != "":
		s.Identity, s.Strength = "AMD unique id", IdentityStable
	case g.PCI != "":
		s.Identity, s.Strength = "PCI address "+g.PCI+" (the slot, not the card)", IdentityLocation
	default:
		s.Identity, s.Strength = "Position in the report (an older agent): may refer to another GPU after changes", IdentityWeak
	}
	if g.PCI != "" {
		s.Label += " (" + g.PCI + ")"
	}
	return s
}

// sensorSubject identifies a temperature sensor. Drive sensors use the
// drive's own identity; others the chip and the sysfs device it belongs
// to; only when neither is known the collector's id (hwmon order).
func sensorSubject(sn monitor.Sensor, disks map[string]string) Subject {
	s := Subject{Kind: SubjectSensor, Label: sensorName(sn), Issue: sn.ID}
	if sn.Kind == "storage" && sn.Device != "" {
		if k := disks[sn.Device]; k != "" {
			if id, st := diskStrength(k); st == IdentityStable {
				s.Key, s.Identity, s.Strength = "sensor:disk:"+k+"/"+sn.Label, "Drive "+strings.ToLower(id[:1])+id[1:]+" and sensor label", IdentityStable
				return s
			}
		}
	}
	if sn.HW != "" {
		s.Key = "sensor:" + sn.Chip + "@" + sn.HW + "/" + sn.Label
		if strings.Count(sn.HW, ":") == 2 && strings.Contains(sn.HW, ".") { // a PCI address
			s.Identity, s.Strength = "Chip "+sn.Chip+" at PCI address "+sn.HW, IdentityLocation
		} else {
			s.Identity, s.Strength = "Chip "+sn.Chip+" on device "+sn.HW, IdentityName
		}
		return s
	}
	s.Key = "sensor:id:" + sn.ID
	s.Identity, s.Strength = "Sensor id "+sn.ID+" (discovery order: may change if hardware changes)", IdentityWeak
	if strings.HasPrefix(sn.ID, "thermal/") {
		s.Identity, s.Strength = "Thermal zone "+sn.ID, IdentityName
	}
	return s
}

// gpuTempSubject is a GPU whose temperature comes from nvidia-smi (no
// hwmon sensor, no hardware limit in the report).
func gpuTempIssue(g monitor.GPU) string { return "gpu:" + gpuID(g) }

func diskKeysByName(r *storage.Report) map[string]string {
	out := map[string]string{}
	if r == nil {
		return out
	}
	for _, d := range r.Devices {
		out[d.Name] = d.Key
	}
	return out
}

// hostSubjects lists everything a subject override can bind to on a host,
// from its latest reports.
func hostSubjects(st *monitor.State, r *storage.Report, units []ServiceRow, watched map[string]bool, probes []string, activeUnits map[string]bool) []Subject {
	var out []Subject
	uuids := mountUUIDs(r)
	disks := diskKeysByName(r)
	if st != nil {
		for _, f := range st.Filesystems {
			out = append(out, fsSubject(f.Mount, uuids))
		}
		for _, sn := range st.Sensors {
			out = append(out, sensorSubject(sn, disks))
		}
		for _, g := range st.GPUs {
			gs := gpuSubject(g)
			gs.Ref = "gpu:" + strconv.Itoa(g.Index)
			gs.Checks = []string{CheckGPUUtil}
			if g.Source == "nvidia-smi" {
				gs.Checks = append(gs.Checks, CheckTemperature) // no hwmon sensor: the GPU itself is the sensor
			}
			out = append(out, gs)
		}
		for _, in := range st.Interfaces {
			if in.Kind == "physical" {
				out = append(out, Subject{Key: "iface:" + in.Name, Kind: SubjectInterface, Label: in.Name, Identity: "Interface name", Strength: IdentityName, Issue: in.Name})
			}
		}
	}
	if r != nil {
		for _, d := range r.Devices {
			out = append(out, diskSubject(d))
		}
	}
	for _, u := range units {
		if watched[u.Name] || activeUnits[u.Name] {
			out = append(out, unitSubject(u.Name))
		}
	}
	for u := range watched {
		if !containsSubject(out, "unit:"+u) {
			out = append(out, unitSubject(u))
		}
	}
	for _, m := range probes {
		out = append(out, Subject{Key: "mount:" + m, Kind: SubjectProbe, Label: m + " (probe)", Identity: "Mount path", Strength: IdentityLocation, Issue: m})
	}
	for i := range out {
		out[i].Present = true
		if out[i].Ref == "" {
			out[i].Ref = out[i].Issue
		}
		if out[i].Checks == nil {
			out[i].Checks = subjectChecks[out[i].Kind]
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}

// unitNameRe is a systemd unit name: no spaces or slashes, with its type
// suffix (nginx.service, getty@tty1.service, backup.timer).
var unitNameRe = regexp.MustCompile(`^[A-Za-z0-9:_.@\\-]{1,250}\.[a-z]{2,10}$`)

func unitSubject(u string) Subject {
	return Subject{Key: "unit:" + u, Kind: SubjectService, Label: u, Identity: "systemd unit name", Strength: IdentityStable, Issue: u}
}

func containsSubject(ss []Subject, key string) bool {
	for _, s := range ss {
		if s.Key == key {
			return true
		}
	}
	return false
}

// subjectKind is the kind of subject a key names.
func subjectKind(check, key string) string {
	spec, ok := Spec(check)
	if !ok {
		return ""
	}
	if check == CheckTemperature && strings.HasPrefix(key, "gpu:") {
		return SubjectGPU
	}
	return spec.Subject
}

// validSubjectKey checks a key's shape for a check.
func validSubjectKey(check, key string) bool {
	if len(key) > 300 || strings.ContainsAny(key, "\x00\n\r") {
		return false
	}
	pre := map[string][]string{
		SubjectFilesystem: {"fsuuid:", "mount:/"},
		SubjectDisk:       {"disk:"},
		SubjectGPU:        {"gpu:"},
		SubjectSensor:     {"sensor:", "gpu:"},
		SubjectInterface:  {"iface:"},
		SubjectService:    {"unit:"},
		SubjectProbe:      {"mount:/"},
	}
	spec, ok := Spec(check)
	if !ok || spec.Subject == "" {
		return false
	}
	if spec.Subject == SubjectService {
		return unitNameRe.MatchString(strings.TrimPrefix(key, "unit:")) && strings.HasPrefix(key, "unit:")
	}
	for _, p := range pre[spec.Subject] {
		if strings.HasPrefix(key, p) && len(key) > len(p) {
			return true
		}
	}
	return false
}
