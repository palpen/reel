package safefs

import (
	"errors"
	"github.com/pspenano/reel/internal/fault"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestExclusiveCapabilityAndAliases(t *testing.T) {
	path, _ := filepath.EvalSymlinks(t.TempDir())
	dir, err := OpenDir(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if err := CheckExclusive(dir); err != nil {
		t.Fatal(err)
	}
	os.Symlink(path, filepath.Join(path, "alias"))
	if _, err := OpenBeneath(dir, "alias", false); err == nil {
		t.Fatal("followed parent alias")
	}
	if _, err := OpenBeneath(dir, "../escape", true); err == nil {
		t.Fatal("traversal accepted")
	}
	if err := EnsureRecord(dir, "protocol", []byte("expected")); err != nil {
		t.Fatal(err)
	}
	if err := EnsureRecord(dir, "protocol", []byte("changed")); err == nil {
		t.Fatal("protocol overwritten")
	}
	data, _ := os.ReadFile(filepath.Join(path, "protocol"))
	if string(data) != "expected" {
		t.Fatal("protocol corrupted")
	}
}

func TestDirectoryCreationSyncFailures(t *testing.T) {
	for _, kind := range []string{"absolute", "beneath", "fresh"} {
		t.Run(kind, func(t *testing.T) {
			path, _ := filepath.EvalSymlinks(t.TempDir())
			root, err := OpenDir(path, false)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			fault.Hook = func(step string) error {
				if step == "directory-sync" {
					return syscall.EIO
				}
				return nil
			}
			defer func() { fault.Hook = nil }()
			var child *os.File
			switch kind {
			case "absolute":
				child, err = OpenDir(filepath.Join(path, "a", "b"), true)
			case "beneath":
				child, err = OpenBeneath(root, "a/b", true)
			case "fresh":
				child, err = FreshDir(root, "entry-")
			}
			if child != nil {
				child.Close()
			}
			if !errors.Is(err, syscall.EIO) {
				t.Fatalf("sync failure not propagated: %v", err)
			}
		})
	}
}
