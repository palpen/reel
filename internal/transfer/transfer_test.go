package transfer_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/pspenano/reel/internal/fault"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/pspenano/reel/internal/transfer"
)

func writeTestFile(t *testing.T, dir, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	return path
}

func sha256hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func TestCopyBasic(t *testing.T) {
	src := realTempDir(t)
	dst := realTempDir(t)

	content := make([]byte, 2048)
	for i := range content {
		content[i] = byte(i % 256)
	}
	srcPath := writeTestFile(t, src, "test.MP4", content)

	ts := time.Date(2026, 5, 10, 11, 18, 26, 0, time.UTC)
	result, err := transfer.Copy(srcPath, dst, "test.MP4", ts, "")
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}

	// Check result
	wantHash := sha256hex(content)
	if result.SHA256 != wantHash {
		t.Errorf("SHA256 = %q, want %q", result.SHA256, wantHash)
	}
	if result.Bytes != int64(len(content)) {
		t.Errorf("Bytes = %d, want %d", result.Bytes, len(content))
	}

	// Check destination exists and has correct content
	gotData, err := os.ReadFile(result.DestPath)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(gotData) != string(content) {
		t.Error("destination content differs from source")
	}

	// Check no .tmp orphan
	tmp := result.DestPath + ".tmp"
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Error("stale .tmp file left after successful copy")
	}
}

func TestCopyHashVerification_Match(t *testing.T) {
	src := realTempDir(t)
	dst := realTempDir(t)

	content := []byte("hello reel")
	srcPath := writeTestFile(t, src, "test.MP4", content)
	expectedHash := sha256hex(content)

	result, err := transfer.Copy(srcPath, dst, "test.MP4", time.Now(), expectedHash)
	if err != nil {
		t.Fatalf("Copy with correct hash: %v", err)
	}
	if result.SHA256 != expectedHash {
		t.Errorf("returned hash mismatch")
	}
}

func TestCopyHashVerification_Mismatch(t *testing.T) {
	src := realTempDir(t)
	dst := realTempDir(t)

	content := []byte("hello reel")
	srcPath := writeTestFile(t, src, "test.MP4", content)
	wrongHash := "0000000000000000000000000000000000000000000000000000000000000000"

	_, err := transfer.Copy(srcPath, dst, "test.MP4", time.Now(), wrongHash)
	if err == nil {
		t.Fatal("expected error for hash mismatch, got nil")
	}

	// No .tmp orphan
	tmp := filepath.Join(dst, "test.MP4.tmp")
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Error("stale .tmp file left after failed hash check")
	}

	// No destination file
	dest := filepath.Join(dst, "test.MP4")
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("destination file should not exist after hash mismatch")
	}
}

func TestCopyPartialFailureCleanup(t *testing.T) {
	src := realTempDir(t)
	dst := realTempDir(t)

	// Try to copy a nonexistent file
	srcPath := filepath.Join(src, "nonexistent.MP4")
	_, err := transfer.Copy(srcPath, dst, "nonexistent.MP4", time.Now(), "")
	if err == nil {
		t.Fatal("expected error for nonexistent src")
	}

	// No .tmp orphan in dst
	tmp := filepath.Join(dst, "nonexistent.MP4.tmp")
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Error("stale .tmp file left after failed open")
	}
}

func TestCopyMtimeSet(t *testing.T) {
	src := realTempDir(t)
	dst := realTempDir(t)

	content := []byte("mtime test")
	srcPath := writeTestFile(t, src, "mtime.MP4", content)

	ts := time.Date(2026, 1, 15, 9, 30, 0, 0, time.UTC)
	result, err := transfer.Copy(srcPath, dst, "mtime.MP4", ts, "")
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}

	info, err := os.Stat(result.DestPath)
	if err != nil {
		t.Fatalf("stat dest: %v", err)
	}
	// Check mtime within 2s of ts (filesystem may truncate to second)
	diff := info.ModTime().UTC().Sub(ts)
	if diff < 0 {
		diff = -diff
	}
	if diff > 2*time.Second {
		t.Errorf("mtime = %v, want ~%v (diff=%v)", info.ModTime().UTC(), ts, diff)
	}
}

func TestHashFile(t *testing.T) {
	dir := realTempDir(t)
	content := []byte("hash me please")
	path := writeTestFile(t, dir, "data.bin", content)

	h, n, err := transfer.HashFile(path)
	if err != nil {
		t.Fatalf("HashFile: %v", err)
	}
	want := sha256hex(content)
	if h != want {
		t.Errorf("HashFile() = %q, want %q", h, want)
	}
	if n != int64(len(content)) {
		t.Errorf("HashFile() size = %d, want %d", n, int64(len(content)))
	}
}

func TestHashFile_NonExistent(t *testing.T) {
	_, _, err := transfer.HashFile("/nonexistent/path/file.bin")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestCopyDestDirCreated(t *testing.T) {
	src := realTempDir(t)
	dst := filepath.Join(realTempDir(t), "nested", "dir")

	content := []byte("mkdir test")
	srcPath := writeTestFile(t, src, "nested.MP4", content)

	result, err := transfer.Copy(srcPath, dst, "nested.MP4", time.Now(), "")
	if err != nil {
		t.Fatalf("Copy with nested dest: %v", err)
	}
	if _, err := os.Stat(result.DestPath); err != nil {
		t.Errorf("dest file not found: %v", err)
	}
}

func TestFreeBytes(t *testing.T) {
	t.Run("valid dir returns positive", func(t *testing.T) {
		dir := realTempDir(t)
		n, err := transfer.FreeBytes(dir)
		if err != nil {
			t.Fatalf("FreeBytes: %v", err)
		}
		if n <= 0 {
			t.Errorf("FreeBytes = %d, want > 0", n)
		}
	})
	t.Run("missing dir returns error", func(t *testing.T) {
		_, err := transfer.FreeBytes("/nonexistent/path/that/does/not/exist")
		if err == nil {
			t.Fatal("expected error for missing dir")
		}
	})
}

func TestPreflightSpace(t *testing.T) {
	dir := realTempDir(t)

	t.Run("enough space returns nil", func(t *testing.T) {
		if err := transfer.PreflightSpace(dir, 1); err != nil {
			t.Errorf("PreflightSpace(1 byte) = %v, want nil", err)
		}
	})

	t.Run("insufficient space returns error", func(t *testing.T) {
		err := transfer.PreflightSpace(dir, 1<<62)
		if err == nil {
			t.Fatal("expected error for absurdly large needed")
		}
		msg := err.Error()
		if !contains(msg, "insufficient free space") {
			t.Errorf("error = %q, want substring 'insufficient free space'", msg)
		}
	})

	t.Run("statfs failure returns nil (unknown is not fatal)", func(t *testing.T) {
		if err := transfer.PreflightSpace("/nonexistent/path/xyz", 1<<62); err != nil {
			t.Errorf("PreflightSpace with bad dir = %v, want nil", err)
		}
	})
}

func TestSweepOrphanTmps(t *testing.T) {
	t.Run("missing root returns nil nil", func(t *testing.T) {
		cleaned, err := transfer.SweepOrphanTmps("/nonexistent/sweep/root")
		if err != nil {
			t.Errorf("err = %v, want nil", err)
		}
		if cleaned != nil {
			t.Errorf("cleaned = %v, want nil", cleaned)
		}
	})

	t.Run("reports nested .tmp files and preserves every file", func(t *testing.T) {
		root := realTempDir(t)
		// Create nested dirs with mixed .tmp and non-.tmp files
		subA := filepath.Join(root, "2026-05-27_100000")
		subB := filepath.Join(root, "2026-05-26_180000")
		for _, d := range []string{subA, subB} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
		}
		tmpFiles := []string{
			filepath.Join(root, "orphan.MP4.tmp"),
			filepath.Join(subA, "DJI_x.MP4.tmp"),
			filepath.Join(subB, "DJI_y.LRF.tmp"),
		}
		realFiles := []string{
			filepath.Join(root, "kept.MP4"),
			filepath.Join(subA, "DJI_x.MP4"),
			filepath.Join(subB, "notes.txt"),
		}
		for _, p := range append(tmpFiles, realFiles...) {
			if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
				t.Fatalf("write %s: %v", p, err)
			}
		}

		cleaned, err := transfer.SweepOrphanTmps(root)
		if err != nil {
			t.Fatalf("SweepOrphanTmps: %v", err)
		}
		if len(cleaned) != len(tmpFiles) {
			t.Errorf("cleaned %d files, want %d (%v)", len(cleaned), len(tmpFiles), cleaned)
		}

		for _, p := range tmpFiles {
			if _, err := os.Stat(p); err != nil {
				t.Errorf("partial was lost: %s", p)
			}
		}
		for _, p := range realFiles {
			if _, err := os.Stat(p); err != nil {
				t.Errorf("real file removed or unreadable: %s (%v)", p, err)
			}
		}
	})
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func realTempDir(t *testing.T) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFailuresRetainSourceAndPartials(t *testing.T) {
	for _, step := range []string{"stage-create", "transfer-intent", "mid-copy", "stage-sync", "stage-close", "publication"} {
		t.Run(step, func(t *testing.T) {
			srcDir, dst := realTempDir(t), realTempDir(t)
			src := writeTestFile(t, srcDir, "clip.MP4", []byte("irreplaceable"))
			fault.Hook = func(name string) error {
				if name == step {
					return fmt.Errorf("injected %s", step)
				}
				return nil
			}
			defer func() { fault.Hook = nil }()
			if _, err := transfer.Copy(src, dst, "clip.MP4", time.Time{}, ""); err == nil {
				t.Fatal("injection missed")
			}
			data, _ := os.ReadFile(src)
			if string(data) != "irreplaceable" {
				t.Fatal("source changed")
			}
			if step == "mid-copy" || step == "stage-sync" || step == "stage-close" || step == "publication" {
				paths, _ := filepath.Glob(filepath.Join(dst, ".reel-stage-*"))
				if len(paths) != 1 {
					t.Fatal("partial missing")
				}
				data, _ := os.ReadFile(paths[0])
				if string(data) != "irreplaceable" {
					t.Fatal("useful staged bytes lost")
				}
			}
		})
	}
}
func TestAliasAndCollisionPreservation(t *testing.T) {
	for _, kind := range []string{"collision", "temp-symlink", "parent-symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			root := realTempDir(t)
			src := writeTestFile(t, root, "source.MP4", []byte("original"))
			destDir := filepath.Join(root, "dest")
			os.Mkdir(destDir, 0700)
			dest := filepath.Join(destDir, "clip.MP4")
			switch kind {
			case "collision":
				os.WriteFile(dest, []byte("archive"), 0600)
			case "temp-symlink":
				os.Symlink(src, dest+".tmp")
			case "parent-symlink":
				os.Remove(destDir)
				os.Symlink(root, destDir)
			case "hardlink":
				os.Link(src, dest)
			}
			_, err := transfer.Copy(src, destDir, "clip.MP4", time.Time{}, "")
			if kind != "temp-symlink" && err == nil {
				t.Fatal("unsafe destination accepted")
			}
			data, _ := os.ReadFile(src)
			if string(data) != "original" {
				t.Fatal("source bytes changed")
			}
			if kind == "collision" {
				data, _ = os.ReadFile(dest)
				if string(data) != "archive" {
					t.Fatal("archive overwritten")
				}
			}
		})
	}
}

func TestCrashRetainsIntentAndStage(t *testing.T) {
	if src := os.Getenv("REEL_TEST_CRASH_SOURCE"); src != "" {
		fault.Hook = func(name string) error {
			if name == "publication" {
				os.Exit(29)
			}
			return nil
		}
		transfer.Copy(src, os.Getenv("REEL_TEST_CRASH_DEST"), "clip.MP4", time.Time{}, "")
		os.Exit(30)
	}
	root := realTempDir(t)
	src := writeTestFile(t, root, "source.MP4", []byte("survives crash"))
	dest := filepath.Join(root, "dest")
	child := exec.Command(os.Args[0], "-test.run=^TestCrashRetainsIntentAndStage$")
	child.Env = append(os.Environ(), "REEL_TEST_CRASH_SOURCE="+src, "REEL_TEST_CRASH_DEST="+dest)
	if err := child.Run(); err == nil {
		t.Fatal("child did not stop at publication")
	}
	stages, _ := filepath.Glob(filepath.Join(dest, ".reel-stage-*"))
	records, _ := filepath.Glob(filepath.Join(dest, ".reel-transfer-*"))
	if len(stages) != 1 || len(records) != 1 {
		t.Fatal("crash recovery material missing")
	}
	data, _ := os.ReadFile(stages[0])
	if string(data) != "survives crash" {
		t.Fatal("stage lost")
	}
	if _, err := transfer.Copy(src, dest, "clip.MP4", time.Time{}, ""); err != nil {
		t.Fatal("retry failed", err)
	}
	data, _ = os.ReadFile(stages[0])
	if string(data) != "survives crash" {
		t.Fatal("retry reclaimed interrupted media")
	}
}

func TestCompetingProcessCopiesNeverReplaceWinner(t *testing.T) {
	if src := os.Getenv("REEL_TEST_RACE_SOURCE"); src != "" {
		_, err := transfer.Copy(src, os.Getenv("REEL_TEST_RACE_DEST"), "clip.MP4", time.Time{}, "")
		if err != nil {
			os.Exit(31)
		}
		os.Exit(0)
	}
	root := realTempDir(t)
	a := writeTestFile(t, root, "a.MP4", []byte("first recording"))
	b := writeTestFile(t, root, "b.MP4", []byte("second recording"))
	dest := filepath.Join(root, "dest")
	makeChild := func(src string) *exec.Cmd {
		c := exec.Command(os.Args[0], "-test.run=^TestCompetingProcessCopiesNeverReplaceWinner$")
		c.Env = append(os.Environ(), "REEL_TEST_RACE_SOURCE="+src, "REEL_TEST_RACE_DEST="+dest)
		return c
	}
	x, y := makeChild(a), makeChild(b)
	if err := x.Start(); err != nil {
		t.Fatal(err)
	}
	if err := y.Start(); err != nil {
		t.Fatal(err)
	}
	ex, ey := x.Wait(), y.Wait()
	if (ex == nil) == (ey == nil) {
		t.Fatalf("expected exactly one winner: %v %v", ex, ey)
	}
	data, err := os.ReadFile(filepath.Join(dest, "clip.MP4"))
	if err != nil {
		t.Fatal(err)
	}
	if ex == nil && string(data) != "first recording" || ey == nil && string(data) != "second recording" {
		t.Fatal("successful copy was overwritten")
	}
}

func TestNestedDestinationSyncFailurePreservesSource(t *testing.T) {
	root := realTempDir(t)
	src := writeTestFile(t, root, "source.MP4", []byte("original"))
	parent := filepath.Join(root, "new-session")
	dest := filepath.Join(parent, "footage")
	injected := false
	fault.Hook = func(step string) error {
		if step == "directory-sync" {
			if _, err := os.Stat(parent); err == nil {
				injected = true
				return fmt.Errorf("parent sync failed")
			}
		}
		return nil
	}
	defer func() { fault.Hook = nil }()
	if _, err := transfer.Copy(src, dest, "clip.MP4", time.Time{}, ""); err == nil {
		t.Fatal("unsynced parent accepted")
	}
	if !injected {
		t.Fatal("did not reach new parent barrier")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("descended before parent was synced")
	}
	data, err := os.ReadFile(src)
	if err != nil || string(data) != "original" {
		t.Fatal("source changed")
	}
	fault.Hook = nil
	if _, err := transfer.Copy(src, dest, "clip.MP4", time.Time{}, ""); err != nil {
		t.Fatal("retry failed", err)
	}
}
