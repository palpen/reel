package cmd

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pspenano/reel/internal/display"
	"github.com/pspenano/reel/internal/lockfile"
	"github.com/pspenano/reel/internal/state"
	"github.com/pspenano/reel/internal/transfer"
	"github.com/pspenano/reel/internal/volume"
)

// RunBackup implements `reel backup` (laptop → HD).
func RunBackup(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
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

	hdDir := hdManaged(cfg)

	// Verify completed archives, then collect files that still need copying.
	var toBackup []*state.Row
	for _, r := range st.All() {
		if cfg.ShouldTransfer(r.Ext) && r.LaptopPath != "" {
			if err := volume.Contains(cfg.LaptopDir, r.LaptopPath); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					if err := validateBackupWithoutSource(cfg, hdIdentity, r); err != nil {
						return fmt.Errorf("laptop copy unavailable at %s: %w", r.LaptopPath, err)
					}
					continue
				}
				return err
			}
			ok, err := validatedBackup(cfg, hdIdentity, r.LaptopPath, r)
			if err != nil {
				return err
			}
			if !ok {
				toBackup = append(toBackup, r)
			}
		}
	}
	if len(toBackup) == 0 {
		if err := mirrorStateToHD(cfg, st); err != nil {
			return fmt.Errorf("verified skips; required mirror failed: %w", err)
		}
		display.Info("Nothing to back up.")
		return nil
	}

	names := map[string]bool{}
	for _, r := range toBackup {
		name := strings.ToLower(r.BaseName + "." + r.Ext)
		if names[name] {
			return fmt.Errorf("batch destination collision: %s", name)
		}
		names[name] = true
	}
	for _, r := range toBackup {
		if err := transfer.Preflight(r.LaptopPath, filepath.Join(hdDir, r.BaseName+"."+r.Ext), r.SHA256); err != nil {
			return err
		}
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

		if err := checkVolume(hdIdentity); err != nil {
			return err
		}
		result, err := transfer.CopyChecked(r.LaptopPath, hdDir, filename, r.RecordedAt, r.SHA256, func() error { return checkVolume(hdIdentity) })
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

		r.HDVolumeUUID = hdIdentity.UUID
		if err := st.Upsert(r); err != nil {
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
	display.Print("Backup complete: %d backed up, %d failed.", backed, failed)
	return nil
}
