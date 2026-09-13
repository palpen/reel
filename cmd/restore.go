package cmd

import (
	"flag"
	"fmt"
	"github.com/pspenano/reel/internal/lockfile"
	"github.com/pspenano/reel/internal/state"
	"github.com/pspenano/reel/internal/trash"
	"path/filepath"
)

func RunRestore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	journal := fs.String("journal", "", "path to recovery.json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *journal == "" {
		return fmt.Errorf("restore requires --journal and no positional arguments")
	}
	r, err := trash.ReadRecord(*journal)
	if err != nil {
		return err
	}
	dir, err := configDir()
	if err != nil {
		return err
	}
	lk, err := lockfile.AcquireExclusive(filepath.Join(dir, "reel.lock"))
	if err != nil {
		return err
	}
	defer lk.Release()
	card, err := lockfile.AcquireExclusive(filepath.Join(r.Volume, "reel.lock"))
	if err != nil {
		return err
	}
	defer card.Release()
	st, err := state.Load(filepath.Join(dir, "state.jsonl"))
	if err != nil {
		return err
	}
	if err = trash.Restore(*journal); err != nil {
		return err
	}
	for _, row := range st.All() {
		if row.CameraPath == r.OriginalPath {
			// Clean can record an untransferred preview without a canonical hash.
			// Restore has verified its bytes against the journal, so use that
			// identity for later transfers without claiming any archive copy.
			if row.Ext == "LRF" && row.SHA256 == "" && row.LaptopPath == "" && row.HDPath == "" {
				row.SHA256 = r.SHA256
				row.SizeBytes = r.SizeBytes
			}
			row.CleanedAt = nil
			if err = st.Upsert(row); err != nil {
				return fmt.Errorf("media restored; state update failed: %w", err)
			}
		}
	}
	fmt.Printf("Restored: %s\n", r.OriginalPath)
	return nil
}
