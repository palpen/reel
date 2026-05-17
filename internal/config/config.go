// Package config handles loading and saving the reel configuration file,
// including a first-run interactive wizard.
package config

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const configFile = "config.json"

// CameraProfile describes one camera model's file-naming conventions.
type CameraProfile struct {
	Name            string   `json:"name"`
	VolumeName      string   `json:"volume_name"`  // exact macOS volume name, e.g. "DJI Pocket 3"
	MediaPath       string   `json:"media_path"`   // path to media files relative to volume root, e.g. "DCIM"
	FilenameRegex   string   `json:"filename_regex"`
	TimestampSource string   `json:"timestamp_source"`
	TimestampGroup  string   `json:"timestamp_group"`
	TimestampFormat string   `json:"timestamp_format"`
	Extensions      []string `json:"extensions"`
	RawExtensions   []string `json:"raw_extensions"`
}

// Config is the top-level configuration structure.
type Config struct {
	LaptopDir    string          `json:"laptop_dir"`
	HDVolumeName string          `json:"hd_volume_name"`
	HDDir        string          `json:"hd_dir"`
	SoftDelete   bool            `json:"soft_delete"`
	Cameras      []CameraProfile `json:"cameras"`
}

// Dir returns the OS-specific config directory for reel.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "reel"), nil
}

// Load reads the config from disk. If it does not exist, runs the first-run wizard.
func Load() (*Config, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, configFile)

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return runWizard(path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return &cfg, nil
}

// Save writes the config to disk atomically.
func Save(cfg *Config) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, configFile)
	return writeJSON(path, cfg)
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// runWizard prompts the user for required config values interactively.
func runWizard(path string) (*Config, error) {
	fmt.Println("reel: no config found. Running first-time setup.")
	fmt.Println()

	r := bufio.NewReader(os.Stdin)

	vols, err := mountedVolumes()
	if err != nil || len(vols) == 0 {
		vols = []string{} // non-fatal; fall back to free-text entry
	}

	// Camera volume
	cameraVolume := pickVolume(r, vols, "Which volume is your camera?")
	mediaPath := ask(r, "Where on the camera are your media files?", "DCIM")

	// HD volume (exclude the camera from the list)
	hdVols := without(vols, cameraVolume)
	hdVolumeName := pickVolume(r, hdVols, "Which volume is your backup HD?")
	hdDir := ask(r, "What folder on the HD should reel manage?", "Footage")

	// Laptop destination
	laptopDir := ask(r, "Where should imported footage go on this laptop?",
		filepath.Join(mustHomeDir(), "Videos", "Footage"))

	softDeleteStr := ask(r, "Use soft-delete (move to Trash instead of permanent delete)? [Y/n]", "y")
	softDelete := !strings.EqualFold(strings.TrimSpace(softDeleteStr), "n")

	cameras := defaultCameraProfiles()
	if len(cameras) > 0 {
		cameras[0].VolumeName = cameraVolume
		cameras[0].MediaPath = mediaPath
	}

	cfg := &Config{
		LaptopDir:    laptopDir,
		HDVolumeName: hdVolumeName,
		HDDir:        hdDir,
		SoftDelete:   softDelete,
		Cameras:      cameras,
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := writeJSON(path, cfg); err != nil {
		return nil, fmt.Errorf("write config: %w", err)
	}
	fmt.Printf("\nConfig saved to %s\n\n", path)
	return cfg, nil
}

// mountedVolumes returns the names of volumes currently mounted under /Volumes.
func mountedVolumes() ([]string, error) {
	entries, err := os.ReadDir("/Volumes")
	if err != nil {
		return nil, err
	}
	var vols []string
	for _, e := range entries {
		if e.IsDir() {
			vols = append(vols, e.Name())
		}
	}
	return vols, nil
}

// pickVolume shows a numbered list of volumes and asks the user to pick one.
// Falls back to free-text entry if vols is empty.
func pickVolume(r *bufio.Reader, vols []string, prompt string) string {
	fmt.Printf("\n  %s\n", prompt)
	if len(vols) == 0 {
		fmt.Print("  Volume name: ")
		line, _ := r.ReadString('\n')
		return strings.TrimSpace(line)
	}
	for i, v := range vols {
		fmt.Printf("    [%d] %s\n", i+1, v)
	}
	for {
		fmt.Printf("  Enter number (1–%d): ", len(vols))
		line, _ := r.ReadString('\n')
		line = strings.TrimSpace(line)
		n := 0
		fmt.Sscanf(line, "%d", &n)
		if n >= 1 && n <= len(vols) {
			return vols[n-1]
		}
		fmt.Printf("  Please enter a number between 1 and %d.\n", len(vols))
	}
}

func ask(r *bufio.Reader, prompt, defaultVal string) string {
	fmt.Printf("\n  %s\n  [%s]: ", prompt, defaultVal)
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultVal
	}
	return line
}

// without returns vols with target removed.
func without(vols []string, target string) []string {
	var out []string
	for _, v := range vols {
		if v != target {
			out = append(out, v)
		}
	}
	return out
}

func mustHomeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

// defaultCameraProfiles returns built-in camera profile templates.
// VolumeName and MediaPath are left empty to be filled in by the wizard.
func defaultCameraProfiles() []CameraProfile {
	return []CameraProfile{
		{
			Name:            "DJI Pocket 3",
			VolumeName:      "", // set by wizard
			MediaPath:       "", // set by wizard
			FilenameRegex:   `^(?P<base>DJI_(?P<ts>\d{14})_\d{4}_[A-Z])\.(?P<ext>MP4|LRF|WAV)$`,
			TimestampSource: "filename",
			TimestampGroup:  "ts",
			TimestampFormat: "20060102150405",
			Extensions:      []string{"MP4", "LRF", "WAV"},
			RawExtensions:   []string{"LRF", "WAV"},
		},
	}
}

// HDRoot returns the path to the HD root volume.
func (c *Config) HDRoot() string {
	return filepath.Join("/Volumes", c.HDVolumeName)
}

// HDManagedDir returns the path to the managed footage folder on the HD.
func (c *Config) HDManagedDir() string {
	return filepath.Join(c.HDRoot(), c.HDDir)
}

// HDStatePath returns the path to the mirrored state file on the HD.
func (c *Config) HDStatePath() string {
	return filepath.Join(c.HDRoot(), ".reel-state.jsonl")
}
