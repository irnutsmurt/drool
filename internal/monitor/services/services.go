// Package services inventories systemd services (M11) for both
// management paths: `systemctl show` for every loaded service and
// `systemctl list-unit-files` for installed ones, parsed once.
//
// Unit states are reported, not judged: whether a stopped unit is a
// problem depends on the unit (a one-shot that finished is fine). Default
// views hide noise (static and transient units, template instances)
// unless they failed; the "all units" view shows everything.
package services

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"

	"drool/internal/monitor/source"
)

// Unit is one service's state.
type Unit struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Load        string `json:"load"`       // loaded | not-found | masked | error | "" (installed, not loaded)
	Active      string `json:"active"`     // active | inactive | failed | activating | deactivating | reloading
	Sub         string `json:"sub"`        // running | exited | dead | failed | auto-restart ...
	FileState   string `json:"file_state"` // enabled | disabled | static | masked | indirect | generated | transient | alias ...
	Since       int64  `json:"since,omitempty"`
	// Restarts is systemd's automatic restart count since the unit was
	// last started by hand (NRestarts); nil when systemd doesn't say.
	Restarts  *int   `json:"restarts,omitempty"`
	Result    string `json:"result,omitempty"` // success | exit-code | signal | timeout | core-dump | ...
	Transient bool   `json:"transient,omitempty"`
}

// Report is the "services" telemetry: the full list.
type Report struct {
	At        int64  `json:"at"`
	Supported bool   `json:"supported"`
	Note      string `json:"note,omitempty"`
	Units     []Unit `json:"units"`
}

// MaxUnits bounds the inventory.
const MaxUnits = 2000

var props = "Id,Description,LoadState,ActiveState,SubState,UnitFileState,ActiveEnterTimestampMonotonic,StateChangeTimestampMonotonic,NRestarts,Result,Transient"

// Commands are the fixed queries (also part of the SSH first pass).
var Commands = [][]string{
	{"systemctl", "show", "--no-pager", "-p", props, "*.service"},
	{"systemctl", "list-unit-files", "--type=service", "--no-legend", "--no-pager"},
}

// Spec for the SSH script.
var Spec = source.Spec{
	Files:    []string{"/proc/stat"},
	Commands: Commands,
	Tools:    []string{"systemctl"},
}

// Collect reads the inventory. Without systemd it reports unsupported.
// files=false skips the unit-file listing (between full refreshes) and
// keeps fileStates from before.
func Collect(ctx context.Context, src source.Source, btime int64, fileStates map[string]string) (*Report, map[string]string, error) {
	rep := &Report{Units: []Unit{}}
	if !src.Has("systemctl") {
		rep.Note = "systemd is not available on this host: no service inventory."
		return rep, fileStates, nil
	}
	r, err := src.Run(ctx, Commands[0][0], Commands[0][1:]...)
	if err != nil {
		if errors.Is(err, source.ErrDeferred) {
			return nil, nil, err
		}
		rep.Note = "systemctl failed: " + err.Error()
		return rep, fileStates, nil
	}
	if r.Exit != 0 && len(r.Stdout) == 0 {
		rep.Note = "systemd is not running on this host (systemctl exited " + strconv.Itoa(r.Exit) + "): no service inventory."
		return rep, fileStates, nil
	}
	if fileStates == nil {
		fileStates = map[string]string{}
		if lf, err := src.Run(ctx, Commands[1][0], Commands[1][1:]...); err == nil && lf.Exit == 0 {
			fileStates = ParseUnitFiles(lf.Stdout)
		}
	}
	rep.Supported = true
	rep.Units = Merge(ParseShow(r.Stdout, btime), fileStates)
	return rep, fileStates, nil
}

// ParseShow parses `systemctl show -p ...` output: blocks of KEY=VALUE
// separated by blank lines. btime converts monotonic timestamps (µs
// since boot) to unix seconds without parsing localized dates.
func ParseShow(b []byte, btime int64) []Unit {
	var out []Unit
	for _, block := range strings.Split(string(b), "\n\n") {
		kv := map[string]string{}
		for _, ln := range strings.Split(block, "\n") {
			k, v, ok := strings.Cut(ln, "=")
			if ok {
				kv[k] = v
			}
		}
		id := kv["Id"]
		if id == "" || !strings.HasSuffix(id, ".service") {
			continue
		}
		u := Unit{Name: id, Description: trunc(kv["Description"], 200), Load: kv["LoadState"], Active: kv["ActiveState"],
			Sub: kv["SubState"], FileState: kv["UnitFileState"], Result: kv["Result"], Transient: kv["Transient"] == "yes"}
		mono := kv["ActiveEnterTimestampMonotonic"]
		if u.Active != "active" {
			mono = kv["StateChangeTimestampMonotonic"]
		}
		if us, err := strconv.ParseInt(mono, 10, 64); err == nil && us > 0 && btime > 0 {
			u.Since = btime + us/1_000_000
		}
		if n, err := strconv.Atoi(kv["NRestarts"]); err == nil && n >= 0 {
			u.Restarts = &n
		}
		out = append(out, u)
		if len(out) >= MaxUnits {
			break
		}
	}
	return out
}

// ParseUnitFiles parses `systemctl list-unit-files --no-legend`.
func ParseUnitFiles(b []byte) map[string]string {
	out := map[string]string{}
	for _, ln := range strings.Split(string(b), "\n") {
		f := strings.Fields(ln)
		if len(f) >= 2 && strings.HasSuffix(f[0], ".service") {
			out[f[0]] = f[1]
		}
		if len(out) >= MaxUnits {
			break
		}
	}
	return out
}

// Merge adds installed-but-not-loaded unit files (never started,
// disabled) to the loaded units. Templates (foo@.service) are skipped.
func Merge(loaded []Unit, files map[string]string) []Unit {
	seen := map[string]bool{}
	for i := range loaded {
		seen[loaded[i].Name] = true
		if loaded[i].FileState == "" {
			loaded[i].FileState = files[loaded[i].Name]
		}
	}
	for name, st := range files {
		if seen[name] || strings.HasSuffix(name, "@.service") || len(loaded) >= MaxUnits {
			continue
		}
		loaded = append(loaded, Unit{Name: name, Active: "inactive", Sub: "dead", FileState: st})
	}
	sort.Slice(loaded, func(i, j int) bool { return loaded[i].Name < loaded[j].Name })
	return loaded
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Notable reports whether a unit belongs in the default view: enabled
// units, anything currently running or failed, but not static helpers,
// transient units, template instances that aren't running, or masked
// units (unless failed).
func Notable(u Unit) bool {
	if u.Active == "failed" || u.Sub == "failed" {
		return true
	}
	if u.Transient || u.FileState == "transient" || u.Load == "not-found" || u.FileState == "masked" {
		return false
	}
	if strings.Contains(u.Name, "@") {
		return u.Active == "active" && u.Sub == "running"
	}
	switch u.FileState {
	case "enabled", "enabled-runtime", "linked", "linked-runtime", "alias", "indirect":
		return true
	case "static", "generated":
		return u.Active == "active" && u.Sub == "running" && !systemInternal(u.Name)
	}
	return u.Active == "active" && u.Sub == "running"
}

// systemInternal are systemd's own plumbing, hidden by default.
func systemInternal(n string) bool {
	return strings.HasPrefix(n, "systemd-") || strings.HasPrefix(n, "dbus") || strings.HasPrefix(n, "user-runtime-dir") ||
		strings.HasPrefix(n, "getty") || strings.HasPrefix(n, "serial-getty") || strings.HasPrefix(n, "polkit") ||
		strings.HasPrefix(n, "kmod-") || strings.HasPrefix(n, "modprobe")
}

// State names one unit's state in the categories the filters use.
func State(u Unit) string {
	switch {
	case u.Active == "failed" || u.Sub == "failed":
		return "failed"
	case u.Active == "active" || u.Active == "reloading" || u.Active == "activating":
		return "running"
	case u.Active == "unknown":
		return "unknown"
	}
	return "stopped"
}

// Healthy reports a default-view unit as fine: anything not failed. A
// stopped unit is a problem only when someone said it should run
// (watched services, judged by the health evaluation).
func Healthy(u Unit) bool { return State(u) != "failed" }

// Change is a transition worth keeping.
type Change struct {
	Unit             string
	FromActive, From string // active state and sub-state before
	ToActive, To     string
	Result           string
}

// Diff compares two inventories and returns state changes (active or
// sub-state), new units (from "") and units that vanished (to "").
func Diff(prev, cur []Unit) []Change {
	old := map[string]Unit{}
	for _, u := range prev {
		old[u.Name] = u
	}
	var out []Change
	seen := map[string]bool{}
	for _, u := range cur {
		seen[u.Name] = true
		o, ok := old[u.Name]
		if ok && o.Active == u.Active && o.Sub == u.Sub {
			continue
		}
		out = append(out, Change{Unit: u.Name, FromActive: o.Active, From: o.Sub, ToActive: u.Active, To: u.Sub, Result: u.Result})
	}
	for _, o := range prev {
		if !seen[o.Name] {
			out = append(out, Change{Unit: o.Name, FromActive: o.Active, From: o.Sub})
		}
	}
	return out
}
