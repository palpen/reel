// Package transfer implements the copy engine with hash verification.
package transfer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/pspenano/reel/internal/fault"
	"github.com/pspenano/reel/internal/lockfile"
	"github.com/pspenano/reel/internal/safefs"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/pspenano/reel/internal/display"
)

const bufSize = 1024 * 1024 // 1MB

// Result holds the outcome of a single file copy.
type Result struct {
	DestPath string
	SHA256   string
	Bytes    int64
}

// Copy copies src to destDir/<filename>, computing SHA-256 during the copy.
// The file is written to a .tmp path, fsynced, then renamed atomically.
// The destination mtime is set to recordedAt.
// If expectedSHA256 is non-empty, the computed hash is compared and an error returned on mismatch.
func Copy(src, destDir, filename string, recordedAt time.Time, expectedSHA256 string) (*Result, error) {
	return CopyChecked(src, destDir, filename, recordedAt, expectedSHA256, nil)
}

// CopyChecked revalidates command-owned volume identities across publication.
func CopyChecked(src, destDir, filename string, recordedAt time.Time, expectedSHA256 string, validate func() error) (*Result, error) {
	if validate != nil {
		if err := validate(); err != nil {
			return nil, err
		}
	}
	if err := safefs.Name(filename); err != nil {
		return nil, err
	}
	in, err := safefs.Open(src)
	if err != nil {
		return nil, err
	}
	defer in.Close()
	before, err := in.Stat()
	if err != nil {
		return nil, err
	}
	dir, err := safefs.OpenDir(destDir, true)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	lk, err := lockfile.AcquireExclusive(filepath.Join(destDir, "reel.lock"))
	if err != nil {
		return nil, err
	}
	defer lk.Release()
	computed, n, err := hashHandle(in)
	if err != nil {
		return nil, err
	}
	if expectedSHA256 != "" && computed != expectedSHA256 {
		return nil, fmt.Errorf("canonical hash conflict: %s", src)
	}
	dest := filepath.Join(destDir, filename)
	result := &Result{DestPath: dest, SHA256: computed, Bytes: n}
	existing, err := safefs.OpenAt(dir, filename, unix.O_RDONLY, 0)
	if err == nil {
		defer existing.Close()
		info, e := existing.Stat()
		if e != nil {
			return nil, e
		}
		if os.SameFile(before, info) {
			return nil, fmt.Errorf("source/destination alias: %s", dest)
		}
		h, size, e := hashHandle(existing)
		if e != nil {
			return nil, e
		}
		if h != computed || size != n {
			return nil, fmt.Errorf("destination collision: %s", dest)
		}
		if err = stable(in, before, computed); err != nil {
			return nil, err
		}
		if validate != nil {
			if err = validate(); err != nil {
				return nil, err
			}
		}
		return result, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("destination conflict: %w", err)
	}
	if err = checkFreeSpace(src, destDir); err != nil {
		return nil, err
	}
	if err = fault.Check("stage-create"); err != nil {
		return nil, err
	}
	if err = safefs.CheckExclusive(dir); err != nil {
		return nil, err
	}
	out, err := safefs.Fresh(dir, ".reel-stage-")
	if err != nil {
		return nil, err
	}
	defer out.Close()
	// Intent precedes copying. Staging and intent are deliberately retained on failure.
	if err = fault.Check("transfer-intent"); err != nil {
		return nil, err
	}
	journal, err := safefs.Fresh(dir, ".reel-transfer-")
	if err != nil {
		return nil, err
	}
	data, _ := json.Marshal(map[string]any{"version": 1, "source": src, "destination": dest, "stage": out.Name(), "sha256": computed, "size": n})
	if _, err = journal.Write(append(data, '\n')); err != nil {
		journal.Close()
		return nil, err
	}
	if err = journal.Sync(); err != nil {
		journal.Close()
		return nil, err
	}
	if err = journal.Close(); err != nil {
		return nil, err
	}
	if err = dir.Sync(); err != nil {
		return nil, err
	}
	if _, err = in.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	copied, err := io.CopyBuffer(faultWriter{out}, in, make([]byte, bufSize))
	if err != nil {
		return nil, fmt.Errorf("partial retained at %s: %w", out.Name(), err)
	}
	if copied != n {
		return nil, fmt.Errorf("source size changed; stage retained at %s", out.Name())
	}
	if !recordedAt.IsZero() {
		tv := unix.NsecToTimeval(recordedAt.UnixNano())
		if err = unix.Futimes(int(out.Fd()), []unix.Timeval{tv, tv}); err != nil {
			return nil, err
		}
	}
	if err = fault.Check("stage-sync"); err != nil {
		return nil, err
	}
	if err = out.Sync(); err != nil {
		return nil, err
	}
	stageInfo, err := out.Stat()
	if err != nil {
		return nil, err
	}
	if err = fault.Check("stage-close"); err != nil {
		return nil, err
	}
	if err = out.Close(); err != nil {
		return nil, err
	}
	staged, err := safefs.OpenAt(dir, filepath.Base(out.Name()), unix.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer staged.Close()
	h, size, err := hashHandle(staged)
	if err != nil {
		return nil, err
	}
	if h != computed || size != n {
		return nil, fmt.Errorf("staged destination verification failed: %s", out.Name())
	}
	if err = stable(in, before, computed); err != nil {
		return nil, err
	}
	if err = safefs.CheckDir(dir); err != nil {
		return nil, err
	}
	if err = fault.Check("publication"); err != nil {
		return nil, err
	}
	if validate != nil {
		if err = validate(); err != nil {
			return nil, err
		}
	}
	if err = safefs.RenameExclusive(dir, filepath.Base(out.Name()), dir, filename); err != nil {
		if errors.Is(err, os.ErrExist) {
			winner, e := safefs.OpenAt(dir, filename, unix.O_RDONLY, 0)
			if e != nil {
				return nil, e
			}
			h, size, e := hashHandle(winner)
			winner.Close()
			if e != nil {
				return nil, e
			}
			if h == computed && size == n {
				if e = stable(in, before, computed); e != nil {
					return nil, e
				}
				if validate != nil {
					if e = validate(); e != nil {
						return nil, e
					}
				}
				return result, nil
			}
		}
		return nil, fmt.Errorf("exclusive publication failed; stage retained at %s: %w", out.Name(), err)
	}
	published, err := safefs.OpenAt(dir, filename, unix.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer published.Close()
	info, err := published.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(stageInfo, info) {
		return nil, fmt.Errorf("published identity changed: %s", dest)
	}
	if err = stable(published, info, computed); err != nil {
		return nil, err
	}
	if err = dir.Sync(); err != nil {
		return nil, err
	}
	if err = safefs.CheckDir(dir); err != nil {
		return nil, err
	}
	if validate != nil {
		if err = validate(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func hashHandle(f *os.File) (string, int64, error) {
	before, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return "", 0, err
	}
	h := sha256.New()
	n, err := io.CopyBuffer(h, f, make([]byte, bufSize))
	if err != nil {
		return "", n, err
	}
	after, err := f.Stat()
	if err != nil {
		return "", n, err
	}
	if !os.SameFile(before, after) || before.Size() != n || after.Size() != n || !before.ModTime().Equal(after.ModTime()) {
		return "", n, fmt.Errorf("file changed while hashing: %s", f.Name())
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
func stable(f *os.File, before os.FileInfo, expected string) error {
	after, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return fmt.Errorf("file changed: %s", f.Name())
	}
	h, _, err := hashHandle(f)
	if err != nil {
		return err
	}
	if h != expected {
		return fmt.Errorf("content changed: %s", f.Name())
	}
	return nil
}

// HashFile independently hashes a regular file without following path aliases.
func HashFile(path string) (string, int64, error) {
	f, err := safefs.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	return hashHandle(f)
}

// checkFreeSpace verifies that destDir has enough free space to hold srcFile.
func checkFreeSpace(srcPath, destDir string) error {
	info, err := os.Stat(srcPath)
	if err != nil {
		return fmt.Errorf("stat src: %w", err)
	}
	return PreflightSpace(destDir, info.Size())
}

// FreeBytes returns the available bytes on the filesystem containing dir.
// Returns an error if statfs fails (caller should treat as "unknown").
func FreeBytes(dir string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", dir, err)
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}

// PreflightSpace returns a descriptive error if destDir has less than needed bytes free.
// If free space cannot be determined (statfs fails), it returns nil — the per-file
// check inside Copy is the safety net.
func PreflightSpace(destDir string, needed int64) error {
	avail, err := FreeBytes(destDir)
	if err != nil {
		return nil
	}
	if avail < needed {
		return fmt.Errorf("insufficient free space at %s: need %s, have %s free",
			destDir, display.Bytes(needed), display.Bytes(avail))
	}
	return nil
}

// SweepOrphanTmps reports legacy partials without modifying them.
// Deprecated: no extension establishes ownership or permits deletion.
func SweepOrphanTmps(root string) ([]string, error) {
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	var found []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && (filepath.Ext(path) == ".tmp" || strings.HasPrefix(d.Name(), ".reel-stage-") || strings.HasPrefix(d.Name(), ".reel-transfer-")) {
			found = append(found, path)
		}
		return nil
	})
	return found, err
}

// Preflight rejects destination conflicts before the batch publishes any files.
func Preflight(src, dest, expected string) error {
	h, n, err := HashFile(src)
	if err != nil {
		return err
	}
	if expected != "" && h != expected {
		return fmt.Errorf("canonical source conflict: %s", src)
	}
	target, size, err := HashFile(dest)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	a, err := os.Stat(src)
	if err != nil {
		return err
	}
	b, err := os.Stat(dest)
	if err != nil {
		return err
	}
	if os.SameFile(a, b) || h != target || n != size {
		return fmt.Errorf("destination collision: %s", dest)
	}
	return nil
}

type faultWriter struct{ f *os.File }

func (w faultWriter) Write(p []byte) (int, error) {
	n, err := w.f.Write(p)
	if err != nil {
		return n, err
	}
	return n, fault.Check("mid-copy")
}
