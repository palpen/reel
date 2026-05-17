package cmd

import (
	"flag"
	"fmt"
	"path/filepath"
	"time"

	"github.com/pspenano/reel/internal/config"
	"github.com/pspenano/reel/internal/display"
	"github.com/pspenano/reel/internal/lockfile"
	"github.com/pspenano/reel/internal/state"
	"github.com/pspenano/reel/internal/transfer"
)

// RunBackup implements `reel backup` (laptop → HD).
func RunBackup(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
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

	hdDir := cfg.HDManagedDir()

	// Collect rows with laptop_path and no hd_path
	var toBackup []*state.Row
	for _, r := range st.All() {
		if r.LaptopPath != "" && r.HDPath == "" {
			toBackup = append(toBackup, r)
		}
	}
	if len(toBackup) == 0 {
		display.Info("Nothing to back up.")
		return nil
	}

	display.Info("Backing up %d files to %s", len(toBackup), hdDir)

	var backed, failed int
	for i, r := range toBackup {
		filename := r.BaseName + "." + r.Ext
		display.Progress("[%d/%d] %s", i+1, len(toBackup), filename)

		result, err := transfer.Copy(r.LaptopPath, hdDir, filename, r.RecordedAt, r.SHA256)
		if err != nil {
			display.ClearProgress()
			display.Error("backup %s: %v", filename, err)
			failed++
			continue
		}

		now := time.Now().UTC()
		r.HDPath = result.DestPath
		r.BackedUpAt = &now
		r.HDVerifiedAt = &now // hash was verified during copy

		if err := st.Upsert(r); err != nil {
			display.Error("save state for %s: %v", filename, err)
		}
		backed++
	}
	display.ClearProgress()

	display.Print("Backup complete: %d backed up, %d failed.", backed, failed)
	mirrorStateToHD(cfg, st)
	return nil
}
