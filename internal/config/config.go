// Package config handles loading and saving the reel configuration file,
// including a first-run interactive wizard.
package config

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/pspenano/reel/internal/safefs"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
)

const configFile = "config.json"

// ErrWizardAborted is returned when the user declines to save at the
// confirmation step of the wizard.
var ErrInvalid = errors.New("invalid configuration")

var ErrWizardAborted = errors.New("wizard aborted by user")

// CameraProfile describes one camera model's file-naming conventions.
type CameraProfile struct {
	Name            string   `json:"name"`
	VolumeName      string   `json:"volume_name"` // exact macOS volume name, e.g. "DJI Pocket 3"
	MediaPath       string   `json:"media_path"`  // path to media files relative to volume root, e.g. "DCIM"
	FilenameRegex   string   `json:"filename_regex"`
	TimestampSource string   `json:"timestamp_source"`
	TimestampGroup  string   `json:"timestamp_group"`
	TimestampFormat string   `json:"timestamp_format"`
	Extensions      []string `json:"extensions"`
	RawExtensions   []string `json:"raw_extensions"`
}

// Config is the top-level configuration structure.
type Config struct {
	TransferExtensions []string        `json:"transfer_extensions"`
	LaptopDir          string          `json:"laptop_dir"`
	HDVolumeUUID       string          `json:"hd_volume_uuid,omitempty"`
	HDVolumeName       string          `json:"hd_volume_name"`
	HDDir              string          `json:"hd_dir"`
	SoftDelete         bool            `json:"-"`
	Cameras            []CameraProfile `json:"cameras"`
}

// ShouldTransfer applies to imports and both backup routes, including old state
// rows. Omitted/null means MP4 only; an explicit empty list transfers nothing.
// Camera recognition stays separate from the cleaning policy for originals/previews.
func (c *Config) ShouldTransfer(ext string) bool {
	extensions := c.TransferExtensions
	if extensions == nil {
		extensions = []string{"MP4"}
	}
	for _, allowed := range extensions {
		if strings.EqualFold(allowed, ext) {
			return true
		}
	}
	return false
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
		return nil, fmt.Errorf("%w: configuration missing; run reel config to set up", ErrInvalid)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg := Config{SoftDelete: true}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("%w: parse config: %v", ErrInvalid, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return &cfg, nil
}

// Save writes the config to disk atomically.
func Save(cfg *Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
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

// EditConfig runs the interactive wizard with the given existing config
// pre-filled as defaults. Returns the new config on save, or
// ErrWizardAborted if the user declines to save. Does NOT write to disk;
// the caller is responsible for calling Save.
func EditConfig(existing *Config) (*Config, error) {
	return runInteractiveWizard(existing)
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	dir, err := safefs.OpenDir(filepath.Dir(path), false)
	if err != nil {
		return err
	}
	defer dir.Close()
	f, err := safefs.Fresh(dir, ".reel-config-")
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = unix.Renameat(int(dir.Fd()), filepath.Base(f.Name()), int(dir.Fd()), filepath.Base(path)); err != nil {
		return err
	}
	return dir.Sync()
}

// runWizard is the first-run entry point. Prompts the user for all config
// values, then writes the config to path.
func runWizard(path string) (*Config, error) {
	fmt.Println("reel: no config found. Running first-time setup.")
	fmt.Println()

	cfg, err := runInteractiveWizard(nil)
	if err != nil {
		return nil, err
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

func runInteractiveWizard(existing *Config) (*Config, error) {
	w := newWizardState(existing)

	w.askCameraVolume()
	w.askMediaPath()
	w.askHDVolume()
	w.askHDDir()
	w.askLaptopDir()

	accepted, err := w.confirmOrEdit()
	if err != nil {
		return nil, err
	}
	if !accepted {
		return nil, ErrWizardAborted
	}
	return w.toConfig(existing), nil
}

// wizardState holds the in-progress wizard answers and shared resources.
type wizardState struct {
	r            *bufio.Reader
	vols         []string
	cameraVolume string
	mediaPath    string
	hdVolumeName string
	hdDir        string
	laptopDir    string
	softDelete   bool
}

func newWizardState(existing *Config) *wizardState {
	vols, err := mountedVolumes()
	if err != nil || vols == nil {
		vols = []string{}
	}
	w := &wizardState{
		r:          bufio.NewReader(os.Stdin),
		vols:       vols,
		softDelete: true,
	}
	if existing != nil {
		if len(existing.Cameras) > 0 {
			w.cameraVolume = existing.Cameras[0].VolumeName
			w.mediaPath = existing.Cameras[0].MediaPath
		}
		w.hdVolumeName = existing.HDVolumeName
		w.hdDir = existing.HDDir
		w.laptopDir = existing.LaptopDir
		w.softDelete = existing.SoftDelete
	}
	return w
}

func (w *wizardState) askCameraVolume() {
	w.cameraVolume = pickVolume(w.r, w.vols, "Which volume is your camera?", w.cameraVolume)
}

func (w *wizardState) askMediaPath() {
	def := w.mediaPath
	if def == "" {
		def = "DCIM"
	}
	volumeRoot := filepath.Join("/Volumes", w.cameraVolume)
	w.mediaPath = askPath(w.r, "Where on the camera are your media files?", def, func(p string) error {
		return validateCameraMediaPath(volumeRoot, p)
	})
}

func (w *wizardState) askHDVolume() {
	hdVols := without(w.vols, w.cameraVolume)
	w.hdVolumeName = pickVolume(w.r, hdVols, "Which volume is your backup HD?", w.hdVolumeName)
}

func (w *wizardState) askHDDir() {
	def := w.hdDir
	if def == "" {
		def = "Footage"
	}
	w.hdDir = askPath(w.r, "What folder on the HD should reel manage?", def, nil)
}

func (w *wizardState) askLaptopDir() {
	def := w.laptopDir
	if def == "" {
		def = filepath.Join(mustHomeDir(), "Videos", "Footage")
	}
	w.laptopDir = askPath(w.r, "Where should imported footage go on this laptop?", def, nil)
}

func (w *wizardState) toConfig(existing *Config) *Config {
	transferExtensions := []string{"MP4"}
	if existing != nil {
		transferExtensions = existing.TransferExtensions
	}
	var cameras []CameraProfile
	if existing != nil && len(existing.Cameras) > 0 {
		cameras = append(cameras, existing.Cameras...)
	} else {
		cameras = defaultCameraProfiles()
	}
	if len(cameras) > 0 {
		cameras[0].VolumeName = w.cameraVolume
		cameras[0].MediaPath = w.mediaPath
	}
	return &Config{
		TransferExtensions: transferExtensions,
		LaptopDir:          w.laptopDir,
		HDVolumeName:       w.hdVolumeName,
		HDDir:              w.hdDir,
		SoftDelete:         true,
		Cameras:            cameras,
	}
}

func (w *wizardState) printSummary() {
	cameraResolved := filepath.Join("/Volumes", w.cameraVolume, w.mediaPath)
	hdResolved := filepath.Join("/Volumes", w.hdVolumeName, w.hdDir)
	fmt.Println()
	fmt.Println("Review your configuration:")
	fmt.Println()
	fmt.Printf("  [1] Camera volume:        %s\n", w.cameraVolume)
	fmt.Printf("  [2] Camera media path:    %s\n", w.mediaPath)
	fmt.Printf("                            → %s\n", cameraResolved)
	fmt.Printf("  [3] HD volume:            %s\n", w.hdVolumeName)
	fmt.Printf("  [4] HD folder:            %s\n", w.hdDir)
	fmt.Printf("                            → %s\n", hdResolved)
	fmt.Printf("  [5] Laptop folder:        %s\n", w.laptopDir)
	fmt.Println("  Cleaning: recoverable moves on the camera card")
	fmt.Println()
}

// confirmOrEdit shows the summary in a loop. Returns (true, nil) to save,
// (false, nil) to abort. The user may also pick a field number to re-edit;
// in that case the loop continues.
func (w *wizardState) confirmOrEdit() (bool, error) {
	for {
		w.printSummary()
		fmt.Print("  Save this configuration? [Y/n/<field#>]: ")
		line, _ := w.r.ReadString('\n')
		line = strings.ToLower(strings.TrimSpace(line))

		switch line {
		case "", "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		case "e", "edit":
			fmt.Print("  Which field number (1-5)? ")
			pick, _ := w.r.ReadString('\n')
			w.editField(strings.TrimSpace(pick))
			continue
		}

		// Try parsing as a field number.
		var n int
		if _, err := fmt.Sscanf(line, "%d", &n); err == nil && n >= 1 && n <= 5 {
			w.editByNumber(n)
			continue
		}
		fmt.Println("  Please enter Y, n, or a field number (1-5).")
	}
}

func (w *wizardState) editField(input string) {
	var n int
	if _, err := fmt.Sscanf(input, "%d", &n); err != nil || n < 1 || n > 5 {
		fmt.Println("  Invalid field number.")
		return
	}
	w.editByNumber(n)
}

func (w *wizardState) editByNumber(n int) {
	switch n {
	case 1:
		w.askCameraVolume()
		// Camera volume changed → media path validation is against the new
		// volume, so re-prompt that too.
		w.askMediaPath()
	case 2:
		w.askMediaPath()
	case 3:
		w.askHDVolume()
	case 4:
		w.askHDDir()
	case 5:
		w.askLaptopDir()
	}
}

// validateCameraMediaPath checks that volumeRoot/mediaPath exists and is
// a directory. volumeRoot is typically "/Volumes/<camera-volume>".
func validateCameraMediaPath(volumeRoot, mediaPath string) error {
	if volumeRoot == "" || volumeRoot == "/Volumes" {
		// No volume selected yet (e.g. user skipped the volume picker
		// because no volumes were mounted); can't validate. Let it through.
		return nil
	}
	full := filepath.Join(volumeRoot, mediaPath)
	info, err := os.Stat(full)
	if err != nil {
		return fmt.Errorf("not found at %s — check the path on your camera", full)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", full)
	}
	return nil
}

// sanitizePath cleans up a user-entered path:
//   - trims surrounding whitespace
//   - strips one pair of matched surrounding quotes (single or double)
//   - expands a leading "~" or "~/" to the user's home directory
func sanitizePath(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		first, last := s[0], s[len(s)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			s = s[1 : len(s)-1]
		}
	}
	s = strings.TrimSpace(s)
	if s == "~" {
		return mustHomeDir()
	}
	if strings.HasPrefix(s, "~/") {
		return filepath.Join(mustHomeDir(), s[2:])
	}
	return s
}

// askPath prompts the user, sanitizes the result, and re-prompts on
// validator error. validator may be nil.
func askPath(r *bufio.Reader, prompt, defaultVal string, validator func(string) error) string {
	for {
		fmt.Printf("\n  %s\n  [%s]: ", prompt, defaultVal)
		line, _ := r.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			line = defaultVal
		}
		line = sanitizePath(line)
		if validator != nil {
			if err := validator(line); err != nil {
				fmt.Printf("  ✗ %v\n", err)
				continue
			}
		}
		return line
	}
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
// If defaultVal matches one of the listed volumes, pressing enter selects it.
// Falls back to free-text entry if vols is empty.
func pickVolume(r *bufio.Reader, vols []string, prompt, defaultVal string) string {
	fmt.Printf("\n  %s\n", prompt)
	if len(vols) == 0 {
		if defaultVal != "" {
			fmt.Printf("  Volume name [%s]: ", defaultVal)
		} else {
			fmt.Print("  Volume name: ")
		}
		line, _ := r.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			return defaultVal
		}
		return line
	}

	defaultIdx := 0
	for i, v := range vols {
		marker := ""
		if v == defaultVal {
			marker = "  (current)"
			defaultIdx = i + 1
		}
		fmt.Printf("    [%d] %s%s\n", i+1, v, marker)
	}
	for {
		if defaultIdx > 0 {
			fmt.Printf("  Enter number (1-%d) [%d]: ", len(vols), defaultIdx)
		} else {
			fmt.Printf("  Enter number (1-%d): ", len(vols))
		}
		line, _ := r.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" && defaultIdx > 0 {
			return vols[defaultIdx-1]
		}
		var n int
		if _, err := fmt.Sscanf(line, "%d", &n); err == nil && n >= 1 && n <= len(vols) {
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

// UnmarshalJSON keeps only explicit compatibility with legacy recoverable mode.
func (c *Config) UnmarshalJSON(data []byte) error {
	type plain Config
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if fields == nil {
		return fmt.Errorf("configuration must be an object")
	}
	if v, ok := fields["soft_delete"]; ok && strings.TrimSpace(string(v)) != "true" {
		return fmt.Errorf("soft_delete is obsolete: remove it or set it to true; permanent deletion is unavailable")
	}
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*c = Config(p)
	c.SoftDelete = true
	return nil
}
func safeRelative(p string) bool {
	if p == "" || filepath.IsAbs(p) {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}
func (c *Config) Validate() error {
	if !filepath.IsAbs(c.LaptopDir) {
		return fmt.Errorf("laptop_dir must be absolute")
	}
	if !safeRelative(c.HDDir) || filepath.Base(c.HDVolumeName) != c.HDVolumeName || c.HDVolumeName == "" || c.HDVolumeName == ".." {
		return fmt.Errorf("unsafe backup volume or relative hd_dir")
	}
	seen := map[string]bool{}
	for _, p := range c.Cameras {
		if p.Name == "" || seen[p.Name] {
			return fmt.Errorf("missing or duplicate camera profile")
		}
		seen[p.Name] = true
		if !safeRelative(p.MediaPath) || p.VolumeName == "" || p.VolumeName == ".." || filepath.Base(p.VolumeName) != p.VolumeName {
			return fmt.Errorf("unsafe camera path for %s", p.Name)
		}
		if p.VolumeName == c.HDVolumeName {
			return fmt.Errorf("camera and backup must use independent volumes")
		}
	}
	return nil
}
