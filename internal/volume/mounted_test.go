package volume

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/pspenano/reel/internal/safefs"
	"github.com/pspenano/reel/internal/transfer"
	"github.com/pspenano/reel/internal/trash"
)

// The disk-image validation script supplies a disposable, mounted filesystem.
func TestMountedVolumeIdentity(t *testing.T) {
	root := os.Getenv("REEL_TEST_VOLUME_ROOT")
	if root == "" {
		t.Skip("requires a disposable mounted volume")
	}
	id, err := Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := id.Check(); err != nil {
		t.Fatal(err)
	}
	if err := Independent(id, id); err == nil {
		t.Fatal("same volume accepted as independent storage")
	}
	wrong := id
	wrong.UUID = "not-the-mounted-volume"
	if err := wrong.Check(); err == nil {
		t.Fatal("wrong volume identity accepted")
	}
	placeholder, err := os.MkdirTemp(root, "unmounted-placeholder-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(placeholder)
	if _, err := Resolve(placeholder); err == nil {
		t.Fatal("ordinary directory accepted as a mounted volume")
	}
	t.Logf("resolved mounted volume UUID=%s device=%d physical=%v", id.UUID, id.Device, id.Physical)
}

func TestMountedVolumeCapabilities(t *testing.T) {
	root := os.Getenv("REEL_TEST_VOLUME_ROOT")
	if root == "" {
		t.Skip("requires a disposable mounted volume")
	}
	probe, err := os.MkdirTemp(root, "preservation-probe-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(probe)
	dir, err := safefs.OpenDir(probe, false)
	if err != nil {
		t.Fatal(err)
	}
	capabilityErr := safefs.CheckExclusive(dir)
	dir.Close()
	if capabilityErr != nil {
		if !errors.Is(capabilityErr, syscall.ENOTSUP) && !errors.Is(capabilityErr, syscall.EOPNOTSUPP) {
			t.Fatalf("unexpected capability failure: %v", capabilityErr)
		}
		source := filepath.Join(probe, "DCIM", "clip.MP4")
		if err := os.Mkdir(filepath.Dir(source), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(source, []byte("sole original"), 0600); err != nil {
			t.Fatal(err)
		}
		assertOriginal := func() {
			t.Helper()
			data, err := os.ReadFile(source)
			if err != nil || string(data) != "sole original" {
				t.Fatalf("original lost on unsupported filesystem: %q %v", data, err)
			}
		}
		archive := filepath.Join(probe, "archive")
		if _, err := transfer.Copy(source, archive, "clip.MP4", time.Now(), ""); err == nil {
			t.Fatal("unsupported destination reported successful publication")
		}
		assertOriginal()
		if _, err := os.Stat(filepath.Join(archive, "clip.MP4")); !os.IsNotExist(err) {
			t.Fatalf("failed publication exposed destination media: %v", err)
		}
		if _, err := trash.MoveOnVolume(source, probe, time.Now()); err == nil {
			t.Fatal("unsupported camera reported successful recovery movement")
		}
		assertOriginal()
		// The unsupported card can still be read into a supported desktop archive.
		hostRoot := os.Getenv("REEL_TEST_HOST_ROOT")
		if hostRoot == "" {
			t.Fatal("requires disposable host directory for cross-filesystem copying")
		}
		host, err := os.MkdirTemp(hostRoot, "desktop-copy-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(host)
		copy, err := transfer.Copy(source, host, "clip.MP4", time.Now(), "")
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(copy.DestPath)
		if err != nil || string(data) != "sole original" {
			t.Fatalf("cross-filesystem copy failed: %q %v", data, err)
		}
		assertOriginal()
		t.Logf("unsupported exclusive rename; publication/recovery refused with original intact: %v", capabilityErr)
	}
	report, err := json.Marshal(map[string]bool{"exclusive_rename": capabilityErr == nil})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "capabilities.json"), report, 0600); err != nil {
		t.Fatal(err)
	}
}
