package discovery

import (
	"bufio"
	"errors"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Facts is everything discovery learns about a host. Fields that could not
// be determined stay empty and a warning explains why.
type Facts struct {
	OSFamily      string `json:"os_family"`
	Distro        string `json:"distro"`
	DistroVersion string `json:"distro_version"`
	DistroName    string `json:"distro_name"`
	DistroLike    string `json:"distro_like,omitempty"`
	Kernel        string `json:"kernel"`
	Arch          string `json:"arch"`
	Hostname      string `json:"hostname"`
	BootID        string `json:"boot_id"`

	CPUCount  int    `json:"cpu_count,omitempty"`
	CPUModel  string `json:"cpu_model,omitempty"`
	MemBytes  int64  `json:"mem_bytes,omitempty"`
	SwapBytes int64  `json:"swap_bytes,omitempty"`

	Filesystems []Filesystem `json:"filesystems"`
	Interfaces  []Interface  `json:"interfaces"`

	PkgManager     string   `json:"pkg_manager"`
	PkgManagers    []string `json:"pkg_managers,omitempty"`
	InitSystem     string   `json:"init_system"` // PID 1's name
	SystemdRunning bool     `json:"systemd_running"`
	// SystemdState is the host's evidence (SystemdProbe): running, none
	// (PID 1 is something else) or unknown (hidden from the account).
	SystemdState string `json:"systemd_state,omitempty"`
	// ServicesError says why the service list couldn't be read although
	// systemd runs; ServicesLimited: because the account can't ask systemd
	// (no D-Bus system bus, or denied), not because it failed.
	ServicesError   string `json:"services_error,omitempty"`
	ServicesLimited bool   `json:"services_limited,omitempty"`

	Services    []Service `json:"services"`
	FailedUnits []string  `json:"failed_units"`
	Ports       []Port    `json:"ports"`

	ContainerRuntimes []string `json:"container_runtimes"`
	Virtualization    string   `json:"virtualization,omitempty"`

	Warnings []string `json:"warnings"`
}

type Filesystem struct {
	Mount      string  `json:"mount"`
	Device     string  `json:"device"`
	Type       string  `json:"type,omitempty"`
	SizeBytes  int64   `json:"size_bytes"`
	UsedBytes  int64   `json:"used_bytes"`
	AvailBytes int64   `json:"avail_bytes"`
	UsedPct    float64 `json:"used_pct"`
}

type Interface struct {
	Name      string   `json:"name"`
	MAC       string   `json:"mac,omitempty"`
	State     string   `json:"state,omitempty"`
	MTU       int      `json:"mtu,omitempty"`
	Addresses []IfAddr `json:"addresses"`
}

type IfAddr struct {
	Address string `json:"address"`
	Prefix  int    `json:"prefix"`
	Family  int    `json:"family"` // 4 or 6
	Scope   string `json:"scope,omitempty"`
}

type Service struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type Port struct {
	Proto   string `json:"proto"` // tcp | udp
	Address string `json:"address"`
	Port    int    `json:"port"`
	Process string `json:"process,omitempty"`
}

var sectionRE = regexp.MustCompile(`^@@DROOL:([a-z0-9-]+)@@$`)

// sections splits script output into named sections. Lines before the first
// marker are ignored (login banners, motd noise).
func sections(out string) map[string][]string {
	m := map[string][]string{}
	cur := ""
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if g := sectionRE.FindStringSubmatch(line); g != nil {
			cur = g[1]
			if _, ok := m[cur]; !ok {
				m[cur] = []string{}
			}
			continue
		}
		if cur != "" && line != "" {
			m[cur] = append(m[cur], line)
		}
	}
	return m
}

// rc extracts "@@rc=N" from a section, returning the lines without it.
func rc(lines []string) ([]string, int, bool) {
	out := lines[:0:0]
	code, found := 0, false
	for _, l := range lines {
		if v, ok := strings.CutPrefix(l, "@@rc="); ok {
			code, _ = strconv.Atoi(strings.TrimSpace(v))
			found = true
			continue
		}
		out = append(out, l)
	}
	return out, code, found
}

// ParseDiscover parses DiscoverScript output. complete reports whether the
// end marker was seen (truncated output still yields partial facts).
func ParseDiscover(out string) (f Facts, complete bool) {
	sec := sections(out)
	_, complete = sec["end"]
	warn := func(msg string) { f.Warnings = append(f.Warnings, msg) }
	f.Filesystems, f.Interfaces, f.Services, f.FailedUnits, f.Ports, f.ContainerRuntimes, f.Warnings =
		[]Filesystem{}, []Interface{}, []Service{}, []string{}, []Port{}, []string{}, []string{}

	parseOSRelease(sec["os-release"], &f)
	if f.Distro == "" {
		warn("could not read /etc/os-release; distribution unknown")
	}
	if u := sec["uname"]; len(u) >= 3 {
		f.Kernel, f.Arch = u[1], u[2]
	} else {
		warn("uname output incomplete; kernel/architecture unknown")
	}
	if h := sec["hostname"]; len(h) > 0 {
		f.Hostname = strings.TrimSpace(h[0])
	}
	if b := sec["boot-id"]; len(b) > 0 {
		f.BootID = strings.TrimSpace(b[0])
	}
	for _, l := range sec["cpu"] {
		if n, err := strconv.Atoi(strings.TrimSpace(l)); err == nil && f.CPUCount == 0 {
			f.CPUCount = n
		} else if _, model, ok := strings.Cut(l, ":"); ok && strings.HasPrefix(l, "model name") {
			f.CPUModel = strings.TrimSpace(model)
		}
	}
	mem := parseMeminfo(sec["meminfo"])
	f.MemBytes, f.SwapBytes = mem["MemTotal"], mem["SwapTotal"]
	if f.MemBytes == 0 {
		warn("could not read memory size")
	}
	f.Filesystems = parseDF(sec["df"])
	if len(f.Filesystems) == 0 {
		warn("could not read filesystems (df)")
	}
	f.Interfaces = parseInterfaces(sec["ip-addr"], sec["ip-link"])
	if len(f.Interfaces) == 0 {
		warn("could not read network interfaces (is iproute2 installed?)")
	}
	f.PkgManagers = append([]string{}, sec["pkg"]...)
	f.PkgManager = choosePkgManager(f.PkgManagers, f.OSFamily)
	for idx, l := range sec["init"] {
		switch {
		case l == "systemd-running": // a Shepherd from before SystemdProbe
			f.SystemdState = SystemdRunning
		case strings.HasPrefix(l, "systemd="):
			f.SystemdState = strings.TrimSpace(strings.TrimPrefix(l, "systemd="))
		case idx == 0:
			f.InitSystem = strings.TrimSpace(l)
		}
	}
	if f.SystemdState == "" {
		f.SystemdState = SystemdNone // the old script printed nothing then
	}
	if f.SystemdState != SystemdNone {
		units, svcErr := sdLines(sec["services"])
		failed, failErr := sdLines(sec["failed"])
		if svcErr == nil {
			f.Services = parseServices(units)
		}
		if failErr == nil {
			f.FailedUnits = parseUnitNames(failed)
		}
		// Unknown evidence, but systemctl answered: it runs.
		f.SystemdRunning = f.SystemdState == SystemdRunning || svcErr == nil && failErr == nil
		if e := errors.Join(svcErr, failErr); e != nil {
			var le *listError
			errors.As(e, &le)
			f.ServicesError, f.ServicesLimited = le.msg, le.limited
		}
	}
	switch {
	case f.SystemdRunning:
		f.InitSystem = "systemd"
		if f.ServicesLimited {
			warn("systemd is running, but this account can't ask it which services there are (" + f.ServicesError + "): on a host without a D-Bus system bus only root can")
		} else if f.ServicesError != "" {
			warn("systemd is running, but listing its services failed: " + f.ServicesError)
		}
	case f.SystemdState == SystemdUnknown:
		warn("whether systemd runs couldn't be read: PID 1 and /run/systemd are hidden from this account" + map[bool]string{true: " (" + f.ServicesError + ")", false: ""}[f.ServicesError != ""])
	case f.InitSystem != "":
		warn("PID 1 is " + f.InitSystem + ", not systemd: services and failed units are unavailable")
	default:
		warn("systemd is not running; services and failed units are unavailable")
	}
	if p, ok := sec["ports"]; ok {
		f.Ports = parsePorts(p)
		if len(p) == 0 {
			warn("no listening sockets reported (ss missing or no permission)")
		}
	}
	f.ContainerRuntimes = append([]string{}, sec["containers"]...)
	if v := sec["virt"]; len(v) > 0 {
		f.Virtualization = strings.TrimSpace(v[0])
	}
	if !complete {
		warn("discovery output was incomplete")
	}
	return f, complete
}

// Systemd states (SystemdProbe).
const (
	SystemdRunning = "running"
	SystemdNone    = "none"
	SystemdUnknown = "unknown"
)

// listError is a systemctl list that couldn't be read (sdlist).
type listError struct {
	msg     string
	limited bool // the account can't reach systemd, rather than a failure
}

func (e *listError) Error() string { return e.msg }

// sdLines separates sdlist's output from its error line.
func sdLines(lines []string) ([]string, error) {
	out := lines[:0:0]
	for _, l := range lines {
		if rest, ok := strings.CutPrefix(l, "@@sd-error "); ok {
			kind, msg, _ := strings.Cut(rest, " ")
			return nil, &listError{msg: strings.TrimSpace(msg), limited: kind == "permission"}
		}
		out = append(out, l)
	}
	return out, nil
}

func unquote(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		v = v[1 : len(v)-1]
	}
	return strings.ReplaceAll(v, `\"`, `"`)
}

func parseOSRelease(lines []string, f *Facts) {
	kv := map[string]string{}
	for _, l := range lines {
		if k, v, ok := strings.Cut(l, "="); ok && !strings.HasPrefix(strings.TrimSpace(l), "#") {
			kv[strings.TrimSpace(k)] = unquote(v)
		}
	}
	f.Distro = strings.ToLower(kv["ID"])
	f.DistroVersion = kv["VERSION_ID"]
	f.DistroName = kv["PRETTY_NAME"]
	f.DistroLike = strings.ToLower(kv["ID_LIKE"])
	f.OSFamily = OSFamily(f.Distro, f.DistroLike)
}

// OSFamily maps os-release ID/ID_LIKE to a family.
func OSFamily(id, like string) string {
	words := append([]string{id}, strings.Fields(like)...)
	for _, w := range words {
		switch w {
		case "debian", "ubuntu", "raspbian", "linuxmint", "pop", "elementary", "kali", "devuan":
			return "debian"
		case "rhel", "fedora", "centos", "rocky", "almalinux", "ol", "amzn", "scientific", "nobara":
			return "redhat"
		case "suse", "opensuse", "sles", "opensuse-leap", "opensuse-tumbleweed":
			return "suse"
		case "arch", "manjaro", "endeavouros":
			return "arch"
		}
	}
	return "unknown"
}

func choosePkgManager(found []string, family string) string {
	has := map[string]bool{}
	for _, p := range found {
		has[p] = true
	}
	prefer := map[string][]string{
		"debian": {"apt-get"}, "redhat": {"dnf", "yum"}, "suse": {"zypper"}, "arch": {"pacman"},
	}[family]
	for _, p := range append(prefer, "apt-get", "dnf", "yum", "zypper", "pacman") {
		if has[p] {
			if p == "apt-get" {
				return "apt"
			}
			return p
		}
	}
	return ""
}

func parseMeminfo(lines []string) map[string]int64 {
	m := map[string]int64{}
	for _, l := range lines {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(v)
		if len(fields) == 0 {
			continue
		}
		n, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		if len(fields) > 1 && strings.EqualFold(fields[1], "kB") {
			n *= 1024
		}
		m[k] = n
	}
	return m
}

// parseDF reads `df -PTk` (7 columns) or `df -Pk` (6 columns) output.
func parseDF(lines []string) []Filesystem {
	out := []Filesystem{}
	if len(lines) == 0 {
		return out
	}
	withType := strings.Contains(lines[0], "Type")
	seen := map[string]bool{}
	for _, l := range lines[1:] {
		f := strings.Fields(l)
		cols := 6
		if withType {
			cols = 7
		}
		if len(f) < cols {
			continue
		}
		fs := Filesystem{Device: f[0]}
		i := 1
		if withType {
			fs.Type = f[1]
			i = 2
		}
		size, e1 := strconv.ParseInt(f[i], 10, 64)
		used, e2 := strconv.ParseInt(f[i+1], 10, 64)
		avail, e3 := strconv.ParseInt(f[i+2], 10, 64)
		if e1 != nil || e2 != nil || e3 != nil || size == 0 {
			continue
		}
		fs.SizeBytes, fs.UsedBytes, fs.AvailBytes = size*1024, used*1024, avail*1024
		// Mount points may contain spaces: everything after the capacity column.
		fs.Mount = strings.Join(f[i+4:], " ")
		if seen[fs.Mount] {
			continue
		}
		seen[fs.Mount] = true
		if used+avail > 0 {
			fs.UsedPct = float64(used) * 100 / float64(used+avail)
		}
		out = append(out, fs)
	}
	return out
}

// parseInterfaces combines `ip -o addr show` and `ip -o link show`.
func parseInterfaces(addrLines, linkLines []string) []Interface {
	byName := map[string]*Interface{}
	var order []string
	get := func(name string) *Interface {
		if i := byName[name]; i != nil {
			return i
		}
		i := &Interface{Name: name, Addresses: []IfAddr{}}
		byName[name] = i
		order = append(order, name)
		return i
	}
	for _, l := range linkLines {
		// "2: eth0@if5: <...> mtu 1500 qdisc ... state UP ... link/ether 02:42:..."
		f := strings.Fields(strings.ReplaceAll(l, `\`, " "))
		if len(f) < 2 {
			continue
		}
		name := strings.TrimSuffix(f[1], ":")
		name, _, _ = strings.Cut(name, "@")
		iface := get(name)
		for j := 2; j+1 < len(f); j++ {
			switch f[j] {
			case "mtu":
				iface.MTU, _ = strconv.Atoi(f[j+1])
			case "state":
				iface.State = f[j+1]
			case "link/ether":
				iface.MAC = f[j+1]
			}
		}
	}
	for _, l := range addrLines {
		// "2: eth0    inet 172.17.0.2/16 brd ... scope global eth0\ valid_lft ..."
		f := strings.Fields(strings.ReplaceAll(l, `\`, " "))
		if len(f) < 4 || (f[2] != "inet" && f[2] != "inet6") {
			continue
		}
		name, _, _ := strings.Cut(f[1], "@")
		addr, prefix, ok := strings.Cut(f[3], "/")
		ip := net.ParseIP(addr)
		if !ok || ip == nil {
			continue
		}
		p, _ := strconv.Atoi(prefix)
		a := IfAddr{Address: ip.String(), Prefix: p, Family: 4}
		if f[2] == "inet6" {
			a.Family = 6
		}
		for j := 4; j+1 < len(f); j++ {
			if f[j] == "scope" {
				a.Scope = f[j+1]
			}
		}
		iface := get(name)
		iface.Addresses = append(iface.Addresses, a)
	}
	out := make([]Interface, 0, len(order))
	for _, n := range order {
		out = append(out, *byName[n])
	}
	return out
}

func parseServices(lines []string) []Service {
	out := []Service{}
	for _, l := range lines {
		f := strings.Fields(strings.TrimPrefix(strings.TrimSpace(l), "●"))
		if len(f) < 4 || !strings.HasSuffix(f[0], ".service") {
			continue
		}
		s := Service{Name: strings.TrimSuffix(f[0], ".service")}
		if len(f) > 4 {
			s.Description = strings.Join(f[4:], " ")
		}
		out = append(out, s)
	}
	return out
}

func parseUnitNames(lines []string) []string {
	out := []string{}
	for _, l := range lines {
		f := strings.Fields(strings.TrimPrefix(strings.TrimSpace(l), "●"))
		if len(f) > 0 && strings.Contains(f[0], ".") {
			out = append(out, f[0])
		}
	}
	return out
}

var processRE = regexp.MustCompile(`\(\("([^"]+)"`)

// parsePorts reads `ss -Htlnup` output.
func parsePorts(lines []string) []Port {
	out := []Port{}
	seen := map[string]bool{}
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) < 5 {
			continue
		}
		proto := f[0]
		if proto != "tcp" && proto != "udp" {
			continue
		}
		local := f[4]
		i := strings.LastIndexByte(local, ':')
		if i < 0 {
			continue
		}
		port, err := strconv.Atoi(local[i+1:])
		if err != nil {
			continue
		}
		addr := strings.Trim(local[:i], "[]")
		addr, _, _ = strings.Cut(addr, "%")
		p := Port{Proto: proto, Address: addr, Port: port}
		if m := processRE.FindStringSubmatch(l); m != nil {
			p.Process = m[1]
		}
		key := proto + "|" + addr + "|" + strconv.Itoa(port)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, p)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Port != out[b].Port {
			return out[a].Port < out[b].Port
		}
		return out[a].Proto+out[a].Address < out[b].Proto+out[b].Address
	})
	return out
}
