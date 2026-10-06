package sshdriver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"drool/internal/driver"
	"drool/internal/model"
	"drool/internal/sshx"
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

func showUnit(ctx context.Context, c *sshx.Client, unit string) (UnitState, error) {
	r, err := run(ctx, c, Become{}, false, "systemctl show --no-pager -p "+showProps+" -- "+shQuote(unit), 30*time.Second)
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

// executeService applies a service operation and verifies the result.
func (x *Executor) executeService(ctx context.Context, c *sshx.Client, b Become, op *model.ServiceOp,
	out driver.OutputSink, started func()) (*driver.OpResult, error) {
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
		note(out, "%s is already in the requested state (%s, %s)", unit, before.Active, before.UnitFile)
		data["after"] = before
		return res, nil
	}
	for _, a := range acts {
		note(out, "systemctl %s %s", a, unit)
		r, err := run(ctx, c, b, true, "systemctl --no-ask-password "+a+" -- "+shQuote(unit), time.Duration(op.TimeoutS)*time.Second)
		if err != nil {
			return nil, err
		}
		out.Stdout().Write(r.Stdout)
		out.Stderr().Write(r.Stderr)
		if r.ExitCode != 0 {
			res.ExitCode = &r.ExitCode
			return res, x.serviceFailure(ctx, c, b, unit, a, out)
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
	note(out, "%s is now %s (%s), %s", unit, after.Active, after.Sub, after.UnitFile)
	switch op.State {
	case "running", "restarted", "reloaded":
		if !after.running() {
			return res, x.serviceFailure(ctx, c, b, unit, "start", out)
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
func (x *Executor) serviceFailure(ctx context.Context, c *sshx.Client, b Become, unit, action string, out driver.OutputSink) error {
	r, err := run(ctx, c, b, true, "systemctl status --no-pager -n 15 -- "+shQuote(unit), 30*time.Second)
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
	notRebooted  bool    // verified still on the old boot id
}

// Reboot polling (ARCHITECTURE-ALPHA §10.4).
var (
	rebootPollStart = 3 * time.Second
	rebootPollMax   = 15 * time.Second
	rebootTrigger   = `if command -v systemd-run >/dev/null 2>&1 && [ -d /run/systemd/system ]; then ` +
		`systemd-run --quiet --on-active=2 --timer-property=AccuracySec=100ms /bin/systemctl reboot; ` +
		`else nohup sh -c 'sleep 2; systemctl reboot || reboot' >/dev/null 2>&1 & fi`
)

func readBootID(ctx context.Context, c *sshx.Client) (string, error) {
	r, err := run(ctx, c, Become{}, false, "cat /proc/sys/kernel/random/boot_id", 20*time.Second)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(r.Stdout))
	if len(id) < 30 {
		return "", &model.OpError{Class: model.ClassConfiguration, Message: "could not read the boot id"}
	}
	return id, nil
}

// reboot triggers a reboot that lets the SSH command return, then (with
// wait) polls with fresh connections until the host answers with a pinned
// host key and a new boot id. It never reports success on the old boot id.
func (x *Executor) reboot(ctx context.Context, c *sshx.Client, t *sshx.Target, b Become, wait bool, out driver.OutputSink) (*RebootResult, error) {
	before, err := readBootID(ctx, c)
	if err != nil {
		return nil, err
	}
	res := &RebootResult{BootIDBefore: before, notRebooted: true}
	note(out, "boot id before reboot: %s", before)
	r, err := run(ctx, c, b, true, "sh -c "+shQuote(rebootTrigger), 30*time.Second)
	if err != nil {
		return res, err
	}
	if r.ExitCode != 0 {
		return res, &model.OpError{Class: model.ClassExecution, Message: "could not schedule the reboot: " + strings.TrimSpace(string(r.Stderr))}
	}
	res.notRebooted = false // triggered; unknown until proven otherwise
	start := time.Now()
	driver.SetPhase(out, "rebooting")
	x.Pool.Evict(t.HostID)
	if !wait {
		note(out, "reboot scheduled; not waiting for the host")
		return res, nil
	}
	driver.SetPhase(out, "waiting_host")
	var wentAway, lastOld time.Time // first failed poll; last answer with the old boot id
	delay := rebootPollStart
	lastErr := ""
	for {
		select {
		case <-ctx.Done():
			// Judge by the latest contact: a slow handshake in between is not a reboot.
			if wentAway.IsZero() || lastOld.After(wentAway) {
				res.notRebooted = true
				return res, &model.OpError{Class: model.ClassTimeout,
					Message: "the host never went away; it still reports the old boot id (the reboot did not happen)",
					Hint:    "The reboot was requested but the host kept running. Something may be blocking shutdown (a hung service or an inhibitor lock); check the host, or reboot it from its console."}
			}
			return res, &model.OpError{Class: model.ClassTimeout,
				Message: fmt.Sprintf("the host went down but did not come back within the timeout (last error: %s)", lastErr),
				Hint:    "Check the host's console or power state. Its status on the host page updates by itself once it answers again."}
		case <-time.After(delay):
		}
		delay = min(delay*2, rebootPollMax)
		fresh, err := x.Pool.Dialer.Dial(ctx, t)
		if err != nil {
			if model.ClassOf(err) == model.ClassHostKey {
				return res, err // never trust a different key after a reboot
			}
			if wentAway.IsZero() {
				wentAway = time.Now()
				note(out, "host went away after %s", time.Since(start).Round(time.Second))
			}
			lastErr = err.Error()
			continue
		}
		id, err := readBootID(ctx, fresh)
		fresh.Close()
		if err != nil {
			lastErr = err.Error()
			continue
		}
		if id == before {
			lastOld = time.Now()
			continue // not rebooted yet
		}
		driver.SetPhase(out, "verifying")
		res.BootIDAfter = id
		res.TotalSeconds = time.Since(start).Seconds()
		if !wentAway.IsZero() {
			res.DownSeconds = time.Since(wentAway).Seconds()
		}
		note(out, "host is back after %s with boot id %s", time.Since(start).Round(time.Second), id)
		return res, nil
	}
}

func (x *Executor) executeReboot(ctx context.Context, c *sshx.Client, t *sshx.Target, b Become, op *model.RebootOp,
	out driver.OutputSink, started func()) (*driver.OpResult, error) {
	started()
	rb, err := x.reboot(ctx, c, t, b, op.ShouldWait(), out)
	zero := 0
	changed := err == nil || (rb != nil && !rb.notRebooted)
	return &driver.OpResult{ExitCode: &zero, Changed: &changed, Data: map[string]any{"reboot": rb}}, err
}
