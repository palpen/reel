// Package state manages the persistent JSONL state file for reel.
package state

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/pspenano/reel/internal/fault"
	"github.com/pspenano/reel/internal/lockfile"
	"github.com/pspenano/reel/internal/safefs"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const schemaVersion = 2

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
	HDVolumeUUID  string     `json:"hd_volume_uuid,omitempty"`
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
	rows   map[string]*Row
	path   string
	legacy bool
}

// Load reads the state file from path. If the file does not exist, returns an empty Store.
func Load(path string) (*Store, error) {
	s := &Store{
		rows: make(map[string]*Row),
		path: path,
	}

	f, err := safefs.Open(path)
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
			return nil, fmt.Errorf("state line %d: %w", lineNum, err)
		}

		var r Row
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, fmt.Errorf("state line %d: %w", lineNum, err)
		}
		if (r.SchemaVersion != 1 && r.SchemaVersion != schemaVersion) || r.CameraProfile == "" || r.BaseName == "" || r.Ext == "" {
			return nil, fmt.Errorf("invalid or unsupported state at line %d", lineNum)
		}
		if r.SchemaVersion == 1 {
			s.legacy = true
		}
		r.extra = extractExtra(raw)

		key := r.Key()
		if _, exists := s.rows[key]; exists {
			return nil, fmt.Errorf("duplicate state key at line %d", lineNum)
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
	"hd_volume_uuid": true,
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
	return clone(s.rows[key])
}

// GetByParts looks up a row by its component parts.
func (s *Store) GetByParts(profile, baseName, ext string) *Row {
	key := profile + "\x00" + baseName + "\x00" + ext
	return clone(s.rows[key])
}

// Upsert inserts or replaces a row and flushes to disk immediately.
func (s *Store) Upsert(r *Row) error {
	next := clone(r)
	next.SchemaVersion = schemaVersion
	old, exists := s.rows[r.Key()]
	if exists && old.SHA256 != "" && next.SHA256 != old.SHA256 {
		return fmt.Errorf("canonical hash cannot change for %s", r.Key())
	}
	s.rows[r.Key()] = next
	if err := s.Save(); err != nil {
		if exists {
			s.rows[r.Key()] = old
		} else {
			delete(s.rows, r.Key())
		}
		return err
	}
	return nil
}

// All returns all rows as a slice (order not guaranteed).
func (s *Store) All() []*Row {
	out := make([]*Row, 0, len(s.rows))
	for _, r := range s.rows {
		out = append(out, clone(r))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}

// Len returns the number of tracked files.
func (s *Store) Len() int {
	return len(s.rows)
}

// Save atomically writes all rows to the state file.
func (s *Store) Save() error {
	if err := fault.Check("state-save"); err != nil {
		return err
	}
	if err := s.snapshotLegacy(); err != nil {
		return err
	}
	return s.writeTo(s.path)
}

func (s *Store) writeTo(path string) error {
	dir, err := safefs.OpenDir(filepath.Dir(path), true)
	if err != nil {
		return err
	}
	defer dir.Close()
	f, err := safefs.Fresh(dir, ".reel-state-")
	if err != nil {
		return err
	}
	tmp := f.Name()

	w := bufio.NewWriter(f)
	for _, r := range s.rows {
		persisted := clone(r)
		persisted.SchemaVersion = schemaVersion
		data, err := marshalRow(persisted)
		if err != nil {
			f.Close()
			return fmt.Errorf("marshal row: %w", err)
		}
		if _, err = w.Write(append(data, '\n')); err != nil {
			f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return fmt.Errorf("flush state: %w", err)
	}
	if err := syncState(f, "state-file-sync"); err != nil {
		f.Close()
		return fmt.Errorf("fsync state: %w", err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := unix.Renameat(int(dir.Fd()), filepath.Base(tmp), int(dir.Fd()), filepath.Base(path)); err != nil {
		return err
	}
	return syncState(dir, "state-directory-sync")
}

func syncState(f *os.File, checkpoint string) error {
	if err := fault.Check(checkpoint); err != nil {
		return err
	}
	return f.Sync()
}

// MirrorTo copies the state to an additional path (e.g., the HD mirror).
func (s *Store) MirrorTo(hdPath string) error {
	if err := fault.Check("mirror-save"); err != nil {
		return err
	}
	dir, err := safefs.OpenDir(filepath.Dir(hdPath), false)
	if err != nil {
		return err
	}
	defer dir.Close()
	lk, err := lockfile.AcquireExclusive(filepath.Join(filepath.Dir(hdPath), "reel.lock"))
	if err != nil {
		return err
	}
	defer lk.Release()
	merged, err := Load(hdPath)
	if err != nil {
		return err
	}
	for key, r := range s.rows {
		if old := merged.rows[key]; old != nil {
			if old.SHA256 != "" && old.SHA256 != r.SHA256 {
				return fmt.Errorf("mirror identity conflict for %s", r.BaseName)
			}
			if old.HDPath != "" && r.HDPath != "" && old.HDPath != r.HDPath {
				return fmt.Errorf("mirror path conflict for %s", r.BaseName)
			}
			next := clone(r)
			if next.HDPath == "" {
				next.HDVolumeUUID = old.HDVolumeUUID
				next.HDPath = old.HDPath
				next.HDVerifiedAt = old.HDVerifiedAt
				next.BackedUpAt = old.BackedUpAt
			}
			for k, v := range old.extra {
				if next.extra == nil {
					next.extra = map[string]json.RawMessage{}
				}
				if current, ok := next.extra[k]; ok && !bytes.Equal(current, v) {
					return fmt.Errorf("conflicting unknown state field %s", k)
				}
				if _, ok := next.extra[k]; !ok {
					next.extra[k] = v
				}
			}
			merged.rows[key] = next
		} else {
			merged.rows[key] = clone(r)
		}
	}
	if err := merged.snapshotLegacy(); err != nil {
		return err
	}
	return merged.writeTo(hdPath)
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

func clone(r *Row) *Row {
	if r == nil {
		return nil
	}
	c := *r
	copyTime := func(v *time.Time) *time.Time {
		if v == nil {
			return nil
		}
		n := *v
		return &n
	}
	c.ImportedAt = copyTime(r.ImportedAt)
	c.BackedUpAt = copyTime(r.BackedUpAt)
	c.HDVerifiedAt = copyTime(r.HDVerifiedAt)
	c.CleanedAt = copyTime(r.CleanedAt)
	if r.extra != nil {
		c.extra = make(map[string]json.RawMessage)
		for k, v := range r.extra {
			c.extra[k] = append(json.RawMessage(nil), v...)
		}
	}
	return &c
}

// snapshotLegacy preserves the exact legacy bytes before the first write.
func (s *Store) snapshotLegacy() error {
	if !s.legacy {
		return nil
	}
	in, err := safefs.Open(s.path)
	if err != nil {
		return err
	}
	defer in.Close()
	dir, err := safefs.OpenDir(filepath.Dir(s.path), false)
	if err != nil {
		return err
	}
	defer dir.Close()
	out, err := safefs.Fresh(dir, ".reel-pre-migration-")
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err = out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	if err = dir.Sync(); err != nil {
		return err
	}
	s.legacy = false
	return nil
}
