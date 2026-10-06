package pkg

import (
	"fmt"
	"regexp"
	"strings"

	"drool/internal/model"
)

// dnf covers dnf4 (RHEL 9 family) and dnf5 (current Fedora); their command
// lines are compatible for what DROOL uses, their output differs.
type dnf struct{}

func (dnf) Name() string { return "dnf" }

func (dnf) Refresh() string { return "LC_ALL=C dnf -q -y makecache" }

func dnfArgs(action string, names []string) (string, error) {
	switch action {
	case "install":
		return "install " + shNames(names), nil
	case "remove":
		return "remove " + shNames(names), nil
	case "upgrade":
		return "upgrade " + shNames(names), nil
	case "system-upgrade", "system-full-upgrade":
		return "upgrade", nil
	case "system-security":
		return "upgrade --security", nil
	}
	return "", fmt.Errorf("unknown package action %q", action)
}

// Simulate uses --assumeno: dnf resolves and prints the transaction, then
// aborts. dnf4 requires root even for this.
func (dnf) Simulate(action string, names []string) (string, error) {
	a, err := dnfArgs(action, names)
	if err != nil {
		return "", err
	}
	return "LC_ALL=C dnf -C --assumeno " + a, nil
}

func (dnf) SimulateNeedsRoot() bool { return true }

func (dnf) Exec(action string, names []string) (string, error) {
	a, err := dnfArgs(action, names)
	if err != nil {
		return "", err
	}
	return "LC_ALL=C dnf -y " + a, nil
}

// ParsePlan reads the transaction table printed by --assumeno. Both dnf4
// and dnf5 exit non-zero after "Operation aborted"; a table or "Nothing to
// do" means the simulation itself succeeded.
func (b dnf) ParsePlan(out string, exit int) (Plan, error) {
	p := newPlan()
	if strings.Contains(out, "Nothing to do") {
		return p, nil
	}
	if !strings.Contains(out, "Operation aborted") && exit != 0 {
		return Plan{}, b.Classify(out, exit)
	}
	section := ""
	parent := ""
	carry := ""
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimRight(raw, " ")
		trim := strings.TrimSpace(line)
		switch {
		case trim == "":
			continue
		case strings.HasPrefix(trim, "Transaction Summary"):
			section = ""
			continue
		case strings.HasSuffix(trim, ":") && !strings.HasPrefix(line, " "):
			head := strings.ToLower(trim)
			switch {
			case strings.HasPrefix(head, "installing"):
				section = "install"
			case strings.HasPrefix(head, "upgrading"):
				section = "upgrade"
			case strings.HasPrefix(head, "removing"):
				section = "remove"
			case strings.HasPrefix(head, "downgrading"):
				section = "downgrade"
			default:
				section = "" // reinstalling, obsoleting headers etc. carry no extra info we act on
			}
			continue
		}
		if section == "" || !strings.HasPrefix(line, " ") {
			continue
		}
		f := strings.Fields(trim)
		if carry != "" {
			// dnf4 wraps long package names onto their own line.
			f = append([]string{carry}, f...)
			carry = ""
		}
		if len(f) == 1 {
			carry = f[0]
			continue
		}
		if f[0] == "replacing" {
			// Obsoleted package: a removal only if it has a different name.
			// dnf4: "replacing name.arch version"; dnf5: "replacing name arch version repo size".
			var name, ver string
			switch {
			case len(f) == 3:
				name, ver = f[1], f[2]
				if i := strings.LastIndexByte(name, '.'); i > 0 {
					name = name[:i]
				}
			case len(f) >= 4:
				name, ver = f[1], f[3]
			}
			if name != "" && name != parent {
				p.Remove = append(p.Remove, Change{Name: name, From: ver})
			}
			continue
		}
		if len(f) < 4 {
			continue
		}
		c := Change{Name: f[0], To: f[2]}
		parent = f[0]
		switch section {
		case "install":
			p.Install = append(p.Install, c)
		case "upgrade":
			p.Upgrade = append(p.Upgrade, c)
		case "remove":
			p.Remove = append(p.Remove, Change{Name: f[0], From: f[2]})
		case "downgrade":
			p.Downgrade = append(p.Downgrade, c)
		}
	}
	return p, nil
}

func (dnf) Versions(names []string) string {
	return `rpm -q --qf '%{NAME}\t%{EPOCHNUM}:%{VERSION}-%{RELEASE}\n' ` + shNames(names) + ` 2>/dev/null; true`
}

func (dnf) AllVersions() string {
	return `rpm -qa --qf '%{NAME}\t%{EPOCHNUM}:%{VERSION}-%{RELEASE}\n' 2>/dev/null; true`
}

func (dnf) ParseVersions(out string, names []string) map[string]string {
	m := map[string]string{}
	for _, n := range names {
		m[n] = ""
	}
	for _, l := range strings.Split(out, "\n") {
		name, ver, ok := strings.Cut(l, "\t")
		if !ok {
			continue // "package X is not installed"
		}
		m[name] = strings.TrimPrefix(ver, "0:")
	}
	return m
}

func (dnf) RebootRequired() string {
	return `if command -v needs-restarting >/dev/null 2>&1; then needs-restarting -r >/dev/null 2>&1; echo "rc=$?"; ` +
		`elif dnf needs-restarting --help >/dev/null 2>&1; then dnf needs-restarting -r >/dev/null 2>&1; echo "rc=$?"; else echo unknown; fi`
}

func (dnf) ParseRebootRequired(out string) (*bool, string) {
	switch strings.TrimSpace(out) {
	case "rc=0":
		b := false
		return &b, ""
	case "rc=1":
		b := true
		return &b, "needs-restarting reports a reboot is required"
	}
	return nil, ""
}

var dnfNoMatchRE = regexp.MustCompile(`No match for argument: (\S+)`)

func (dnf) Classify(out string, exit int) *model.OpError {
	switch {
	case dnfNoMatchRE.MatchString(out):
		var names []string
		for _, m := range dnfNoMatchRE.FindAllStringSubmatch(out, -1) {
			names = append(names, m[1])
		}
		return &model.OpError{Class: model.ClassValidation, Message: "package not found: " + strings.Join(names, ", "),
			Hint: "Check the name and that the repository providing it is enabled."}
	case strings.Contains(out, "transaction lock"), strings.Contains(out, "Waiting for process with pid"),
		strings.Contains(out, "is locked"):
		return &model.OpError{Class: model.ClassPackageManager, Retryable: true,
			Message: "the package manager is locked by another process", Hint: "DROOL retries automatically."}
	case strings.Contains(out, "Failed to resolve the transaction"), strings.Contains(out, "Problem:"):
		return &model.OpError{Class: model.ClassPackageManager,
			Message: "dnf cannot resolve this change: " + firstLines(afterLine(out, "Problem"), 3),
			Hint:    "Usually a protected package (e.g. dnf itself) or a conflicting dependency."}
	case strings.Contains(out, "Cache-only enabled but no cache"), strings.Contains(out, "Cache-only enabled"):
		return &model.OpError{Class: model.ClassPackageManager, Retryable: true,
			Message: "no package metadata cache yet", Hint: "Refresh package metadata first (enabled by default for updates)."}
	case strings.Contains(out, "Curl error"), strings.Contains(out, "Cannot download"):
		return &model.OpError{Class: model.ClassPackageManager, Retryable: true, Message: "could not download packages: " + lastError(out)}
	}
	return &model.OpError{Class: model.ClassPackageManager, Message: fmt.Sprintf("dnf failed (exit %d): %s", exit, lastError(out))}
}

// afterLine returns out from the first line containing marker (or all of out).
func afterLine(out, marker string) string {
	if i := strings.Index(out, marker); i >= 0 {
		return out[i:]
	}
	return out
}
