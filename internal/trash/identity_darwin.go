package trash

import (
	"golang.org/x/sys/unix"
	"os"
)

type fileIdentity struct {
	Inode        uint64 `json:"inode"`
	BirthSeconds int64  `json:"birth_seconds"`
	BirthNanos   int64  `json:"birth_nanos"`
}

func identityOf(f *os.File) (fileIdentity, error) {
	var st unix.Stat_t
	err := unix.Fstat(int(f.Fd()), &st)
	return fileIdentity{st.Ino, st.Btim.Sec, st.Btim.Nsec}, err
}
