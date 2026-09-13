package safefs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// CheckExclusive probes this filesystem using exclusively owned empty files,
// before risking a media move. Probe records are retained for diagnostics.
func CheckExclusive(parent *os.File) error {
	if err := SupportedFS(parent); err != nil {
		return err
	}
	d, err := FreshDir(parent, ".reel-capability-")
	if err != nil {
		return err
	}
	defer d.Close()
	a, err := Fresh(d, "source-")
	if err != nil {
		return err
	}
	defer a.Close()
	b, err := Fresh(d, "occupied-")
	if err != nil {
		return err
	}
	defer b.Close()
	before, err := b.Stat()
	if err != nil {
		return err
	}
	err = RenameExclusive(d, filepath.Base(a.Name()), d, filepath.Base(b.Name()))
	if !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("filesystem does not provide exclusive rename collision semantics: %v", err)
	}
	after, err := Open(b.Name())
	if err != nil {
		return err
	}
	info, err := after.Stat()
	after.Close()
	if err != nil {
		return err
	}
	if !os.SameFile(before, info) {
		return fmt.Errorf("exclusive rename probe replaced a target")
	}
	if err = RenameExclusive(d, filepath.Base(a.Name()), d, "published"); err != nil {
		return fmt.Errorf("exclusive rename unsupported: %w", err)
	}
	if err = d.Sync(); err != nil {
		return err
	}
	return parent.Sync()
}
