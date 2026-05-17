// Package state manages the persistent JSONL state file for reel.
package state

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

const schemaVersion = 1

// Row represents a single tracked file.
type Row struct {
	SchemaVersion int        `json:"schema_version"`
	CameraProfile string     `json:"camera_profile"`
	BaseName      string     `json:"base_name"`
	Ext           string     `json:"ext"`
	RecordedAt    time.Time  `json:"recorded_at"`
	SizeBytes     int64      `json:"size_bytes"`
	SHA256        string     `json:"sha256"`
	CameraPath    string     `json:"camera_path"`
	LaptopPath    string     `json:"laptop_path"`
	HDPath        string     `json:"hd_path"`
	ImportedAt    *time.Time `json:"imported_at"`
	BackedUpAt    *time.Time `json:"backed_up_at"`
	HDVerifiedAt  *time.Time `json:"hd_verified_at"`
	CleanedAt     *time.Time `json:"cleaned_at"`

	// extra holds unknown fields for forward-compatibility
	extra map[string]json.RawMessage
}

// Key returns the dedup key for a row.
func (r *Row) Key() string {
	return r.CameraProfile + "\x00" + r.BaseName + "\x00" + r.Ext
}

// Store holds the full state in memory.
type Store struct {
	rows map[string]*Row
	path string
}

// Load reads the state file from path. If the file does not exist, returns an empty Store.
func Load(path string) (*Store, error) {
	s := &Store{
		rows: make(map[string]*Row),
		path: path,
	}

	// Remove stale .tmp if present
	tmp := path + ".tmp"
	if _, err := os.Stat(tmp); err == nil {
		slog.Warn("removing stale tmp state file", "path", tmp)
		os.Remove(tmp)
	}

	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open state: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
	lineNum := 0
	for sc.Scan() {
		lineNum++
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}

		// First decode into raw map for forward-compat
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(line, &raw); err != nil {
			slog.Warn("state: skipping malformed line", "line", lineNum, "err", err)
			continue
		}

		var r Row
		if err := json.Unmarshal(line, &r); err != nil {
			slog.Warn("state: skipping unparseable row", "line", lineNum, "err", err)
			continue
		}
		r.extra = extractExtra(raw)

		key := r.Key()
		if _, exists := s.rows[key]; exists {
			slog.Warn("state: duplicate key, overwriting", "key", key, "line", lineNum)
		}
		s.rows[key] = &r
	}
	return s, sc.Err()
}

// knownFields lists all JSON keys that are serialized by the Row struct.
var knownFields = map[string]bool{
	"schema_version": true,
	"camera_profile": true,
	"base_name":      true,
	"ext":            true,
	"recorded_at":    true,
	"size_bytes":     true,
	"sha256":         true,
	"camera_path":    true,
	"laptop_path":    true,
	"hd_path":        true,
	"imported_at":    true,
	"backed_up_at":   true,
	"hd_verified_at": true,
	"cleaned_at":     true,
}

func extractExtra(raw map[string]json.RawMessage) map[string]json.RawMessage {
	extra := make(map[string]json.RawMessage)
	for k, v := range raw {
		if !knownFields[k] {
			extra[k] = v
		}
	}
	return extra
}

// Get returns the row for a given key, or nil.
func (s *Store) Get(key string) *Row {
	return s.rows[key]
}

// GetByParts looks up a row by its component parts.
func (s *Store) GetByParts(profile, baseName, ext string) *Row {
	key := profile + "\x00" + baseName + "\x00" + ext
	return s.rows[key]
}

// Upsert inserts or replaces a row and flushes to disk immediately.
func (s *Store) Upsert(r *Row) error {
	r.SchemaVersion = schemaVersion
	s.rows[r.Key()] = r
	return s.Save()
}

// All returns all rows as a slice (order not guaranteed).
func (s *Store) All() []*Row {
	out := make([]*Row, 0, len(s.rows))
	for _, r := range s.rows {
		out = append(out, r)
	}
	return out
}

// Len returns the number of tracked files.
func (s *Store) Len() int {
	return len(s.rows)
}

// Save atomically writes all rows to the state file.
func (s *Store) Save() error {
	return s.writeTo(s.path)
}

func (s *Store) writeTo(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create tmp state: %w", err)
	}

	w := bufio.NewWriter(f)
	for _, r := range s.rows {
		data, err := marshalRow(r)
		if err != nil {
			f.Close()
			os.Remove(tmp)
			return fmt.Errorf("marshal row: %w", err)
		}
		w.Write(data)
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("flush state: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("fsync state: %w", err)
	}
	f.Close()

	return os.Rename(tmp, path)
}

// MirrorTo copies the state to an additional path (e.g., the HD mirror).
func (s *Store) MirrorTo(hdPath string) error {
	if err := os.MkdirAll(filepath.Dir(hdPath), 0o700); err != nil {
		return err
	}
	return s.writeTo(hdPath)
}

func marshalRow(r *Row) ([]byte, error) {
	// Marshal the known fields
	type rowAlias Row
	data, err := json.Marshal((*rowAlias)(r))
	if err != nil {
		return nil, err
	}

	if len(r.extra) == 0 {
		return data, nil
	}

	// Merge extra unknown fields
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	for k, v := range r.extra {
		m[k] = v
	}
	return json.Marshal(m)
}

// Path returns the on-disk path of this store.
func (s *Store) Path() string {
	return s.path
}

// NowPtr returns a pointer to the current time (UTC).
func NowPtr() *time.Time {
	t := time.Now().UTC()
	return &t
}
