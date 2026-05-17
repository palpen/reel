package clean_test

import (
	"testing"
	"testing/quick"
	"time"

	"github.com/pspenano/reel/internal/clean"
)

// TestShouldDeletePurity verifies that ShouldDelete is a pure function:
// identical inputs always produce identical outputs.
func TestShouldDeletePurity(t *testing.T) {
	f := func(
		hdPath, cameraPath, hdSHA, stateSHA string,
		hdExists bool,
		hdSize, stateSize int64,
		verifiedSecs, nowSecs int64,
		staleSecs uint32,
		forceStale bool,
	) bool {
		if staleSecs == 0 {
			staleSecs = 1
		}
		var verifiedAt time.Time
		if verifiedSecs != 0 {
			verifiedAt = time.Unix(verifiedSecs, 0)
		}
		now := time.Unix(nowSecs, 0)
		stale := time.Duration(staleSecs) * time.Second

		s := clean.FileState{
			HDPath:         hdPath,
			CameraPath:     cameraPath,
			HDFileSHA256:   hdSHA,
			StateSHA256:    stateSHA,
			HDFileExists:   hdExists,
			HDFileSize:     hdSize,
			StateSize:      stateSize,
			HDVerifiedAt:   verifiedAt,
			Now:            now,
			StaleThreshold: stale,
			ForceStale:     forceStale,
		}

		d1 := clean.ShouldDelete(s)
		d2 := clean.ShouldDelete(s)
		return d1.Delete == d2.Delete && d1.Reason == d2.Reason
	}

	if err := quick.Check(f, &quick.Config{MaxCount: 500}); err != nil {
		t.Errorf("purity property failed: %v", err)
	}
}

// TestShouldDeleteTrueImpliesAllChecksFail verifies that when ShouldDelete
// returns true, each individual invariant check (D1–D7) would return false
// if applied in isolation (i.e., the condition that triggers each check is satisfied).
func TestShouldDeleteTrueImpliesInvariantsHold(t *testing.T) {
	// When Delete=true, check that:
	// - HDPath != ""              (D1 condition satisfied)
	// - HDVerifiedAt is not zero  (D2 condition satisfied)
	// - HDFileExists is true      (D3 condition satisfied)
	// - HDFileSize == StateSize   (D4 condition satisfied)
	// - HDFileSHA256 == StateSHA256 (D5 condition satisfied)
	// - Either not stale or ForceStale (D6 condition satisfied)
	// - CameraPath != ""          (D7 condition satisfied)
	f := func(
		hdPath, cameraPath, hdSHA, stateSHA string,
		hdExists bool,
		hdSize, stateSize int64,
		verifiedSecs, nowSecs int64,
		staleSecs uint32,
		forceStale bool,
	) bool {
		if staleSecs == 0 {
			staleSecs = 1
		}
		var verifiedAt time.Time
		if verifiedSecs != 0 {
			verifiedAt = time.Unix(verifiedSecs, 0)
		}
		now := time.Unix(nowSecs, 0)
		stale := time.Duration(staleSecs) * time.Second

		s := clean.FileState{
			HDPath:         hdPath,
			CameraPath:     cameraPath,
			HDFileSHA256:   hdSHA,
			StateSHA256:    stateSHA,
			HDFileExists:   hdExists,
			HDFileSize:     hdSize,
			StateSize:      stateSize,
			HDVerifiedAt:   verifiedAt,
			Now:            now,
			StaleThreshold: stale,
			ForceStale:     forceStale,
		}

		d := clean.ShouldDelete(s)
		if !d.Delete {
			return true // nothing to check
		}

		// If Delete=true, ALL of the following must hold:
		if s.HDPath == "" {
			return false // D1 violated
		}
		if s.HDVerifiedAt.IsZero() {
			return false // D2 violated
		}
		if !s.HDFileExists {
			return false // D3 violated
		}
		if s.HDFileSize != s.StateSize {
			return false // D4 violated
		}
		if s.HDFileSHA256 != s.StateSHA256 {
			return false // D5 violated
		}
		if !s.ForceStale && s.Now.Sub(s.HDVerifiedAt) > s.StaleThreshold {
			return false // D6 violated
		}
		if s.CameraPath == "" {
			return false // D7 violated
		}
		return true
	}

	if err := quick.Check(f, &quick.Config{MaxCount: 500}); err != nil {
		t.Errorf("invariant property failed: %v", err)
	}
}
