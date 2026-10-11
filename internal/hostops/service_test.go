package hostops

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"drool/internal/hostexec"
	"drool/internal/model"
)

func TestClassifySystemctlWithoutSystemd(t *testing.T) {
	for _, out := range []string{
		"System has not been booted with systemd as init system (PID 1). Can't operate.\nFailed to connect to bus: Host is down",
		"bash: line 1: systemctl: command not found",
		"sh: 1: systemctl: not found",
	} {
		if e := classifySystemctl(out); e == nil || e.Class != model.ClassConfiguration {
			t.Errorf("%q: got %v, want ConfigurationError", out, e)
		}
	}
	if e := classifySystemctl("LoadState=loaded\n"); e != nil {
		t.Errorf("normal output classified as %v", e)
	}
}

// unitHost is a fake host whose `systemctl show` answers come from shows,
// in order (the last one repeats); every other command succeeds.
type unitHost struct {
	shows []string
	cmds  []string
}

func (u *unitHost) Run(_ context.Context, cmd string, _ hostexec.RunOptions) (*hostexec.RunResult, error) {
	u.cmds = append(u.cmds, cmd)
	if !strings.Contains(cmd, "systemctl show") {
		return &hostexec.RunResult{}, nil
	}
	out := u.shows[0]
	if len(u.shows) > 1 {
		u.shows = u.shows[1:]
	}
	return &hostexec.RunResult{Stdout: []byte(out)}, nil
}

func (u *unitHost) Start(string, []byte, io.Writer, io.Writer, bool) (hostexec.Process, error) {
	return nil, errors.New("not used")
}

type discard struct{}

func (discard) Stdout() io.Writer { return io.Discard }
func (discard) Stderr() io.Writer { return io.Discard }

func unitShow(active, sub string) string {
	return "LoadState=loaded\nActiveState=" + active + "\nSubState=" + sub + "\nUnitFileState=disabled\nCanReload=no\n"
}

// A Type=simple unit that exits right after `systemctl start` is active at
// the first look: DROOL looks again and reports the failure (M17).
func TestServiceStartedUnitThatExitsRightAway(t *testing.T) {
	old := StartCheck
	StartCheck = 10 * time.Millisecond
	t.Cleanup(func() { StartCheck = old })
	run := func(state string, shows ...string) (*unitHost, error) {
		u := &unitHost{shows: shows}
		_, err := Host{R: u}.Service(context.Background(), &model.ServiceOp{Name: "app", State: state, TimeoutS: 30}, discard{}, func() {})
		return u, err
	}

	_, err := run("running", unitShow("inactive", "dead"), unitShow("active", "running"), unitShow("failed", "failed"))
	var oe *model.OpError
	if !errors.As(err, &oe) || oe.Class != model.ClassService {
		t.Fatalf("a unit that exited right after starting: %v", err)
	}
	_, err = run("restarted", unitShow("active", "running"), unitShow("active", "running"), unitShow("activating", "auto-restart"))
	if !errors.As(err, &oe) || oe.Class != model.ClassService {
		t.Fatalf("a unit restarting itself right after a restart: %v", err)
	}
	if _, err := run("running", unitShow("inactive", "dead"), unitShow("active", "running")); err != nil {
		t.Fatalf("a unit that keeps running: %v", err)
	}
	// Nothing started, nothing to wait for.
	u, err := run("running", unitShow("active", "running"))
	if err != nil || len(u.cmds) != 1 {
		t.Fatalf("an already running unit: %v, commands %q", err, u.cmds)
	}
}
