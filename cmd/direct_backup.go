package cmd

import (
	"flag"
	"fmt"
	"path/filepath"
	"time"

	"github.com/pspenano/reel/internal/camera"
	"github.com/pspenano/reel/internal/config"
	"github.com/pspenano/reel/internal/display"
	"github.com/pspenano/reel/internal/lockfile"
	"github.com/pspenano/reel/internal/state"
	"github.com/pspenano/reel/internal/transfer"
)

// RunDirectBackup implements `reel direct_backup` (camera → HD, skipping laptop).
func RunDirectBackup(args []string) error {
	fs := flag.NewFlagSet("direct_backup", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	cfgDir, err := config.Dir()
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

	cameras, err := camera.Detect(cfg.Cameras)
	if err != nil {
		return fmt.Errorf("detect cameras: %w", err)
	}
	if len(cameras) == 0 {
		display.Info("No camera found.")
		return nil
	}

	dc := &cameras[0]
	display.Info("Camera: %s (%s)", dc.Profile.Name, dc.VolumePath)

	files, err := dc.Walk()
	if err != nil {
		return fmt.Errorf("walk DCIM: %w", err)
	}

	// Filter: only files without hd_path
	var toBackup []camera.File
	for _, f := range files {
		existing := st.GetByParts(f.Profile.Name, f.BaseName, f.Ext)
		if existing != nil && existing.HDPath != "" {
			continue
		}
		toBackup = append(toBackup, f)
	}
	if len(toBackup) == 0 {
		display.Info("All files already backed up to HD.")
		return nil
	}

	hdDir := cfg.HDManagedDir()
	display.Info("Direct backup of %d files to %s", len(toBackup), hdDir)

	var backed, failed int
	for i, f := range toBackup {
		filename := f.BaseName + "." + f.Ext
		display.Progress("[%d/%d] %s", i+1, len(toBackup), filename)

		result, err := transfer.Copy(f.FullPath, hdDir, filename, f.RecordedAt, "")
		if err != nil {
			display.ClearProgress()
			display.Error("direct_backup %s: %v", filename, err)
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
		if err := st.Upsert(row); err != nil {
			display.Error("save state for %s: %v", filename, err)
		}
		backed++
	}
	display.ClearProgress()

	display.Print("Direct backup complete: %d backed up, %d failed.", backed, failed)
	mirrorStateToHD(cfg, st)
	return nil
}
