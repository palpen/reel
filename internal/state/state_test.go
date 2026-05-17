package state_test

import (
	"encoding/json"
	"os"
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
	dir := t.TempDir()
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
	dir := t.TempDir()
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
	dir := t.TempDir()
	path := filepath.Join(dir, "state.jsonl")
	tmp := path + ".tmp"

	// Create a stale .tmp
	os.WriteFile(tmp, []byte(`{"schema_version":1,"camera_profile":"X","base_name":"Y","ext":"Z"}`+"\n"), 0o600)

	// Load should remove it
	_, err := state.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Error("stale .tmp was not cleaned up on load")
	}
}

func TestDuplicateKeyWarning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.jsonl")

	// Write a file with two rows with the same key
	line := `{"schema_version":1,"camera_profile":"DJI Pocket 3","base_name":"DJI_20260510111826_0001_D","ext":"MP4","sha256":"first"}` + "\n" +
		`{"schema_version":1,"camera_profile":"DJI Pocket 3","base_name":"DJI_20260510111826_0001_D","ext":"MP4","sha256":"second"}` + "\n"
	os.WriteFile(path, []byte(line), 0o600)

	// Should load without error; last row wins
	st, err := state.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := st.GetByParts("DJI Pocket 3", "DJI_20260510111826_0001_D", "MP4")
	if got == nil {
		t.Fatal("row not found")
	}
	// Second row should win
	if got.SHA256 != "second" {
		t.Errorf("SHA256 = %q, want %q", got.SHA256, "second")
	}
}

func TestForwardCompatibility(t *testing.T) {
	dir := t.TempDir()
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
	dir := t.TempDir()
	path := filepath.Join(dir, "state.jsonl")

	st, _ := state.Load(path)
	r := makeRow("DJI Pocket 3", "DJI_20260510111826_0001_D", "MP4")
	st.Upsert(r)

	// Update
	r.SHA256 = "updated"
	now := time.Now().UTC()
	r.ImportedAt = &now
	st.Upsert(r)

	// Reload
	st2, _ := state.Load(path)
	got := st2.GetByParts("DJI Pocket 3", "DJI_20260510111826_0001_D", "MP4")
	if got.SHA256 != "updated" {
		t.Errorf("SHA256 = %q, want %q", got.SHA256, "updated")
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
