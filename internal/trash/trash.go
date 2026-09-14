// Package trash implements recoverable, same-volume moves. It never deletes media.
package trash

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/pspenano/reel/internal/fault"
	"github.com/pspenano/reel/internal/safefs"
	"github.com/pspenano/reel/internal/transfer"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var ErrCrossDevice = errors.New("cross-volume recovery move refused")

// Test seam only; production always selects from the actual mounted filesystem.
var recoveryCopies = safefs.RecoveryCopies

type Record struct {
	Version        int           `json:"version"`
	Strategy       string        `json:"strategy,omitempty"`
	SourceIdentity *fileIdentity `json:"source_identity,omitempty"`
	Volume         string        `json:"volume"`
	OriginalPath   string        `json:"original_path"`
	RecoveryPath   string        `json:"recovery_path"`
	SizeBytes      int64         `json:"size_bytes"`
	SHA256         string        `json:"sha256"`
	Instructions   string        `json:"instructions"`
}

func contained(src, volume string) error {
	rel, err := filepath.Rel(volume, src)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || strings.EqualFold(strings.Split(rel, "/")[0], ".reel-trash") {
		return fmt.Errorf("source outside camera media: %s", src)
	}
	return nil
}
func MoveOnVolume(src, volume string, ts time.Time) (string, error) {
	return MoveChecked(src, volume, ts, nil)
}

// MoveChecked revalidates command-owned backup and source snapshots before removal.
func MoveChecked(src, volume string, ts time.Time, validate func() error) (string, error) {
	src, err := filepath.Abs(src)
	if err != nil {
		return "", err
	}
	volume, err = filepath.Abs(volume)
	if err != nil {
		return "", err
	}
	if err = contained(src, volume); err != nil {
		return "", err
	}
	in, err := safefs.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	before, err := in.Stat()
	if err != nil {
		return "", err
	}
	hash, n, err := transfer.HashFile(src)
	if err != nil {
		return "", err
	}
	card, err := safefs.OpenDir(volume, false)
	if err != nil {
		return "", err
	}
	defer card.Close()
	root, err := safefs.OpenBeneath(card, ".reel-trash", true)
	if err != nil {
		return "", err
	}
	defer root.Close()
	parent, err := safefs.OpenDir(filepath.Dir(src), false)
	if err != nil {
		return "", err
	}
	defer parent.Close()
	var a, b unix.Stat_t
	if err = unix.Fstat(int(in.Fd()), &a); err != nil {
		return "", err
	}
	if err = unix.Fstat(int(root.Fd()), &b); err != nil {
		return "", err
	}
	if a.Dev != b.Dev {
		return "", ErrCrossDevice
	}
	copies, err := recoveryCopies(root)
	if err != nil {
		return "", err
	}
	if !copies {
		if err = safefs.CheckExclusive(root); err != nil {
			return "", err
		}
	}
	entry, err := safefs.FreshDir(root, "reel-deleted-"+ts.UTC().Format("20060102-150405")+"-")
	if err != nil {
		return "", err
	}
	defer entry.Close()
	dst := filepath.Join(entry.Name(), filepath.Base(src))
	r := Record{Version: 1, Volume: volume, OriginalPath: src, RecoveryPath: dst, SizeBytes: n, SHA256: hash,
		Instructions: "Use reel restore --journal <this recovery.json>. Recovery never expires and occupies card space."}
	if copies {
		r.Version = 2
		r.Strategy = "verified-copy"
		identity, e := identityOf(in)
		if e != nil {
			return "", e
		}
		r.SourceIdentity = &identity
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	if err = fault.Check("recovery-intent"); err != nil {
		return "", err
	}
	f, err := safefs.OpenAt(entry, "recovery.json", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	if _, err = f.Write(append(data, '\n')); err != nil {
		f.Close()
		return "", err
	}
	if err = safefs.DurableSync(f); err != nil {
		f.Close()
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	if err = safefs.DurableSync(entry); err != nil {
		return "", err
	}
	if err = safefs.DurableSync(root); err != nil {
		return "", err
	}
	if copies {
		return copyToRecovery(card, root, parent, entry, in, before, r, validate)
	}
	current, err := safefs.OpenAt(parent, filepath.Base(src), unix.O_RDONLY, 0)
	if err != nil {
		return "", err
	}
	info, err := current.Stat()
	current.Close()
	if err != nil {
		return "", err
	}
	if !os.SameFile(before, info) {
		return "", fmt.Errorf("source identity changed; intent at %s", entry.Name())
	}
	if err = fault.Check("media-move"); err != nil {
		return "", err
	}
	if err = validateEndpoints([]*os.File{card, root, parent, entry}, in, before, r.SHA256, r.SizeBytes, validate); err != nil {
		return "", err
	}
	if err = safefs.RenameExclusive(parent, filepath.Base(src), entry, filepath.Base(src)); err != nil {
		return "", fmt.Errorf("recoverable move: %w", err)
	}
	if err = fault.Check("media-moved"); err != nil {
		return dst, err
	}
	// Any post-move failure retains the media and its intent. Never roll back over a new recording.
	fail := func(e error) (string, error) {
		return dst, fmt.Errorf("media retained at %s; recovery check failed: %w", dst, e)
	}
	moved, err := safefs.OpenAt(entry, filepath.Base(src), unix.O_RDONLY, 0)
	if err != nil {
		return fail(err)
	}
	info, err = moved.Stat()
	moved.Close()
	if err != nil {
		return fail(err)
	}
	if !os.SameFile(before, info) {
		return fail(fmt.Errorf("moved identity changed"))
	}
	h, size, err := transfer.HashFile(dst)
	if err != nil {
		return fail(err)
	}
	if h != hash || size != n {
		return fail(fmt.Errorf("moved content changed"))
	}
	if err = safefs.DurableSync(entry); err != nil {
		return fail(err)
	}
	if err = parent.Sync(); err != nil {
		return fail(err)
	}
	return dst, nil
}

// ReadRecord validates journal provenance and path containment without mutation.
func ReadRecord(path string) (Record, error) {
	var r Record
	f, err := safefs.Open(path)
	if err != nil {
		return r, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1024*1024))
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal(data, &r); err != nil {
		return r, err
	}
	if (r.Version != 1 && r.Version != 2) || (r.Version == 2 && (r.Strategy != "verified-copy" || r.SourceIdentity == nil)) || len(r.SHA256) != 64 || filepath.Base(path) != "recovery.json" || filepath.Dir(path) != filepath.Dir(r.RecoveryPath) {
		return r, fmt.Errorf("invalid recovery journal")
	}
	for _, p := range []string{r.Volume, r.OriginalPath, r.RecoveryPath, path} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p {
			return r, fmt.Errorf("journal paths must be absolute and canonical")
		}
	}
	if r.SizeBytes < 0 {
		return r, fmt.Errorf("invalid recovery size")
	}
	root := filepath.Join(r.Volume, ".reel-trash")
	rel, err := filepath.Rel(root, r.RecoveryPath)
	if err != nil || len(strings.Split(rel, string(os.PathSeparator))) != 2 || !strings.HasPrefix(filepath.Dir(rel), "reel-deleted-") || strings.HasPrefix(rel, "../") || rel == ".." || filepath.Base(r.OriginalPath) != filepath.Base(r.RecoveryPath) {
		return r, fmt.Errorf("invalid recovery path")
	}
	return r, contained(r.OriginalPath, r.Volume)
}
func Restore(path string) error {
	r, err := ReadRecord(path)
	if err != nil {
		return err
	}
	if r.Version == 2 {
		return restoreCopy(r)
	}
	if _, err := os.Lstat(r.RecoveryPath); os.IsNotExist(err) {
		h, n, e := transfer.HashFile(r.OriginalPath)
		if e == nil && h == r.SHA256 && n == r.SizeBytes {
			return nil
		}
		return fmt.Errorf("neither a valid recovery nor restored file exists")
	}
	h, n, err := transfer.HashFile(r.RecoveryPath)
	if err != nil {
		return err
	}
	if h != r.SHA256 || n != r.SizeBytes {
		return fmt.Errorf("recovery content differs from journal")
	}
	if err = fault.Check("restore"); err != nil {
		return err
	}
	if err = safefs.Move(r.RecoveryPath, r.OriginalPath); err != nil {
		return err
	}
	if err = fault.Check("restored"); err != nil {
		return err
	}
	h, n, err = transfer.HashFile(r.OriginalPath)
	if err != nil {
		return err
	}
	if h != r.SHA256 || n != r.SizeBytes {
		return fmt.Errorf("restored content changed; retained at %s", r.OriginalPath)
	}
	return nil
}
func sameVolume(a, b string) (bool, error) {
	var x, y syscall.Stat_t
	if err := syscall.Stat(a, &x); err != nil {
		return false, err
	}
	if err := syscall.Stat(b, &y); err != nil {
		return false, err
	}
	return x.Dev == y.Dev, nil
}
