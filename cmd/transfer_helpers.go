package cmd

import (
	"errors"
	"fmt"
	"github.com/pspenano/reel/internal/camera"
	"github.com/pspenano/reel/internal/config"
	"github.com/pspenano/reel/internal/lockfile"
	"github.com/pspenano/reel/internal/safefs"
	"github.com/pspenano/reel/internal/state"
	"github.com/pspenano/reel/internal/transfer"
	"github.com/pspenano/reel/internal/volume"
	"os"
	"path/filepath"
	"strings"

	"github.com/pspenano/reel/internal/display"
)

// handleTransferError reports a failed file and stops the batch. Integrity and
// persistence failures must never be treated as successful partial work.
func handleTransferError(err error, filename, srcDir, destDir string) (abort bool) {
	display.ClearProgress()

	if !pathExists(destDir) {
		display.Error("destination volume is no longer mounted: %s", destDir)
		display.Info("  The drive may have disconnected, or the network share dropped.")
		display.Info("  Re-plug and re-run — already-copied files won't be re-done.")
		return true
	}
	if !pathExists(srcDir) {
		display.Error("source volume is no longer mounted: %s", srcDir)
		display.Info("  The camera or source disk may have disconnected.")
		display.Info("  Re-plug and re-run — already-copied files won't be re-done.")
		return true
	}
	display.Error("%s: %v", filename, err)
	return true
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func canonicalHash(st *state.Store, f camera.File) string {
	r := st.GetByParts(f.Profile.Name, f.BaseName, f.Ext)
	if r == nil {
		return ""
	}
	return r.SHA256
}

func validatedCopy(src, dst, canonical string) (bool, error) {
	h, _, err := transfer.HashFile(src)
	if err != nil {
		return false, err
	}
	if canonical == "" || h != canonical {
		return false, fmt.Errorf("source identity conflict: %s", src)
	}
	if dst == "" {
		return false, nil
	}
	h, _, err = transfer.HashFile(dst)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if h != canonical {
		return false, fmt.Errorf("tracked destination conflict: %s", dst)
	}
	a, err := os.Stat(src)
	if err != nil {
		return false, err
	}
	b, err := os.Stat(dst)
	if err != nil {
		return false, err
	}
	if os.SameFile(a, b) {
		return false, fmt.Errorf("source/destination alias")
	}
	return true, nil
}

// A canonical copy only satisfies a backup request on the selected archive.
// Refuse old-drive history rather than silently rebinding the single HD record.
func validatedBackup(cfg *config.Config, id volume.Identity, src string, row *state.Row) (bool, error) {
	if err := checkVolume(id); err != nil {
		return false, err
	}
	if row.HDPath != "" {
		rel, err := filepath.Rel(hdManaged(cfg), row.HDPath)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return false, fmt.Errorf("backup identity conflict: recorded path %s is outside selected archive %s; existing history was preserved", row.HDPath, hdManaged(cfg))
		}
		if row.HDVolumeUUID == "" {
			return false, fmt.Errorf("backup identity is unbound for %s; verify the intended drive with reel verify --bind-legacy", row.HDPath)
		}
		if row.HDVolumeUUID != id.UUID {
			return false, fmt.Errorf("backup identity conflict: recorded volume for %s differs from selected drive; existing history was preserved", row.HDPath)
		}
		if err := volume.Contains(hdManaged(cfg), row.HDPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	ok, err := validatedCopy(src, row.HDPath, row.SHA256)
	if err != nil {
		return false, err
	}
	if err := checkVolume(id); err != nil {
		return false, err
	}
	return ok, nil
}

func validateCameraBatch(files []camera.File) error {
	names := map[string]bool{}
	for _, f := range files {
		name := strings.ToLower(f.BaseName + "." + f.Ext)
		if names[name] {
			return fmt.Errorf("ambiguous recording filename: %s", name)
		}
		names[name] = true
	}
	return nil
}

func approvedHD(cfg *config.Config) (volume.Identity, error) {
	id, err := resolveVolume(hdRoot(cfg))
	if err != nil {
		return id, err
	}
	if cfg.HDVolumeUUID == "" {
		return id, fmt.Errorf("backup volume is not bound; run reel config with the intended drive mounted")
	}
	if cfg.HDVolumeUUID != id.UUID {
		return id, fmt.Errorf("backup volume identity differs from configuration")
	}
	return id, nil
}

// Lock order: local config, managed archive, camera, per-copy directory,
// mirrored state directory. Mirror merge never acquires a media lock.
func archiveLock(cfg *config.Config) (*lockfile.Lock, error) {
	root, err := safefs.OpenDir(hdRoot(cfg), false)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	dir, err := safefs.OpenBeneath(root, cfg.HDDir, true)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	lk, err := lockfile.AcquireExclusive(filepath.Join(hdManaged(cfg), ".reel-archive.lock"))
	if err != nil {
		return nil, err
	}
	if err = safefs.EnsureRecord(dir, ".reel-protocol.json", []byte(safefs.Protocol)); err != nil {
		lk.Release()
		return nil, err
	}
	return lk, nil
}
