package cmd

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pspenano/reel/internal/display"
	"github.com/pspenano/reel/internal/lockfile"
	"github.com/pspenano/reel/internal/state"
	"github.com/pspenano/reel/internal/transfer"
	"github.com/pspenano/reel/internal/volume"
)

// RunVerify implements `reel verify`.
func RunVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	bindLegacy := fs.Bool("bind-legacy", false, "verify and explicitly bind legacy backup records to the configured drive")
	scope := fs.String("scope", "hd", "what to verify: hd")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unsupported arguments: %v", fs.Args())
	}

	if *scope != "hd" {
		return fmt.Errorf("unsupported scope: %s", *scope)
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

	if *scope != "hd" {
		return fmt.Errorf("unknown scope %q (supported: hd)", *scope)
	}

	rows := st.All()
	var toVerify []*state.Row
	for _, r := range rows {
		if r.HDPath != "" {
			toVerify = append(toVerify, r)
		}
	}
	if len(toVerify) == 0 {
		display.Info("No HD files to verify.")
		return nil
	}

	display.Info("Verifying %d HD files...", len(toVerify))

	var verified, mismatched, missing int
	var firstMismatch error
	for i, r := range toVerify {
		if err := volume.Contains(hdManaged(cfg), r.HDPath); err != nil {
			r.HDVerifiedAt = nil
			if e := st.Upsert(r); e != nil {
				return e
			}
			if e := mirrorStateToHD(cfg, st); e != nil {
				return e
			}
			return err
		}
		filename := r.BaseName + "." + r.Ext
		display.Progress("[%d/%d] %s", i+1, len(toVerify), filename)

		if _, err := os.Stat(r.HDPath); os.IsNotExist(err) {
			display.ClearProgress()
			display.Error("HD file missing: %s", r.HDPath)
			missing++
			r.HDVerifiedAt = nil
			if err := st.Upsert(r); err != nil {
				return err
			}
			continue
		}

		computed, _, err := transfer.HashFile(r.HDPath)
		if err != nil {
			display.ClearProgress()
			display.Error("hash %s: %v", r.HDPath, err)
			missing++
			r.HDVerifiedAt = nil
			if err := st.Upsert(r); err != nil {
				return err
			}
			continue
		}

		if computed != r.SHA256 {
			display.ClearProgress()
			display.Error("HASH MISMATCH: %s\n  expected: %s\n  got:      %s", r.HDPath, r.SHA256, computed)
			mismatched++
			r.HDVerifiedAt = nil
			if err := st.Upsert(r); err != nil {
				return err
			}
			if firstMismatch == nil {
				firstMismatch = fmt.Errorf("hash mismatch for %s", filename)
			}
			continue
		}

		if r.HDVolumeUUID != hdIdentity.UUID && !*bindLegacy {
			return fmt.Errorf("legacy backup requires --bind-legacy after checking the selected drive")
		}
		if err := checkVolume(hdIdentity); err != nil {
			return err
		}
		r.HDVolumeUUID = hdIdentity.UUID
		now := time.Now().UTC()
		r.HDVerifiedAt = &now
		if err := st.Upsert(r); err != nil {
			return fmt.Errorf("save state for %s: %w", filename, err)
		}
		verified++
	}
	display.ClearProgress()

	display.Print("Verify complete: %d OK, %d missing, %d mismatched.", verified, missing, mismatched)

	if e := mirrorStateToHD(cfg, st); e != nil {
		return e
	}

	if firstMismatch != nil || mismatched > 0 || missing > 0 {
		return fmt.Errorf("%d file(s) failed verification", mismatched+missing)
	}
	return nil
}
