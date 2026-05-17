// Package camera handles camera volume detection and DCIM walking.
package camera

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/pspenano/reel/internal/config"
)

// File represents a single media file found on a camera.
type File struct {
	Profile    *config.CameraProfile
	VolumePath string // e.g. /Volumes/DJI_MINI4PRO
	FullPath   string // full path to file
	BaseName   string // e.g. DJI_20260510111826_0015_D
	Ext        string // e.g. MP4
	RecordedAt time.Time
	Size       int64
}

// DetectedCamera is a camera volume that was found.
type DetectedCamera struct {
	Profile    *config.CameraProfile
	VolumePath string
	DCIMPath   string
}

// Detect finds connected camera volumes for all configured profiles.
// Each profile must have VolumeName set (configured by the wizard).
func Detect(cameras []config.CameraProfile) ([]DetectedCamera, error) {
	var found []DetectedCamera
	for i := range cameras {
		p := &cameras[i]
		if p.VolumeName == "" {
			continue // not yet configured
		}
		// Support absolute paths (used in tests); otherwise scope to /Volumes/.
		volPath := p.VolumeName
		if !filepath.IsAbs(volPath) {
			volPath = filepath.Join("/Volumes", p.VolumeName)
		}
		mediaPath := filepath.Join(volPath, p.MediaPath)
		if _, err := os.Stat(mediaPath); err == nil {
			found = append(found, DetectedCamera{
				Profile:    p,
				VolumePath: volPath,
				DCIMPath:   mediaPath,
			})
		}
	}
	return found, nil
}

// Walk returns all media files in the camera's DCIM directory.
func (dc *DetectedCamera) Walk() ([]File, error) {
	re, err := compileProfile(dc.Profile)
	if err != nil {
		return nil, err
	}

	var files []File
	err = filepath.WalkDir(dc.DCIMPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		f, ok := matchFile(dc.Profile, re, dc.VolumePath, path, name)
		if !ok {
			return nil
		}
		files = append(files, f)
		return nil
	})
	return files, err
}

// MatchFile attempts to parse a filename against a camera profile.
// Exported for testing.
func MatchFile(profile *config.CameraProfile, volumePath, fullPath, name string) (File, bool) {
	re, err := compileProfile(profile)
	if err != nil {
		return File{}, false
	}
	return matchFile(profile, re, volumePath, fullPath, name)
}

func matchFile(profile *config.CameraProfile, re *regexp.Regexp, volumePath, fullPath, name string) (File, bool) {
	m := re.FindStringSubmatch(name)
	if m == nil {
		return File{}, false
	}

	groups := namedGroups(re, m)
	baseName, ok := groups["base"]
	if !ok {
		return File{}, false
	}
	ext, ok := groups["ext"]
	if !ok {
		return File{}, false
	}

	// Parse timestamp
	var recordedAt time.Time
	if profile.TimestampSource == "filename" {
		ts, ok := groups[profile.TimestampGroup]
		if !ok {
			return File{}, false
		}
		var parseErr error
		recordedAt, parseErr = time.ParseInLocation(profile.TimestampFormat, ts, time.UTC)
		if parseErr != nil {
			return File{}, false
		}
	}

	info, err := os.Stat(fullPath)
	var size int64
	if err == nil {
		size = info.Size()
	}

	return File{
		Profile:    profile,
		VolumePath: volumePath,
		FullPath:   fullPath,
		BaseName:   baseName,
		Ext:        strings.ToUpper(ext),
		RecordedAt: recordedAt,
		Size:       size,
	}, true
}

func compileProfile(p *config.CameraProfile) (*regexp.Regexp, error) {
	re, err := regexp.Compile(p.FilenameRegex)
	if err != nil {
		return nil, fmt.Errorf("compile regex for %s: %w", p.Name, err)
	}
	// Validate required named groups
	required := []string{"base", "ext"}
	if p.TimestampSource == "filename" {
		required = append(required, p.TimestampGroup)
	}
	names := re.SubexpNames()
	nameSet := make(map[string]bool)
	for _, n := range names {
		nameSet[n] = true
	}
	for _, req := range required {
		if !nameSet[req] {
			return nil, fmt.Errorf("profile %s: regex missing required named group %q", p.Name, req)
		}
	}
	return re, nil
}

func namedGroups(re *regexp.Regexp, m []string) map[string]string {
	result := make(map[string]string)
	for i, name := range re.SubexpNames() {
		if i > 0 && name != "" && i < len(m) {
			result[name] = m[i]
		}
	}
	return result
}

// ValidateProfile checks that a profile's regex has all required groups.
func ValidateProfile(p *config.CameraProfile) error {
	_, err := compileProfile(p)
	return err
}
