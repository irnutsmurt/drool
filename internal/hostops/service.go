package hostops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"drool/internal/driver"
	"drool/internal/hostexec"
	"drool/internal/model"
)

// UnitState is what `systemctl show` reports for a unit.
type UnitState struct {
	Load      string `json:"load"`      // loaded | not-found | masked | error
	Active    string `json:"active"`    // active | inactive | failed | activating | deactivating | reloading
	Sub       string `json:"sub"`       // running | dead | exited | ...
	UnitFile  string `json:"unit_file"` // enabled | disabled | static | masked | indirect | ...
	CanReload bool   `json:"can_reload"`
}

const showProps = "LoadState,ActiveState,SubState,UnitFileState,CanReload"

// ParseUnitState parses `systemctl show -p ...` key=value output.
func ParseUnitState(out string) (UnitState, bool) {
	var u UnitState
	found := false
	for _, l := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), "=")
		if !ok {
			continue
		}
		found = true
		switch k {
		case "LoadState":
			u.Load = v
		case "ActiveState":
			u.Active = v
		case "SubState":
			u.Sub = v
		case "UnitFileState":
			u.UnitFile = v
		case "CanReload":
			u.CanReload = v == "yes"
		}
	}
	return u, found && u.Load != ""
}

func (u UnitState) running() bool { return u.Active == "active" || u.Active == "reloading" }
func (u UnitState) enabled() bool {
	return u.UnitFile == "enabled" || u.UnitFile == "enabled-runtime" || u.UnitFile == "alias"
}
func (u UnitState) transitional() bool {
	return u.Active == "activating" || u.Active == "deactivating" || u.Active == "reloading"
}

// classifySystemctl explains systemctl failures that aren't about the unit.
func classifySystemctl(out string) *model.OpError {
	if strings.Contains(out, "System has not been booted with systemd") || strings.Contains(out, "Failed to connect to bus") ||
		strings.Contains(out, "systemctl: command not found") || strings.Contains(out, "systemctl: not found") {
		return &model.OpError{Class: model.ClassConfiguration, Message: "systemd is not running on this host",
			Hint: "Service operations need systemd as the init system."}
	}
	return nil
}

func showUnit(ctx context.Context, c hostexec.Runner, unit string) (UnitState, error) {
	r, err := RunCmd(ctx, c, Become{}, false, "systemctl show --no-pager -p "+showProps+" -- "+ShQuote(unit), 30*time.Second)
	if err != nil {
		return UnitState{}, err
	}
	all := string(r.Stdout) + string(r.Stderr)
	if e := classifySystemctl(all); e != nil {
		return UnitState{}, e
	}
	u, ok := ParseUnitState(string(r.Stdout))
	if !ok {
		return UnitState{}, &model.OpError{Class: model.ClassService, Message: "could not read the state of " + unit + ": " + strings.TrimSpace(all)}
	}
	return u, nil
}

// serviceActions decides which systemctl verbs reach the desired state.
func serviceActions(unit string, u UnitState, op *model.ServiceOp) ([]string, error) {
	if u.Load == "not-found" {
		return nil, &model.OpError{Class: model.ClassService, Message: "service " + unit + " not found",
			Hint: "Check the name; the package that provides it may not be installed."}
	}
	masked := u.Load == "masked" || u.UnitFile == "masked"
	var acts []string
	switch op.State {
	case "running":
		if !u.running() {
			acts = append(acts, "start")
		}
	case "stopped":
		if u.running() || u.transitional() {
			acts = append(acts, "stop")
		}
	case "restarted":
		acts = append(acts, "restart")
	case "reloaded":
		if !u.CanReload {
			return nil, &model.OpError{Class: model.ClassService, Message: unit + " does not support reload",
				Hint: "Use restart instead."}
		}
		acts = append(acts, "reload")
	}
	if op.Enabled != nil {
		switch {
		case *op.Enabled && !u.enabled():
			if u.UnitFile == "static" || u.UnitFile == "indirect" || u.UnitFile == "generated" || u.UnitFile == "transient" {
				return nil, &model.OpError{Class: model.ClassService,
					Message: fmt.Sprintf("%s cannot be enabled (unit file state: %s)", unit, u.UnitFile),
					Hint:    "It is started by another unit or has no [Install] section."}
			}
			acts = append(acts, "enable")
		case !*op.Enabled && u.enabled():
			acts = append(acts, "disable")
		}
	}
	if masked {
		for _, a := range acts {
			if a == "start" || a == "restart" || a == "reload" || a == "enable" {
				return nil, &model.OpError{Class: model.ClassService, Message: unit + " is masked",
					Hint: "Unmask it on the host first (systemctl unmask) if it should run."}
			}
		}
	}
	return acts, nil
}

// Service applies a service operation and verifies the result.
func (h Host) Service(ctx context.Context, op *model.ServiceOp, out driver.OutputSink, started func()) (*driver.OpResult, error) {
	c, b := h.R, h.B
	unit := op.UnitName()
	before, err := showUnit(ctx, c, unit)
	if err != nil {
		return nil, err
	}
	acts, err := serviceActions(unit, before, op)
	if err != nil {
		return nil, err
	}
	started()
	data := map[string]any{"unit": unit, "before": before, "actions": acts}
	zero := 0
	changed := len(acts) > 0
	res := &driver.OpResult{ExitCode: &zero, Changed: &changed, Data: data}
	if len(acts) == 0 {
		Note(out, "%s is already in the requested state (%s, %s)", unit, before.Active, before.UnitFile)
		data["after"] = before
		return res, nil
	}
	for _, a := range acts {
		Note(out, "systemctl %s %s", a, unit)
		r, err := RunCmd(ctx, c, b, true, "systemctl --no-ask-password "+a+" -- "+ShQuote(unit), time.Duration(op.TimeoutS)*time.Second)
		if err != nil {
			return nil, err
		}
		out.Stdout().Write(r.Stdout)
		out.Stderr().Write(r.Stderr)
		if r.ExitCode != 0 {
			res.ExitCode = &r.ExitCode
			return res, serviceFailure(ctx, c, b, unit, a, out)
		}
	}
	// Let start/stop settle before verifying.
	after, err := showUnit(ctx, c, unit)
	for i := 0; err == nil && after.transitional() && i < 20; i++ {
		time.Sleep(500 * time.Millisecond)
		after, err = showUnit(ctx, c, unit)
	}
	if err != nil {
		return res, err
	}
	data["after"] = after
	Note(out, "%s is now %s (%s), %s", unit, after.Active, after.Sub, after.UnitFile)
	switch op.State {
	case "running", "restarted", "reloaded":
		if !after.running() {
			return res, serviceFailure(ctx, c, b, unit, "start", out)
		}
	case "stopped":
		if after.running() {
			return res, &model.OpError{Class: model.ClassService, Message: unit + " is still running after stop"}
		}
	}
	if op.Enabled != nil && *op.Enabled != after.enabled() {
		return res, &model.OpError{Class: model.ClassService,
			Message: fmt.Sprintf("%s unit file state is %s after %s", unit, after.UnitFile, map[bool]string{true: "enable", false: "disable"}[*op.Enabled])}
	}
	return res, nil
}

// serviceFailure attaches the unit's recent status/journal to the output and
// returns a ServiceError.
func serviceFailure(ctx context.Context, c hostexec.Runner, b Become, unit, action string, out driver.OutputSink) error {
	r, err := RunCmd(ctx, c, b, true, "systemctl status --no-pager -n 15 -- "+ShQuote(unit), 30*time.Second)
	state := "unknown"
	if err == nil {
		out.Stdout().Write(r.Stdout)
		if u, e := showUnit(ctx, c, unit); e == nil {
			state = u.Active + "/" + u.Sub
		}
	}
	return &model.OpError{Class: model.ClassService, Message: fmt.Sprintf("%s failed to %s (state: %s)", unit, action, state),
		Hint: "The unit's recent log lines are in the output above."}
}

// ---- Reboot ----

// RebootResult records a reboot for the step result.
type RebootResult struct {
	BootIDBefore string  `json:"boot_id_before"`
	BootIDAfter  string  `json:"boot_id_after,omitempty"`
	DownSeconds  float64 `json:"down_seconds,omitempty"`
	TotalSeconds float64 `json:"total_seconds,omitempty"`
	// Pending: triggered, and the caller (the controller, for agent hosts)
	// still has to wait for the new boot id.
	Pending     bool `json:"pending,omitempty"`
	NotRebooted bool `json:"-"` // verified still on the old boot id
}

// RebootTrigger schedules a reboot two seconds out, so the command that
// triggers it returns cleanly (ARCHITECTURE-ALPHA §10.4).
var (
	RebootTrigger = `if command -v systemd-run >/dev/null 2>&1 && [ -d /run/systemd/system ]; then ` +
		`systemd-run --quiet --on-active=2 --timer-property=AccuracySec=100ms /bin/systemctl reboot; ` +
		`else nohup sh -c 'sleep 2; systemctl reboot || reboot' >/dev/null 2>&1 & fi`
)

func ReadBootID(ctx context.Context, c hostexec.Runner) (string, error) {
	r, err := RunCmd(ctx, c, Become{}, false, "cat /proc/sys/kernel/random/boot_id", 20*time.Second)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(r.Stdout))
	if len(id) < 30 {
		return "", &model.OpError{Class: model.ClassConfiguration, Message: "could not read the boot id"}
	}
	return id, nil
}

// Reboot triggers a reboot that lets the triggering command return, then
// (with wait and WaitReboot) waits until the host is back with a new boot
// id. It never reports success on the old boot id.
func (h Host) Reboot(ctx context.Context, wait bool, out driver.OutputSink) (*RebootResult, error) {
	before, err := ReadBootID(ctx, h.R)
	if err != nil {
		return nil, err
	}
	res := &RebootResult{BootIDBefore: before, NotRebooted: true}
	Note(out, "boot id before reboot: %s", before)
	r, err := RunCmd(ctx, h.R, h.B, true, "sh -c "+ShQuote(RebootTrigger), 30*time.Second)
	if err != nil {
		return res, err
	}
	if r.ExitCode != 0 {
		return res, &model.OpError{Class: model.ClassExecution, Message: "could not schedule the reboot: " + strings.TrimSpace(string(r.Stderr))}
	}
	res.NotRebooted = false // triggered; unknown until proven otherwise
	driver.SetPhase(out, "rebooting")
	if h.Rebooting != nil {
		h.Rebooting()
	}
	if !wait || h.WaitReboot == nil {
		if !wait {
			Note(out, "reboot scheduled; not waiting for the host")
		}
		res.Pending = wait
		return res, nil
	}
	driver.SetPhase(out, "waiting_host")
	return res, h.WaitReboot(ctx, res, out)
}

// RebootOp runs a reboot operation.
func (h Host) RebootOp(ctx context.Context, op *model.RebootOp, out driver.OutputSink, started func()) (*driver.OpResult, error) {
	started()
	rb, err := h.Reboot(ctx, op.ShouldWait(), out)
	zero := 0
	changed := err == nil || (rb != nil && !rb.NotRebooted)
	return &driver.OpResult{ExitCode: &zero, Changed: &changed, Data: map[string]any{"reboot": rb}}, err
}
