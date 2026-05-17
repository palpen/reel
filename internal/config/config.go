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
	VolumePattern   string   `json:"volume_pattern"`
	DCIMSubdir      string   `json:"dcim_subdir"`
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

	laptopDir := ask(r, "Where should imported footage go on this laptop?",
		filepath.Join(mustHomeDir(), "Videos", "Footage"))

	hdVolumeName := ask(r, "What is the volume name of your external HD?", "SanDisk4TB")

	hdDir := ask(r, "What folder on the HD should reel manage?", "Footage")

	softDeleteStr := ask(r, "Use soft-delete (move to Trash instead of permanent delete)? [Y/n]", "y")
	softDelete := !strings.EqualFold(strings.TrimSpace(softDeleteStr), "n")

	cfg := &Config{
		LaptopDir:    laptopDir,
		HDVolumeName: hdVolumeName,
		HDDir:        hdDir,
		SoftDelete:   softDelete,
		Cameras:      defaultCameras(),
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

func ask(r *bufio.Reader, prompt, defaultVal string) string {
	fmt.Printf("  %s\n  [%s]: ", prompt, defaultVal)
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultVal
	}
	return line
}

func mustHomeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

// defaultCameras returns built-in camera profiles.
func defaultCameras() []CameraProfile {
	return []CameraProfile{
		{
			Name:            "DJI Pocket 3",
			VolumePattern:   "DJI*",
			DCIMSubdir:      "DCIM",
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
