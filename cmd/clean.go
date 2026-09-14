package cmd

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pspenano/reel/internal/display"
	"github.com/pspenano/reel/internal/lockfile"
	"github.com/pspenano/reel/internal/safefs"
	"github.com/pspenano/reel/internal/state"
	"github.com/pspenano/reel/internal/trash"
	"github.com/pspenano/reel/internal/volume"
)

const defaultStaleThreshold = 7 * 24 * time.Hour

// RunClean implements `reel clean`.
func RunClean(args []string) error {
	fs := flag.NewFlagSet("clean", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "preview recoverable moves without changing media or state")
	forceStale := fs.Bool("force-stale", false, "ignore stale verification threshold")
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

	if !*dryRun {
		archive, err := archiveLock(cfg)
		if err != nil {
			return err
		}
		defer archive.Release()
	}

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
	if !*dryRun {
		cardLock, err := lockfile.AcquireExclusive(filepath.Join(dc.VolumePath, "reel.lock"))
		if err != nil {
			return err
		}
		defer cardLock.Release()
	}
	files, err := dc.Walk()
	if err != nil {
		return fmt.Errorf("walk DCIM: %w", err)
	}

	if err := validateCameraBatch(files); err != nil {
		return err
	}
	cameraIdentity, err := resolveVolume(dc.VolumePath)
	if err != nil {
		return err
	}
	if err = volume.Independent(cameraIdentity, hdIdentity); err != nil {
		return err
	}
	toDelete, heldBack := planClean(files, st, time.Now().UTC(), *forceStale)

	for i := range toDelete {
		d := &toDelete[i]
		for _, snapshot := range d.snapshots {
			if snapshot.path == d.row.HDPath {
				if err := volume.Contains(hdManaged(cfg), snapshot.path); err != nil {
					return err
				}
			}
		}
		if d.file.Ext != "LRF" && d.row.HDVolumeUUID != hdIdentity.UUID {
			return fmt.Errorf("legacy backup identity needs explicit verification: reel verify --bind-legacy")
		}
		d.validate = func() error {
			if err := checkVolume(cameraIdentity); err != nil {
				return err
			}
			return checkVolume(hdIdentity)
		}
	}
	// Print held-back list
	if len(heldBack) > 0 {
		display.Print("\nHeld back (%d files):", len(heldBack))
		for _, c := range heldBack {
			display.Print("  %-40s  reason: %s", c.file.BaseName+"."+c.file.Ext, c.decision.Reason)
		}
	}

	if len(toDelete) == 0 {
		display.Print("\nNothing eligible for recovery.")
		return nil
	}

	display.Print("\nFiles eligible for recoverable movement (%d):", len(toDelete))
	for _, d := range toDelete {
		display.Print("  %s%s", d.file.FullPath, d.note)
	}

	display.Print("\nCamera: %s; backup: %s (%s)", dc.VolumePath, hdRoot(cfg), hdIdentity.UUID)
	display.Print("Files will move to %s. Recovery occupies card space and never expires.", filepath.Join(dc.VolumePath, ".reel-trash"))
	cardDir, err := safefs.OpenDir(dc.VolumePath, false)
	if err != nil {
		return err
	}
	copies, err := safefs.RecoveryCopies(cardDir)
	cardDir.Close()
	if err != nil {
		return err
	}
	if copies {
		display.Print("exFAT recovery uses verified copies and needs temporary free space for one file. Restore retains the recovery copy.")
	}

	if *dryRun {
		display.Print("\n--dry-run: no files deleted.")
		return nil
	}

	// Confirm

	fmt.Printf("\nMove %d files to recovery? [y/N]: ", len(toDelete))
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer != "y" && answer != "yes" {
		return fmt.Errorf("clean cancelled")
	}

	deleted, err := executeCleanPlan(toDelete, st, dc.VolumePath, true)
	display.Print("\nClean result: %d moved to recovery.", deleted)
	if e := mirrorStateToHD(cfg, st); err == nil {
		err = e
	}
	return err
}

// executeCleanPlan is shared by the interactive command and fixture tests.
// It performs no prompting and never handles a dry run.
func executeCleanPlan(toDelete []cleanCandidate, st *state.Store, volume string, softDelete bool) (int, error) {
	if !softDelete {
		return 0, fmt.Errorf("permanent deletion is unavailable")
	}
	// Recheck the entire plan after confirmation, before moving any files.
	for _, d := range toDelete {
		if d.validate != nil {
			if err := d.validate(); err != nil {
				return 0, err
			}
		}
		for _, snapshot := range d.snapshots {
			if err := snapshot.check(); err != nil {
				return 0, fmt.Errorf("camera/backup changed after verification; rerun clean: %w", err)
			}
		}
	}

	deleteTs := time.Now()
	deleted := 0
	for _, d := range toDelete {
		for _, snapshot := range d.snapshots {
			if err := snapshot.check(); err != nil {
				return deleted, fmt.Errorf("clean stopped; camera/backup changed: %w", err)
			}
		}
		filename := d.file.BaseName + "." + d.file.Ext
		if d.validate != nil {
			if err := d.validate(); err != nil {
				return deleted, err
			}
		}
		dest, delErr := trash.MoveChecked(d.file.FullPath, volume, deleteTs, func() error {
			for _, snapshot := range d.snapshots {
				if err := snapshot.check(); err != nil {
					return err
				}
			}
			if d.validate != nil {
				return d.validate()
			}
			return nil
		})
		if dest != "" {
			display.Print("  Recoverable: %s", dest)
		}
		if delErr != nil {
			return deleted, fmt.Errorf("clean stopped at %s: %w", filename, delErr)
		}
		deleted++
		now := time.Now().UTC()
		d.row.CleanedAt = &now
		if err := st.Upsert(d.row); err != nil {
			return deleted, fmt.Errorf("save state for %s (file already removed from camera folder): %w", filename, err)
		}
	}
	return deleted, nil
}
