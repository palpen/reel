// Package transfer implements the copy engine with hash verification.
package transfer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", destDir, err)
	}

	// Precheck free space
	if err := checkFreeSpace(src, destDir); err != nil {
		return nil, err
	}

	in, err := os.Open(src)
	if err != nil {
		return nil, fmt.Errorf("open src %s: %w", src, err)
	}
	defer in.Close()

	dest := filepath.Join(destDir, filename)
	tmp := dest + ".tmp"

	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("create dest %s: %w", tmp, err)
	}

	h := sha256.New()
	tee := io.TeeReader(in, h)

	buf := make([]byte, bufSize)
	var written int64
	if _, err := io.CopyBuffer(out, tee, buf); err != nil {
		out.Close()
		os.Remove(tmp)
		return nil, fmt.Errorf("copy %s -> %s: %w", src, tmp, err)
	}

	// Get size
	if pos, err := out.Seek(0, io.SeekCurrent); err == nil {
		written = pos
	}

	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(tmp)
		return nil, fmt.Errorf("fsync %s: %w", tmp, err)
	}
	out.Close()

	computed := hex.EncodeToString(h.Sum(nil))

	if expectedSHA256 != "" && computed != expectedSHA256 {
		os.Remove(tmp)
		return nil, fmt.Errorf("hash mismatch for %s: expected %s, got %s", filename, expectedSHA256, computed)
	}

	// Set mtime
	if !recordedAt.IsZero() {
		os.Chtimes(tmp, recordedAt, recordedAt)
	}

	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return nil, fmt.Errorf("rename %s -> %s: %w", tmp, dest, err)
	}

	return &Result{
		DestPath: dest,
		SHA256:   computed,
		Bytes:    written,
	}, nil
}

// HashFile computes the SHA-256 of an existing file.
func HashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	h := sha256.New()
	buf := make([]byte, bufSize)
	n, err := io.CopyBuffer(h, f, buf)
	if err != nil {
		return "", 0, fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
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

// SweepOrphanTmps recursively removes *.tmp files under root.
// Returns the absolute paths of cleaned files. Errors on individual files are
// skipped (not returned). If root does not exist, returns (nil, nil).
//
// Orphans only appear if a transfer process was killed between fsync and rename.
// They're safe to delete because the source file still exists and no state record
// references the .tmp path.
func SweepOrphanTmps(root string) ([]string, error) {
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil, nil
	}
	var cleaned []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".tmp" {
			return nil
		}
		if rmErr := os.Remove(path); rmErr == nil {
			cleaned = append(cleaned, path)
		}
		return nil
	})
	return cleaned, err
}
