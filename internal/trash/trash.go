// Package trash implements macOS-compatible soft-delete by moving files to ~/.Trash.
package trash

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Move moves src to ~/.Trash/reel-deleted-<ts>/<basename>.
// ts is used to group all files deleted in the same clean run.
// Returns the destination path.
func Move(src string, ts time.Time) (string, error) {
	trashDir, err := trashDirFor(ts)
	if err != nil {
		return "", err
	}
	dst := filepath.Join(trashDir, filepath.Base(src))
	if err := os.Rename(src, dst); err != nil {
		return "", fmt.Errorf("trash move %s -> %s: %w", src, dst, err)
	}
	return dst, nil
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
