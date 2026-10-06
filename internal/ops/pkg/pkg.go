// Package pkg generates package-manager commands for APT and DNF (dnf4 and
// dnf5) and parses their output (ARCHITECTURE-ALPHA §10.2).
//
// Every command here is a single shell line; the SSH driver decides how to
// run it (simulation and queries directly, real changes through the
// cancellable run-directory protocol).
package pkg

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"drool/internal/model"
)

// Change is one package in a transaction plan.
type Change struct {
	Name string `json:"name"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

// Plan is what a transaction would do.
type Plan struct {
	Install   []Change `json:"install"`
	Upgrade   []Change `json:"upgrade"`
	Remove    []Change `json:"remove"`
	Downgrade []Change `json:"downgrade"`
}

// Empty reports whether nothing would change.
func (p Plan) Empty() bool {
	return len(p.Install)+len(p.Upgrade)+len(p.Remove)+len(p.Downgrade) == 0
}

func newPlan() Plan {
	return Plan{Install: []Change{}, Upgrade: []Change{}, Remove: []Change{}, Downgrade: []Change{}}
}

// ExtraRemovals returns removals of packages other than those named.
func (p Plan) ExtraRemovals(named []string) []Change {
	keep := map[string]bool{}
	for _, n := range named {
		keep[n] = true
	}
	var out []Change
	for _, c := range p.Remove {
		if !keep[c.Name] {
			out = append(out, c)
		}
	}
	return out
}

// Backend builds commands for one package manager.
type Backend interface {
	Name() string // apt | dnf
	// Refresh updates package metadata (root).
	Refresh() string
	// Simulate shows what a transaction would do without changing anything.
	// action: install | remove | upgrade (named) | system-upgrade | system-full-upgrade | system-security.
	Simulate(action string, names []string) (string, error)
	SimulateNeedsRoot() bool
	ParsePlan(out string, exit int) (Plan, error)
	// Exec performs a transaction (root).
	Exec(action string, names []string) (string, error)
	// Versions lists installed versions; ParseVersions maps name → version ("" = not installed).
	Versions(names []string) string
	ParseVersions(out string, names []string) map[string]string
	// AllVersions lists every installed package (for system updates).
	AllVersions() string
	// RebootRequired prints whether a reboot is needed; Parse returns nil when unknown.
	RebootRequired() string
	ParseRebootRequired(out string) (*bool, string)
	// Classify turns a failed command's output into a structured error.
	Classify(out string, exit int) *model.OpError
}

// ForManager returns the backend for a detected package manager.
func ForManager(name string) (Backend, error) {
	switch name {
	case "apt", "apt-get":
		return apt{}, nil
	case "dnf", "dnf5", "yum":
		return dnf{}, nil
	}
	return nil, model.Errorf(model.ClassConfiguration, false, "unsupported package manager %q (supported: apt, dnf)", name)
}

// DetectCommand prints the available package manager.
const DetectCommand = `for c in apt-get dnf yum; do if command -v "$c" >/dev/null 2>&1; then echo "$c"; exit 0; fi; done; echo none`

func shNames(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = "'" + strings.ReplaceAll(n, "'", `'\''`) + "'"
	}
	return strings.Join(q, " ")
}

func joinChanges(cs []Change) string {
	names := make([]string, len(cs))
	for i, c := range cs {
		names[i] = c.Name
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// RemovalError explains removals that need explicit approval.
func RemovalError(extra []Change) *model.OpError {
	return &model.OpError{Class: model.ClassValidation,
		Message: fmt.Sprintf("this would also remove %d package(s): %s", len(extra), joinChanges(extra)),
		Hint:    "Nothing was changed. Use \"Preview changes\" on the New job page to review them, and allow the removals there if they are expected."}
}

func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "; ")
}

var lockProcRE = regexp.MustCompile(`held by process (\d+) \(([^)]+)\)`)
