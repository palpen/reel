package camera_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pspenano/reel/internal/camera"
	"github.com/pspenano/reel/internal/config"
)

func djiProfile() config.CameraProfile {
	return config.CameraProfile{
		Name:            "DJI Pocket 3",
		VolumeName:      "DJI Pocket 3",
		MediaPath:       "DCIM",
		FilenameRegex:   `^(?P<base>DJI_(?P<ts>\d{14})_\d{4}_[A-Z])\.(?P<ext>MP4|LRF|WAV)$`,
		TimestampSource: "filename",
		TimestampGroup:  "ts",
		TimestampFormat: "20060102150405",
		Extensions:      []string{"MP4", "LRF", "WAV"},
		RawExtensions:   []string{"LRF", "WAV"},
	}
}

func TestDJIFilenameRegex(t *testing.T) {
	p := djiProfile()

	tests := []struct {
		name      string
		filename  string
		wantMatch bool
		wantBase  string
		wantExt   string
		wantYear  int
		wantMonth int
	}{
		{
			name:      "mp4",
			filename:  "DJI_20260510111826_0015_D.MP4",
			wantMatch: true,
			wantBase:  "DJI_20260510111826_0015_D",
			wantExt:   "MP4",
			wantYear:  2026,
			wantMonth: 5,
		},
		{
			name:      "lrf",
			filename:  "DJI_20260510111826_0015_D.LRF",
			wantMatch: true,
			wantBase:  "DJI_20260510111826_0015_D",
			wantExt:   "LRF",
		},
		{
			name:      "wav",
			filename:  "DJI_20260510111826_0015_D.WAV",
			wantMatch: true,
			wantBase:  "DJI_20260510111826_0015_D",
			wantExt:   "WAV",
		},
		{
			name:      "lowercase_ext",
			filename:  "DJI_20260510111826_0015_D.mp4",
			wantMatch: false, // regex is case-sensitive
		},
		{
			name:      "wrong_prefix",
			filename:  "VID_20260510111826_0015_D.MP4",
			wantMatch: false,
		},
		{
			name:      "missing_seq",
			filename:  "DJI_20260510111826_D.MP4",
			wantMatch: false,
		},
		{
			name:      "wrong_ext",
			filename:  "DJI_20260510111826_0015_D.MOV",
			wantMatch: false,
		},
		{
			name:      "short_timestamp",
			filename:  "DJI_202605101118_0015_D.MP4",
			wantMatch: false,
		},
		{
			name:      "random_garbage",
			filename:  "not_a_dji_file.txt",
			wantMatch: false,
		},
		{
			name:      "thumbnail",
			filename:  "DJI_20260510111826_0015_D.THM",
			wantMatch: false,
		},
		{
			name:      "multi_digit_seq",
			filename:  "DJI_20260510111826_9999_Z.MP4",
			wantMatch: true,
			wantBase:  "DJI_20260510111826_9999_Z",
			wantExt:   "MP4",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, ok := camera.MatchFile(&p, "/Volumes/DJI", "/Volumes/DJI/DCIM/"+tc.filename, tc.filename)
			if ok != tc.wantMatch {
				t.Errorf("MatchFile(%q) matched=%v, want %v", tc.filename, ok, tc.wantMatch)
				return
			}
			if !tc.wantMatch {
				return
			}
			if f.BaseName != tc.wantBase {
				t.Errorf("BaseName = %q, want %q", f.BaseName, tc.wantBase)
			}
			if f.Ext != tc.wantExt {
				t.Errorf("Ext = %q, want %q", f.Ext, tc.wantExt)
			}
			if tc.wantYear != 0 && f.RecordedAt.Year() != tc.wantYear {
				t.Errorf("RecordedAt.Year = %d, want %d", f.RecordedAt.Year(), tc.wantYear)
			}
			if tc.wantMonth != 0 && int(f.RecordedAt.Month()) != tc.wantMonth {
				t.Errorf("RecordedAt.Month = %d, want %d", int(f.RecordedAt.Month()), tc.wantMonth)
			}
		})
	}
}

func TestValidateProfile_MissingRequiredGroups(t *testing.T) {
	tests := []struct {
		name    string
		profile config.CameraProfile
		wantErr bool
	}{
		{
			name:    "valid",
			profile: djiProfile(),
			wantErr: false,
		},
		{
			name: "missing_base_group",
			profile: config.CameraProfile{
				Name:            "Test",
				FilenameRegex:   `^DJI_(?P<ts>\d{14})_\d{4}_[A-Z]\.(?P<ext>MP4)$`,
				TimestampSource: "filename",
				TimestampGroup:  "ts",
				TimestampFormat: "20060102150405",
			},
			wantErr: true,
		},
		{
			name: "missing_ext_group",
			profile: config.CameraProfile{
				Name:            "Test",
				FilenameRegex:   `^(?P<base>DJI_(?P<ts>\d{14})_\d{4}_[A-Z])\.(MP4)$`,
				TimestampSource: "filename",
				TimestampGroup:  "ts",
				TimestampFormat: "20060102150405",
			},
			wantErr: true,
		},
		{
			name: "missing_timestamp_group",
			profile: config.CameraProfile{
				Name:            "Test",
				FilenameRegex:   `^(?P<base>DJI_\d{14}_\d{4}_[A-Z])\.(?P<ext>MP4)$`,
				TimestampSource: "filename",
				TimestampGroup:  "ts",
				TimestampFormat: "20060102150405",
			},
			wantErr: true,
		},
		{
			name: "bad_regex",
			profile: config.CameraProfile{
				Name:          "Test",
				FilenameRegex: `[invalid`,
			},
			wantErr: true,
		},
		{
			name: "no_timestamp_source",
			profile: config.CameraProfile{
				Name:            "Test",
				FilenameRegex:   `^(?P<base>DJI_\d{14}_\d{4}_[A-Z])\.(?P<ext>MP4)$`,
				TimestampSource: "", // not "filename", so ts group not required
			},
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := camera.ValidateProfile(&tc.profile)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateProfile() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestDetect_MockVolumes(t *testing.T) {
	dir := t.TempDir()

	// Create a fake camera volume with a DCIM/100MEDIA tree.
	fakeVol := filepath.Join(dir, "DJI Pocket 3")
	mediaDir := filepath.Join(fakeVol, "DCIM")
	os.MkdirAll(mediaDir, 0o755)

	// Point the profile at the temp dir using an absolute VolumeName.
	p := djiProfile()
	p.VolumeName = fakeVol  // absolute path — Detect treats it as-is
	p.MediaPath = "DCIM"

	cameras, err := camera.Detect([]config.CameraProfile{p})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(cameras) == 0 {
		t.Fatal("expected 1 camera, got 0")
	}
	if cameras[0].VolumePath != fakeVol {
		t.Errorf("VolumePath = %q, want %q", cameras[0].VolumePath, fakeVol)
	}
}

func TestDetect_NoCameras(t *testing.T) {
	dir := t.TempDir()
	p := djiProfile()
	p.VolumeName = filepath.Join(dir, "NONEXISTENT_VOLUME")

	cameras, err := camera.Detect([]config.CameraProfile{p})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(cameras) != 0 {
		t.Errorf("expected 0 cameras, got %d", len(cameras))
	}
}

func TestDetect_VolumeWithoutMediaPath(t *testing.T) {
	dir := t.TempDir()

	// Volume exists but the configured media path doesn't.
	fakeVol := filepath.Join(dir, "DJI Pocket 3")
	os.MkdirAll(fakeVol, 0o755) // volume root exists, but no DCIM inside

	p := djiProfile()
	p.VolumeName = fakeVol
	p.MediaPath = "DCIM"

	cameras, err := camera.Detect([]config.CameraProfile{p})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(cameras) != 0 {
		t.Errorf("expected 0 cameras (media path missing), got %d", len(cameras))
	}
}

func TestDetect_UnconfiguredProfile(t *testing.T) {
	p := djiProfile()
	p.VolumeName = "" // wizard hasn't run yet

	cameras, err := camera.Detect([]config.CameraProfile{p})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(cameras) != 0 {
		t.Errorf("expected 0 cameras for unconfigured profile, got %d", len(cameras))
	}
}
