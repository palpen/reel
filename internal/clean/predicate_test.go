package clean_test

import (
	"testing"
	"time"

	"github.com/pspenano/reel/internal/clean"
)

var (
	baseTime      = time.Date(2026, 5, 10, 11, 18, 26, 0, time.UTC)
	recentVerify  = baseTime.Add(-1 * time.Hour)          // 1h ago — fresh
	staleVerify   = baseTime.Add(-8 * 24 * time.Hour)     // 8d ago — stale
	staleThreshold = 7 * 24 * time.Hour
)

// goodState returns a FileState where all invariants pass.
func goodState() clean.FileState {
	return clean.FileState{
		CameraProfile:  "DJI Pocket 3",
		BaseName:       "DJI_20260510111826_0015_D",
		Ext:            "MP4",
		CameraPath:     "/Volumes/DJI_MINI/DCIM/DJI_20260510111826_0015_D.MP4",
		HDPath:         "/Volumes/SanDisk4TB/Footage/DJI_20260510111826_0015_D.MP4",
		HDFileExists:   true,
		HDFileSize:     1024,
		StateSize:      1024,
		HDFileSHA256:   "abc123",
		StateSHA256:    "abc123",
		HDVerifiedAt:   recentVerify,
		Now:            baseTime,
		StaleThreshold: staleThreshold,
		ForceStale:     false,
	}
}

func TestShouldDelete(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(s *clean.FileState)
		wantDelete bool
		wantReason string
	}{
		// Happy path
		{
			name:       "all_pass",
			mutate:     nil,
			wantDelete: true,
			wantReason: "",
		},
		// D1
		{
			name:       "D1_no_hd_path",
			mutate:     func(s *clean.FileState) { s.HDPath = "" },
			wantDelete: false,
			wantReason: "not backed up to HD",
		},
		// D2
		{
			name:       "D2_never_verified",
			mutate:     func(s *clean.FileState) { s.HDVerifiedAt = time.Time{} },
			wantDelete: false,
			wantReason: "HD copy never verified",
		},
		// D3
		{
			name:       "D3_file_missing",
			mutate:     func(s *clean.FileState) { s.HDFileExists = false },
			wantDelete: false,
			wantReason: "HD copy missing from disk",
		},
		// D4
		{
			name:       "D4_size_mismatch",
			mutate:     func(s *clean.FileState) { s.HDFileSize = 9999 },
			wantDelete: false,
			wantReason: "HD file size mismatch",
		},
		{
			name:       "D4_state_size_zero",
			mutate:     func(s *clean.FileState) { s.StateSize = 0 },
			wantDelete: false,
			wantReason: "HD file size mismatch",
		},
		// D5
		{
			name:       "D5_hash_mismatch",
			mutate:     func(s *clean.FileState) { s.HDFileSHA256 = "bad" },
			wantDelete: false,
			wantReason: "HD file hash mismatch",
		},
		{
			name:       "D5_state_sha_empty",
			mutate:     func(s *clean.FileState) { s.StateSHA256 = "" },
			wantDelete: false,
			wantReason: "HD file hash mismatch",
		},
		// D6
		{
			name:       "D6_stale_verification",
			mutate:     func(s *clean.FileState) { s.HDVerifiedAt = staleVerify },
			wantDelete: false,
			wantReason: "verification stale",
		},
		{
			name: "D6_stale_but_force",
			mutate: func(s *clean.FileState) {
				s.HDVerifiedAt = staleVerify
				s.ForceStale = true
			},
			wantDelete: true,
			wantReason: "",
		},
		{
			name: "D6_exactly_at_threshold",
			mutate: func(s *clean.FileState) {
				// Exactly at the threshold boundary (not over)
				s.HDVerifiedAt = baseTime.Add(-staleThreshold)
			},
			wantDelete: true, // Not strictly greater, so passes
			wantReason: "",
		},
		{
			name: "D6_one_ns_over_threshold",
			mutate: func(s *clean.FileState) {
				s.HDVerifiedAt = baseTime.Add(-staleThreshold - 1)
			},
			wantDelete: false,
			wantReason: "verification stale",
		},
		// D7
		{
			name:       "D7_no_camera_path",
			mutate:     func(s *clean.FileState) { s.CameraPath = "" },
			wantDelete: false,
			wantReason: "no camera path recorded",
		},
		// Combined: D1 takes precedence over D2
		{
			name: "D1_before_D2",
			mutate: func(s *clean.FileState) {
				s.HDPath = ""
				s.HDVerifiedAt = time.Time{}
			},
			wantDelete: false,
			wantReason: "not backed up to HD",
		},
		// Combined: D2 before D3
		{
			name: "D2_before_D3",
			mutate: func(s *clean.FileState) {
				s.HDVerifiedAt = time.Time{}
				s.HDFileExists = false
			},
			wantDelete: false,
			wantReason: "HD copy never verified",
		},
		// Combined: D3 before D4
		{
			name: "D3_before_D4",
			mutate: func(s *clean.FileState) {
				s.HDFileExists = false
				s.HDFileSize = 9999
			},
			wantDelete: false,
			wantReason: "HD copy missing from disk",
		},
		// Combined: D4 before D5
		{
			name: "D4_before_D5",
			mutate: func(s *clean.FileState) {
				s.HDFileSize = 9999
				s.HDFileSHA256 = "bad"
			},
			wantDelete: false,
			wantReason: "HD file size mismatch",
		},
		// Combined: D5 before D6
		{
			name: "D5_before_D6",
			mutate: func(s *clean.FileState) {
				s.HDFileSHA256 = "bad"
				s.HDVerifiedAt = staleVerify
			},
			wantDelete: false,
			wantReason: "HD file hash mismatch",
		},
		// Combined: D6 before D7
		{
			name: "D6_before_D7",
			mutate: func(s *clean.FileState) {
				s.HDVerifiedAt = staleVerify
				s.CameraPath = ""
			},
			wantDelete: false,
			wantReason: "verification stale",
		},
		// Edge: zero-value Now (unusual but shouldn't panic)
		{
			name: "zero_now",
			mutate: func(s *clean.FileState) {
				s.Now = time.Time{}
				// HDVerifiedAt is in the past relative to zero-Now — negative duration
				// means Now.Sub(HDVerifiedAt) is negative, which is not > threshold
			},
			wantDelete: true,
			wantReason: "",
		},
		// All fields zero except the ones that matter for D1
		{
			name: "empty_state",
			mutate: func(s *clean.FileState) {
				*s = clean.FileState{}
			},
			wantDelete: false,
			wantReason: "not backed up to HD",
		},
		// ForceStale with otherwise good state
		{
			name: "force_stale_with_good_state",
			mutate: func(s *clean.FileState) {
				s.ForceStale = true
			},
			wantDelete: true,
			wantReason: "",
		},
		// D5 with matching empty strings (both empty = match — this is intentional for "no hash yet")
		// In practice StateSHA256="" means we should catch it via D5 but only if HDFileSHA256 != StateSHA256
		// Empty == Empty is actually a match — but in goodState both are "abc123" so this tests only empty-HD
		{
			name: "D5_both_empty_hashes",
			mutate: func(s *clean.FileState) {
				s.HDFileSHA256 = ""
				s.StateSHA256 = ""
			},
			wantDelete: true, // both empty strings are equal
			wantReason: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := goodState()
			if tc.mutate != nil {
				tc.mutate(&s)
			}
			got := clean.ShouldDelete(s)
			if got.Delete != tc.wantDelete {
				t.Errorf("ShouldDelete() Delete = %v, want %v (reason: %q)", got.Delete, tc.wantDelete, got.Reason)
			}
			if tc.wantReason != "" && got.Reason != tc.wantReason {
				t.Errorf("ShouldDelete() Reason = %q, want %q", got.Reason, tc.wantReason)
			}
			if tc.wantDelete && got.Reason != "" {
				t.Errorf("ShouldDelete() returned Delete=true but Reason=%q (expected empty)", got.Reason)
			}
		})
	}
}
