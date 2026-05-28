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

	if cleaned, _ := transfer.SweepOrphanTmps(hdDir); len(cleaned) > 0 {
		display.Info("Cleaned %d orphan .tmp file(s) from previous run.", len(cleaned))
	}

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

	var totalBytes int64
	for _, r := range toBackup {
		totalBytes += r.SizeBytes
	}
	if err := transfer.PreflightSpace(hdDir, totalBytes); err != nil {
		return err
	}

	display.Info("Backing up %d files (%s) to %s", len(toBackup), display.Bytes(totalBytes), hdDir)

	var backed, failed int
	aborted := false
	remaining := 0
	for i, r := range toBackup {
		filename := r.BaseName + "." + r.Ext
		display.Progress("[%d/%d] %s", i+1, len(toBackup), filename)

		result, err := transfer.Copy(r.LaptopPath, hdDir, filename, r.RecordedAt, r.SHA256)
		if err != nil {
			if abort := handleTransferError(err, filename, filepath.Dir(r.LaptopPath), hdDir); abort {
				failed++
				aborted = true
				remaining = len(toBackup) - i - 1
				break
			}
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

	mirrorStateToHD(cfg, st)

	if aborted {
		return fmt.Errorf("aborted after %d backed up, %d failed, %d not attempted", backed, failed, remaining)
	}
	display.Print("Backup complete: %d backed up, %d failed.", backed, failed)
	return nil
}
