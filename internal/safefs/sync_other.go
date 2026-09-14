//go:build !darwin

package safefs

import "os"

func DurableSync(f *os.File) error { return f.Sync() }
