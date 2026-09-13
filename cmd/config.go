package cmd

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pspenano/reel/internal/config"
	"github.com/pspenano/reel/internal/display"
)

// RunConfig implements `reel config`. It re-runs the interactive wizard
// against the existing config, pre-filling current values as defaults.
func RunConfig(args []string) error {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unsupported arguments: %v", fs.Args())
	}

	existing, err := loadConfig()
	if err != nil {
		dir, e := configDir()
		if e != nil {
			return e
		}
		if _, e = os.Stat(filepath.Join(dir, "config.json")); !os.IsNotExist(e) {
			return err
		}
		existing = nil
	}

	updated, err := config.EditConfig(existing)
	if err != nil {
		if errors.Is(err, config.ErrWizardAborted) {
			display.Info("Config not modified.")
			return nil
		}
		return err
	}

	id, err := resolveVolume(updated.HDRoot())
	if err != nil {
		return err
	}
	updated.HDVolumeUUID = id.UUID
	if err := config.Save(updated); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	display.Info("Config updated.")
	return nil
}
