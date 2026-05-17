// Package logger sets up the structured slog logger with size-based rotation.
package logger

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

const (
	maxLogSize = 5 * 1024 * 1024 // 5MB
	logFile    = "reel.log"
)

// Setup initializes the global slog logger, writing to ~/.config/reel/reel.log.
// If the log file exceeds 5MB, it is rotated (keeping .1 and .2).
func Setup(cfgDir string) error {
	logPath := filepath.Join(cfgDir, logFile)

	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	// Rotate if over max size
	if info, err := os.Stat(logPath); err == nil && info.Size() > maxLogSize {
		rotate(logPath)
	}

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}

	h := slog.NewJSONHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.SetDefault(slog.New(h))
	return nil
}

// rotate renames path -> path.1 -> path.2, dropping .2+ if it exists.
func rotate(path string) {
	p2 := path + ".2"
	p1 := path + ".1"
	os.Remove(p2)
	os.Rename(p1, p2)
	os.Rename(path, p1)
}
