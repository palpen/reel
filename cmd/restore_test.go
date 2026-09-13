package cmd

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/pspenano/reel/internal/state"
	"github.com/pspenano/reel/internal/trash"
)

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
