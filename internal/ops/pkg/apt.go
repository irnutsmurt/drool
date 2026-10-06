package pkg

import (
	"fmt"
	"regexp"
	"strings"

	"drool/internal/discovery"
	"drool/internal/model"
)

type apt struct{}

func (apt) Name() string { return "apt" }

// aptEnv makes APT fully non-interactive: no debconf prompts, keep existing
// config files on conflicts, and wait up to two minutes for the dpkg lock
// instead of failing immediately. needrestart (Ubuntu) is suspended, so no
// services are restarted behind the user's back; MODE=a only matters if a
// version ignores the suspend, and then avoids a prompt.
const aptEnv = "DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a NEEDRESTART_SUSPEND=1 LC_ALL=C "
const aptOpts = "-o DPkg::Lock::Timeout=120 -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold"

func (apt) Refresh() string { return aptEnv + "apt-get -q -o DPkg::Lock::Timeout=120 update" }

func aptArgs(action string, names []string) (string, error) {
	switch action {
	case "install":
		return "install " + shNames(names), nil
	case "remove":
		return "remove " + shNames(names), nil
	case "upgrade":
		return "install --only-upgrade " + shNames(names), nil
	case "system-upgrade":
		return "upgrade", nil
	case "system-full-upgrade":
		return "full-upgrade", nil
	case "system-security":
		return "", &model.OpError{Class: model.ClassValidation,
			Message: "security-only updates are not supported on Debian/Ubuntu in this version",
			Hint:    "Use a normal system update; security updates are shown on the host page."}
	}
	return "", fmt.Errorf("unknown package action %q", action)
}

func (apt) Simulate(action string, names []string) (string, error) {
	a, err := aptArgs(action, names)
	if err != nil {
		return "", err
	}
	return aptEnv + "apt-get -s " + a, nil
}

func (apt) SimulateNeedsRoot() bool { return false }

func (apt) Exec(action string, names []string) (string, error) {
	a, err := aptArgs(action, names)
	if err != nil {
		return "", err
	}
	return aptEnv + "apt-get -y " + aptOpts + " " + a, nil
}

var (
	// "Inst perl-base [5.36.0-7+deb12u3] (5.36.0-7+deb12u4 Debian-Security:12/... [amd64])"
	aptInstRE = regexp.MustCompile(`^Inst (\S+)(?: \[([^\]]+)\])? \((\S+)`)
	// "Remv curl [7.88.1-10+deb12u15]" (also "Purg")
	aptRemvRE = regexp.MustCompile(`^(?:Remv|Purg) (\S+)(?: \[([^\]]+)\])?`)
)

// ParsePlan reads `apt-get -s` output.
func (b apt) ParsePlan(out string, exit int) (Plan, error) {
	if exit != 0 {
		return Plan{}, b.Classify(out, exit)
	}
	p := newPlan()
	for _, l := range strings.Split(out, "\n") {
		if m := aptInstRE.FindStringSubmatch(l); m != nil {
			c := Change{Name: m[1], From: m[2], To: m[3]}
			if c.From == "" {
				p.Install = append(p.Install, c)
			} else {
				p.Upgrade = append(p.Upgrade, c)
			}
		} else if m := aptRemvRE.FindStringSubmatch(l); m != nil {
			p.Remove = append(p.Remove, Change{Name: m[1], From: m[2]})
		}
	}
	return p, nil
}

func (apt) Versions(names []string) string {
	return `dpkg-query -W -f='${Package}\t${db:Status-Abbrev}\t${Version}\n' ` + shNames(names) + ` 2>/dev/null; true`
}

func (apt) AllVersions() string {
	return `dpkg-query -W -f='${Package}\t${db:Status-Abbrev}\t${Version}\n' 2>/dev/null; true`
}

// ParseVersions reads dpkg-query output; only "ii" (installed) counts.
func (apt) ParseVersions(out string, names []string) map[string]string {
	m := map[string]string{}
	for _, n := range names {
		m[n] = ""
	}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Split(l, "\t")
		if len(f) != 3 {
			continue
		}
		name, _, _ := strings.Cut(f[0], ":") // strip :arch
		if strings.HasPrefix(strings.TrimSpace(f[1]), "ii") {
			m[name] = f[2]
		}
	}
	return m
}

// RebootRequired checks Ubuntu's reboot-required file, and on systems that
// don't maintain it (plain Debian) compares the newest installed kernel with
// the running one.
func (apt) RebootRequired() string {
	return `if [ -f /var/run/reboot-required ]; then echo required; head -n 5 /var/run/reboot-required.pkgs 2>/dev/null; else echo not-required; fi; ` +
		`echo @@kernels; ` + discovery.InstalledKernelsCommand
}

func (apt) ParseRebootRequired(out string) (*bool, string) {
	head, kernels, _ := strings.Cut(out, "@@kernels")
	lines := strings.Fields(strings.TrimSpace(head))
	if len(lines) == 0 {
		return nil, ""
	}
	b := lines[0] == "required"
	reason := ""
	if b {
		reason = "reboot-required"
		if len(lines) > 1 {
			reason += ": " + strings.Join(lines[1:], ", ")
		}
		return &b, reason
	}
	if newer, why := discovery.NewerKernel(strings.Fields(kernels)); newer {
		b = true
		reason = why
	}
	return &b, reason
}

// Classify explains APT failures (ARCHITECTURE-ALPHA §10.2).
func (apt) Classify(out string, exit int) *model.OpError {
	switch {
	case strings.Contains(out, "dpkg was interrupted"):
		return &model.OpError{Class: model.ClassPackageManager,
			Message: "dpkg was interrupted during an earlier operation",
			Hint:    "Log in to the host and run: sudo dpkg --configure -a  (DROOL does not repair this automatically)."}
	case strings.Contains(out, "Could not get lock"), strings.Contains(out, "Unable to acquire the dpkg frontend lock"),
		strings.Contains(out, "Unable to lock the administration directory"):
		msg := "the package manager is locked by another process"
		if m := lockProcRE.FindStringSubmatch(out); m != nil {
			msg = fmt.Sprintf("the package manager is locked by %s (pid %s)", m[2], m[1])
		}
		return &model.OpError{Class: model.ClassPackageManager, Retryable: true, Message: msg,
			Hint: "Another package operation is running (often unattended-upgrades). DROOL waits and retries; if this is the final result, run the job again once it has finished."}
	case strings.Contains(out, "Unable to locate package"):
		pkgs := regexp.MustCompile(`Unable to locate package (\S+)`).FindAllStringSubmatch(out, -1)
		names := make([]string, len(pkgs))
		for i, m := range pkgs {
			names[i] = m[1]
		}
		return &model.OpError{Class: model.ClassValidation, Message: "package not found: " + strings.Join(names, ", "),
			Hint: "Check the name, or refresh package metadata (apt-get update) first."}
	case strings.Contains(out, "Temporary failure resolving"), strings.Contains(out, "Failed to fetch"):
		return &model.OpError{Class: model.ClassPackageManager, Retryable: true,
			Message: "could not download packages: " + lastError(out)}
	}
	return &model.OpError{Class: model.ClassPackageManager, Message: fmt.Sprintf("apt-get failed (exit %d): %s", exit, lastError(out))}
}

// lastError returns the last "E:" line, or the last non-empty line.
func lastError(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], "E: ") || strings.HasPrefix(lines[i], "Error:") {
			return strings.TrimSpace(lines[i])
		}
	}
	if len(lines) > 0 {
		return strings.TrimSpace(lines[len(lines)-1])
	}
	return ""
}
