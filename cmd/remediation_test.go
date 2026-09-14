package cmd

import (
	"fmt"
	"github.com/pspenano/reel/internal/camera"
	"github.com/pspenano/reel/internal/config"
	"github.com/pspenano/reel/internal/fault"
	"github.com/pspenano/reel/internal/state"
	"github.com/pspenano/reel/internal/volume"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type commandFixture struct {
	root, card, hd, local string
	cfg                   *config.Config
}

func commands(t *testing.T) *commandFixture {
	t.Helper()
	root, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	f := &commandFixture{root: root, card: filepath.Join(root, "card"), hd: filepath.Join(root, "hd"), local: filepath.Join(root, "config")}
	// Mounted-image validation puts the camera and archive on different actual
	// filesystems. Production identity resolution is tested separately.
	for _, target := range []struct {
		env  string
		path *string
	}{
		{"REEL_TEST_CARD_ROOT", &f.card}, {"REEL_TEST_ARCHIVE_ROOT", &f.hd},
	} {
		if mount := os.Getenv(target.env); mount != "" {
			p, err := os.MkdirTemp(mount, "reel-command-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(p) })
			*target.path = filepath.Join(p, filepath.Base(*target.path))
		}
	}
	for _, p := range []string{f.card, f.hd, f.local, filepath.Join(root, "laptop")} {
		if e := os.Mkdir(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	f.cfg = &config.Config{LaptopDir: filepath.Join(root, "laptop"), HDVolumeName: "fixture", HDVolumeUUID: "backup-id", HDDir: "Footage", SoftDelete: true, Cameras: []config.CameraProfile{{Name: "fixture", VolumeName: "card", MediaPath: ".", FilenameRegex: `^(?P<base>clip)\.(?P<ext>MP4|LRF|WAV)$`}}}
	oldLoad, oldDir, oldDetect, oldResolve, oldCheck, oldHD := loadConfig, configDir, detectCameras, resolveVolume, checkVolume, hdRoot
	t.Cleanup(func() {
		loadConfig = oldLoad
		configDir = oldDir
		detectCameras = oldDetect
		resolveVolume = oldResolve
		checkVolume = oldCheck
		hdRoot = oldHD
	})
	loadConfig = func() (*config.Config, error) { return f.cfg, nil }
	configDir = func() (string, error) { return f.local, nil }
	hdRoot = func(*config.Config) string { return f.hd }
	detectCameras = func([]config.CameraProfile) ([]camera.DetectedCamera, error) {
		return []camera.DetectedCamera{{Profile: &f.cfg.Cameras[0], VolumePath: f.card, DCIMPath: f.card}}, nil
	}
	resolveVolume = func(path string) (volume.Identity, error) {
		if path == f.hd {
			return volume.Identity{UUID: "backup-id", Device: 2, Mount: path, Physical: []string{"disk2"}}, nil
		}
		if path == f.card {
			return volume.Identity{UUID: "camera-id", Device: 3, Mount: path, Physical: []string{"disk3"}}, nil
		}
		return volume.Identity{}, fmt.Errorf("unknown fixture volume")
	}
	checkVolume = func(volume.Identity) error { return nil }
	return f
}
func (f *commandFixture) write(t *testing.T, path, data string) {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, []byte(data), 0600); e != nil {
		t.Fatal(e)
	}
}
func requireBytes(t *testing.T, path, want string) {
	t.Helper()
	data, e := os.ReadFile(path)
	if e != nil || string(data) != want {
		t.Fatalf("preservation failed at %s: %q %v", path, data, e)
	}
}
func TestCommandsCollisionPreservesArchive(t *testing.T) {
	for _, route := range []string{"import", "backup", "direct_backup"} {
		t.Run(route, func(t *testing.T) {
			f := commands(t)
			f.write(t, filepath.Join(f.card, "clip.MP4"), "new media")
			dest := filepath.Join(f.hd, "Footage", "clip.MP4")
			if route == "import" {
				dc, _ := detectCameras(nil)
				files, e := dc[0].Walk()
				if e != nil || len(files) != 1 {
					t.Fatal(files, e)
				}
				dest = filepath.Join(f.cfg.LaptopDir, files[0].RecordedAt.UTC().Format("2006-01-02_150405"), "clip.MP4")
			}
			if route == "backup" {
				if e := RunImport(nil); e != nil {
					t.Fatal(e)
				}
			}
			f.write(t, dest, "sole older archive")
			if code := Run([]string{route}, "test"); code == 0 {
				t.Fatal("collision reported success")
			}
			requireBytes(t, dest, "sole older archive")
			requireBytes(t, filepath.Join(f.card, "clip.MP4"), "new media")
		})
	}
}
func TestMissingCopyRecreatedAndCanonicalConflict(t *testing.T) {
	f := commands(t)
	src := filepath.Join(f.card, "clip.MP4")
	f.write(t, src, "original")
	if e := RunDirectBackup(nil); e != nil {
		t.Fatal(e)
	}
	dest := filepath.Join(f.hd, "Footage", "clip.MP4")
	if e := os.Remove(dest); e != nil {
		t.Fatal(e)
	}
	if e := RunDirectBackup(nil); e != nil {
		t.Fatal(e)
	}
	requireBytes(t, dest, "original")
	f.write(t, src, "modified")
	if e := RunDirectBackup(nil); e == nil {
		t.Fatal("changed recording accepted")
	}
	requireBytes(t, dest, "original")
}
func TestVerifyMissingRevokesAndFails(t *testing.T) {
	f := commands(t)
	f.write(t, filepath.Join(f.card, "clip.MP4"), "original")
	if e := RunDirectBackup(nil); e != nil {
		t.Fatal(e)
	}
	os.Remove(filepath.Join(f.hd, "Footage", "clip.MP4"))
	if e := RunVerify(nil); e == nil {
		t.Fatal("missing file succeeded")
	}
	st, e := state.Load(filepath.Join(f.local, "state.jsonl"))
	if e != nil {
		t.Fatal(e)
	}
	if st.All()[0].HDVerifiedAt != nil {
		t.Fatal("missing backup retains verification")
	}
}
func TestInvalidArgumentsBeforeDependencies(t *testing.T) {
	old := loadConfig
	defer func() { loadConfig = old }()
	loadConfig = func() (*config.Config, error) { t.Fatal("invalid arguments reached setup"); return nil, nil }
	for _, name := range []string{"import", "backup", "direct_backup", "verify", "clean", "status", "history", "config"} {
		if code := Run([]string{name, "camera-name", "--dry-run"}, "test"); code != 2 {
			t.Fatalf("%s exit=%d", name, code)
		}
	}
	if code := Run([]string{"clean", "--permanent"}, "test"); code != 2 {
		t.Fatal(code)
	}
}
func TestDryRunDoesNotMoveOrMirror(t *testing.T) {
	f := commands(t)
	f.write(t, filepath.Join(f.card, "clip.MP4"), "original")
	if e := RunDirectBackup(nil); e != nil {
		t.Fatal(e)
	}
	local, _ := os.ReadFile(filepath.Join(f.local, "state.jsonl"))
	mirror, _ := os.ReadFile(hdState(f.cfg))
	if e := RunClean([]string{"--dry-run"}); e != nil {
		t.Fatal(e)
	}
	requireBytes(t, filepath.Join(f.card, "clip.MP4"), "original")
	requireBytes(t, filepath.Join(f.local, "state.jsonl"), string(local))
	requireBytes(t, hdState(f.cfg), string(mirror))
	if _, e := os.Stat(filepath.Join(f.card, ".reel-trash")); !os.IsNotExist(e) {
		t.Fatal("dry run created recovery")
	}
}
func TestPreservedMtimeBackupChangeStopsClean(t *testing.T) {
	f := newCleanFixture(t)
	eligible, _ := planClean(f.files, f.st, f.now, false)
	r := f.st.All()[0]
	before, e := os.Stat(r.HDPath)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(r.HDPath, []byte("modified MP4"), 0600); e != nil {
		t.Fatal(e)
	}
	os.Chtimes(r.HDPath, time.Now(), before.ModTime())
	if n, e := executeCleanPlan(eligible, f.st, filepath.Join(f.root, "camera"), true); e == nil || n != 0 {
		t.Fatal("changed backup authorized cleanup")
	}
	requireBytes(t, f.files[0].FullPath, "original MP4")
}
func TestPermanentModeRejected(t *testing.T) {
	f := newCleanFixture(t)
	eligible, _ := planClean(f.files, f.st, f.now, true)
	if n, e := executeCleanPlan(eligible, f.st, filepath.Join(f.root, "camera"), false); e == nil || n != 0 {
		t.Fatal("permanent mode available")
	}
	requireBytes(t, f.files[0].FullPath, "original MP4")
}

func TestNoWorkImportPreservesUnownedTemporary(t *testing.T) {
	f := commands(t)
	path := filepath.Join(f.cfg.LaptopDir, "only-recording.MP4.tmp")
	f.write(t, path, "only copy")
	if err := RunImport(nil); err != nil {
		t.Fatal(err)
	}
	requireBytes(t, path, "only copy")
}
func TestCommandWorkflowsRestore(t *testing.T) {
	for _, route := range []string{"import-backup", "direct"} {
		t.Run(route, func(t *testing.T) {
			f := commands(t)
			f.write(t, filepath.Join(f.card, "clip.MP4"), "video")
			f.write(t, filepath.Join(f.card, "clip.LRF"), "preview")
			f.write(t, filepath.Join(f.card, "clip.WAV"), "audio")
			f.cfg.TransferExtensions = []string{"MP4", "WAV"}
			if route == "direct" {
				if e := RunDirectBackup(nil); e != nil {
					t.Fatal(e)
				}
			} else {
				if e := RunImport(nil); e != nil {
					t.Fatal(e)
				}
				if e := RunBackup(nil); e != nil {
					t.Fatal(e)
				}
			}
			if e := RunVerify(nil); e != nil {
				t.Fatal(e)
			}
			input, e := os.CreateTemp(f.root, "confirmation-")
			if e != nil {
				t.Fatal(e)
			}
			input.WriteString("yes\n")
			input.Seek(0, 0)
			old := os.Stdin
			os.Stdin = input
			defer func() { os.Stdin = old; input.Close() }()
			if e := RunClean(nil); e != nil {
				t.Fatal(e)
			}
			journals, e := filepath.Glob(filepath.Join(f.card, ".reel-trash", "*", "recovery.json"))
			if e != nil || len(journals) != 3 {
				t.Fatal("missing recoveries", journals, e)
			}
			for _, path := range journals {
				if e := RunRestore([]string{"--journal", path}); e != nil {
					t.Fatal(e)
				}
			}
			requireBytes(t, filepath.Join(f.card, "clip.MP4"), "video")
			requireBytes(t, filepath.Join(f.card, "clip.LRF"), "preview")
			requireBytes(t, filepath.Join(f.card, "clip.WAV"), "audio")

			// A preview first tracked by clean must be transferable after restore.
			f.cfg.TransferExtensions = []string{"MP4", "LRF", "WAV"}
			transferRoute := "direct_backup"
			if route == "import-backup" {
				transferRoute = "import"
			}
			if code := Run([]string{transferRoute}, "test"); code != 0 {
				t.Fatalf("transfer after restore exit=%d", code)
			}
			if route == "import-backup" {
				if err := RunBackup(nil); err != nil {
					t.Fatal(err)
				}
			}
			requireBytes(t, filepath.Join(f.hd, "Footage", "clip.LRF"), "preview")
			// The restored identity must still reject a changed preview.
			f.write(t, filepath.Join(f.card, "clip.LRF"), "changed")
			if code := Run([]string{transferRoute}, "test"); code != 1 {
				t.Fatalf("changed preview exit=%d", code)
			}
			requireBytes(t, filepath.Join(f.hd, "Footage", "clip.LRF"), "preview")
		})
	}
}

func TestStateAndMirrorFailureDoNotReportSuccess(t *testing.T) {
	for _, step := range []string{"state-save", "mirror-save"} {
		t.Run(step, func(t *testing.T) {
			f := commands(t)
			f.write(t, filepath.Join(f.card, "clip.MP4"), "original")
			fault.Hook = func(name string) error {
				if name == step {
					return fmt.Errorf("injected %s", step)
				}
				return nil
			}
			defer func() { fault.Hook = nil }()
			if code := Run([]string{"direct_backup"}, "test"); code != 1 {
				t.Fatalf("failure exit=%d", code)
			}
			requireBytes(t, filepath.Join(f.card, "clip.MP4"), "original")
			requireBytes(t, filepath.Join(f.hd, "Footage", "clip.MP4"), "original")
		})
	}
}

func TestBackupSkipRequiresSelectedArchive(t *testing.T) {
	for _, route := range []string{"backup", "direct_backup"} {
		for _, mismatch := range []string{"outside-root", "wrong-uuid", "unbound"} {
			t.Run(route+"/"+mismatch, func(t *testing.T) {
				f := commands(t)
				f.write(t, filepath.Join(f.card, "clip.MP4"), "original")
				if err := RunImport(nil); err != nil {
					t.Fatal(err)
				}
				if err := RunDirectBackup(nil); err != nil {
					t.Fatal(err)
				}
				st, err := state.Load(filepath.Join(f.local, "state.jsonl"))
				if err != nil {
					t.Fatal(err)
				}
				r := st.All()[0]
				if mismatch == "outside-root" {
					r.HDPath = filepath.Join(f.root, "old-drive", "clip.MP4")
					f.write(t, r.HDPath, "original")
				} else if mismatch == "wrong-uuid" {
					r.HDVolumeUUID = "old-drive"
				} else {
					r.HDVolumeUUID = ""
				}
				if err = st.Upsert(r); err != nil {
					t.Fatal(err)
				}
				before, err := os.ReadFile(st.Path())
				if err != nil {
					t.Fatal(err)
				}
				if code := Run([]string{route}, "test"); code != 1 {
					t.Fatalf("identity conflict exit=%d", code)
				}
				requireBytes(t, st.Path(), string(before))
				requireBytes(t, r.HDPath, "original")
			})
		}
	}
}

func TestTransferRetryRepairsMirror(t *testing.T) {
	for _, route := range []string{"import", "backup", "direct_backup"} {
		selections := []string{"unchanged"}
		if route != "backup" {
			selections = append(selections, "no-camera", "empty-card", "excluded")
		}
		for _, selection := range selections {
			t.Run(route+"/"+selection, func(t *testing.T) {
				f := commands(t)
				src := filepath.Join(f.card, "clip.MP4")
				f.write(t, src, "original")
				if route == "backup" {
					if err := RunImport(nil); err != nil {
						t.Fatal(err)
					}
					if err := os.Remove(hdState(f.cfg)); err != nil {
						t.Fatal(err)
					}
				}
				fault.Hook = func(name string) error {
					if name == "mirror-save" {
						return fmt.Errorf("injected mirror failure")
					}
					return nil
				}
				defer func() { fault.Hook = nil }()
				if code := Run([]string{route}, "test"); code != 1 {
					t.Fatalf("mirror failure exit=%d", code)
				}
				local, err := state.Load(filepath.Join(f.local, "state.jsonl"))
				if err != nil || local.Len() != 1 {
					t.Fatalf("missing local state: %v", err)
				}
				row := local.All()[0]
				dest := row.HDPath
				if route == "import" {
					dest = row.LaptopPath
				}
				before, err := os.Stat(dest)
				if err != nil {
					t.Fatal(err)
				}
				switch selection {
				case "no-camera":
					detectCameras = func([]config.CameraProfile) ([]camera.DetectedCamera, error) { return nil, nil }
				case "empty-card":
					if err := os.Remove(src); err != nil {
						t.Fatal(err)
					}
				case "excluded":
					f.cfg.TransferExtensions = []string{}
				}
				if code := Run([]string{route}, "test"); code != 1 {
					t.Fatalf("skipped copy ignored ongoing mirror failure: %d", code)
				}
				fault.Hook = nil
				if code := Run([]string{route}, "test"); code != 0 {
					t.Fatalf("mirror retry exit=%d", code)
				}
				mirrored, err := state.Load(hdState(f.cfg))
				if err != nil || mirrored.Len() != 1 {
					t.Fatalf("missing mirrored state: %v", err)
				}
				got := mirrored.All()[0]
				if got.SHA256 != row.SHA256 || got.LaptopPath != row.LaptopPath || got.HDPath != row.HDPath || got.HDVolumeUUID != row.HDVolumeUUID {
					t.Fatal("repaired mirror differs from committed transfer")
				}
				after, err := os.Stat(dest)
				if err != nil || !os.SameFile(before, after) {
					t.Fatal("retry recopied verified media")
				}
				requireBytes(t, dest, "original")
			})
		}
	}
}

func TestImportWithDisconnectedOptionalMirror(t *testing.T) {
	f := commands(t)
	f.write(t, filepath.Join(f.card, "clip.MP4"), "original")
	if err := os.Rename(f.hd, f.hd+"-offline"); err != nil {
		t.Fatal(err)
	}
	fault.Hook = func(step string) error {
		if step == "mirror-save" {
			t.Fatal("attempted a mirror write to a disconnected drive")
		}
		return nil
	}
	defer func() { fault.Hook = nil }()
	// Both a new import and an already-imported retry work without the drive.
	for i := 0; i < 2; i++ {
		if code := Run([]string{"import"}, "test"); code != 0 {
			t.Fatalf("disconnected optional mirror exit=%d", code)
		}
	}
	local, err := state.Load(filepath.Join(f.local, "state.jsonl"))
	if err != nil || local.Len() != 1 {
		t.Fatalf("missing imported state: %v", err)
	}
	requireBytes(t, local.All()[0].LaptopPath, "original")
	if _, err := os.Stat(f.hd); !os.IsNotExist(err) {
		t.Fatalf("import recreated the disconnected volume path: %v", err)
	}
}
