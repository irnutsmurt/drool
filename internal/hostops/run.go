package hostops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"drool/internal/driver"
	"drool/internal/hostexec"
	"drool/internal/model"
)

// Remote execution conventions (ARCHITECTURE-ALPHA §9, decision D5).
//
//  1. prepare (as the login user): create a private run directory with
//     mktemp -d under $XDG_RUNTIME_DIR, /run/user/$UID or $TMPDIR//tmp, and
//     write the script into it from stdin. No predictable names.
//  2. run (with privilege escalation if requested): setsid -w starts the
//     script in its own process group and records the group id in
//     "$dir/pid"; stdin carries only the sudo password, if any.
//  3. on cancel/timeout: a separate session kills the process group
//     (TERM, then KILL after 10 s).
//  4. cleanup: remove the run directory.
const prepareScript = `set -eu
base=""
for b in "${XDG_RUNTIME_DIR:-}" "/run/user/$(id -u)"; do
  if [ -n "$b" ] && [ -d "$b" ] && [ ! -L "$b" ] && [ -O "$b" ] && [ -w "$b" ]; then base=$b; break; fi
done
if [ -z "$base" ]; then base=${TMPDIR:-/tmp}; fi
d=$(mktemp -d "$base/drool.XXXXXXXXXX")
chmod 700 "$d"
cat > "$d/script"
printf '%s\n' "$d"`

var runDirRE = regexp.MustCompile(`^/[A-Za-z0-9._/-]+/drool\.[A-Za-z0-9]{10}$`)

// shQuote single-quotes s for POSIX sh.
func ShQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// StreamResult is the outcome of RunStreamed.
type StreamResult struct {
	Exit int
	Head string // first 4 KiB of stderr
	Tail string // last 16 KiB of stdout+stderr
}

// runStreamed runs body under the D5 run-directory protocol, streaming
// output to out. Failures before the command starts are NotStartedErrors,
// except a start that may have reached the host (hostexec.MaybeStartedError);
// cancellation and timeouts terminate the remote process group.
func RunStreamed(ctx context.Context, c hostexec.Runner, b Become, interp, body string, out driver.OutputSink, started func()) (*StreamResult, error) {
	dir, err := prepare(ctx, c, body)
	if err != nil {
		return nil, &driver.NotStartedError{Err: err}
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		c.Run(cctx, "rm -rf -- "+ShQuote(dir), hostexec.RunOptions{})
	}()

	run := fmt.Sprintf(`setsid -w sh -c 'echo $$ > "$1/pid"; exec %s "$1/script"' drool-run %s`, interp, ShQuote(dir))
	cmd, stdin := b.Wrap(run)
	head := &headCapture{max: 4096}
	tail := &tailCapture{max: 16 << 10}
	r, err := c.Start(ctx, cmd, stdin, io.MultiWriter(out.Stdout(), tail), io.MultiWriter(out.Stderr(), head, tail), b.PTY)
	if hostexec.MaybeStarted(err) {
		return nil, err // it may be running: never re-run as if it hadn't started
	}
	if err != nil {
		return nil, &driver.NotStartedError{Err: err}
	}
	if started != nil {
		started()
	}
	select {
	case <-r.Done():
	case <-ctx.Done():
		kill(c, b, dir)
		select {
		case <-r.Done():
		case <-time.After(15 * time.Second):
		}
		r.Close()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, &model.OpError{Class: model.ClassTimeout, Message: "timed out; the remote process group was terminated"}
		}
		return nil, &model.OpError{Class: model.ClassCancelled, Message: "cancelled; the remote process group was terminated"}
	}
	code, err := r.ExitCode()
	if err != nil {
		return nil, err
	}
	return &StreamResult{Exit: code, Head: head.String(), Tail: tail.String()}, nil
}

func prepare(ctx context.Context, c hostexec.Runner, body string) (string, error) {
	pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := c.Run(pctx, "sh -c "+ShQuote(prepareScript), hostexec.RunOptions{Stdin: []byte(body), MaxOutput: 4096})
	if err != nil {
		return "", err
	}
	dir := strings.TrimSpace(string(res.Stdout))
	if res.ExitCode != 0 || !runDirRE.MatchString(dir) {
		return "", &model.OpError{Class: model.ClassConfiguration,
			Message: "could not create a private run directory on the host: " + strings.TrimSpace(string(res.Stderr)),
			Hint:    "The host needs a writable /tmp (or XDG_RUNTIME_DIR) and coreutils mktemp."}
	}
	return dir, nil
}

// killScript must work in dash (Debian/Ubuntu /bin/sh): its kill builtin
// rejects "--" before a negative process group id, so use "kill -SIG -pgid".
const killScript = `p=$(cat "$1/pid" 2>/dev/null) || exit 0
case $p in ''|*[!0-9]*) exit 0;; esac
kill -TERM "-$p" 2>/dev/null || exit 0
i=0
while kill -0 "-$p" 2>/dev/null && [ $i -lt 10 ]; do sleep 1; i=$((i+1)); done
kill -KILL "-$p" 2>/dev/null
exit 0`

// kill terminates the run's process group from a separate session.
func kill(c hostexec.Runner, b Become, dir string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd, stdin := b.Wrap("sh -c " + ShQuote(killScript) + " drool-kill " + ShQuote(dir))
	c.Run(ctx, cmd, hostexec.RunOptions{Stdin: stdin, PTY: b.PTY})
}

func LooksLikeSudoFailure(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.HasPrefix(strings.TrimSpace(s), "sudo:") || strings.Contains(s, "\nsudo:")
}

// tailCapture keeps the last max bytes written.
type tailCapture struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailCapture) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-t.max:]...)
	}
	return len(p), nil
}

func (t *tailCapture) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// headCapture keeps the first max bytes written.
type headCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
}

func (h *headCapture) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if room := h.max - h.buf.Len(); room > 0 {
		if len(p) > room {
			h.buf.Write(p[:room])
		} else {
			h.buf.Write(p)
		}
	}
	return len(p), nil
}

func (h *headCapture) String() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.buf.String()
}

// Shell runs a command or script body with interp. With become, it runs
// through b (sudo failures are classified as AuthorizationError).
func Shell(ctx context.Context, c hostexec.Runner, b Become, interp, body string, out driver.OutputSink, started func()) (*driver.OpResult, error) {
	r, err := RunStreamed(ctx, c, b, interp, body, out, started)
	if err != nil {
		return nil, err
	}
	res := &driver.OpResult{ExitCode: &r.Exit}
	if r.Exit == 0 {
		return res, nil
	}
	if b.Method == "sudo" && r.Exit == 1 && LooksLikeSudoFailure(r.Head) {
		detail, hint := ClassifySudo(r.Head)
		return res, &model.OpError{Class: model.ClassAuthorization, Message: detail, Hint: hint}
	}
	return res, &model.OpError{Class: model.ClassExecution, Message: fmt.Sprintf("exited with status %d", r.Exit)}
}
