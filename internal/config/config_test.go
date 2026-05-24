package config

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizePath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "/Users/me/Videos", "/Users/me/Videos"},
		{"trim spaces", "  /Users/me/Videos  ", "/Users/me/Videos"},
		{"strip double quotes", `"/Users/me/Videos"`, "/Users/me/Videos"},
		{"strip single quotes", `'/Users/me/Videos'`, "/Users/me/Videos"},
		{"strip quotes then trim", `  "/Users/me/Videos"  `, "/Users/me/Videos"},
		{"trim inside quotes", `"  /Users/me/Videos  "`, "/Users/me/Videos"},
		{"unmatched leading quote left alone", `"/Users/me/Videos`, `"/Users/me/Videos`},
		{"unmatched trailing quote left alone", `/Users/me/Videos"`, `/Users/me/Videos"`},
		{"mixed quotes left alone", `"/Users/me/Videos'`, `"/Users/me/Videos'`},
		{"empty stays empty", "", ""},
		{"whitespace becomes empty", "   ", ""},
		{"bare tilde expands to home", "~", home},
		{"tilde slash expands to home", "~/Videos", filepath.Join(home, "Videos")},
		{"tilde inside path is not expanded", "/tmp/~/Videos", "/tmp/~/Videos"},
		{"quoted tilde still expands", `"~/Videos"`, filepath.Join(home, "Videos")},
		{"path with spaces preserved", "/Users/me/My Videos", "/Users/me/My Videos"},
		{"quoted path with spaces", `"/Users/me/My Videos"`, "/Users/me/My Videos"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizePath(tt.in)
			if got != tt.want {
				t.Errorf("sanitizePath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestValidateCameraMediaPath(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "DCIM", "DJI_001"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Also create a regular file to test the not-a-directory branch.
	if err := os.WriteFile(filepath.Join(tmp, "afile"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	t.Run("valid nested dir", func(t *testing.T) {
		if err := validateCameraMediaPath(tmp, "DCIM/DJI_001"); err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})

	t.Run("valid top-level dir", func(t *testing.T) {
		if err := validateCameraMediaPath(tmp, "DCIM"); err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})

	t.Run("missing path returns error", func(t *testing.T) {
		err := validateCameraMediaPath(tmp, "DCIM/DOES_NOT_EXIST")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("expected 'not found' in error, got %q", err.Error())
		}
	})

	t.Run("path is file, not dir", func(t *testing.T) {
		err := validateCameraMediaPath(tmp, "afile")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "not a directory") {
			t.Errorf("expected 'not a directory' in error, got %q", err.Error())
		}
	})

	t.Run("empty volume root skipped", func(t *testing.T) {
		if err := validateCameraMediaPath("", "DCIM"); err != nil {
			t.Errorf("expected nil for empty volumeRoot, got %v", err)
		}
		if err := validateCameraMediaPath("/Volumes", "DCIM"); err != nil {
			t.Errorf("expected nil for bare /Volumes, got %v", err)
		}
	})
}

// askPath writes prompts to os.Stdout. To avoid noisy test output, we
// redirect os.Stdout for the duration of the test.
func withSilencedStdout(t *testing.T, fn func()) {
	t.Helper()
	old := os.Stdout
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open devnull: %v", err)
	}
	os.Stdout = devnull
	defer func() {
		os.Stdout = old
		devnull.Close()
	}()
	fn()
}

func TestAskPathAcceptsValidInput(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("/tmp/foo\n"))
	var got string
	withSilencedStdout(t, func() {
		got = askPath(r, "where?", "/default", nil)
	})
	if got != "/tmp/foo" {
		t.Errorf("got %q, want %q", got, "/tmp/foo")
	}
}

func TestAskPathSanitizesQuotedInput(t *testing.T) {
	r := bufio.NewReader(strings.NewReader(`"/Users/me/My Videos"` + "\n"))
	var got string
	withSilencedStdout(t, func() {
		got = askPath(r, "where?", "/default", nil)
	})
	want := "/Users/me/My Videos"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestAskPathEmptyReturnsDefault(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("\n"))
	var got string
	withSilencedStdout(t, func() {
		got = askPath(r, "where?", "/default", nil)
	})
	if got != "/default" {
		t.Errorf("got %q, want %q", got, "/default")
	}
}

func TestAskPathSanitizesDefault(t *testing.T) {
	// Default that contains a tilde gets expanded too, since defaults
	// flow through sanitizePath when the user accepts them.
	home, _ := os.UserHomeDir()
	r := bufio.NewReader(strings.NewReader("\n"))
	var got string
	withSilencedStdout(t, func() {
		got = askPath(r, "where?", "~/Videos", nil)
	})
	want := filepath.Join(home, "Videos")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestAskPathRepromptsOnValidatorError(t *testing.T) {
	// First input fails validation, second succeeds.
	r := bufio.NewReader(strings.NewReader("/bad\n/good\n"))
	calls := 0
	validator := func(p string) error {
		calls++
		if p == "/bad" {
			return io.EOF // any error
		}
		return nil
	}
	var got string
	withSilencedStdout(t, func() {
		got = askPath(r, "where?", "/default", validator)
	})
	if got != "/good" {
		t.Errorf("got %q, want %q", got, "/good")
	}
	if calls != 2 {
		t.Errorf("validator calls = %d, want 2", calls)
	}
}

func TestNewWizardStatePreservesExistingValues(t *testing.T) {
	existing := &Config{
		LaptopDir:    "/some/laptop",
		HDVolumeName: "MyHD",
		HDDir:        "Footage",
		SoftDelete:   false,
		Cameras: []CameraProfile{
			{Name: "DJI Pocket 3", VolumeName: "SD_Card", MediaPath: "DCIM/DJI_001"},
		},
	}
	w := newWizardState(existing)
	if w.laptopDir != "/some/laptop" {
		t.Errorf("laptopDir = %q", w.laptopDir)
	}
	if w.hdVolumeName != "MyHD" {
		t.Errorf("hdVolumeName = %q", w.hdVolumeName)
	}
	if w.hdDir != "Footage" {
		t.Errorf("hdDir = %q", w.hdDir)
	}
	if w.softDelete {
		t.Error("softDelete should be false (carried from existing)")
	}
	if w.cameraVolume != "SD_Card" {
		t.Errorf("cameraVolume = %q", w.cameraVolume)
	}
	if w.mediaPath != "DCIM/DJI_001" {
		t.Errorf("mediaPath = %q", w.mediaPath)
	}
}

func TestNewWizardStateDefaultsForFirstRun(t *testing.T) {
	w := newWizardState(nil)
	if !w.softDelete {
		t.Error("softDelete should default to true on first run")
	}
	if w.cameraVolume != "" || w.laptopDir != "" || w.hdDir != "" {
		t.Error("first-run state should have empty string fields")
	}
}

func TestToConfigPreservesNonFirstCameraProfiles(t *testing.T) {
	existing := &Config{
		Cameras: []CameraProfile{
			{Name: "DJI Pocket 3", VolumeName: "old", MediaPath: "old"},
			{Name: "Custom Cam", VolumeName: "C", MediaPath: "DCIM"},
		},
	}
	w := newWizardState(existing)
	w.cameraVolume = "NewVol"
	w.mediaPath = "NewPath"
	cfg := w.toConfig(existing)

	if len(cfg.Cameras) != 2 {
		t.Fatalf("expected 2 cameras, got %d", len(cfg.Cameras))
	}
	if cfg.Cameras[0].VolumeName != "NewVol" || cfg.Cameras[0].MediaPath != "NewPath" {
		t.Errorf("first camera not updated: %+v", cfg.Cameras[0])
	}
	if cfg.Cameras[1].Name != "Custom Cam" {
		t.Errorf("second camera was modified: %+v", cfg.Cameras[1])
	}
}
