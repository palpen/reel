package cmd

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pspenano/reel/internal/camera"
	"github.com/pspenano/reel/internal/config"
	"github.com/pspenano/reel/internal/display"
	"github.com/pspenano/reel/internal/lockfile"
	"github.com/pspenano/reel/internal/state"
	"github.com/pspenano/reel/internal/transfer"
	"github.com/pspenano/reel/internal/volume"
)

// RunImport implements `reel import`.
func RunImport(args []string) error {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unsupported arguments: %v", fs.Args())
	}

	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	cfgDir, err := configDir()
	if err != nil {
		return err
	}
	lk, err := lockfile.AcquireExclusive(filepath.Join(cfgDir, "reel.lock"))
	if err != nil {
		return err
	}
	defer lk.Release()

	st, err := state.Load(filepath.Join(cfgDir, "state.jsonl"))
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	cameras, err := detectCameras(cfg.Cameras)
	if err != nil {
		return fmt.Errorf("detect cameras: %w", err)
	}
	if len(cameras) == 0 {
		if err := mirrorStateIfHDConnected(cfg, st); err != nil {
			return err
		}
		display.Info("No camera found.")
		return nil
	}

	if len(cameras) != 1 {
		return fmt.Errorf("multiple cameras detected; connect exactly one camera")
	}
	dc := &cameras[0]
	display.Info("Camera: %s (%s)", dc.Profile.Name, dc.VolumePath)

	cameraIdentity, err := resolveVolume(dc.VolumePath)
	if err != nil {
		return err
	}
	cardLock, err := lockfile.AcquireExclusive(filepath.Join(dc.VolumePath, "reel.lock"))
	if err != nil {
		return err
	}
	defer cardLock.Release()
	files, err := dc.Walk()
	if err != nil {
		return fmt.Errorf("walk DCIM: %w", err)
	}
	if len(files) == 0 {
		if err := mirrorStateIfHDConnected(cfg, st); err != nil {
			return err
		}
		display.Info("No files found on camera.")
		return nil
	}

	for _, f := range files {
		if err := volume.Contains(dc.VolumePath, f.FullPath); err != nil {
			return err
		}
	}
	if err := validateCameraBatch(files); err != nil {
		return err
	}

	// Filter already-imported
	var toImport []camera.File
	for _, f := range files {
		if !cfg.ShouldTransfer(f.Ext) {
			continue
		}
		existing := st.GetByParts(f.Profile.Name, f.BaseName, f.Ext)
		if existing != nil {
			if existing.CameraPath != "" && filepath.Clean(existing.CameraPath) != filepath.Clean(f.FullPath) {
				return fmt.Errorf("recording path conflicts with existing identity: %s", f.FullPath)
			}
			ok, err := validatedCopy(f.FullPath, existing.LaptopPath, existing.SHA256)
			if err != nil {
				return err
			}
			if ok {
				continue
			}
		}
		toImport = append(toImport, f)
	}
	if len(toImport) == 0 {
		if err := mirrorStateIfHDConnected(cfg, st); err != nil {
			return err
		}
		display.Info("No eligible files to import (already imported or excluded by transfer_extensions).")
		return nil
	}

	// Determine destination folder from min(recorded_at)
	var minTime time.Time
	for _, f := range toImport {
		if minTime.IsZero() || f.RecordedAt.Before(minTime) {
			minTime = f.RecordedAt
		}
	}
	folderName := minTime.UTC().Format("2006-01-02_150405")
	destDir := filepath.Join(cfg.LaptopDir, folderName)

	for _, f := range toImport {
		if err := transfer.Preflight(f.FullPath, filepath.Join(destDir, f.BaseName+"."+f.Ext), canonicalHash(st, f)); err != nil {
			return err
		}
	}
	var totalBytes int64
	for _, f := range toImport {
		totalBytes += f.Size
	}
	if err := transfer.PreflightSpace(cfg.LaptopDir, totalBytes); err != nil {
		return err
	}

	display.Info("Importing %d files (%s) to %s", len(toImport), display.Bytes(totalBytes), destDir)

	var imported, failed int
	aborted := false
	remaining := 0
	for i, f := range toImport {
		filename := f.BaseName + "." + f.Ext
		display.Progress("[%d/%d] %s", i+1, len(toImport), filename)

		if err := checkVolume(cameraIdentity); err != nil {
			return err
		}
		result, err := transfer.CopyChecked(f.FullPath, destDir, filename, f.RecordedAt, canonicalHash(st, f), func() error { return checkVolume(cameraIdentity) })
		if err != nil {
			if abort := handleTransferError(err, filename, filepath.Dir(f.FullPath), destDir); abort {
				failed++
				aborted = true
				remaining = len(toImport) - i - 1
				break
			}
			failed++
			continue
		}

		now := time.Now().UTC()
		row := st.GetByParts(f.Profile.Name, f.BaseName, f.Ext)
		if row == nil {
			row = &state.Row{
				CameraProfile: f.Profile.Name,
				BaseName:      f.BaseName,
				Ext:           f.Ext,
				RecordedAt:    f.RecordedAt,
				SizeBytes:     result.Bytes,
				SHA256:        result.SHA256,
				CameraPath:    f.FullPath,
				LaptopPath:    result.DestPath,
				ImportedAt:    &now,
			}
		} else {
			row.LaptopPath = result.DestPath
			row.ImportedAt = &now
			if row.SHA256 == "" {
				row.SHA256 = result.SHA256
			}
		}
		if err := st.Upsert(row); err != nil {
			return fmt.Errorf("stopped: %d committed, 1 published without state, %d not attempted; save %s: %w", imported, len(toImport)-i-1, filename, err)
		}
		imported++
	}
	display.ClearProgress()

	if err := mirrorStateIfHDConnected(cfg, st); err != nil {
		return err
	}

	if aborted {
		return fmt.Errorf("aborted after %d imported, %d failed, %d not attempted", imported, failed, remaining)
	}
	display.Print("Import complete: %d imported, %d failed.", imported, failed)
	return nil
}

// Imports reconcile their mirror even when no media needs copying. An absent
// backup drive remains optional; failures on a connected drive are not skipped.
func mirrorStateIfHDConnected(cfg *config.Config, st *state.Store) error {
	if _, err := os.Stat(hdRoot(cfg)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("optional mirror unavailable: %w", err)
	}
	return mirrorStateToHD(cfg, st)
}

// mirrorStateToHD merges local state into the configured, mounted backup drive.
func mirrorStateToHD(cfg *config.Config, st *state.Store) error {
	if _, err := approvedHD(cfg); err != nil {
		return err
	}
	return st.MirrorTo(hdState(cfg))
}
