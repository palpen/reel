package trash

import (
	"encoding/json"
	"github.com/pspenano/reel/internal/fault"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestMoveOnVolumeRecoverableAndCollisionFree(t *testing.T) {
	volume, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(volume, "clip.LRF")
	var first string
	for i := 0; i < 2; i++ {
		contents := []byte{byte(i)}
		if err := os.WriteFile(src, contents, 0o600); err != nil {
			t.Fatal(err)
		}
		before, _ := os.Stat(src)
		dest, err := MoveOnVolume(src, volume, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		after, err := os.Stat(dest)
		if err != nil || !os.SameFile(before, after) {
			t.Fatal("move did not preserve file identity")
		}
		if _, err := os.Stat(src); !os.IsNotExist(err) {
			t.Fatal("source was not moved")
		}
		data, err := os.ReadFile(filepath.Join(filepath.Dir(dest), "recovery.json"))
		if err != nil {
			t.Fatal(err)
		}
		var metadata map[string]any
		if err := json.Unmarshal(data, &metadata); err != nil {
			t.Fatal(err)
		}
		if metadata["original_path"] != src || metadata["recovery_path"] != dest {
			t.Fatal("missing original/recovery path")
		}
		if i == 0 {
			first = dest
		} else {
			if dest == first {
				t.Fatal("reused recovery path")
			}
			data, err := os.ReadFile(first)
			if err != nil || len(data) != 1 || data[0] != 0 {
				t.Fatal("overwrote previous recovery")
			}
			// Restore through the saved paths and confirm byte-for-byte recovery.
			if err := os.Rename(dest, src); err != nil {
				t.Fatal(err)
			}
			data, err = os.ReadFile(src)
			if err != nil || len(data) != 1 || data[0] != 1 {
				t.Fatal("restore failed")
			}
		}
	}
}

func TestMoveOnVolumeRejectsUnsafePaths(t *testing.T) {
	for _, mode := range []string{"outside volume", "source symlink", "recovery symlink", "recovery is a file"} {
		t.Run(mode, func(t *testing.T) {
			root, _ := filepath.EvalSymlinks(t.TempDir())
			volume := filepath.Join(root, "card")
			if err := os.Mkdir(volume, 0o700); err != nil {
				t.Fatal(err)
			}
			src := filepath.Join(volume, "clip.MP4")
			if mode == "outside volume" {
				src = filepath.Join(root, "clip.MP4")
			}
			if err := os.WriteFile(src, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "source symlink":
				link := filepath.Join(volume, "link.MP4")
				if err := os.Symlink(src, link); err != nil {
					t.Fatal(err)
				}
				src = link
			case "recovery symlink":
				if err := os.Symlink(root, filepath.Join(volume, ".reel-trash")); err != nil {
					t.Fatal(err)
				}
			case "recovery is a file":
				if err := os.WriteFile(filepath.Join(volume, ".reel-trash"), []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := MoveOnVolume(src, volume, time.Now()); err == nil {
				t.Fatal("unsafe move allowed")
			}
			if data, err := os.ReadFile(src); err != nil || string(data) != "keep" {
				t.Fatal("source changed on failure")
			}
		})
	}
}

func TestSameVolumeIdenticalPath(t *testing.T) {
	dir := t.TempDir()
	same, err := sameVolume(dir, dir)
	if err != nil {
		t.Fatalf("sameVolume: %v", err)
	}
	if !same {
		t.Fatal("expected identical path to report same volume")
	}
}

func TestRestoreRefusesCollision(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	src := filepath.Join(root, "clip.MP4")
	os.WriteFile(src, []byte("original"), 0600)
	dst, err := MoveOnVolume(src, root, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(filepath.Dir(dst), "recovery.json")
	os.WriteFile(src, []byte("new recording"), 0600)
	if err := Restore(journal); err == nil {
		t.Fatal("overwrote recording")
	}
	data, _ := os.ReadFile(src)
	if string(data) != "new recording" {
		t.Fatal("original path changed")
	}
	data, _ = os.ReadFile(dst)
	if string(data) != "original" {
		t.Fatal("recovery lost")
	}
	os.Remove(src)
	if err := Restore(journal); err != nil {
		t.Fatal(err)
	}
	if err := Restore(journal); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryProcessInterruption(t *testing.T) {
	if root := os.Getenv("REEL_TEST_RECOVERY_ROOT"); root != "" {
		step := os.Getenv("REEL_TEST_RECOVERY_STEP")
		fault.Hook = func(name string) error {
			if name == step {
				os.Exit(37)
			}
			return nil
		}
		if step == "restored" {
			Restore(os.Getenv("REEL_TEST_JOURNAL"))
		} else {
			MoveOnVolume(filepath.Join(root, "clip.MP4"), root, time.Now())
		}
		os.Exit(38)
	}
	for _, step := range []string{"media-move", "media-moved", "restored"} {
		t.Run(step, func(t *testing.T) {
			root, _ := filepath.EvalSymlinks(t.TempDir())
			src := filepath.Join(root, "clip.MP4")
			os.WriteFile(src, []byte("recover after crash"), 0600)
			journal := ""
			if step == "restored" {
				dest, err := MoveOnVolume(src, root, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				journal = filepath.Join(filepath.Dir(dest), "recovery.json")
			}
			c := exec.Command(os.Args[0], "-test.run=^TestRecoveryProcessInterruption$")
			c.Env = append(os.Environ(), "REEL_TEST_RECOVERY_ROOT="+root, "REEL_TEST_RECOVERY_STEP="+step, "REEL_TEST_JOURNAL="+journal)
			if err := c.Run(); err == nil {
				t.Fatal("child did not stop")
			}
			paths, _ := filepath.Glob(filepath.Join(root, ".reel-trash", "reel-deleted-*", "recovery.json"))
			if len(paths) != 1 {
				t.Fatal("intent lost", paths)
			}
			if err := Restore(paths[0]); err != nil {
				t.Fatal("restart restore failed", err)
			}
			data, _ := os.ReadFile(src)
			if string(data) != "recover after crash" {
				t.Fatal("bytes lost")
			}
		})
	}
}

func TestRecoveryFailuresNeverDelete(t *testing.T) {
	for _, tc := range []struct {
		step string
		err  error
	}{{"recovery-intent", syscall.ENOSPC}, {"media-move", syscall.EXDEV}, {"media-moved", syscall.EIO}} {
		t.Run(tc.step, func(t *testing.T) {
			root, _ := filepath.EvalSymlinks(t.TempDir())
			src := filepath.Join(root, "clip.MP4")
			os.WriteFile(src, []byte("preserve"), 0600)
			fault.Hook = func(step string) error {
				if step == tc.step {
					return tc.err
				}
				return nil
			}
			defer func() { fault.Hook = nil }()
			dest, err := MoveOnVolume(src, root, time.Now())
			if err == nil {
				t.Fatal("injected error ignored")
			}
			path := src
			if dest != "" {
				path = dest
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "preserve" {
				t.Fatal("recovery failure lost bytes", err)
			}
		})
	}
}
