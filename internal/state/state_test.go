package state_test

import (
	"encoding/json"
	"fmt"
	"github.com/pspenano/reel/internal/fault"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/pspenano/reel/internal/state"
)

func makeRow(profile, base, ext string) *state.Row {
	t := time.Date(2026, 5, 10, 11, 18, 26, 0, time.UTC)
	return &state.Row{
		CameraProfile: profile,
		BaseName:      base,
		Ext:           ext,
		RecordedAt:    t,
		SizeBytes:     1024,
		SHA256:        "deadbeef",
		CameraPath:    "/Volumes/DJI/DCIM/" + base + "." + ext,
		LaptopPath:    "/Users/test/Videos/" + base + "." + ext,
	}
}

func TestRoundTrip(t *testing.T) {
	dir := realTempDir(t)
	path := filepath.Join(dir, "state.jsonl")

	st, err := state.Load(path)
	if err != nil {
		t.Fatalf("Load (empty): %v", err)
	}
	if st.Len() != 0 {
		t.Fatalf("expected 0 rows, got %d", st.Len())
	}

	r1 := makeRow("DJI Pocket 3", "DJI_20260510111826_0015_D", "MP4")
	r2 := makeRow("DJI Pocket 3", "DJI_20260510111826_0015_D", "LRF")
	r3 := makeRow("DJI Pocket 3", "DJI_20260510111826_0016_D", "MP4")

	for _, r := range []*state.Row{r1, r2, r3} {
		if err := st.Upsert(r); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
	}

	// Reload
	st2, err := state.Load(path)
	if err != nil {
		t.Fatalf("Load (after write): %v", err)
	}
	if st2.Len() != 3 {
		t.Fatalf("expected 3 rows, got %d", st2.Len())
	}

	got := st2.GetByParts("DJI Pocket 3", "DJI_20260510111826_0015_D", "MP4")
	if got == nil {
		t.Fatal("expected row not found after reload")
	}
	if got.SHA256 != "deadbeef" {
		t.Errorf("SHA256 = %q, want %q", got.SHA256, "deadbeef")
	}
	if got.SizeBytes != 1024 {
		t.Errorf("SizeBytes = %d, want 1024", got.SizeBytes)
	}
}

func TestAtomicWrite(t *testing.T) {
	dir := realTempDir(t)
	path := filepath.Join(dir, "state.jsonl")

	st, _ := state.Load(path)
	r := makeRow("DJI Pocket 3", "DJI_20260510111826_0001_D", "MP4")
	st.Upsert(r)

	// No .tmp file should remain after save
	tmp := path + ".tmp"
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Error("stale .tmp file found after atomic write")
	}

	// Primary file should exist
	if _, err := os.Stat(path); err != nil {
		t.Errorf("primary state file not found: %v", err)
	}
}

func TestStaleTmpCleanup(t *testing.T) {
	dir := realTempDir(t)
	path := filepath.Join(dir, "state.jsonl")
	tmp := path + ".tmp"

	// Create a stale .tmp
	os.WriteFile(tmp, []byte(`{"schema_version":1,"camera_profile":"X","base_name":"Y","ext":"Z"}`+"\n"), 0o600)

	// Load should remove it
	_, err := state.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := os.Stat(tmp); err != nil {
		t.Error("read-only load removed a partial")
	}
}

func TestDuplicateKeyWarning(t *testing.T) {
	dir := realTempDir(t)
	path := filepath.Join(dir, "state.jsonl")

	// Write a file with two rows with the same key
	line := `{"schema_version":1,"camera_profile":"DJI Pocket 3","base_name":"DJI_20260510111826_0001_D","ext":"MP4","sha256":"first"}` + "\n" +
		`{"schema_version":1,"camera_profile":"DJI Pocket 3","base_name":"DJI_20260510111826_0001_D","ext":"MP4","sha256":"second"}` + "\n"
	os.WriteFile(path, []byte(line), 0o600)

	if _, err := state.Load(path); err == nil {
		t.Fatal("ambiguous state accepted")
	}
}

func TestForwardCompatibility(t *testing.T) {
	dir := realTempDir(t)
	path := filepath.Join(dir, "state.jsonl")

	// Write a row with an unknown field
	line := `{"schema_version":1,"camera_profile":"DJI Pocket 3","base_name":"DJI_20260510111826_0001_D","ext":"MP4","sha256":"abc","future_field":"hello"}` + "\n"
	os.WriteFile(path, []byte(line), 0o600)

	st, err := state.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Save and reload
	if err := st.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	data, _ := os.ReadFile(path)
	var m map[string]json.RawMessage
	for _, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("parse saved row: %v", err)
		}
		// future_field should be preserved
		if _, ok := m["future_field"]; !ok {
			t.Error("future_field was lost during round-trip")
		}
	}
}

func TestUpsertUpdates(t *testing.T) {
	dir := realTempDir(t)
	path := filepath.Join(dir, "state.jsonl")

	st, _ := state.Load(path)
	r := makeRow("DJI Pocket 3", "DJI_20260510111826_0001_D", "MP4")
	st.Upsert(r)

	// Update
	r.LaptopPath = "/updated/path"
	now := time.Now().UTC()
	r.ImportedAt = &now
	st.Upsert(r)

	// Reload
	st2, _ := state.Load(path)
	got := st2.GetByParts("DJI Pocket 3", "DJI_20260510111826_0001_D", "MP4")
	if got.LaptopPath != "/updated/path" {
		t.Errorf("LaptopPath = %q", got.LaptopPath)
	}
	if got.ImportedAt == nil {
		t.Error("ImportedAt should be set")
	}
	// Should still be exactly 1 row (not doubled)
	if st2.Len() != 1 {
		t.Errorf("expected 1 row, got %d", st2.Len())
	}
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}

func realTempDir(t *testing.T) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFailedPersistenceDoesNotCommitMemory(t *testing.T) {
	root := realTempDir(t)
	s, err := state.Load(filepath.Join(root, "state.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	r := makeRow("camera", "clip", "MP4")
	if err = s.Upsert(r); err != nil {
		t.Fatal(err)
	}
	r = s.All()[0]
	r.LaptopPath = "changed"
	fault.Hook = func(name string) error {
		if name == "state-save" {
			return fmt.Errorf("injected save failure")
		}
		return nil
	}
	defer func() { fault.Hook = nil }()
	if err = s.Upsert(r); err == nil {
		t.Fatal("save failure ignored")
	}
	if s.All()[0].LaptopPath == "changed" {
		t.Fatal("failed save committed to memory")
	}
}

func TestStateSyncFailureRetainsCompleteStateAndRetries(t *testing.T) {
	for _, step := range []string{"state-file-sync", "state-directory-sync"} {
		t.Run(step, func(t *testing.T) {
			path := filepath.Join(realTempDir(t), "state.jsonl")
			st, err := state.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			row := makeRow("camera", "clip", "MP4")
			if err := st.Upsert(row); err != nil {
				t.Fatal(err)
			}
			oldPath := row.LaptopPath
			row.LaptopPath = "/new/location/clip.MP4"
			failed := false
			fault.Hook = func(name string) error {
				if name == step {
					failed = true
					return fmt.Errorf("injected %s", step)
				}
				return nil
			}
			defer func() { fault.Hook = nil }()
			if err := st.Upsert(row); err == nil || !failed {
				t.Fatalf("sync failure missed: %v", err)
			}
			if got := st.Get(row.Key()); got.LaptopPath != oldPath {
				t.Fatal("failed write reported committed in memory")
			}
			disk, err := state.Load(path)
			if err != nil || disk.Len() != 1 {
				t.Fatalf("state became unreadable: %v", err)
			}
			wantPath := oldPath
			if step == "state-directory-sync" {
				// Rename has happened, but its durability is uncertain. The
				// visible file must still contain a complete, valid state.
				wantPath = row.LaptopPath
			}
			if got := disk.Get(row.Key()); got.LaptopPath != wantPath || got.SHA256 != row.SHA256 {
				t.Fatalf("unexpected state after failure: %+v", got)
			}
			fault.Hook = nil
			if err := st.Upsert(row); err != nil {
				t.Fatal(err)
			}
			disk, err = state.Load(path)
			if err != nil || disk.Get(row.Key()).LaptopPath != row.LaptopPath {
				t.Fatalf("retry did not persist state: %v", err)
			}
		})
	}
}

func TestMirrorMergesHistoryAndRejectsConflicts(t *testing.T) {
	root := realTempDir(t)
	a, _ := state.Load(filepath.Join(root, "a.jsonl"))
	b, _ := state.Load(filepath.Join(root, "b.jsonl"))
	mirror := filepath.Join(root, "mirror.jsonl")
	one, two := makeRow("camera", "one", "MP4"), makeRow("camera", "two", "MP4")
	a.Upsert(one)
	b.Upsert(two)
	if err := a.MirrorTo(mirror); err != nil {
		t.Fatal(err)
	}
	if err := b.MirrorTo(mirror); err != nil {
		t.Fatal(err)
	}
	m, err := state.Load(mirror)
	if err != nil || m.Len() != 2 {
		t.Fatal("mirror erased unrelated history", err)
	}
	c, _ := state.Load(filepath.Join(root, "c.jsonl"))
	one.SHA256 = "conflict"
	c.Upsert(one)
	before, _ := os.ReadFile(mirror)
	if err := c.MirrorTo(mirror); err == nil {
		t.Fatal("mirror accepted conflicting identity")
	}
	after, _ := os.ReadFile(mirror)
	if string(before) != string(after) {
		t.Fatal("conflict changed mirror")
	}
}

func TestIndependentProcessMirrorsMerge(t *testing.T) {
	if root := os.Getenv("REEL_TEST_MIRROR_ROOT"); root != "" {
		name := os.Getenv("REEL_TEST_MIRROR_CLIENT")
		s, e := state.Load(filepath.Join(root, name+".jsonl"))
		if e != nil {
			os.Exit(40)
		}
		r := makeRow("camera", name, "MP4")
		if s.Upsert(r) != nil || s.MirrorTo(filepath.Join(root, "mirror.jsonl")) != nil {
			os.Exit(41)
		}
		os.Exit(0)
	}
	root := realTempDir(t)
	makeChild := func(name string) *exec.Cmd {
		c := exec.Command(os.Args[0], "-test.run=^TestIndependentProcessMirrorsMerge$")
		c.Env = append(os.Environ(), "REEL_TEST_MIRROR_ROOT="+root, "REEL_TEST_MIRROR_CLIENT="+name)
		return c
	}
	a, b := makeChild("one"), makeChild("two")
	if e := a.Start(); e != nil {
		t.Fatal(e)
	}
	if e := b.Start(); e != nil {
		t.Fatal(e)
	}
	if e := a.Wait(); e != nil {
		t.Fatal(e)
	}
	if e := b.Wait(); e != nil {
		t.Fatal(e)
	}
	s, e := state.Load(filepath.Join(root, "mirror.jsonl"))
	if e != nil || s.Len() != 2 {
		t.Fatal("concurrent history lost", e)
	}
}
