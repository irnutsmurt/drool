package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// OS families recognized by conditions and per-OS script bodies.
var OSFamilies = []string{"debian", "redhat", "suse", "arch"}

// When restricts a step to hosts whose discovered facts match. Empty lists
// match everything; within a list any value matches.
type When struct {
	OSFamily []string `json:"os_family,omitempty"`
	Distro   []string `json:"distro,omitempty"` // os-release ID, e.g. ubuntu, rocky
}

func (w *When) empty() bool { return w == nil || (len(w.OSFamily) == 0 && len(w.Distro) == 0) }

func (w *When) normalize() error {
	if w == nil {
		return nil
	}
	for i, f := range w.OSFamily {
		w.OSFamily[i] = strings.ToLower(strings.TrimSpace(f))
		if !slices.Contains(OSFamilies, w.OSFamily[i]) {
			return fmt.Errorf("when.os_family: unknown family %q (use %s)", f, strings.Join(OSFamilies, ", "))
		}
	}
	for i, d := range w.Distro {
		w.Distro[i] = strings.ToLower(strings.TrimSpace(d))
		if w.Distro[i] == "" {
			return errors.New("when.distro: empty value")
		}
	}
	return nil
}

// HostOS is what conditions are evaluated against.
type HostOS struct {
	Family string // "" when facts are unknown
	Distro string
	Known  bool
}

func (w *When) match(os HostOS) (bool, string) {
	if w.empty() {
		return true, ""
	}
	if !os.Known {
		return false, "OS facts unknown (discovery has not run yet)"
	}
	if len(w.OSFamily) > 0 && !slices.Contains(w.OSFamily, os.Family) {
		return false, fmt.Sprintf("OS family %s is not one of %s", os.Family, strings.Join(w.OSFamily, ", "))
	}
	if len(w.Distro) > 0 && !slices.Contains(w.Distro, os.Distro) {
		return false, fmt.Sprintf("distribution %s is not one of %s", os.Distro, strings.Join(w.Distro, ", "))
	}
	return true, ""
}

// ScriptBody is either one script or per-OS variants keyed by family or
// distro id, with an optional "default". JSON: a string or an object.
type ScriptBody struct {
	Text string            // single body (also the resolved body)
	ByOS map[string]string // per-OS variants; nil for a single body
}

func (b ScriptBody) MarshalJSON() ([]byte, error) {
	if b.ByOS != nil {
		return json.Marshal(b.ByOS)
	}
	return json.Marshal(b.Text)
}

func (b *ScriptBody) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*b = ScriptBody{Text: s}
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return errors.New("body must be a string or an object of per-OS scripts")
	}
	*b = ScriptBody{ByOS: m}
	return nil
}

func (b *ScriptBody) normalize() error {
	if b.ByOS == nil {
		if strings.TrimSpace(b.Text) == "" {
			return errors.New("script body must not be empty")
		}
		if len(b.Text) > MaxScriptBytes {
			return fmt.Errorf("script is longer than %d bytes", MaxScriptBytes)
		}
		return nil
	}
	if len(b.ByOS) == 0 {
		return errors.New("per-OS script body needs at least one variant")
	}
	total := 0
	for k, v := range b.ByOS {
		if k == "" || strings.ToLower(k) != k || strings.ContainsAny(k, " \t") {
			return fmt.Errorf("per-OS script key %q must be a lowercase OS family, distro id or \"default\"", k)
		}
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("per-OS script %q is empty", k)
		}
		total += len(v)
	}
	if total > MaxScriptBytes {
		return fmt.Errorf("scripts are longer than %d bytes in total", MaxScriptBytes)
	}
	return nil
}

// Keys lists per-OS variant keys, sorted.
func (b ScriptBody) Keys() []string {
	keys := make([]string, 0, len(b.ByOS))
	for k := range b.ByOS {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// resolve picks the body for os: distro id first, then family, then default.
func (b ScriptBody) resolve(os HostOS) (string, string) {
	if b.ByOS == nil {
		return b.Text, ""
	}
	if os.Known {
		if v, ok := b.ByOS[os.Distro]; ok {
			return v, ""
		}
		if v, ok := b.ByOS[os.Family]; ok {
			return v, ""
		}
	}
	if v, ok := b.ByOS["default"]; ok {
		return v, ""
	}
	if !os.Known {
		return "", "OS facts unknown (discovery has not run yet) and the script has no default variant"
	}
	return "", fmt.Sprintf("no script variant for %s/%s (variants: %s)", os.Family, os.Distro, strings.Join(b.Keys(), ", "))
}

// ForHost resolves a step for one host: whether it runs, why not, and the
// concrete operation to execute (with a single resolved script body).
func (o Operation) ForHost(os HostOS) (Operation, bool, string) {
	switch o.Type {
	case OpCommand:
		if ok, why := o.Command.When.match(os); !ok {
			return o, false, why
		}
		return o, true, ""
	case OpScript:
		if ok, why := o.Script.When.match(os); !ok {
			return o, false, why
		}
		body, why := o.Script.Body.resolve(os)
		if why != "" {
			return o, false, why
		}
		s := *o.Script
		s.Body = ScriptBody{Text: body}
		return Operation{Type: OpScript, Script: &s}, true, ""
	}
	if ok, why := o.when().match(os); !ok {
		return o, false, why
	}
	return o, true, ""
}

func (o Operation) when() *When {
	switch o.Type {
	case OpPackage:
		return o.Package.When
	case OpSystemUpdate:
		return o.SystemUpdate.When
	case OpService:
		return o.Service.When
	case OpReboot:
		return o.Reboot.When
	}
	return nil
}

// NeedsFacts reports whether any step depends on discovered facts.
func (j JobSpec) NeedsFacts() bool {
	for _, s := range j.Steps {
		switch s.Type {
		case OpCommand:
			if !s.Command.When.empty() {
				return true
			}
		case OpScript:
			if !s.Script.When.empty() || s.Script.Body.ByOS != nil {
				return true
			}
		default:
			if !s.when().empty() {
				return true
			}
		}
	}
	return false
}
