package discovery

import (
	"strconv"
	"strings"
	"unicode"
)

// Status is the result of a status check. Pointer fields are nil when the
// value could not be determined (shown as "unknown", never as zero).
type Status struct {
	BootID          string
	UptimeS         *int64
	Load1           *float64
	MemUsedPct      *float64
	MaxFSUsedPct    *float64
	MaxFSMount      string
	FailedUnits     *int // nil when systemd isn't running
	FailedUnitNames []string
	RebootRequired  *bool
	RebootReason    string
}

// ParseStatus parses StatusScript output.
func ParseStatus(out string) Status {
	sec := sections(out)
	var st Status
	if b := sec["boot-id"]; len(b) > 0 {
		st.BootID = strings.TrimSpace(b[0])
	}
	if u := sec["uptime"]; len(u) > 0 {
		if f := strings.Fields(u[0]); len(f) > 0 {
			if v, err := strconv.ParseFloat(f[0], 64); err == nil {
				n := int64(v)
				st.UptimeS = &n
			}
		}
	}
	if l := sec["loadavg"]; len(l) > 0 {
		if f := strings.Fields(l[0]); len(f) > 0 {
			if v, err := strconv.ParseFloat(f[0], 64); err == nil {
				st.Load1 = &v
			}
		}
	}
	mem := parseMeminfo(sec["meminfo"])
	if total, avail := mem["MemTotal"], mem["MemAvailable"]; total > 0 && avail > 0 {
		p := float64(total-avail) * 100 / float64(total)
		st.MemUsedPct = &p
	}
	for _, fs := range parseDF(sec["df"]) {
		if st.MaxFSUsedPct == nil || fs.UsedPct > *st.MaxFSUsedPct {
			p := fs.UsedPct
			st.MaxFSUsedPct, st.MaxFSMount = &p, fs.Mount
		}
	}
	if f := sec["failed"]; len(f) > 0 && f[0] == "systemd-running" {
		names := parseUnitNames(f[1:])
		n := len(names)
		st.FailedUnits = &n
		if len(names) > 20 {
			names = names[:20]
		}
		st.FailedUnitNames = names
	}
	// Debian/Ubuntu: authoritative reboot-required file.
	if r := sec["reboot-file"]; len(r) > 0 && r[0] == "required" {
		yes := true
		st.RebootRequired = &yes
		st.RebootReason = "reboot-required"
		if len(r) > 1 {
			st.RebootReason += ": " + strings.Join(r[1:], ", ")
		}
		return st
	}
	// Elsewhere: a newer installed kernel than the running one.
	if k := sec["kernels"]; len(k) > 1 {
		running := k[0]
		newest := ""
		for _, v := range k[1:] {
			if newest == "" || CompareVersions(v, newest) > 0 {
				newest = v
			}
		}
		if newest != "" && CompareVersions(newest, running) > 0 {
			yes := true
			st.RebootRequired, st.RebootReason = &yes, "kernel "+newest+" installed, "+running+" running"
		} else if _, ok := sec["reboot-file"]; ok {
			no := false
			st.RebootRequired = &no
		}
	}
	return st
}

// CompareVersions compares version strings segment by segment, numbers
// numerically and text lexically (good enough for kernel release strings).
func CompareVersions(a, b string) int {
	split := func(s string) []string {
		var out []string
		cur := []rune{}
		digit := false
		for _, r := range s {
			isD := unicode.IsDigit(r)
			if !unicode.IsDigit(r) && !unicode.IsLetter(r) {
				if len(cur) > 0 {
					out = append(out, string(cur))
					cur = cur[:0]
				}
				continue
			}
			if len(cur) > 0 && isD != digit {
				out = append(out, string(cur))
				cur = cur[:0]
			}
			cur = append(cur, r)
			digit = isD
		}
		if len(cur) > 0 {
			out = append(out, string(cur))
		}
		return out
	}
	as, bs := split(a), split(b)
	for i := 0; i < len(as) && i < len(bs); i++ {
		an, aerr := strconv.Atoi(as[i])
		bn, berr := strconv.Atoi(bs[i])
		switch {
		case aerr == nil && berr == nil:
			if an != bn {
				if an < bn {
					return -1
				}
				return 1
			}
		case aerr == nil:
			return 1 // numbers sort after text (1.0 > 1.rc)
		case berr == nil:
			return -1
		default:
			if c := strings.Compare(as[i], bs[i]); c != 0 {
				return c
			}
		}
	}
	switch {
	case len(as) < len(bs):
		return -1
	case len(as) > len(bs):
		return 1
	}
	return 0
}

// Updates is the result of an update check.
type Updates struct {
	Available      *int  // nil = unknown (e.g. no package cache yet)
	Security       *int  // nil = not detectable
	RebootRequired *bool // nil = not determined by this check
	RebootReason   string
	Packages       []PendingUpdate
}

// PendingUpdate is one upgradable package.
type PendingUpdate struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Security bool   `json:"security"`
}

func intPtr(n int) *int { return &n }

// ParseUpdatesAPT parses UpdatesAPTScript output. Packages whose candidate
// comes from a *-security suite count as security updates.
func ParseUpdatesAPT(out string) Updates {
	sec := sections(out)
	lines, code, ok := rc(sec["apt"])
	var u Updates
	if !ok || code != 0 {
		return u
	}
	u.Packages = []PendingUpdate{}
	nsec := 0
	for _, l := range lines {
		// "openssl/bookworm-security 3.0.15-1~deb12u1 amd64 [upgradable from: 3.0.14-1~deb12u2]"
		f := strings.Fields(l)
		if len(f) < 2 || !strings.Contains(f[0], "/") || strings.HasPrefix(l, "Listing") {
			continue
		}
		name, suites, _ := strings.Cut(f[0], "/")
		p := PendingUpdate{Name: name, Version: f[1]}
		for _, s := range strings.Split(suites, ",") {
			if strings.HasSuffix(s, "-security") || strings.Contains(s, "/updates") {
				p.Security = true
			}
		}
		if p.Security {
			nsec++
		}
		u.Packages = append(u.Packages, p)
	}
	u.Available, u.Security = intPtr(len(u.Packages)), intPtr(nsec)
	return u
}

// ParseUpdatesDNF parses UpdatesDNFScript output.
func ParseUpdatesDNF(out string) Updates {
	sec := sections(out)
	var u Updates
	lines, code, ok := rc(sec["dnf-check"])
	if ok && (code == 0 || code == 100) {
		u.Packages = []PendingUpdate{}
		for _, l := range lines {
			f := strings.Fields(l)
			// "openssl.x86_64  1:3.0.7-28.el9_4  baseos"; skip section headers.
			if len(f) != 3 || !strings.Contains(f[0], ".") || strings.HasSuffix(l, ":") {
				continue
			}
			name := f[0]
			if i := strings.LastIndexByte(name, '.'); i > 0 {
				name = name[:i]
			}
			u.Packages = append(u.Packages, PendingUpdate{Name: name, Version: f[1]})
		}
		u.Available = intPtr(len(u.Packages))
	}
	secPkgs := map[string]bool{}
	secKnown := false
	if l, c, ok := rc(sec["dnf-security"]); ok && c == 0 {
		secKnown = true
		for _, line := range l {
			// dnf4: "RHSA-2024:1234 Important/Sec. openssl-1:3.0.7-28.el9_4.x86_64"
			if f := strings.Fields(line); len(f) == 3 {
				secPkgs[f[2]] = true
			}
		}
	} else if l, c, ok := rc(sec["dnf5-security"]); ok && c == 0 {
		secKnown = true
		for _, line := range l {
			// dnf5: "FEDORA-2024-abc security Moderate openssl-3.2.2-3.fc40.x86_64 2024-08-01 ..."
			f := strings.Fields(line)
			if len(f) >= 4 && f[0] != "Name" && strings.EqualFold(f[1], "security") {
				secPkgs[f[3]] = true
			}
		}
	}
	if secKnown {
		// Advisories list package NEVRAs, often several versions of one
		// package; count pending packages that have a security fix.
		names := map[string]bool{}
		for nevra := range secPkgs {
			names[nevraName(nevra)] = true
		}
		n := 0
		for i := range u.Packages {
			if names[u.Packages[i].Name] {
				u.Packages[i].Security = true
				n++
			}
		}
		if u.Packages == nil {
			n = len(names) // pending list unknown: best available count
		}
		u.Security = intPtr(n)
	}
	if _, c, ok := rc(sec["needs-restarting"]); ok {
		switch c {
		case 0:
			no := false
			u.RebootRequired = &no
		case 1:
			yes := true
			u.RebootRequired, u.RebootReason = &yes, "needs-restarting reports a reboot is required"
		}
	}
	return u
}

// nevraName extracts the package name from "name-[epoch:]version-release.arch".
func nevraName(nevra string) string {
	if i := strings.LastIndexByte(nevra, '.'); i > 0 {
		nevra = nevra[:i] // drop arch
	}
	for k := 0; k < 2; k++ { // drop release, then version
		if i := strings.LastIndexByte(nevra, '-'); i > 0 {
			nevra = nevra[:i]
		}
	}
	return nevra
}
