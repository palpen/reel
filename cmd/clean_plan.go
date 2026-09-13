package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pspenano/reel/internal/camera"
	"github.com/pspenano/reel/internal/clean"
	"github.com/pspenano/reel/internal/state"
	"github.com/pspenano/reel/internal/transfer"
)

type fileSnapshot struct {
	path string
	info os.FileInfo
	hash string
}

func snapshotFile(path string) (fileSnapshot, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != filepath.Clean(path) {
		return fileSnapshot{}, fmt.Errorf("missing file or symlink: %s", path)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return fileSnapshot{}, fmt.Errorf("not a regular file: %s", path)
	}
	hash, _, err := transfer.HashFile(path)
	if err != nil {
		return fileSnapshot{}, err
	}
	return fileSnapshot{path: path, info: info, hash: hash}, nil
}

func (s fileSnapshot) check() error {
	current, err := snapshotFile(s.path)
	if err != nil {
		return err
	}
	if s.hash != current.hash || !os.SameFile(s.info, current.info) || s.info.Size() != current.info.Size() || !s.info.ModTime().Equal(current.info.ModTime()) {
		return fmt.Errorf("file changed: %s", s.path)
	}
	return nil
}

type cleanCandidate struct {
	file      camera.File
	row       *state.Row
	decision  clean.Decision
	snapshots []fileSnapshot
	note      string
	validate  func() error
}

// planClean verifies originals independently. An LRF may rely on the current
// MP4 in the same camera/profile/directory, never on an unrelated or historic row.
// WAV is original audio: an unbacked WAV stays, without blocking a verified MP4.
func planClean(files []camera.File, st *state.Store, now time.Time, forceStale bool) (eligible, held []cleanCandidate) {
	candidates := make([]cleanCandidate, len(files))
	mp4s := make(map[string][]int)
	clipKey := func(f camera.File) string {
		return f.Profile.Name + "\x00" + filepath.Dir(f.FullPath) + "\x00" + f.BaseName
	}
	for i, f := range files {
		candidates[i] = cleanCandidate{file: f, row: st.GetByParts(f.Profile.Name, f.BaseName, f.Ext)}
		if strings.EqualFold(f.Ext, "LRF") {
			continue
		}
		verifyOriginal(&candidates[i], now, forceStale)
		if strings.EqualFold(f.Ext, "MP4") {
			mp4s[clipKey(f)] = append(mp4s[clipKey(f)], i)
		}
	}
	for i := range candidates {
		c := &candidates[i]
		if !strings.EqualFold(c.file.Ext, "LRF") {
			continue
		}
		matches := mp4s[clipKey(c.file)]
		if len(matches) != 1 {
			c.decision = clean.Decision{Reason: "LRF needs a matching MP4 on the camera"}
			continue
		}
		mp4 := &candidates[matches[0]]
		if !mp4.decision.Delete {
			c.decision = clean.Decision{Reason: "matching MP4: " + mp4.decision.Reason}
			continue
		}
		snapshot, err := snapshotFile(c.file.FullPath)
		if err != nil {
			c.decision = clean.Decision{Reason: err.Error()}
			continue
		}
		c.snapshots = append([]fileSnapshot{snapshot}, mp4.snapshots...)
		c.decision = clean.Decision{Delete: true}
		c.note = " (preview; matching MP4 backup verified)"
		// Do not invent an LRF backup or canonical hash. A new row records only
		// camera identity and the eventual clean event; prior rows retain history.
		if c.row == nil {
			c.row = &state.Row{CameraProfile: c.file.Profile.Name, BaseName: c.file.BaseName,
				Ext: c.file.Ext, CameraPath: c.file.FullPath, RecordedAt: c.file.RecordedAt,
				SizeBytes: snapshot.info.Size()}
		}
	}
	for _, c := range candidates {
		if c.decision.Delete {
			eligible = append(eligible, c)
		} else {
			held = append(held, c)
		}
	}
	// Remove previews before originals. If a preview move fails, stop with the
	// corresponding MP4 still on the card so a later run can retry safely.
	sort.SliceStable(eligible, func(i, j int) bool {
		return strings.EqualFold(eligible[i].file.Ext, "LRF") && !strings.EqualFold(eligible[j].file.Ext, "LRF")
	})
	return eligible, held
}

func verifyOriginal(c *cleanCandidate, now time.Time, forceStale bool) {
	fail := func(reason string) { c.decision = clean.Decision{Reason: reason} }
	r := c.row
	if r == nil {
		fail("not tracked in state")
		return
	}
	if filepath.Clean(r.CameraPath) != filepath.Clean(c.file.FullPath) {
		fail("camera path differs from recorded original")
		return
	}
	src, err := snapshotFile(c.file.FullPath)
	if err != nil {
		fail(err.Error())
		return
	}
	s := clean.FileState{HDPath: r.HDPath, CameraPath: r.CameraPath, StateSize: r.SizeBytes,
		StateSHA256: r.SHA256, Now: now, StaleThreshold: defaultStaleThreshold, ForceStale: forceStale}
	if r.HDVerifiedAt != nil {
		s.HDVerifiedAt = *r.HDVerifiedAt
	}
	if r.HDPath != "" {
		hd, err := snapshotFile(r.HDPath)
		if err == nil {
			if os.SameFile(src.info, hd.info) {
				fail("backup points to the camera original")
				return
			}
			s.HDFileExists, s.HDFileSize = true, hd.info.Size()
			s.HDFileSHA256, _, err = transfer.HashFile(r.HDPath)
			if err != nil {
				fail("could not hash HD backup")
				return
			}
			c.snapshots = []fileSnapshot{src, hd}
		}
	}
	c.decision = clean.ShouldDelete(s)
	if !c.decision.Delete {
		return
	}
	// A reused filename must not let an old backup authorize a new recording.
	if src.info.Size() != r.SizeBytes {
		fail("camera original size differs from verified backup")
		return
	}
	hash, _, err := transfer.HashFile(c.file.FullPath)
	if err != nil || hash != r.SHA256 {
		fail("camera original hash differs from verified backup")
		return
	}
	for _, snapshot := range c.snapshots {
		if err := snapshot.check(); err != nil {
			fail(err.Error())
			return
		}
	}
}
