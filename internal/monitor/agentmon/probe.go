package agentmon

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func uintptrOf(b []byte) uintptr { return uintptr(unsafe.Pointer(&b[0])) }

// The flash probe writes a tiny unpredictable value to the filesystem it
// checks, flushes it to the device, drops it from the page cache and
// reads it back from the device. It is the only routine monitoring
// function that writes to a monitored filesystem, and it writes one
// 512-byte file in a directory DROOL creates and owns:
//
//	/var/lib/drool-agent/probe       when that is on the probed filesystem
//	<mount>/.drool-agent-probe       otherwise
//
// It never touches anything else. It is a smoke test, not a benchmark,
// and a pass is one signal: it doesn't prove every flash block healthy.

// ProbeSize is the probe file's size.
const ProbeSize = 512

// ProbeResult is one probe run.
type ProbeResult struct {
	Mount    string `json:"mount"`
	At       int64  `json:"at"`
	OK       bool   `json:"ok"`
	Stage    string `json:"stage,omitempty"` // where it failed: location | write | sync | read | verify | timeout
	Error    string `json:"error,omitempty"`
	Direct   bool   `json:"direct"` // read back with O_DIRECT (from the device, not the cache)
	Millis   int64  `json:"ms"`
	Location string `json:"location,omitempty"`
}

// probeOps are the filesystem operations (tests inject failures).
type probeOps struct {
	write func(f *os.File, b []byte) error
	sync  func(f *os.File) error
	read  func(path string) ([]byte, bool, error)
}

var defaultOps = probeOps{
	write: func(f *os.File, b []byte) error {
		n, err := f.WriteAt(b, 0)
		if err == nil && n != len(b) {
			err = errors.New("short write")
		}
		return err
	},
	sync: func(f *os.File) error {
		if err := unix.Fdatasync(int(f.Fd())); err != nil {
			return err
		}
		// Clean pages can now be dropped so the read comes from the device.
		unix.Fadvise(int(f.Fd()), 0, 0, unix.FADV_DONTNEED)
		return nil
	},
	read: readBack,
}

// readBack reads the probe file, with O_DIRECT where the filesystem
// supports it (bypassing the page cache), otherwise normally (the
// pages were just dropped).
func readBack(path string) ([]byte, bool, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err == nil {
		defer unix.Close(fd)
		// O_DIRECT needs an aligned buffer: take a 4 KiB-aligned window.
		raw := make([]byte, 8192)
		off := 0
		if a := int(uintptrOf(raw)) & 4095; a != 0 {
			off = 4096 - a
		}
		buf := raw[off : off+4096]
		n, err := unix.Pread(fd, buf, 0)
		if err == nil {
			return append([]byte(nil), buf[:n]...), true, nil
		}
		if !errors.Is(err, unix.EINVAL) {
			return nil, true, err
		}
	} else if !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.EOPNOTSUPP) {
		return nil, false, err
	}
	b, err := os.ReadFile(path)
	return b, false, err
}

// Prober runs probes; at most one at a time per mount, so a device
// whose IO hangs can't accumulate stuck goroutines.
type Prober struct {
	StateDir string // the agent's own directory (/var/lib/drool-agent)
	Timeout  time.Duration
	ops      probeOps
	mu       sync.Mutex
	running  map[string]bool
}

// NewProber returns a prober.
func NewProber(stateDir string) *Prober {
	return &Prober{StateDir: stateDir, Timeout: 30 * time.Second, ops: defaultOps, running: map[string]bool{}}
}

func devOf(p string) (uint64, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(p, &st); err != nil {
		return 0, err
	}
	return uint64(st.Dev), nil
}

// location picks and prepares the DROOL-owned probe directory on mount.
func (p *Prober) location(mount string) (string, error) {
	mdev, err := devOf(mount)
	if err != nil {
		return "", err
	}
	var dir string
	if p.StateDir != "" {
		if d, err := devOf(p.StateDir); err == nil && d == mdev {
			dir = filepath.Join(p.StateDir, "probe")
		}
	}
	if dir == "" {
		dir = filepath.Join(mount, ".drool-agent-probe")
	}
	fi, err := os.Lstat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.Mkdir(dir, 0o700); err != nil {
			return "", err
		}
		if f, err := os.Open(filepath.Dir(dir)); err == nil {
			f.Sync() // the new directory entry is durable before the first probe
			f.Close()
		}
	case err != nil:
		return "", err
	case fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir():
		return "", fmt.Errorf("%s exists and is not a directory DROOL created; refusing to write there", dir)
	default:
		st := fi.Sys().(*syscall.Stat_t)
		if int(st.Uid) != os.Getuid() || fi.Mode().Perm()&0o077 != 0 {
			return "", fmt.Errorf("%s is not owned by the agent with mode 0700; refusing to write there", dir)
		}
	}
	if d, err := devOf(dir); err != nil || d != mdev {
		return "", fmt.Errorf("%s is not on the filesystem being probed", dir)
	}
	return dir, nil
}

// Probe checks one mount. It returns quickly with a "timeout" result if
// the device doesn't answer; a probe still hung from before fails at once.
func (p *Prober) Probe(mount string, now time.Time) ProbeResult {
	res := ProbeResult{Mount: mount, At: now.Unix()}
	p.mu.Lock()
	if p.running[mount] {
		p.mu.Unlock()
		res.Stage, res.Error = "timeout", "the previous probe of this filesystem never finished (the device may be hung)"
		return res
	}
	p.running[mount] = true
	p.mu.Unlock()
	done := make(chan ProbeResult, 1)
	in := res
	go func() {
		r := p.run(mount, in)
		p.mu.Lock()
		delete(p.running, mount)
		p.mu.Unlock()
		done <- r
	}()
	select {
	case r := <-done:
		return r
	case <-time.After(p.Timeout):
		res.Stage, res.Error = "timeout", fmt.Sprintf("no answer from the device within %s", p.Timeout)
		return res
	}
}

func (p *Prober) run(mount string, in ProbeResult) (res ProbeResult) {
	res = in
	start := time.Now()
	defer func() { res.Millis = time.Since(start).Milliseconds() }()
	dir, err := p.location(mount)
	if err != nil {
		res.Stage, res.Error = "location", err.Error()
		return res
	}
	res.Location = dir
	path := filepath.Join(dir, "probe")
	want := make([]byte, ProbeSize)
	rand.Read(want)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		res.Stage, res.Error = "write", err.Error()
		return res
	}
	err = p.ops.write(f, want)
	if err == nil {
		err = f.Truncate(ProbeSize)
	}
	if err != nil {
		f.Close()
		res.Stage, res.Error = "write", err.Error()
		return res
	}
	if err := p.ops.sync(f); err != nil {
		f.Close()
		res.Stage, res.Error = "sync", "flushing to the device failed: "+err.Error()
		return res
	}
	f.Close()
	got, direct, err := p.ops.read(path)
	res.Direct = direct
	if err != nil {
		res.Stage, res.Error = "read", err.Error()
		return res
	}
	if !bytes.Equal(got, want) {
		res.Stage, res.Error = "verify", fmt.Sprintf("read back %d bytes that differ from what was written", len(got))
		return res
	}
	res.OK = true
	return res
}
