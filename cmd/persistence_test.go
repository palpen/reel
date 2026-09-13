package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/pspenano/reel/internal/fault"
	"github.com/pspenano/reel/internal/state"
	"github.com/pspenano/reel/internal/trash"
)

func runConfirmedClean(t *testing.T) int {
	t.Helper()
	input, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if _, err := output.WriteString("yes\n"); err != nil {
		output.Close()
		t.Fatal(err)
	}
	output.Close()
	old := os.Stdin
	os.Stdin = input
	defer func() { os.Stdin = old }()
	return Run([]string{"clean"}, "test")
}

func TestTransferStateSyncFailuresPreserveMediaAndRetry(t *testing.T) {
	for _, route := range []string{"import", "backup", "direct_backup"} {
		for _, target := range []string{"local", "mirror"} {
			for _, step := range []string{"state-file-sync", "state-directory-sync"} {
				t.Run(route+"/"+target+"/"+step, func(t *testing.T) {
					f := commands(t)
					source := filepath.Join(f.card, "clip.MP4")
					f.write(t, source, "original")
					if route == "backup" {
						if err := RunImport(nil); err != nil {
							t.Fatal(err)
						}
					}
					dest := filepath.Join(f.hd, "Footage", "clip.MP4")
					if route == "import" {
						cameras, err := detectCameras(nil)
						if err != nil {
							t.Fatal(err)
						}
						files, err := cameras[0].Walk()
						if err != nil || len(files) != 1 {
							t.Fatalf("camera files: %v, %v", files, err)
						}
						dest = filepath.Join(f.cfg.LaptopDir, files[0].RecordedAt.UTC().Format("2006-01-02_150405"), "clip.MP4")
					}
					calls, failAt := 0, 1
					if target == "mirror" {
						failAt = 2
					}
					fault.Hook = func(name string) error {
						if name == step {
							calls++
							if calls == failAt {
								return fmt.Errorf("injected %s %s", target, step)
							}
						}
						return nil
					}
					defer func() { fault.Hook = nil }()
					if code := Run([]string{route}, "test"); code != 1 || calls != failAt {
						t.Fatalf("sync failure exit=%d calls=%d", code, calls)
					}
					requireBytes(t, source, "original")
					requireBytes(t, dest, "original")
					before, err := os.Stat(dest)
					if err != nil {
						t.Fatal(err)
					}
					fault.Hook = nil
					if code := Run([]string{route}, "test"); code != 0 {
						t.Fatalf("retry exit=%d", code)
					}
					after, err := os.Stat(dest)
					if err != nil || !os.SameFile(before, after) {
						t.Fatalf("retry replaced existing media: %v", err)
					}
					requireBytes(t, source, "original")
					requireBytes(t, dest, "original")
					local, err := state.Load(filepath.Join(f.local, "state.jsonl"))
					if err != nil || local.Len() != 1 {
						t.Fatalf("local state: %v", err)
					}
					mirror, err := state.Load(hdState(f.cfg))
					if err != nil || mirror.Len() != 1 {
						t.Fatalf("mirror state: %v", err)
					}
					row, mirrored := local.All()[0], mirror.All()[0]
					if row.SHA256 == "" || row.SHA256 != mirrored.SHA256 || row.LaptopPath != mirrored.LaptopPath || row.HDPath != mirrored.HDPath || row.HDVolumeUUID != mirrored.HDVolumeUUID {
						t.Fatal("retry failed to reconcile local and mirrored state")
					}
				})
			}
		}
	}
}

func TestRecoveryStateSyncFailuresRetainJournalAndRetry(t *testing.T) {
	for _, route := range []string{"clean", "restore"} {
		for _, step := range []string{"state-file-sync", "state-directory-sync"} {
			t.Run(route+"/"+step, func(t *testing.T) {
				f := commands(t)
				source := filepath.Join(f.card, "clip.MP4")
				f.write(t, source, "original")
				if err := RunDirectBackup(nil); err != nil {
					t.Fatal(err)
				}
				journal := func() string {
					t.Helper()
					paths, err := filepath.Glob(filepath.Join(f.card, ".reel-trash", "*", "recovery.json"))
					if err != nil || len(paths) != 1 {
						t.Fatalf("missing recovery journal: %v, %v", paths, err)
					}
					return paths[0]
				}
				if route == "restore" && runConfirmedClean(t) != 0 {
					t.Fatal("initial clean failed")
				}
				failed := false
				fault.Hook = func(name string) error {
					if name == step {
						failed = true
						return fmt.Errorf("injected %s", step)
					}
					return nil
				}
				defer func() { fault.Hook = nil }()
				var code int
				if route == "clean" {
					code = runConfirmedClean(t)
				} else {
					code = Run([]string{"restore", "--journal", journal()}, "test")
				}
				if code != 1 || !failed {
					t.Fatalf("sync failure exit=%d injected=%v", code, failed)
				}
				path := journal()
				record, err := trash.ReadRecord(path)
				if err != nil {
					t.Fatal(err)
				}
				if route == "clean" {
					requireBytes(t, record.RecoveryPath, "original")
				} else {
					requireBytes(t, source, "original")
				}
				requireBytes(t, filepath.Join(f.hd, "Footage", "clip.MP4"), "original")
				fault.Hook = nil
				if code := Run([]string{"restore", "--journal", path}, "test"); code != 0 {
					t.Fatalf("restore retry exit=%d", code)
				}
				requireBytes(t, source, "original")
				st, err := state.Load(filepath.Join(f.local, "state.jsonl"))
				if err != nil || st.Len() != 1 {
					t.Fatalf("restored state: %v", err)
				}
				row := st.All()[0]
				if row.CleanedAt != nil || row.SHA256 != record.SHA256 {
					t.Fatalf("restored state not reconciled: %+v", row)
				}
			})
		}
	}
}
