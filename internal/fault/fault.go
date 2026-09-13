// Package fault exposes deterministic checkpoints for fixture tests. Production
// leaves Hook nil; no environment variable or command-line switch enables it.
package fault

var Hook func(string) error

func Check(name string) error {
	if Hook != nil {
		return Hook(name)
	}
	return nil
}
