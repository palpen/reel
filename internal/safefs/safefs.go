// Package safefs owns descriptor-relative, no-follow filesystem operations.
package safefs

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func Name(name string) error {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return fmt.Errorf("unsafe filename %q", name)
	}
	return nil
}

// OpenDir walks each component with O_NOFOLLOW. Creation never follows aliases.
func OpenDir(path string, create bool) (*os.File, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	for _, p := range strings.Split(path, string(os.PathSeparator)) {
		if p == ".." {
			return nil, fmt.Errorf("path traversal: %s", path)
		}
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(abs, "/"), "/") {
		if part == "" {
			continue
		}
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if e == unix.ENOENT && create {
			if e = unix.Mkdirat(fd, part, 0700); e == nil || e == unix.EEXIST {
				next, e = unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			}
		}
		unix.Close(fd)
		if e != nil {
			return nil, fmt.Errorf("open directory %s: %w", abs, e)
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), abs), nil
}

func OpenAt(dir *os.File, name string, flags int, mode uint32) (*os.File, error) {
	if err := Name(name); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(int(dir.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, mode)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), filepath.Join(dir.Name(), name))
	info, err := f.Stat()
	var st unix.Stat_t
	if err == nil {
		err = unix.Fstat(fd, &st)
	}
	if err != nil || !info.Mode().IsRegular() || st.Nlink != 1 {
		f.Close()
		return nil, fmt.Errorf("not a regular unaliased file: %s", f.Name())
	}
	return f, nil
}

func Open(path string) (*os.File, error) {
	d, err := OpenDir(filepath.Dir(path), false)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	return OpenAt(d, filepath.Base(path), unix.O_RDONLY, 0)
}

func Fresh(dir *os.File, prefix string) (*os.File, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return OpenAt(dir, prefix+hex.EncodeToString(b), unix.O_RDWR|unix.O_CREAT|unix.O_EXCL, 0600)
}

func RenameExclusive(from *os.File, src string, to *os.File, dst string) error {
	if err := Name(src); err != nil {
		return err
	}
	if err := Name(dst); err != nil {
		return err
	}
	return renameExclusive(int(from.Fd()), src, int(to.Fd()), dst)
}

func Move(src, dst string) error {
	a, err := OpenDir(filepath.Dir(src), false)
	if err != nil {
		return err
	}
	defer a.Close()
	b, err := OpenDir(filepath.Dir(dst), false)
	if err != nil {
		return err
	}
	defer b.Close()
	if err = RenameExclusive(a, filepath.Base(src), b, filepath.Base(dst)); err != nil {
		return err
	}
	if err = b.Sync(); err != nil {
		return err
	}
	return a.Sync()
}

func FreshDir(parent *os.File, prefix string) (*os.File, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	name := prefix + hex.EncodeToString(b)
	if err := unix.Mkdirat(int(parent.Fd()), name, 0700); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), filepath.Join(parent.Name(), name)), nil
}

// CheckDir detects replacement of a retained directory's pathname.
func CheckDir(dir *os.File) error {
	current, err := OpenDir(dir.Name(), false)
	if err != nil {
		return err
	}
	defer current.Close()
	a, err := dir.Stat()
	if err != nil {
		return err
	}
	b, err := current.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(a, b) {
		return fmt.Errorf("directory identity changed: %s", dir.Name())
	}
	return nil
}

// EnsureRecord creates an immutable protocol marker or verifies identical bytes.
func EnsureRecord(dir *os.File, name string, data []byte) error {
	f, err := OpenAt(dir, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0600)
	if err == nil {
		if _, err = f.Write(data); err != nil {
			f.Close()
			return err
		}
		if err = f.Sync(); err != nil {
			f.Close()
			return err
		}
		if err = f.Close(); err != nil {
			return err
		}
		return dir.Sync()
	}
	if !errors.Is(err, os.ErrExist) {
		return err
	}
	f, err = OpenAt(dir, name, unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	got, err := io.ReadAll(io.LimitReader(f, int64(len(data)+1)))
	if err != nil {
		return err
	}
	if !bytes.Equal(got, data) {
		return fmt.Errorf("incompatible protocol marker: %s", f.Name())
	}
	return nil
}

const Protocol = "{\"version\":2,\"media_publication\":\"exclusive\",\"recovery\":\"retain\"}\n"

// OpenBeneath anchors a relative directory walk to one retained filesystem root.
// Every descendant must remain on that root's device.
func OpenBeneath(root *os.File, rel string, create bool) (*os.File, error) {
	if filepath.IsAbs(rel) {
		return nil, fmt.Errorf("absolute descendant path")
	}
	fd, err := unix.Dup(int(root.Fd()))
	if err != nil {
		return nil, err
	}
	var base unix.Stat_t
	if err = unix.Fstat(fd, &base); err != nil {
		unix.Close(fd)
		return nil, err
	}
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		if err = Name(part); err != nil {
			unix.Close(fd)
			return nil, err
		}
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if e == unix.ENOENT && create {
			if e = unix.Mkdirat(fd, part, 0700); e == nil || e == unix.EEXIST {
				next, e = unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			}
		}
		unix.Close(fd)
		if e != nil {
			return nil, e
		}
		fd = next
		var st unix.Stat_t
		if e = unix.Fstat(fd, &st); e != nil {
			unix.Close(fd)
			return nil, e
		}
		if st.Dev != base.Dev {
			unix.Close(fd)
			return nil, fmt.Errorf("unexpected nested mount beneath %s", root.Name())
		}
	}
	return os.NewFile(uintptr(fd), filepath.Join(root.Name(), rel)), nil
}
