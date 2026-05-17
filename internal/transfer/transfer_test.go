package transfer_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
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
	src := t.TempDir()
	dst := t.TempDir()

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
	src := t.TempDir()
	dst := t.TempDir()

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
	src := t.TempDir()
	dst := t.TempDir()

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
	src := t.TempDir()
	dst := t.TempDir()

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
	src := t.TempDir()
	dst := t.TempDir()

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
	dir := t.TempDir()
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
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "nested", "dir")

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
