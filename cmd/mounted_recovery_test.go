package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pspenano/reel/internal/fault"
	"github.com/pspenano/reel/internal/volume"
)

// Also runs in ordinary fixtures; the image harness places card and archive on
// exFAT and HFS+ respectively through commands(t).
func TestMountedRecoveryGuards(t *testing.T) {
	for _, kind := range []string{"eligibility", "source-replaced", "backup-replaced", "same-drive"} {
		t.Run(kind, func(t *testing.T) {
			f := commands(t)
			f.cfg.TransferExtensions = []string{"MP4"}
			src := filepath.Join(f.card, "clip.MP4")
			f.write(t, src, "original")
			f.write(t, filepath.Join(f.card, "clip.LRF"), "preview")
			f.write(t, filepath.Join(f.card, "clip.WAV"), "unbacked audio")
			if err := RunDirectBackup(nil); err != nil {
				t.Fatal(err)
			}
			if err := RunVerify(nil); err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(f.hd, "Footage", "clip.MP4")
			if kind == "same-drive" {
				old := resolveVolume
				resolveVolume = func(path string) (id volume.Identity, err error) {
					id, err = old(path)
					id.Physical = []string{"same-physical-drive"}
					return
				}
			} else if kind != "eligibility" {
				changed := false
				fault.Hook = func(step string) error {
					if step != "media-move" || changed {
						return nil
					}
					changed = true
					path := src
					if kind == "backup-replaced" {
						path = archive
					}
					if err := os.Rename(path, path+".old"); err != nil {
						return err
					}
					return os.WriteFile(path, []byte("original"), 0600)
				}
				t.Cleanup(func() { fault.Hook = nil })
			}
			code := runConfirmedClean(t)
			if kind == "eligibility" {
				if code != 0 {
					t.Fatal("clean failed")
				}
				for _, ext := range []string{"MP4", "LRF"} {
					if _, err := os.Stat(filepath.Join(f.card, "clip."+ext)); !os.IsNotExist(err) {
						t.Fatal("eligible file not moved", ext, err)
					}
				}
			} else {
				if code != 1 {
					t.Fatal("guard failed", code)
				}
				requireBytes(t, src, "original")
				requireBytes(t, filepath.Join(f.card, "clip.LRF"), "preview")
			}
			requireBytes(t, archive, "original")
			requireBytes(t, filepath.Join(f.card, "clip.WAV"), "unbacked audio")
		})
	}
}
