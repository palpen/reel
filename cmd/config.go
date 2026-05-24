package cmd

import (
	"errors"
	"flag"
	"fmt"

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

	existing, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	updated, err := config.EditConfig(existing)
	if err != nil {
		if errors.Is(err, config.ErrWizardAborted) {
			display.Info("Config not modified.")
			return nil
		}
		return err
	}

	if err := config.Save(updated); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	display.Info("Config updated.")
	return nil
}
