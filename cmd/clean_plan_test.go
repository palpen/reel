package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pspenano/reel/internal/camera"
	"github.com/pspenano/reel/internal/config"
	"github.com/pspenano/reel/internal/state"
	"github.com/pspenano/reel/internal/transfer"
)

type cleanFixture struct {
	root  string
	st    *state.Store
	files []camera.File
	now   time.Time
}

func newCleanFixture(t *testing.T) *cleanFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := state.Load(filepath.Join(root, "state.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	f := &cleanFixture{root: root, st: st, now: time.Now().UTC()}
	profile := &config.CameraProfile{Name: "DJI Pocket 3"}
	for _, ext := range []string{"MP4", "LRF", "WAV"} {
		path := filepath.Join(root, "camera", "clip."+ext)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("original "+ext), 0o600); err != nil {
			t.Fatal(err)
		}
		f.files = append(f.files, camera.File{Profile: profile, VolumePath: filepath.Dir(path), FullPath: path, BaseName: "clip", Ext: ext, Size: int64(len("original " + ext))})
	}
	f.backup(t, 0)
	return f
}

func (f *cleanFixture) backup(t *testing.T, index int) *state.Row {
	t.Helper()
	file := f.files[index]
	result, err := transfer.Copy(file.FullPath, filepath.Join(f.root, "hd"), filepath.Base(file.FullPath), time.Time{}, "")
	if err != nil {
		t.Fatal(err)
	}
	r := &state.Row{CameraProfile: file.Profile.Name, BaseName: file.BaseName, Ext: file.Ext, CameraPath: file.FullPath,
		HDPath: result.DestPath, HDVerifiedAt: &f.now, SizeBytes: result.Bytes, SHA256: result.SHA256}
	if err := f.st.Upsert(r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestCleanMP4WithUntrackedLRFAndWAV(t *testing.T) {
	f := newCleanFixture(t)
	before, _ := os.ReadFile(f.st.Path())
	eligible, held := planClean(f.files, f.st, f.now, false)
	if len(eligible) != 2 || eligible[0].file.Ext != "LRF" || eligible[1].file.Ext != "MP4" {
		t.Fatalf("eligible = %+v", eligible)
	}
	if len(held) != 1 || held[0].file.Ext != "WAV" {
		t.Fatalf("held = %+v", held)
	}
	if eligible[0].row == nil || eligible[0].row.HDPath != "" || eligible[0].row.SHA256 != "" {
		t.Fatal("untracked preview must have a row without invented backup/hash")
	}
	after, _ := os.ReadFile(f.st.Path())
	if string(before) != string(after) || f.st.Len() != 1 {
		t.Fatal("planning changed state")
	}
	for _, file := range f.files {
		if _, err := os.Stat(file.FullPath); err != nil {
			t.Fatal("planning changed camera", err)
		}
	}
}

func TestCleanLRFAfterBackupQuarantine(t *testing.T) {
	f := newCleanFixture(t)
	r := &state.Row{CameraProfile: f.files[1].Profile.Name, BaseName: "clip", Ext: "LRF", CameraPath: f.files[1].FullPath, SHA256: "historic hash"}
	if err := f.st.Upsert(r); err != nil {
		t.Fatal(err)
	}
	eligible, _ := planClean(f.files, f.st, f.now, false)
	if len(eligible) != 2 || eligible[0].row.SHA256 != r.SHA256 {
		t.Fatal("quarantined LRF record blocked MP4 cleanup")
	}
}

func TestCleanNeverUsesUnsafeMP4ForPreview(t *testing.T) {
	for _, tc := range []string{"missing backup", "corrupt backup", "stale backup", "empty hash", "reused camera filename", "wrong camera path", "backup aliases camera", "symlink camera"} {
		t.Run(tc, func(t *testing.T) {
			f := newCleanFixture(t)
			r := f.st.GetByParts(f.files[0].Profile.Name, "clip", "MP4")
			switch tc {
			case "missing backup":
				if err := os.Remove(r.HDPath); err != nil {
					t.Fatal(err)
				}
			case "corrupt backup":
				if err := os.WriteFile(r.HDPath, []byte("corrupt! MP4"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "stale backup":
				old := f.now.Add(-8 * 24 * time.Hour)
				r.HDVerifiedAt = &old
			case "empty hash":
				r.SHA256 = ""
			case "reused camera filename":
				if err := os.WriteFile(f.files[0].FullPath, []byte("changed! MP4"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "wrong camera path":
				r.CameraPath += ".other"
			case "backup aliases camera":
				r.HDPath = r.CameraPath
			case "symlink camera":
				if err := os.Remove(r.CameraPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(r.HDPath, r.CameraPath); err != nil {
					t.Fatal(err)
				}
			}
			// Inject legacy fixture data on disk, not by mutating Store-owned pointers.
			data, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			r.SchemaVersion = 1
			data, err = json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(f.st.Path(), append(data, '\n'), 0600); err != nil {
				t.Fatal(err)
			}
			f.st, err = state.Load(f.st.Path())
			if err != nil {
				t.Fatal(err)
			}
			eligible, held := planClean(f.files, f.st, f.now, false)
			if len(eligible) != 0 || len(held) != 3 {
				t.Fatalf("unsafe MP4 allowed cleanup: eligible=%+v held=%+v", eligible, held)
			}
		})
	}
}

func TestCleanOrphanAndUnrelatedLRFStay(t *testing.T) {
	for _, mode := range []string{"MP4 absent", "other profile", "other directory"} {
		t.Run(mode, func(t *testing.T) {
			f := newCleanFixture(t)
			switch mode {
			case "MP4 absent":
				f.files = f.files[1:]
			case "other profile":
				f.files[1].Profile = &config.CameraProfile{Name: "Other"}
			case "other directory":
				f.files[1].FullPath = filepath.Join(f.root, "elsewhere", "clip.LRF")
			}
			_, held := planClean(f.files, f.st, f.now, false)
			found := false
			for _, c := range held {
				if c.file.Ext == "LRF" && strings.Contains(c.decision.Reason, "matching MP4") {
					found = true
				}
			}
			if !found {
				t.Fatal("orphan/unrelated LRF was not held")
			}
		})
	}
}

func TestCleanVerifiedWAVAndForceStale(t *testing.T) {
	f := newCleanFixture(t)
	f.backup(t, 2)
	r := f.st.GetByParts(f.files[0].Profile.Name, "clip", "MP4")
	old := f.now.Add(-8 * 24 * time.Hour)
	r.HDVerifiedAt = &old
	if err := f.st.Upsert(r); err != nil {
		t.Fatal(err)
	}
	eligible, held := planClean(f.files, f.st, f.now, true)
	if len(eligible) != 3 || len(held) != 0 {
		t.Fatal("verified original audio or force-stale failed")
	}
}

func TestCleanDetectsChangeAfterPlanning(t *testing.T) {
	f := newCleanFixture(t)
	eligible, _ := planClean(f.files, f.st, f.now, false)
	if err := os.WriteFile(f.files[0].FullPath, []byte("new footage"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range eligible {
		failed := false
		for _, snapshot := range c.snapshots {
			if snapshot.check() != nil {
				failed = true
			}
		}
		if !failed {
			t.Fatalf("change missed for %s", c.file.Ext)
		}
	}
}

func TestCleanExecutesRecoverablyAndTracksNewPreview(t *testing.T) {
	f := newCleanFixture(t)
	eligible, _ := planClean(f.files, f.st, f.now, false)
	volume := filepath.Join(f.root, "camera")
	count, err := executeCleanPlan(eligible, f.st, volume, true)
	if err != nil || count != 2 {
		t.Fatalf("clean: count=%d err=%v", count, err)
	}
	for _, ext := range []string{"LRF", "MP4"} {
		if _, err := os.Stat(filepath.Join(volume, "clip."+ext)); !os.IsNotExist(err) {
			t.Fatal("media not moved")
		}
		matches, err := filepath.Glob(filepath.Join(volume, ".reel-trash", "*", "clip."+ext))
		if err != nil || len(matches) != 1 {
			t.Fatalf("recovery for %s: %v %v", ext, matches, err)
		}
		data, err := os.ReadFile(matches[0])
		if err != nil || string(data) != "original "+ext {
			t.Fatal("recoverable media content changed")
		}
		r := f.st.GetByParts(f.files[0].Profile.Name, "clip", ext)
		if r == nil || r.CleanedAt == nil {
			t.Fatal("clean event not recorded")
		}
	}
	if _, err := os.Stat(f.files[2].FullPath); err != nil {
		t.Fatal("unbacked WAV was removed")
	}
	r := f.st.GetByParts(f.files[0].Profile.Name, "clip", "LRF")
	if r.HDPath != "" || r.HDVerifiedAt != nil {
		t.Fatal("invented preview backup")
	}
	// Even when the media path is the volume root, recovery files are excluded.
	profile := config.CameraProfile{Name: "DJI Pocket 3", FilenameRegex: `^(?P<base>clip)\.(?P<ext>MP4|LRF|WAV)$`}
	dc := camera.DetectedCamera{Profile: &profile, VolumePath: volume, DCIMPath: volume}
	files, err := dc.Walk()
	if err != nil || len(files) != 1 || files[0].Ext != "WAV" {
		t.Fatalf("recovery files re-entered scan: %+v %v", files, err)
	}
}

func TestCleanMoveFailureKeepsCameraFiles(t *testing.T) {
	f := newCleanFixture(t)
	eligible, _ := planClean(f.files, f.st, f.now, false)
	volume := filepath.Join(f.root, "camera")
	if err := os.WriteFile(filepath.Join(volume, ".reel-trash"), []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	count, err := executeCleanPlan(eligible, f.st, volume, true)
	if err == nil || count != 0 {
		t.Fatal("expected failure before first move")
	}
	for _, file := range f.files {
		if _, err := os.Stat(file.FullPath); err != nil {
			t.Fatal("failure caused media deletion")
		}
	}
	if f.st.Len() != 1 {
		t.Fatal("failed move created a clean record")
	}
}
