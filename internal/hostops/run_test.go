package hostops

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"drool/internal/driver"
	"drool/internal/hostexec"
)

// Remote scripts run under dash on Debian/Ubuntu. dash's kill builtin rejects
// "--" before a negative process group id, which once left cancelled sudo
// jobs running. Guard against reintroducing that form.
func TestRemoteScriptsAvoidDashIncompatibleKill(t *testing.T) {
	for _, bad := range []string{"kill -- ", "-- \"-$p\"", "kill -s "} {
		if strings.Contains(killScript, bad) {
			t.Errorf("killScript contains %q, which dash rejects", bad)
		}
	}
}

func TestRunDirPattern(t *testing.T) {
	for _, ok := range []string{"/tmp/drool.AbCdEf1234", "/run/user/1000/drool.0123456789"} {
		if !runDirRE.MatchString(ok) {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"", "/tmp/drool.", "/tmp/x; rm -rf /", "relative/drool.AbCdEf1234", "/tmp/drool.AbCdEf1234\n"} {
		if runDirRE.MatchString(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestShQuote(t *testing.T) {
	if got := ShQuote(`a'b`); got != `'a'\''b'` {
		t.Fatalf("ShQuote: %s", got)
	}
}

// startFails prepares a run directory, then fails to start the command.
type startFails struct{ err error }

func (s startFails) Run(_ context.Context, cmd string, _ hostexec.RunOptions) (*hostexec.RunResult, error) {
	return &hostexec.RunResult{Stdout: []byte("/tmp/drool.AbCdEf1234\n")}, nil
}

func (s startFails) Start(context.Context, string, []byte, io.Writer, io.Writer, bool) (hostexec.Process, error) {
	return nil, s.err
}

// A start that failed before the command reached the host may be retried or
// sent the other way; one that may have reached it must not (the command may
// be running).
func TestRunStreamedKeepsAStartThatMayHaveReachedTheHost(t *testing.T) {
	_, err := RunStreamed(context.Background(), startFails{errors.New("open session: EOF")}, Become{}, "sh", "true", discard{}, nil)
	if !driver.IsNotStarted(err) {
		t.Fatalf("a failed session open: %v, want not started", err)
	}
	_, err = RunStreamed(context.Background(), startFails{&hostexec.MaybeStartedError{Err: errors.New("lost")}}, Become{}, "sh", "true", discard{}, nil)
	if driver.IsNotStarted(err) || !hostexec.MaybeStarted(err) {
		t.Fatalf("an unanswered start: %v, want possibly started", err)
	}
}
