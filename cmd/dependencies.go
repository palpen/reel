package cmd

import (
	"github.com/pspenano/reel/internal/camera"
	"github.com/pspenano/reel/internal/config"
	"github.com/pspenano/reel/internal/volume"
	"path/filepath"
)

// Package-private dependencies permit fixture-only command tests. There are no
// environment variables or CLI options that bypass production volume checks.
var loadConfig = config.Load
var configDir = config.Dir
var detectCameras = camera.Detect
var resolveVolume = volume.Resolve
var checkVolume = func(id volume.Identity) error { return id.Check() }
var hdRoot = func(c *config.Config) string { return c.HDRoot() }

func hdManaged(c *config.Config) string { return filepath.Join(hdRoot(c), c.HDDir) }
func hdState(c *config.Config) string   { return filepath.Join(hdRoot(c), ".reel-state.jsonl") }
