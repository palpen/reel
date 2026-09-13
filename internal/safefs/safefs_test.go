package safefs

import (
	"os"
	"path/filepath"
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
