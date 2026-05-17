package cmd

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pspenano/reel/internal/config"
	"github.com/pspenano/reel/internal/display"
	"github.com/pspenano/reel/internal/lockfile"
	"github.com/pspenano/reel/internal/state"
	"github.com/pspenano/reel/internal/transfer"
)

// RunVerify implements `reel verify`.
func RunVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	scope := fs.String("scope", "hd", "what to verify: hd")
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
	lk, err := lockfile.AcquireShared(filepath.Join(cfgDir, "reel.lock"))
	if err != nil {
		return err
	}
	defer lk.Release()

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
		filename := r.BaseName + "." + r.Ext
		display.Progress("[%d/%d] %s", i+1, len(toVerify), filename)

		if _, err := os.Stat(r.HDPath); os.IsNotExist(err) {
			display.ClearProgress()
			display.Error("HD file missing: %s", r.HDPath)
			missing++
			continue
		}

		computed, _, err := transfer.HashFile(r.HDPath)
		if err != nil {
			display.ClearProgress()
			display.Error("hash %s: %v", r.HDPath, err)
			missing++
			continue
		}

		if computed != r.SHA256 {
			display.ClearProgress()
			display.Error("HASH MISMATCH: %s\n  expected: %s\n  got:      %s", r.HDPath, r.SHA256, computed)
			mismatched++
			if firstMismatch == nil {
				firstMismatch = fmt.Errorf("hash mismatch for %s", filename)
			}
			continue
		}

		now := time.Now().UTC()
		r.HDVerifiedAt = &now
		if err := st.Upsert(r); err != nil {
			display.Error("save state for %s: %v", filename, err)
		}
		verified++
	}
	display.ClearProgress()

	display.Print("Verify complete: %d OK, %d missing, %d mismatched.", verified, missing, mismatched)

	mirrorStateToHD(cfg, st)

	if firstMismatch != nil || mismatched > 0 {
		return fmt.Errorf("%d file(s) failed verification", mismatched)
	}
	return nil
}
