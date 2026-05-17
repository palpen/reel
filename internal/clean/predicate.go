// Package clean contains the safety predicate for deciding whether a camera
// file may be deleted after being backed up to the HD.
package clean

import "time"

// FileState encapsulates all the information needed to make a delete decision.
type FileState struct {
	CameraProfile  string
	BaseName       string
	Ext            string
	CameraPath     string
	HDPath         string
	HDFileExists   bool
	HDFileSize     int64
	StateSize      int64
	HDFileSHA256   string
	StateSHA256    string
	HDVerifiedAt   time.Time
	Now            time.Time
	StaleThreshold time.Duration
	ForceStale     bool
}

// Decision is the output of ShouldDelete.
type Decision struct {
	Delete bool
	Reason string
}

// ShouldDelete applies the safety invariants D1–D8 in order.
// The first failing invariant wins; all must pass for Delete=true.
func ShouldDelete(s FileState) Decision {
	// D1: must have an HD path recorded
	if s.HDPath == "" {
		return Decision{false, "not backed up to HD"}
	}
	// D2: HD copy must have been verified at least once
	if s.HDVerifiedAt.IsZero() {
		return Decision{false, "HD copy never verified"}
	}
	// D3: HD file must physically exist
	if !s.HDFileExists {
		return Decision{false, "HD copy missing from disk"}
	}
	// D4: on-disk size must match state
	if s.HDFileSize != s.StateSize {
		return Decision{false, "HD file size mismatch"}
	}
	// D5: hash must match state
	if s.HDFileSHA256 != s.StateSHA256 {
		return Decision{false, "HD file hash mismatch"}
	}
	// D6: verification must not be stale (unless --force-stale)
	if !s.ForceStale && s.Now.Sub(s.HDVerifiedAt) > s.StaleThreshold {
		return Decision{false, "verification stale"}
	}
	// D7: must have a camera path recorded (so we know what to delete)
	if s.CameraPath == "" {
		return Decision{false, "no camera path recorded"}
	}
	// D8: all invariants satisfied
	return Decision{true, ""}
}
