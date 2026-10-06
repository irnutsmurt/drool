package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// LockFileName is held (flock) by a running controller.
const LockFileName = "drool.lock"

// ErrLocked means another process (normally a running controller) holds the
// data directory lock.
var ErrLocked = errors.New("the data directory is in use by a running DROOL controller")

// LockDataDir takes an exclusive, non-blocking lock on the data directory.
// `serve` holds it for its lifetime; `restore` needs it so it never replaces
// files under a running controller. The lock is released when the process
// exits, even on a crash.
func LockDataDir(dir string) (release func(), err error) {
	f, err := os.OpenFile(filepath.Join(dir, LockFileName), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("lock data directory: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
