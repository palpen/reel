package safefs

import (
	"golang.org/x/sys/unix"
	"os"
)

// DurableSync requests both filesystem writeback and a device cache flush.
// Unsupported flushes fail closed; recovery must not remove the source on error.
func DurableSync(f *os.File) error {
	if err := f.Sync(); err != nil {
		return err
	}
	_, err := unix.FcntlInt(f.Fd(), unix.F_FULLFSYNC, 0)
	return err
}
