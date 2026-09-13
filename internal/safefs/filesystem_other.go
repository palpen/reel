//go:build !darwin

package safefs

import "os"

// Non-macOS builds support disposable primitive tests; volume.Resolve rejects
// production CLI mutations without macOS DiskManagement.
func SupportedFS(dir *os.File) error { return nil }
