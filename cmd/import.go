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

// RunImport implements `reel import`.
func RunImport(args []string) error {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
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

	if cleaned, _ := transfer.SweepOrphanTmps(cfg.LaptopDir); len(cleaned) > 0 {
		display.Info("Cleaned %d orphan .tmp file(s) from previous run.", len(cleaned))
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
	if len(files) == 0 {
		display.Info("No files found on camera.")
		return nil
	}

	// Filter already-imported
	var toImport []camera.File
	for _, f := range files {
		existing := st.GetByParts(f.Profile.Name, f.BaseName, f.Ext)
		if existing != nil && existing.LaptopPath != "" {
			continue
		}
		toImport = append(toImport, f)
	}
	if len(toImport) == 0 {
		display.Info("All files already imported.")
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

		result, err := transfer.Copy(f.FullPath, destDir, filename, f.RecordedAt, "")
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
			display.Error("save state for %s: %v", filename, err)
		}
		imported++
	}
	display.ClearProgress()

	// Mirror state to HD if connected
	mirrorStateToHD(cfg, st)

	if aborted {
		return fmt.Errorf("aborted after %d imported, %d failed, %d not attempted", imported, failed, remaining)
	}
	display.Print("Import complete: %d imported, %d failed.", imported, failed)
	return nil
}

// mirrorStateToHD copies the state file to the HD if it's connected.
func mirrorStateToHD(cfg *config.Config, st *state.Store) {
	hdPath := cfg.HDStatePath()
	if err := st.MirrorTo(hdPath); err != nil {
		display.Warn("mirror state to HD: %v", err)
	}
}
