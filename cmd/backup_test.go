package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pspenano/reel/internal/state"
	"github.com/pspenano/reel/internal/transfer"
)

func TestBackupAfterLaptopCopyRemoval(t *testing.T) {
	for _, archive := range []string{"valid", "missing-parent", "missing-archive", "unrecorded", "corrupt", "wrong-size", "outside-root", "wrong-uuid", "unbound", "symlink"} {
		t.Run(archive, func(t *testing.T) {
			f := commands(t)
			f.write(t, filepath.Join(f.card, "clip.MP4"), "archived media")
			if err := RunImport(nil); err != nil {
				t.Fatal(err)
			}
			if err := RunBackup(nil); err != nil {
				t.Fatal(err)
			}
			st, err := state.Load(filepath.Join(f.local, "state.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			old := st.All()[0]
			if err := os.Remove(old.LaptopPath); err != nil {
				t.Fatal(err)
			}
			switch archive {
			case "missing-parent":
				// Session folders also hold Reel metadata. Move the entire folder
				// aside to simulate removing a completed import from the laptop.
				if err := os.Rename(filepath.Dir(old.LaptopPath), filepath.Join(f.root, "old-session")); err != nil {
					t.Fatal(err)
				}
			case "missing-archive":
				if err := os.Remove(old.HDPath); err != nil {
					t.Fatal(err)
				}
			case "unrecorded":
				old.HDPath = ""
			case "corrupt":
				f.write(t, old.HDPath, "modified media")
			case "wrong-size":
				old.SizeBytes++
			case "outside-root":
				old.HDPath = filepath.Join(f.root, "other-archive", "clip.MP4")
				f.write(t, old.HDPath, "archived media")
			case "wrong-uuid":
				old.HDVolumeUUID = "other-drive"
			case "unbound":
				old.HDVolumeUUID = ""
			case "symlink":
				if err := os.Rename(old.HDPath, old.HDPath+".retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(old.HDPath+".retained", old.HDPath); err != nil {
					t.Fatal(err)
				}
			}
			if err := st.Upsert(old); err != nil {
				t.Fatal(err)
			}
			src := filepath.Join(f.cfg.LaptopDir, "new.MP4")
			f.write(t, src, "new recording")
			hash, size, err := transfer.HashFile(src)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.Upsert(&state.Row{CameraProfile: "fixture", BaseName: "new", Ext: "MP4", LaptopPath: src, SHA256: hash, SizeBytes: size}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(st.Path())
			if err != nil {
				t.Fatal(err)
			}
			err = RunBackup(nil)
			if archive != "valid" && archive != "missing-parent" {
				if err == nil {
					t.Fatal("accepted an invalid archive without its source")
				}
				requireBytes(t, st.Path(), string(before))
				if _, err := os.Stat(filepath.Join(f.hd, "Footage", "new.MP4")); !os.IsNotExist(err) {
					t.Fatal("published before archive validation", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			requireBytes(t, filepath.Join(f.hd, "Footage", "new.MP4"), "new recording")
			requireBytes(t, old.HDPath, "archived media")
			mirror, err := state.Load(hdState(f.cfg))
			if err != nil || mirror.Len() != 2 {
				t.Fatalf("mirror not updated: %v", err)
			}
			if got := mirror.Get(old.Key()); got.LaptopPath != old.LaptopPath || got.HDPath != old.HDPath || got.SHA256 != old.SHA256 {
				t.Fatal("historical identity changed")
			}
			if err := RunBackup(nil); err != nil {
				t.Fatalf("no-copy retry failed: %v", err)
			}
		})
	}
}
