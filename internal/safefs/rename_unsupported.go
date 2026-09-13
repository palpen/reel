//go:build (darwin && !cgo) || (!darwin && !linux)

package safefs

import "fmt"

func renameExclusive(a int, src string, b int, dst string) error {
	return fmt.Errorf("exclusive rename unavailable; macOS mutations require a cgo build")
}
