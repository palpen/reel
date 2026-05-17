package cmd

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pspenano/reel/internal/camera"
	"github.com/pspenano/reel/internal/clean"
	"github.com/pspenano/reel/internal/config"
	"github.com/pspenano/reel/internal/display"
	"github.com/pspenano/reel/internal/lockfile"
	"github.com/pspenano/reel/internal/state"
	"github.com/pspenano/reel/internal/transfer"
	"github.com/pspenano/reel/internal/trash"
)

const defaultStaleThreshold = 7 * 24 * time.Hour

// RunClean implements `reel clean`.
func RunClean(args []string) error {
	fs := flag.NewFlagSet("clean", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "show what would be deleted without deleting")
	forceStale := fs.Bool("force-stale", false, "ignore stale verification threshold")
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
	files, err := dc.Walk()
	if err != nil {
		return fmt.Errorf("walk DCIM: %w", err)
	}

	now := time.Now().UTC()

	// Build per-base_name decision map
	// key = profile+base_name, value = list of (ext, file, decision)
	type candidate struct {
		file     camera.File
		row      *state.Row
		decision clean.Decision
	}
	type baseGroup struct {
		candidates []candidate
	}
	groups := make(map[string]*baseGroup)

	for _, f := range files {
		f := f // capture
		row := st.GetByParts(f.Profile.Name, f.BaseName, f.Ext)
		if row == nil {
			// Not tracked — can't delete
			key := f.Profile.Name + "\x00" + f.BaseName
			if groups[key] == nil {
				groups[key] = &baseGroup{}
			}
			groups[key].candidates = append(groups[key].candidates, candidate{
				file: f,
				row:  nil,
				decision: clean.Decision{
					Delete: false,
					Reason: "not tracked in state",
				},
			})
			continue
		}

		// Re-stat and re-hash HD copy
		var hdExists bool
		var hdSize int64
		var hdHash string
		var hdVerifiedAt time.Time

		if row.HDPath != "" {
			if info, err := os.Stat(row.HDPath); err == nil {
				hdExists = true
				hdSize = info.Size()
			}
			if hdExists {
				h, _, err := transfer.HashFile(row.HDPath)
				if err == nil {
					hdHash = h
				}
			}
		}
		if row.HDVerifiedAt != nil {
			hdVerifiedAt = *row.HDVerifiedAt
		}

		fs := clean.FileState{
			CameraProfile:  row.CameraProfile,
			BaseName:       row.BaseName,
			Ext:            row.Ext,
			CameraPath:     row.CameraPath,
			HDPath:         row.HDPath,
			HDFileExists:   hdExists,
			HDFileSize:     hdSize,
			StateSize:      row.SizeBytes,
			HDFileSHA256:   hdHash,
			StateSHA256:    row.SHA256,
			HDVerifiedAt:   hdVerifiedAt,
			Now:            now,
			StaleThreshold: defaultStaleThreshold,
			ForceStale:     *forceStale,
		}
		dec := clean.ShouldDelete(fs)

		key := f.Profile.Name + "\x00" + f.BaseName
		if groups[key] == nil {
			groups[key] = &baseGroup{}
		}
		groups[key].candidates = append(groups[key].candidates, candidate{
			file:     f,
			row:      row,
			decision: dec,
		})
	}

	// B1: all siblings must pass before any are deleted
	type deleteItem struct {
		file camera.File
		row  *state.Row
	}
	var toDelete []deleteItem
	var heldBack []candidate

	for _, grp := range groups {
		// Check if all candidates pass
		allPass := true
		for _, c := range grp.candidates {
			if !c.decision.Delete {
				allPass = false
				break
			}
		}
		if allPass {
			for _, c := range grp.candidates {
				toDelete = append(toDelete, deleteItem{c.file, c.row})
			}
		} else {
			for _, c := range grp.candidates {
				if !c.decision.Delete {
					heldBack = append(heldBack, c)
				}
			}
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
		display.Print("\nNothing to delete.")
		return nil
	}

	display.Print("\nFiles eligible for deletion (%d):", len(toDelete))
	for _, d := range toDelete {
		display.Print("  %s", d.file.BaseName+"."+d.file.Ext)
	}

	if *dryRun {
		display.Print("\n--dry-run: no files deleted.")
		return nil
	}

	// Confirm
	if !cfg.SoftDelete {
		fmt.Printf("\nWARNING: soft_delete is disabled. Files will be permanently deleted.\n")
	}
	fmt.Printf("\nDelete %d files? [y/N]: ", len(toDelete))
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer != "y" && answer != "yes" {
		display.Print("Aborted.")
		return nil
	}

	// Delete
	deleteTs := time.Now()
	var deleted, failedDel int
	for _, d := range toDelete {
		filename := d.file.BaseName + "." + d.file.Ext
		var delErr error
		if cfg.SoftDelete {
			_, delErr = trash.Move(d.file.FullPath, deleteTs)
		} else {
			delErr = os.Remove(d.file.FullPath)
		}
		if delErr != nil {
			display.Error("delete %s: %v", filename, delErr)
			failedDel++
			continue
		}
		now := time.Now().UTC()
		d.row.CleanedAt = &now
		if err := st.Upsert(d.row); err != nil {
			display.Error("save state for %s: %v", filename, err)
		}
		deleted++
	}

	display.Print("\nClean complete: %d deleted, %d failed.", deleted, failedDel)
	mirrorStateToHD(cfg, st)
	return nil
}
