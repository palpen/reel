// Package lockfile provides PID-based file locking using syscall.Flock.
package lockfile

import (
	"fmt"
	"os"
	"strconv"
	"strings"
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
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
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
			// Read PID from file
			pid := readPID(f)
			f.Close()
			if pid > 0 {
				return nil, fmt.Errorf("another reel process is running (pid %d)", pid)
			}
			return nil, fmt.Errorf("another reel process is running")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Write our PID
	if how == syscall.LOCK_EX {
		if err := f.Truncate(0); err == nil {
			f.WriteAt([]byte(strconv.Itoa(os.Getpid())), 0)
		}
	}

	return &Lock{path: path, f: f}, nil
}

func readPID(f *os.File) int {
	buf := make([]byte, 32)
	n, _ := f.ReadAt(buf, 0)
	if n == 0 {
		return 0
	}
	s := strings.TrimSpace(string(buf[:n]))
	pid, _ := strconv.Atoi(s)
	return pid
}

// Release releases the lock and removes the lockfile.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	l.f.Close()
	os.Remove(l.path)
	l.f = nil
	return nil
}
