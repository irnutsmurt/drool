// Package hostexec is the minimal interface typed operations need to run
// commands on a host: over SSH (sshx.Client) or locally inside the agent
// (Local). Keeping it this small lets both management paths share one
// implementation of package, update and service operations (hostops).
package hostexec

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"sync"
	"syscall"
)

// RunOptions control Run.
type RunOptions struct {
	Stdin     []byte
	PTY       bool
	MaxOutput int // per stream; 0 = 1 MiB
}

// RunResult is a finished command. A non-zero exit status is not an error.
type RunResult struct {
	Stdout    []byte
	Stderr    []byte
	ExitCode  int
	Truncated bool
}

// Process is a started command.
type Process interface {
	Done() <-chan struct{}
	ExitCode() (int, error)
	Close()
}

// Runner runs shell command lines on a host.
type Runner interface {
	// Run executes cmd (a sh command line) and waits for it. Cancelling
	// ctx stops waiting and tears the command down.
	Run(ctx context.Context, cmd string, opt RunOptions) (*RunResult, error)
	// Start runs cmd, streaming its output, and returns once it started.
	Start(cmd string, stdin []byte, stdout, stderr io.Writer, pty bool) (Process, error)
}

// Local runs commands on this machine with /bin/sh. It is what the agent
// uses: it already runs as root, so Become is never needed.
type Local struct {
	// Env is added to the inherited environment.
	Env []string
	// Wrap prefixes the shell (e.g. systemd-run --scope ... --: the agent
	// runs operations outside its own resource budget). It must exec the
	// command in place, so the process group and exit status are the
	// command's.
	Wrap []string
}

// limitWriter keeps up to max bytes and records truncation.
type limitWriter struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (w *limitWriter) Write(p []byte) (int, error) {
	if room := w.max - w.buf.Len(); room > 0 {
		if len(p) > room {
			w.buf.Write(p[:room])
			w.truncated = true
		} else {
			w.buf.Write(p)
		}
	} else if len(p) > 0 {
		w.truncated = true
	}
	return len(p), nil
}

func (l Local) command(cmd string, stdin []byte) *exec.Cmd {
	args := append(append([]string{}, l.Wrap...), "/bin/sh", "-c", cmd)
	c := exec.Command(args[0], args[1:]...)
	c.Env = append(c.Environ(), l.Env...)
	if stdin != nil {
		c.Stdin = bytes.NewReader(stdin)
	}
	// Own process group, so cancellation reaches children too.
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return c
}

// Run implements Runner.
func (l Local) Run(ctx context.Context, cmd string, opt RunOptions) (*RunResult, error) {
	max := opt.MaxOutput
	if max <= 0 {
		max = 1 << 20
	}
	c := l.command(cmd, opt.Stdin)
	out, errw := &limitWriter{max: max}, &limitWriter{max: max}
	c.Stdout, c.Stderr = out, errw
	if err := c.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		<-done
		return nil, ctx.Err()
	}
	res := &RunResult{Stdout: out.buf.Bytes(), Stderr: errw.buf.Bytes(), Truncated: out.truncated || errw.truncated}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		res.ExitCode = ee.ExitCode()
		if res.ExitCode < 0 {
			res.ExitCode = 128 + int(ee.Sys().(syscall.WaitStatus).Signal())
		}
	default:
		return nil, err
	}
	return res, nil
}

type localProc struct {
	c    *exec.Cmd
	done chan struct{}
	err  error
	once sync.Once
}

func (p *localProc) Done() <-chan struct{} { return p.done }

func (p *localProc) ExitCode() (int, error) {
	<-p.done
	var ee *exec.ExitError
	switch {
	case p.err == nil:
		return 0, nil
	case errors.As(p.err, &ee):
		if code := ee.ExitCode(); code >= 0 {
			return code, nil
		}
		return 128 + int(ee.Sys().(syscall.WaitStatus).Signal()), nil
	}
	return -1, p.err
}

func (p *localProc) Close() {
	p.once.Do(func() {
		select {
		case <-p.done:
		default:
			syscall.Kill(-p.c.Process.Pid, syscall.SIGKILL)
		}
	})
}

// Start implements Runner. pty is ignored: the agent never needs sudo.
func (l Local) Start(cmd string, stdin []byte, stdout, stderr io.Writer, _ bool) (Process, error) {
	c := l.command(cmd, stdin)
	c.Stdout, c.Stderr = stdout, stderr
	if err := c.Start(); err != nil {
		return nil, err
	}
	p := &localProc{c: c, done: make(chan struct{})}
	go func() { p.err = c.Wait(); close(p.done) }()
	return p, nil
}
