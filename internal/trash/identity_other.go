//go:build !darwin

package trash

import (
	"golang.org/x/sys/unix"
	"os"
)

type fileIdentity struct {
	Inode uint64 `json:"inode"`
}

func identityOf(f *os.File) (fileIdentity, error) {
	var st unix.Stat_t
	err := unix.Fstat(int(f.Fd()), &st)
	return fileIdentity{st.Ino}, err
}
