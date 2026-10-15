package services

import (
	"slices"
	"strings"
	"testing"
)

// The actions offered follow the unit as it is: runtime state and
// boot-time (unit-file) state separately, nothing misleading for units
// that can't be enabled, nothing that starts a masked one.
func TestActions(t *testing.T) {
	for _, c := range []struct {
		name string
		u    Unit
		want []string
		note string
	}{
		{"active and enabled", Unit{Load: "loaded", Active: "active", Sub: "running", FileState: "enabled"}, []string{"stop", "restart", "disable", "disable_stop"}, ""},
		{"inactive and disabled", Unit{Load: "loaded", Active: "inactive", Sub: "dead", FileState: "disabled"}, []string{"start", "enable", "enable_start"}, ""},
		{"active and disabled", Unit{Load: "loaded", Active: "active", Sub: "running", FileState: "disabled"}, []string{"stop", "restart", "enable"}, ""},
		{"inactive and enabled", Unit{Load: "loaded", Active: "inactive", Sub: "dead", FileState: "enabled"}, []string{"start", "disable"}, ""},
		{"failed and enabled", Unit{Load: "loaded", Active: "failed", Sub: "failed", FileState: "enabled"}, []string{"start", "disable"}, ""},
		{"static, running", Unit{Load: "loaded", Active: "active", Sub: "running", FileState: "static"}, []string{"stop", "restart"}, "Static"},
		{"static, stopped", Unit{Load: "loaded", Active: "inactive", Sub: "dead", FileState: "static"}, []string{"start"}, "can't be enabled"},
		{"masked, stopped", Unit{Load: "masked", Active: "inactive", Sub: "dead", FileState: "masked"}, []string{}, "Masked"},
		{"masked, still running", Unit{Load: "masked", Active: "active", Sub: "running", FileState: "masked"}, []string{"stop"}, "unmask"},
		{"masked at runtime", Unit{Load: "loaded", Active: "inactive", FileState: "masked-runtime"}, []string{}, "Masked"},
		{"indirect", Unit{Load: "loaded", Active: "inactive", FileState: "indirect"}, []string{"start"}, "Indirect"},
		{"generated", Unit{Load: "loaded", Active: "active", FileState: "generated"}, []string{"stop", "restart"}, "Generated"},
		{"transient", Unit{Load: "loaded", Active: "active", FileState: "transient", Transient: true}, []string{"stop"}, "Transient"},
		{"alias", Unit{Load: "loaded", Active: "active", FileState: "alias"}, []string{"stop", "restart"}, "alias"},
		{"not found", Unit{Load: "not-found", Active: "inactive"}, []string{}, "doesn't find"},
		{"enabled at runtime", Unit{Load: "loaded", Active: "active", FileState: "enabled-runtime"}, []string{"stop", "restart", "disable", "disable_stop"}, ""},
	} {
		got, note := Actions(c.u)
		if !slices.Equal(got, c.want) || c.note != "" && !strings.Contains(note, c.note) || c.note == "" && note != "" {
			t.Errorf("%s: %v %q, want %v %q", c.name, got, note, c.want, c.note)
		}
	}
}
