package trash

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

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

func TestSameVolumeAsTrash(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	p := filepath.Join(home, "x")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	same, err := SameVolumeAsTrash(p)
	if err != nil {
		t.Fatalf("SameVolumeAsTrash: %v", err)
	}
	if !same {
		t.Fatal("expected a path under HOME to share the Trash volume")
	}
}

func TestMoveSameVolume(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	srcDir := filepath.Join(home, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(srcDir, "file.txt")
	if err := os.WriteFile(src, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	dst, err := Move(src, time.Now())
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("destination missing after move: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("source should be gone after move, stat err = %v", err)
	}
}
