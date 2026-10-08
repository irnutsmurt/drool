package source

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Local reads the machine the agent runs on.
//
// Helpers are bounded: one at a time by default (Slots), a timeout,
// bounded output, their own process group killed as a whole when they
// overrun, so no child of a helper outlives it.
type Local struct {
	// Root prefixes every path (tests: a fixture tree). "" is /.
	Root string
	// Path is the search path for helpers (default: $PATH plus sbin dirs).
	Path string
	// Timeout per helper (default 30s); MaxOutput per stream (default 1 MiB).
	Timeout   time.Duration
	MaxOutput int
	// Slots bounds concurrent helpers (default 1).
	Slots int

	once sync.Once
	sem  chan struct{}
	mu   sync.Mutex
	hung map[string]time.Time // mounts whose statfs never returned
	look map[string]string
}

func (l *Local) init() {
	l.once.Do(func() {
		if l.Timeout <= 0 {
			l.Timeout = 30 * time.Second
		}
		if l.MaxOutput <= 0 {
			l.MaxOutput = 1 << 20
		}
		if l.Slots <= 0 {
			l.Slots = 1
		}
		if l.Path == "" {
			l.Path = os.Getenv("PATH") + ":/usr/local/sbin:/usr/sbin:/sbin"
		}
		l.sem = make(chan struct{}, l.Slots)
		l.hung = map[string]time.Time{}
		l.look = map[string]string{}
	})
}

func (l *Local) real(p string) string {
	if l.Root == "" {
		return p
	}
	return filepath.Join(l.Root, p)
}

// ReadFile implements Source.
func (l *Local) ReadFile(p string) ([]byte, error) {
	f, err := os.Open(l.real(p))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, MaxFile))
}

// Glob implements Source.
func (l *Local) Glob(pattern string) []string {
	m, _ := filepath.Glob(l.real(pattern))
	if l.Root == "" {
		return m
	}
	for i := range m {
		m[i] = strings.TrimPrefix(m[i], l.Root)
	}
	return m
}

// Link implements Source.
func (l *Local) Link(p string) string {
	fi, err := os.Lstat(l.real(p))
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return ""
	}
	t, err := os.Readlink(l.real(p))
	if err != nil {
		return ""
	}
	if !filepath.IsAbs(t) {
		t = filepath.Join(filepath.Dir(p), t)
	} else if l.Root != "" {
		t = strings.TrimPrefix(t, l.Root)
	}
	return filepath.Clean(t)
}

// Statfs implements Source. A filesystem whose statfs never returns (a
// dead device) is reported unresponsive and not asked again until the
// stuck call finishes, so hung calls can't pile up.
func (l *Local) Statfs(ctx context.Context, mount string) (*Statfs, error) {
	l.init()
	l.mu.Lock()
	if _, stuck := l.hung[mount]; stuck {
		l.mu.Unlock()
		return nil, ErrUnresponsive
	}
	l.mu.Unlock()
	type res struct {
		st  syscall.Statfs_t
		err error
	}
	ch := make(chan res, 1)
	go func() {
		var r res
		r.err = syscall.Statfs(l.real(mount), &r.st)
		ch <- r
		l.mu.Lock()
		delete(l.hung, mount)
		l.mu.Unlock()
	}()
	t := time.NewTimer(5 * time.Second)
	defer t.Stop()
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		return &Statfs{Blocks: r.st.Blocks, Bfree: r.st.Bfree, Bavail: r.st.Bavail, Bsize: uint64(r.st.Bsize),
			Files: r.st.Files, Ffree: r.st.Ffree}, nil
	case <-t.C:
		l.mu.Lock()
		l.hung[mount] = time.Now()
		l.mu.Unlock()
		return nil, ErrUnresponsive
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ErrUnresponsive: a filesystem didn't answer in time.
var ErrUnresponsive = errors.New("the filesystem did not respond")

func (l *Local) lookPath(tool string) string {
	l.init()
	l.mu.Lock()
	defer l.mu.Unlock()
	if p, ok := l.look[tool]; ok {
		return p
	}
	p := ""
	for _, d := range filepath.SplitList(l.Path) {
		c := filepath.Join(d, tool)
		if fi, err := os.Stat(c); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			p = c
			break
		}
	}
	l.look[tool] = p
	return p
}

// Rescan forgets which helpers were found (after an installation).
func (l *Local) Rescan() {
	l.init()
	l.mu.Lock()
	l.look = map[string]string{}
	l.mu.Unlock()
}

// Has implements Source.
func (l *Local) Has(tool string) bool { return l.lookPath(tool) != "" }

type capWriter struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	room := w.max - w.buf.Len()
	if room <= 0 {
		w.truncated = w.truncated || len(p) > 0
		return len(p), nil
	}
	if len(p) > room {
		w.buf.Write(p[:room])
		w.truncated = true
		return len(p), nil
	}
	return w.buf.Write(p)
}

// Run implements Source.
func (l *Local) Run(ctx context.Context, tool string, args ...string) (*Result, error) {
	if err := CheckArgs(tool, args); err != nil {
		return nil, err
	}
	p := l.lookPath(tool)
	if p == "" {
		return nil, ErrNotFound
	}
	select {
	case l.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-l.sem }()
	ctx, cancel := context.WithTimeout(ctx, l.Timeout)
	defer cancel()
	c := exec.Command(p, args...)
	c.Env = []string{"PATH=" + l.Path, "LC_ALL=C", "LANG=C"}
	c.Stdin = nil
	out, errw := &capWriter{max: l.MaxOutput}, &capWriter{max: 64 << 10}
	c.Stdout, c.Stderr = out, errw
	// Own process group: a timeout kills the helper and every child.
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	r := &Result{}
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		err = <-done
		r.TimedOut = true
	}
	// Children that outlived the helper itself (daemonised grandchildren)
	// are in its process group too.
	syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	r.Stdout, r.Stderr, r.Truncated = out.buf.Bytes(), errw.buf.Bytes(), out.truncated
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		r.Exit = ee.ExitCode()
		if r.Exit < 0 {
			r.Exit = 128 + int(ee.Sys().(syscall.WaitStatus).Signal())
		}
	default:
		return nil, err
	}
	if r.TimedOut {
		return r, context.DeadlineExceeded
	}
	return r, nil
}
