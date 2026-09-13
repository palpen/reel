// Package logger optionally opens a fresh structured log; it never rotates or
// overwrites existing files. The CLI uses stderr and performs no startup writes.
package logger

import (
	"github.com/pspenano/reel/internal/safefs"
	"log/slog"
)

func Setup(cfgDir string) error {
	dir, err := safefs.OpenDir(cfgDir, true)
	if err != nil {
		return err
	}
	defer dir.Close()
	f, err := safefs.Fresh(dir, "reel-log-")
	if err != nil {
		return err
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo})))
	return nil
}
