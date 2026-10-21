package hostops

import (
	"context"
	"errors"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"

	"drool/internal/hostexec"
	"drool/internal/model"
)

// systemdHost is a fake host with one unit whose state systemctl changes:
// show reports it, start/stop/restart/enable/disable change it (unless
// told to fail or to lie), status prints a line. It records the systemctl
// verbs it ran.
type systemdHost struct {
	load, active, file string
	failVerb           string // this verb exits 1 and changes nothing
	lieVerb            string // this verb exits 0 and changes nothing
	verbs              []string
}

var verbRe = regexp.MustCompile(`systemctl --no-ask-password (\w+) -- `)

func (h *systemdHost) Run(_ context.Context, cmd string, _ hostexec.RunOptions) (*hostexec.RunResult, error) {
	switch {
	case strings.Contains(cmd, "systemctl show"):
		return &hostexec.RunResult{Stdout: []byte("LoadState=" + h.load + "\nActiveState=" + h.active + "\nSubState=x\nUnitFileState=" + h.file + "\nCanReload=no\n")}, nil
	case strings.Contains(cmd, "systemctl status"):
		return &hostexec.RunResult{Stdout: []byte("● app.service - the app\n")}, nil
	}
	m := verbRe.FindStringSubmatch(cmd)
	if m == nil {
		return &hostexec.RunResult{}, nil
	}
	v := m[1]
	h.verbs = append(h.verbs, v)
	if v == h.failVerb {
		return &hostexec.RunResult{ExitCode: 1, Stderr: []byte("Job for app.service failed.\n")}, nil
	}
	if v == h.lieVerb {
		return &hostexec.RunResult{}, nil
	}
	switch v {
	case "start", "restart":
		h.active = "active"
	case "stop":
		h.active = "inactive"
	case "enable":
		h.file = "enabled"
	case "disable":
		h.file = "disabled"
	}
	return &hostexec.RunResult{}, nil
}

func (h *systemdHost) Start(context.Context, string, []byte, io.Writer, io.Writer, bool) (hostexec.Process, error) {
	return nil, errors.New("not used")
}

// TestServiceActionMatrix (final After-M17 batch): the seven service actions
// the Services view offers, as the typed service operation runs them (the
// same code over SSH and on a Shepherd): only the systemctl verbs needed,
// each result read back from the host, and refusals for units that can't
// do what was asked.
func TestServiceActionMatrix(t *testing.T) {
	old := StartCheck
	StartCheck = time.Millisecond
	t.Cleanup(func() { StartCheck = old })
	on, off := true, false
	type tc struct {
		name               string
		load, active, file string
		fail, lie          string
		op                 model.ServiceOp
		verbs              string // systemctl verbs run, in order
		after              string // active/file afterwards
		err                string // the error, if any
	}
	for _, c := range []tc{
		{name: "start", load: "loaded", active: "inactive", file: "disabled", op: model.ServiceOp{State: "running"}, verbs: "start", after: "active/disabled"},
		{name: "stop", load: "loaded", active: "active", file: "enabled", op: model.ServiceOp{State: "stopped"}, verbs: "stop", after: "inactive/enabled"},
		{name: "restart", load: "loaded", active: "active", file: "enabled", op: model.ServiceOp{State: "restarted"}, verbs: "restart", after: "active/enabled"},
		{name: "enable", load: "loaded", active: "inactive", file: "disabled", op: model.ServiceOp{Enabled: &on}, verbs: "enable", after: "inactive/enabled"},
		{name: "disable", load: "loaded", active: "active", file: "enabled", op: model.ServiceOp{Enabled: &off}, verbs: "disable", after: "active/disabled"},
		{name: "enable & start", load: "loaded", active: "inactive", file: "disabled", op: model.ServiceOp{State: "running", Enabled: &on}, verbs: "start enable", after: "active/enabled"},
		{name: "disable & stop", load: "loaded", active: "active", file: "enabled", op: model.ServiceOp{State: "stopped", Enabled: &off}, verbs: "stop disable", after: "inactive/disabled"},
		{name: "already as asked", load: "loaded", active: "active", file: "enabled", op: model.ServiceOp{State: "running", Enabled: &on}, verbs: "", after: "active/enabled"},
		{name: "start a failed unit", load: "loaded", active: "failed", file: "enabled", op: model.ServiceOp{State: "running"}, verbs: "start", after: "active/enabled"},
		// A static unit starts and stops; enabling it is refused before
		// anything runs.
		{name: "start a static unit", load: "loaded", active: "inactive", file: "static", op: model.ServiceOp{State: "running"}, verbs: "start", after: "active/static"},
		{name: "enable a static unit", load: "loaded", active: "inactive", file: "static", op: model.ServiceOp{Enabled: &on}, verbs: "", after: "inactive/static",
			err: "app.service cannot be enabled (unit file state: static)"},
		{name: "enable an indirect unit", load: "loaded", active: "inactive", file: "indirect", op: model.ServiceOp{Enabled: &on}, verbs: "", after: "inactive/indirect",
			err: "cannot be enabled (unit file state: indirect)"},
		// A masked unit isn't started or enabled; stopping it still works.
		{name: "start a masked unit", load: "masked", active: "inactive", file: "masked", op: model.ServiceOp{State: "running"}, verbs: "", after: "inactive/masked", err: "app.service is masked"},
		{name: "enable a masked unit", load: "masked", active: "inactive", file: "masked", op: model.ServiceOp{Enabled: &on}, verbs: "", after: "inactive/masked", err: "app.service is masked"},
		{name: "stop a masked unit", load: "masked", active: "active", file: "masked", op: model.ServiceOp{State: "stopped"}, verbs: "stop", after: "inactive/masked"},
		{name: "a unit systemd doesn't know", load: "not-found", active: "inactive", file: "", op: model.ServiceOp{State: "running"}, verbs: "", after: "inactive/", err: "service app.service not found"},
		// systemctl fails: a service error with the unit's state.
		{name: "start fails", load: "loaded", active: "inactive", file: "disabled", fail: "start", op: model.ServiceOp{State: "running"}, verbs: "start", after: "inactive/disabled",
			err: "app.service failed to start (state: inactive/x)"},
		// systemctl says yes but the host says otherwise: the host wins.
		{name: "stop that didn't stop", load: "loaded", active: "active", file: "enabled", lie: "stop", op: model.ServiceOp{State: "stopped"}, verbs: "stop", after: "active/enabled",
			err: "app.service is still running after stop"},
		{name: "disable that didn't disable", load: "loaded", active: "active", file: "enabled", lie: "disable", op: model.ServiceOp{Enabled: &off}, verbs: "disable", after: "active/enabled",
			err: "unit file state is enabled after disable"},
		{name: "start that didn't start", load: "loaded", active: "inactive", file: "disabled", lie: "start", op: model.ServiceOp{State: "running"}, verbs: "start", after: "inactive/disabled",
			err: "app.service failed to start"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := &systemdHost{load: c.load, active: c.active, file: c.file, failVerb: c.fail, lieVerb: c.lie}
			op := c.op
			op.Name, op.TimeoutS = "app", 30
			res, err := Host{R: h}.Service(context.Background(), &op, discard{}, func() {})
			if got := strings.Join(h.verbs, " "); got != c.verbs {
				t.Errorf("systemctl ran %q, want %q", got, c.verbs)
			}
			if got := h.active + "/" + h.file; got != c.after {
				t.Errorf("afterwards %s, want %s", got, c.after)
			}
			if c.err == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				after, _ := res.Data["after"].(UnitState)
				if after.Active+"/"+after.UnitFile != c.after {
					t.Errorf("reported after %+v, want %s", after, c.after)
				}
				return
			}
			var oe *model.OpError
			if !errors.As(err, &oe) || oe.Class != model.ClassService || !strings.Contains(oe.Message, c.err) {
				t.Fatalf("error %v, want a service error with %q", err, c.err)
			}
		})
	}
}
