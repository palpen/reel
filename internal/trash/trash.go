// Package trash implements macOS-compatible soft-delete by moving files to ~/.Trash.
package trash

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// ErrCrossDevice is returned by Move when src and the Trash directory live on
// different filesystems. A rename into the Trash is impossible across volumes,
// and copying gigabytes of camera media onto the internal disk just to delete
// it is pointless, so the caller decides what to do (reel permanently deletes,
// since a verified HD backup is required before any delete).
var ErrCrossDevice = errors.New("source is on a different volume than ~/.Trash")

// Move moves src to ~/.Trash/reel-deleted-<ts>/<basename>.
// ts is used to group all files deleted in the same clean run.
// Returns the destination path. If src and the Trash dir are on different
// volumes it returns ErrCrossDevice without moving anything.
func Move(src string, ts time.Time) (string, error) {
	trashDir, err := trashDirFor(ts)
	if err != nil {
		return "", err
	}
	dst := filepath.Join(trashDir, filepath.Base(src))
	if err := os.Rename(src, dst); err != nil {
		if errors.Is(err, syscall.EXDEV) {
			return "", ErrCrossDevice
		}
		return "", fmt.Errorf("trash move %s -> %s: %w", src, dst, err)
	}
	return dst, nil
}

// SameVolumeAsTrash reports whether src lives on the same filesystem as the
// user's Trash (~/.Trash, under the home directory). Soft-delete via rename is
// only possible when they match; camera cards mount as a separate volume.
func SameVolumeAsTrash(src string) (bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, err
	}
	return sameVolume(src, home)
}

// sameVolume reports whether paths a and b reside on the same filesystem,
// compared by device ID.
func sameVolume(a, b string) (bool, error) {
	var sa, sb syscall.Stat_t
	if err := syscall.Stat(a, &sa); err != nil {
		return false, fmt.Errorf("stat %s: %w", a, err)
	}
	if err := syscall.Stat(b, &sb); err != nil {
		return false, fmt.Errorf("stat %s: %w", b, err)
	}
	return sa.Dev == sb.Dev, nil
}

// trashDirFor returns (and creates) the timestamped reel trash directory.
func trashDirFor(ts time.Time) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	name := "reel-deleted-" + ts.UTC().Format("20060102-150405")
	dir := filepath.Join(home, ".Trash", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create trash dir %s: %w", dir, err)
	}
	return dir, nil
}
