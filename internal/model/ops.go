package model

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// M4 operation types (ARCHITECTURE-ALPHA §5.1).
const (
	OpPackage      = "package"
	OpSystemUpdate = "system_update"
	OpService      = "service"
	OpReboot       = "reboot"
)

// PackageOp installs, removes or upgrades named packages.
type PackageOp struct {
	Action string   `json:"action"` // install | remove | upgrade
	Names  []string `json:"names"`
	// AllowRemovals permits removing packages other than the named ones
	// (e.g. dependents of a removed package, or conflicts of an install).
	AllowRemovals bool  `json:"allow_removals"`
	TimeoutS      int   `json:"timeout_s"`
	When          *When `json:"when,omitempty"`
}

// SystemUpdateOp upgrades the whole system.
type SystemUpdateOp struct {
	// Mode: upgrade (never removes on APT), full_upgrade (may remove or
	// replace packages), security (DNF only in the alpha; decision D4).
	Mode string `json:"mode"`
	// AllowRemovals must be true to proceed when the simulation shows removals.
	AllowRemovals bool   `json:"allow_removals"`
	Refresh       *bool  `json:"refresh"` // refresh metadata first (default true)
	Reboot        string `json:"reboot"`  // never | if_required | always
	TimeoutS      int    `json:"timeout_s"`
	When          *When  `json:"when,omitempty"`
}

// ServiceOp sets a systemd unit's state. running/stopped and enabled are
// idempotent; restarted/reloaded always act.
type ServiceOp struct {
	Name     string `json:"name"`
	State    string `json:"state,omitempty"` // running | stopped | restarted | reloaded | "" (unchanged)
	Enabled  *bool  `json:"enabled"`         // nil = unchanged
	TimeoutS int    `json:"timeout_s"`
	When     *When  `json:"when,omitempty"`
}

// RebootOp reboots and (by default) waits for the host to come back.
type RebootOp struct {
	Wait     *bool `json:"wait"` // default true
	TimeoutS int   `json:"timeout_s"`
	When     *When `json:"when,omitempty"`
}

var (
	packageNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+_~:@-]{0,127}$`)
	unitNameRE    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_.@\\-]{0,254}$`)
)

// RefreshMetadata reports whether to refresh package metadata first.
func (o *SystemUpdateOp) RefreshMetadata() bool { return o.Refresh == nil || *o.Refresh }

// ShouldWait reports whether the reboot waits for the host.
func (o *RebootOp) ShouldWait() bool { return o.Wait == nil || *o.Wait }

// UnitName returns the systemd unit, adding ".service" when no suffix is given.
func (o *ServiceOp) UnitName() string {
	if strings.Contains(o.Name, ".") && !strings.HasSuffix(o.Name, ".") {
		return o.Name
	}
	return o.Name + ".service"
}

func (o *PackageOp) normalize() error {
	switch o.Action {
	case "install", "remove", "upgrade":
	default:
		return errors.New(`action must be "install", "remove" or "upgrade"`)
	}
	if len(o.Names) == 0 {
		return errors.New("name at least one package")
	}
	if len(o.Names) > 200 {
		return errors.New("at most 200 packages per step")
	}
	seen := map[string]bool{}
	var names []string
	for _, n := range o.Names {
		n = strings.TrimSpace(n)
		if n == "" || seen[n] {
			continue
		}
		if !packageNameRE.MatchString(n) {
			return fmt.Errorf("invalid package name %q", n)
		}
		seen[n] = true
		names = append(names, n)
	}
	o.Names = names
	if err := o.When.normalize(); err != nil {
		return err
	}
	return normTimeout(&o.TimeoutS, 1800)
}

func (o *SystemUpdateOp) normalize() error {
	switch o.Mode {
	case "":
		o.Mode = "upgrade"
	case "upgrade", "full_upgrade", "security":
	default:
		return errors.New(`mode must be "upgrade", "full_upgrade" or "security"`)
	}
	switch o.Reboot {
	case "":
		o.Reboot = "never"
	case "never", "if_required", "always":
	default:
		return errors.New(`reboot must be "never", "if_required" or "always"`)
	}
	if err := o.When.normalize(); err != nil {
		return err
	}
	return normTimeout(&o.TimeoutS, 3600)
}

func (o *ServiceOp) normalize() error {
	o.Name = strings.TrimSpace(o.Name)
	if !unitNameRE.MatchString(o.Name) {
		return fmt.Errorf("invalid service name %q", o.Name)
	}
	switch o.State {
	case "", "running", "stopped", "restarted", "reloaded":
	default:
		return errors.New(`state must be "running", "stopped", "restarted" or "reloaded"`)
	}
	if o.State == "" && o.Enabled == nil {
		return errors.New("set a state, enabled, or both")
	}
	if err := o.When.normalize(); err != nil {
		return err
	}
	return normTimeout(&o.TimeoutS, 120)
}

func (o *RebootOp) normalize() error {
	if err := o.When.normalize(); err != nil {
		return err
	}
	return normTimeout(&o.TimeoutS, 900)
}
