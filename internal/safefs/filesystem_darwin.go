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
	if name != "apfs" && name != "exfat" {
		return fmt.Errorf("unsupported filesystem %s; APFS or exFAT required", name)
	}
	return nil
}
