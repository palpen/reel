package cmd

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/pspenano/reel/internal/fault"
	"github.com/pspenano/reel/internal/state"
	"github.com/pspenano/reel/internal/trash"
)

func TestRestoreInitializesLocalDirectorySafely(t *testing.T) {
	for _, setup := range []string{"fresh", "symlink", "sync-failure"} {
		t.Run(setup, func(t *testing.T) {
			f := commands(t)
			source := filepath.Join(f.card, "clip.MP4")
			f.write(t, source, "recover me")
			recovered, err := trash.MoveOnVolume(source, f.card, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			journal := filepath.Join(filepath.Dir(recovered), "recovery.json")
			f.local = filepath.Join(f.root, "fresh-home", ".config", "reel")
			if setup == "symlink" {
				if err := os.Symlink(f.root, filepath.Join(f.root, "fresh-home")); err != nil {
					t.Fatal(err)
				}
			}
			if setup == "sync-failure" {
				fault.Hook = func(step string) error {
					if step == "directory-sync" {
						return syscall.EIO
					}
					return nil
				}
				t.Cleanup(func() { fault.Hook = nil })
			}
			err = RunRestore([]string{"--journal", journal})
			if setup != "fresh" {
				if err == nil {
					t.Fatal("unsafe local initialization accepted")
				}
				requireBytes(t, recovered, "recover me")
				if _, err := os.Stat(source); !os.IsNotExist(err) {
					t.Fatal("restored before local initialization succeeded", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			requireBytes(t, source, "recover me")
			if _, err := os.Stat(filepath.Join(f.local, "reel.lock")); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"config.json", "state.jsonl"} {
				if _, err := os.Stat(filepath.Join(f.local, name)); !os.IsNotExist(err) {
					t.Fatal("invented local configuration or history", err)
				}
			}
			if err := RunRestore([]string{"--journal", journal}); err != nil {
				t.Fatal("restore retry failed", err)
			}
		})
	}
}

func TestRestoreBindsOnlyUntransferredPreviewIdentity(t *testing.T) {
	for _, kind := range []string{"clean-only", "already-restored", "historical-hash", "hashless-archive"} {
		t.Run(kind, func(t *testing.T) {
			f := commands(t)
			source := filepath.Join(f.card, "clip.LRF")
			f.write(t, source, "preview")
			recovered, err := trash.MoveOnVolume(source, f.card, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			journal := filepath.Join(filepath.Dir(recovered), "recovery.json")
			record, err := trash.ReadRecord(journal)
			if err != nil {
				t.Fatal(err)
			}
			st, err := state.Load(filepath.Join(f.local, "state.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			row := &state.Row{CameraProfile: "fixture", BaseName: "clip", Ext: "LRF",
				CameraPath: source, SizeBytes: record.SizeBytes, CleanedAt: state.NowPtr()}
			if kind == "historical-hash" {
				row.SHA256 = "existing-canonical-identity"
			}
			if kind == "hashless-archive" {
				row.LaptopPath = filepath.Join(f.cfg.LaptopDir, "clip.LRF")
				f.write(t, row.LaptopPath, "older archive")
			}
			if kind == "already-restored" {
				// A prior build may have restored the bytes without binding state.
				if err := trash.Restore(journal); err != nil {
					t.Fatal(err)
				}
				row.CleanedAt = nil
			}
			if err := st.Upsert(row); err != nil {
				t.Fatal(err)
			}
			if err := RunRestore([]string{"--journal", journal}); err != nil {
				t.Fatal(err)
			}
			st, err = state.Load(st.Path())
			if err != nil {
				t.Fatal(err)
			}
			got := st.Get(row.Key())
			wantHash := row.SHA256
			if kind == "clean-only" || kind == "already-restored" {
				wantHash = record.SHA256
			}
			if got.SHA256 != wantHash || got.SizeBytes != record.SizeBytes || got.CleanedAt != nil || got.LaptopPath != row.LaptopPath || got.HDPath != "" || got.HDVerifiedAt != nil {
				t.Fatalf("restore changed archive history or missed preview identity: %+v", got)
			}
			requireBytes(t, source, "preview")
			if kind == "historical-hash" || kind == "hashless-archive" {
				f.cfg.TransferExtensions = []string{"LRF"}
				if code := Run([]string{"import"}, "test"); code != 1 {
					t.Fatalf("invalid archive identity exit=%d", code)
				}
			}
			if kind == "hashless-archive" {
				requireBytes(t, row.LaptopPath, "older archive")
			}
		})
	}
}
