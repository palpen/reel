package cmd

import (
	"flag"
	"fmt"
	"path/filepath"
	"time"

	"github.com/pspenano/reel/internal/camera"
	"github.com/pspenano/reel/internal/display"
	"github.com/pspenano/reel/internal/lockfile"
	"github.com/pspenano/reel/internal/state"
	"github.com/pspenano/reel/internal/transfer"
	"github.com/pspenano/reel/internal/volume"
)

// RunDirectBackup implements `reel direct_backup` (camera → HD, skipping laptop).
func RunDirectBackup(args []string) error {
	fs := flag.NewFlagSet("direct_backup", flag.ContinueOnError)
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

	hdIdentity, err := approvedHD(cfg)
	if err != nil {
		return err
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

	archive, err := archiveLock(cfg)
	if err != nil {
		return err
	}
	defer archive.Release()

	st, err := state.Load(filepath.Join(cfgDir, "state.jsonl"))
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	cameras, err := detectCameras(cfg.Cameras)
	if err != nil {
		return fmt.Errorf("detect cameras: %w", err)
	}
	if len(cameras) == 0 {
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

	for _, f := range files {
		if err := volume.Contains(dc.VolumePath, f.FullPath); err != nil {
			return err
		}
	}
	if err := validateCameraBatch(files); err != nil {
		return err
	}

	// Filter: only files without hd_path
	var toBackup []camera.File
	for _, f := range files {
		if !cfg.ShouldTransfer(f.Ext) {
			continue
		}
		existing := st.GetByParts(f.Profile.Name, f.BaseName, f.Ext)
		if existing != nil {
			if existing.CameraPath != "" && filepath.Clean(existing.CameraPath) != filepath.Clean(f.FullPath) {
				return fmt.Errorf("recording path conflicts with existing identity: %s", f.FullPath)
			}
			ok, err := validatedCopy(f.FullPath, existing.HDPath, existing.SHA256)
			if err != nil {
				return err
			}
			if ok {
				continue
			}
		}
		toBackup = append(toBackup, f)
	}
	if len(toBackup) == 0 {
		display.Info("No eligible files to back up (already backed up or excluded by transfer_extensions).")
		return nil
	}

	hdDir := hdManaged(cfg)

	for _, f := range toBackup {
		if err := transfer.Preflight(f.FullPath, filepath.Join(hdDir, f.BaseName+"."+f.Ext), canonicalHash(st, f)); err != nil {
			return err
		}
	}
	var totalBytes int64
	for _, f := range toBackup {
		totalBytes += f.Size
	}
	if err := transfer.PreflightSpace(hdDir, totalBytes); err != nil {
		return err
	}

	display.Info("Direct backup of %d files (%s) to %s", len(toBackup), display.Bytes(totalBytes), hdDir)

	var backed, failed int
	aborted := false
	remaining := 0
	for i, f := range toBackup {
		filename := f.BaseName + "." + f.Ext
		display.Progress("[%d/%d] %s", i+1, len(toBackup), filename)

		if err := checkVolume(hdIdentity); err != nil {
			return err
		}
		if err := checkVolume(cameraIdentity); err != nil {
			return err
		}
		result, err := transfer.CopyChecked(f.FullPath, hdDir, filename, f.RecordedAt, canonicalHash(st, f), func() error {
			if err := checkVolume(cameraIdentity); err != nil {
				return err
			}
			return checkVolume(hdIdentity)
		})
		if err != nil {
			if abort := handleTransferError(err, filename, filepath.Dir(f.FullPath), hdDir); abort {
				failed++
				aborted = true
				remaining = len(toBackup) - i - 1
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
				HDPath:        result.DestPath,
				BackedUpAt:    &now,
				HDVerifiedAt:  &now,
			}
		} else {
			row.HDPath = result.DestPath
			row.BackedUpAt = &now
			row.HDVerifiedAt = &now
			if row.SHA256 == "" {
				row.SHA256 = result.SHA256
			}
		}
		row.HDVolumeUUID = hdIdentity.UUID
		if err := st.Upsert(row); err != nil {
			return fmt.Errorf("stopped: %d committed, 1 published without state, %d not attempted; save %s: %w", backed, len(toBackup)-i-1, filename, err)
		}
		backed++
	}
	display.ClearProgress()

	if e := mirrorStateToHD(cfg, st); e != nil {
		return e
	}

	if aborted {
		return fmt.Errorf("aborted after %d backed up, %d failed, %d not attempted", backed, failed, remaining)
	}
	display.Print("Direct backup complete: %d backed up, %d failed.", backed, failed)
	return nil
}
