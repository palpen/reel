package main

import (
	"os"

	"github.com/pspenano/reel/cmd"
	"github.com/pspenano/reel/internal/config"
	"github.com/pspenano/reel/internal/logger"
)

var version = "dev"

func main() {
	// Best-effort logger setup. Ignore errors (can't log to log yet).
	if cfgDir, err := config.Dir(); err == nil {
		logger.Setup(cfgDir) //nolint:errcheck
	}

	os.Exit(cmd.Run(os.Args[1:], version))
}
