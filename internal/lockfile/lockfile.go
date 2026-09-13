// Package lockfile provides PID-based file locking using syscall.Flock.
package lockfile

import (
	"fmt"
	"os"

	"github.com/pspenano/reel/internal/safefs"
	"path/filepath"
	"syscall"
	"time"
)

// Lock represents a held file lock.
type Lock struct {
	path string
	f    *os.File
}

// AcquireExclusive acquires an exclusive lock with a 5-second timeout.
// Returns an error if another process holds the lock.
func AcquireExclusive(path string) (*Lock, error) {
	return acquire(path, syscall.LOCK_EX, 5*time.Second)
}

// AcquireShared acquires a shared lock with a 5-second timeout.
func AcquireShared(path string) (*Lock, error) {
	return acquire(path, syscall.LOCK_SH, 5*time.Second)
}

func acquire(path string, how int, timeout time.Duration) (*Lock, error) {
	d, err := safefs.OpenDir(filepath.Dir(path), false)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	if err := safefs.SupportedFS(d); err != nil {
		return nil, err
	}
	f, err := safefs.OpenAt(d, filepath.Base(path), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("lockfile open %s: %w", path, err)
	}

	deadline := time.Now().Add(timeout)
	for {
		err = syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK {
			f.Close()
			return nil, fmt.Errorf("flock %s: %w", path, err)
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("another reel process is running")
		}
		time.Sleep(100 * time.Millisecond)
	}

	return &Lock{path: path, f: f}, nil
}

// Release releases the descriptor; the lockfile is permanent.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	closeErr := l.f.Close()
	l.f = nil
	if err != nil {
		return err
	}
	return closeErr
}
