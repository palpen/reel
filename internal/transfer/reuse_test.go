package transfer_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pspenano/reel/internal/fault"
	"github.com/pspenano/reel/internal/transfer"
)

func TestCopyRejectsReplacedDirectoryBeforeExistingCopyCheck(t *testing.T) {
	root := realTempDir(t)
	src := writeTestFile(t, root, "source.MP4", []byte("original"))
	destDir := filepath.Join(root, "archive")
	if err := os.Mkdir(destDir, 0700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, destDir, "clip.MP4", []byte("original"))
	components := len(strings.Split(strings.Trim(destDir, "/"), "/"))
	syncs := 0
	swapped := false
	fault.Hook = func(step string) error {
		if step != "directory-sync" {
			return nil
		}
		syncs++
		if syncs != components {
			return nil
		}
		// The final directory descriptor is open, but the lock and existing
		// destination have not been opened yet.
		if err := os.Rename(destDir, destDir+"-retained"); err != nil {
			return err
		}
		if err := os.Mkdir(destDir, 0700); err != nil {
			return err
		}
		writeTestFile(t, destDir, "clip.MP4", []byte("conflict"))
		swapped = true
		return nil
	}
	t.Cleanup(func() { fault.Hook = nil })
	if _, err := transfer.Copy(src, destDir, "clip.MP4", time.Time{}, ""); err == nil {
		t.Fatal("verified a path in the replacement directory")
	}
	if !swapped {
		t.Fatal("did not exercise directory substitution")
	}
	for path, want := range map[string]string{src: "original", filepath.Join(destDir, "clip.MP4"): "conflict", filepath.Join(destDir+"-retained", "clip.MP4"): "original"} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatalf("media changed at %s: %q, %v", path, data, err)
		}
	}
}

func TestCopyRechecksDestinationAfterVolumeValidation(t *testing.T) {
	for _, route := range []string{"existing", "publication-collision", "new"} {
		for _, change := range []string{"none", "directory", "file", "content"} {
			t.Run(route+"/"+change, func(t *testing.T) {
				root := realTempDir(t)
				src := writeTestFile(t, root, "source.MP4", []byte("original"))
				destDir := filepath.Join(root, "archive")
				if err := os.Mkdir(destDir, 0700); err != nil {
					t.Fatal(err)
				}
				dest := filepath.Join(destDir, "clip.MP4")
				if route == "existing" {
					writeTestFile(t, destDir, "clip.MP4", []byte("original"))
				}
				checks := 0
				finishAt := 3
				if route == "existing" {
					finishAt = 2
				}
				result, err := transfer.CopyChecked(src, destDir, "clip.MP4", time.Time{}, "", func() error {
					checks++
					if route == "publication-collision" && checks == 2 {
						writeTestFile(t, destDir, "clip.MP4", []byte("original"))
					}
					if checks != finishAt {
						return nil
					}
					switch change {
					case "directory":
						if err := os.Rename(destDir, destDir+"-retained"); err != nil {
							return err
						}
						if err := os.Mkdir(destDir, 0700); err != nil {
							return err
						}
						writeTestFile(t, destDir, "clip.MP4", []byte("original"))
					case "file":
						if err := os.Rename(dest, dest+".retained"); err != nil {
							return err
						}
						writeTestFile(t, destDir, "clip.MP4", []byte("original"))
					case "content":
						info, err := os.Stat(dest)
						if err != nil {
							return err
						}
						writeTestFile(t, destDir, "clip.MP4", []byte("conflict"))
						return os.Chtimes(dest, info.ModTime(), info.ModTime())
					}
					return nil // These changes leave the mounted volume unchanged.
				})
				if checks != finishAt {
					t.Fatalf("did not reach final validation: %d, %v", checks, err)
				}
				if change == "none" {
					if err != nil || result == nil {
						t.Fatalf("unchanged copy failed: %v", err)
					}
				} else if err == nil || result != nil {
					t.Fatal("accepted destination substitution")
				}
				data, err := os.ReadFile(src)
				if err != nil || string(data) != "original" {
					t.Fatal("source changed", err)
				}
			})
		}
	}
}
