package safefs

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
)

func SupportedFS(dir *os.File) error {
	var st unix.Statfs_t
	if err := unix.Fstatfs(int(dir.Fd()), &st); err != nil {
		return err
	}
	name := unix.ByteSliceToString(st.Fstypename[:])
	if name != "apfs" && name != "exfat" && name != "hfs" {
		return fmt.Errorf("unsupported filesystem %s; APFS, HFS+, or exFAT required", name)
	}
	return nil
}

// RecoveryCopies selects an explicit protocol, never an error-triggered fallback.
func RecoveryCopies(dir *os.File) (bool, error) {
	var st unix.Statfs_t
	if err := unix.Fstatfs(int(dir.Fd()), &st); err != nil {
		return false, err
	}
	return unix.ByteSliceToString(st.Fstypename[:]) == "exfat", SupportedFS(dir)
}
