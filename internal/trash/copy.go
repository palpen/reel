package trash

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/pspenano/reel/internal/fault"
	"github.com/pspenano/reel/internal/safefs"
	"github.com/pspenano/reel/internal/transfer"
	"golang.org/x/sys/unix"
)

// validateEndpoints checks the retained handles AND their names. Callers hold
// the card lock throughout the transaction. No pathname check grants permission
// to overwrite a destination: all copy destinations use O_EXCL.
func validateEndpoints(dirs []*os.File, in *os.File, before os.FileInfo, hash string, size int64, validate func() error) error {
	if validate != nil {
		if err := validate(); err != nil {
			return err
		}
	}
	for _, dir := range dirs {
		if err := safefs.CheckDir(dir); err != nil {
			return err
		}
	}
	current, err := safefs.Open(in.Name())
	if err != nil {
		return err
	}
	defer current.Close()
	info, err := current.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(before, info) || before.Size() != info.Size() || !before.ModTime().Equal(info.ModTime()) {
		return fmt.Errorf("source identity changed: %s", in.Name())
	}
	h, n, err := transfer.HashFile(in.Name())
	if err != nil {
		return err
	}
	if h != hash || n != size {
		return fmt.Errorf("source content changed: %s", in.Name())
	}
	// Hashing can take time; check the name again after reading.
	named, err := safefs.Open(in.Name())
	if err != nil {
		return err
	}
	defer named.Close()
	info, err = named.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(before, info) {
		return fmt.Errorf("source replaced: %s", in.Name())
	}
	return nil
}

func verifyCopy(dir *os.File, f *os.File, hash string, size int64) error {
	if err := safefs.CheckDir(dir); err != nil {
		return err
	}
	before, err := f.Stat()
	if err != nil {
		return err
	}
	h, n, err := transfer.HashFile(f.Name())
	if err != nil {
		return err
	}
	current, err := safefs.OpenAt(dir, filepath.Base(f.Name()), unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer current.Close()
	info, err := current.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(before, info) || h != hash || n != size {
		return fmt.Errorf("copy changed: %s", f.Name())
	}
	return nil
}

type recoveryWriter struct {
	f          *os.File
	checkpoint string
}

func (w recoveryWriter) Write(p []byte) (int, error) {
	n, err := w.f.Write(p)
	if err != nil {
		return n, err
	}
	return n, fault.Check(w.checkpoint)
}

// copyToRecovery is the exFAT protocol. The immutable intent is already durable.
// A partial copy is retained, but never authorizes removing the source. Only a
// synced, independently verified, still-named copy does. No media is overwritten,
// truncated, swept, or removed from recovery, including on any failure path.
func copyToRecovery(card, root, parent, entry, in *os.File, before os.FileInfo, r Record, validate func() error) (string, error) {
	dirs := []*os.File{card, root, parent, entry}
	if err := fault.Check("media-move"); err != nil {
		return "", err
	}
	if err := validateEndpoints(dirs, in, before, r.SHA256, r.SizeBytes, validate); err != nil {
		return "", err
	}
	out, err := safefs.OpenAt(entry, filepath.Base(r.RecoveryPath), unix.O_RDWR|unix.O_CREAT|unix.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	defer out.Close()
	fail := func(err error) (string, error) {
		return r.RecoveryPath, fmt.Errorf("recovery intent/copy retained at %s; source removal not completed: %w", entry.Name(), err)
	}
	if _, err = in.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	n, err := io.CopyBuffer(recoveryWriter{out, "recovery-copy"}, in, make([]byte, 1024*1024))
	if err != nil {
		return fail(err)
	}
	if n != r.SizeBytes {
		return fail(fmt.Errorf("source size changed"))
	}
	if err = fault.Check("recovery-copy-sync"); err != nil {
		return fail(err)
	}
	if err = safefs.DurableSync(out); err != nil {
		return fail(err)
	}
	if err = safefs.DurableSync(entry); err != nil {
		return fail(err)
	}
	if err = verifyCopy(entry, out, r.SHA256, r.SizeBytes); err != nil {
		return fail(err)
	}
	if err = fault.Check("recovery-copied"); err != nil {
		return fail(err)
	}
	// Revalidate all command snapshots, including independent backups, after the
	// potentially long copy. Source removal is the final mutation, under the lock.
	if err = fault.Check("recovery-remove"); err != nil {
		return fail(err)
	}
	if err = validateEndpoints(dirs, in, before, r.SHA256, r.SizeBytes, validate); err != nil {
		return fail(err)
	}
	if err = verifyCopy(entry, out, r.SHA256, r.SizeBytes); err != nil {
		return fail(err)
	}
	// Check the source name once more after verifying the recovery copy.
	current, err := safefs.OpenAt(parent, filepath.Base(r.OriginalPath), unix.O_RDONLY, 0)
	if err != nil {
		return fail(err)
	}
	info, err := current.Stat()
	current.Close()
	if err != nil {
		return fail(err)
	}
	if !os.SameFile(before, info) || info.Size() != before.Size() || !info.ModTime().Equal(before.ModTime()) {
		return fail(fmt.Errorf("source replaced before removal"))
	}
	if err = unix.Unlinkat(int(parent.Fd()), filepath.Base(r.OriginalPath), 0); err != nil {
		return fail(err)
	}
	if err = fault.Check("media-moved"); err != nil {
		return r.RecoveryPath, err
	}
	if err = safefs.DurableSync(parent); err != nil {
		return r.RecoveryPath, err
	}
	return r.RecoveryPath, nil
}

// A receipt authorizes append-only resumption, never truncation or replacement.
// The inode is paired with creation time on macOS; device numbers may change
// after reattaching a card. A remount that changes file identity fails closed.
type restoreReceipt struct {
	Version      int          `json:"version"`
	OriginalPath string       `json:"original_path"`
	SHA256       string       `json:"sha256"`
	Identity     fileIdentity `json:"identity"`
}

func restoreCopy(r Record) error {
	card, err := safefs.OpenDir(r.Volume, false)
	if err != nil {
		return err
	}
	defer card.Close()
	openParent := func(path string) (*os.File, error) {
		rel, err := filepath.Rel(r.Volume, filepath.Dir(path))
		if err != nil {
			return nil, err
		}
		return safefs.OpenBeneath(card, rel, false)
	}
	entry, err := openParent(r.RecoveryPath)
	if err != nil {
		return err
	}
	defer entry.Close()
	parent, err := openParent(r.OriginalPath)
	if err != nil {
		return err
	}
	defer parent.Close()
	dirs := []*os.File{card, entry, parent}
	in, err := safefs.OpenAt(entry, filepath.Base(r.RecoveryPath), unix.O_RDONLY, 0)
	if err != nil {
		// Intent may precede even copy creation. There is nothing to restore;
		// accept only an intact original and leave it untouched.
		if os.IsNotExist(err) {
			return verifyOriginal(r)
		}
		return err
	}
	defer in.Close()
	before, err := in.Stat()
	if err != nil {
		return err
	}
	if err = verifyCopy(entry, in, r.SHA256, r.SizeBytes); err != nil {
		// A partial recovery never takes precedence over a valid original.
		if e := verifyOriginal(r); e == nil {
			return nil
		}
		return err
	}
	if err = fault.Check("restore"); err != nil {
		return err
	}
	if err = validateEndpoints(dirs, in, before, r.SHA256, r.SizeBytes, nil); err != nil {
		return err
	}
	name := filepath.Base(r.OriginalPath)
	out, err := safefs.OpenAt(parent, name, unix.O_RDWR|unix.O_APPEND|unix.O_CREAT|unix.O_EXCL, 0600)
	if os.IsExist(err) {
		// A clean interrupted after copying can leave both valid original and
		// recovery. It is not a completed restore and must not bless a collision.
		out, err = safefs.OpenAt(parent, name, unix.O_RDWR|unix.O_APPEND, 0)
		if err != nil {
			return err
		}
		defer out.Close()
		receipt, e := readReceipt(entry)
		if e != nil {
			if os.IsNotExist(e) {
				if e := verifyOriginal(r); e == nil {
					return nil
				}
			}
			return fmt.Errorf("restore destination conflict (recovery retained): %w", e)
		}
		identity, e := identityOf(out)
		if e != nil {
			return e
		}
		if receipt.Version != 1 || receipt.OriginalPath != r.OriginalPath || receipt.SHA256 != r.SHA256 || receipt.Identity != identity {
			return fmt.Errorf("restore destination replaced; recovery retained at %s", r.RecoveryPath)
		}
	} else if err != nil {
		return err
	} else {
		defer out.Close()
		if err = fault.Check("restore-created"); err != nil {
			return err
		}
		// exFAT assigns a temporary inode to empty files, then replaces it with
		// the first data cluster on allocation. Allocate with one actual byte
		// before recording identity. A crash before the receipt fails closed.
		if r.SizeBytes > 0 {
			var first [1]byte
			if _, err = in.ReadAt(first[:], 0); err != nil {
				return err
			}
			if _, err = out.Write(first[:]); err != nil {
				return err
			}
		}
		if err = safefs.DurableSync(out); err != nil {
			return err
		}
		if err = safefs.DurableSync(parent); err != nil {
			return err
		}
		identity, e := identityOf(out)
		if e != nil {
			return e
		}
		data, e := json.Marshal(restoreReceipt{1, r.OriginalPath, r.SHA256, identity})
		if e != nil {
			return e
		}
		if e = safefs.EnsureRecord(entry, "restore.json", append(data, '\n')); e != nil {
			return e
		}
		if e = safefs.DurableSync(entry); e != nil {
			return e
		}
	}
	if err = fault.Check("restore-intent"); err != nil {
		return err
	}
	// Compare every existing byte with the verified recovery. Only append the
	// missing suffix. A modified prefix or an oversized file is a conflict.
	info, err := out.Stat()
	if err != nil {
		return err
	}
	if info.Size() > r.SizeBytes {
		return fmt.Errorf("restore destination exceeds journal size")
	}
	if _, err = in.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err = out.Seek(0, io.SeekStart); err != nil {
		return err
	}
	a, b := make([]byte, 1024*1024), make([]byte, 1024*1024)
	for left := info.Size(); left > 0; {
		n := int64(len(a))
		if left < n {
			n = left
		}
		if _, err = io.ReadFull(in, a[:n]); err != nil {
			return err
		}
		if _, err = io.ReadFull(out, b[:n]); err != nil {
			return err
		}
		if !bytes.Equal(a[:n], b[:n]) {
			return fmt.Errorf("restore destination prefix differs; recovery retained")
		}
		left -= n
	}
	if err = validateEndpoints(dirs, in, before, r.SHA256, r.SizeBytes, nil); err != nil {
		return err
	}
	if err = verifyNamedOutput(parent, out); err != nil {
		return err
	}
	if _, err = io.CopyBuffer(recoveryWriter{out, "restore-copy"}, in, a); err != nil {
		return err
	}
	if err = fault.Check("restore-copy-sync"); err != nil {
		return err
	}
	if err = safefs.DurableSync(out); err != nil {
		return err
	}
	if err = safefs.DurableSync(parent); err != nil {
		return err
	}
	if err = verifyCopy(parent, out, r.SHA256, r.SizeBytes); err != nil {
		return err
	}
	if err = validateEndpoints(dirs, in, before, r.SHA256, r.SizeBytes, nil); err != nil {
		return err
	}
	if err = fault.Check("restored"); err != nil {
		return err
	}
	return verifyCopy(parent, out, r.SHA256, r.SizeBytes)
}

func verifyNamedOutput(parent, out *os.File) error {
	named, err := safefs.OpenAt(parent, filepath.Base(out.Name()), unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer named.Close()
	a, err := out.Stat()
	if err != nil {
		return err
	}
	b, err := named.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(a, b) {
		return fmt.Errorf("restore destination replaced")
	}
	return safefs.CheckDir(parent)
}

func verifyOriginal(r Record) error {
	if r.SourceIdentity == nil {
		return fmt.Errorf("missing original identity")
	}
	f, err := safefs.Open(r.OriginalPath)
	if err != nil {
		return err
	}
	defer f.Close()
	identity, err := identityOf(f)
	if err != nil {
		return err
	}
	if identity != *r.SourceIdentity {
		return fmt.Errorf("original identity changed")
	}

	before, err := f.Stat()
	if err != nil {
		return err
	}
	return validateEndpoints(nil, f, before, r.SHA256, r.SizeBytes, nil)
}

func readReceipt(entry *os.File) (restoreReceipt, error) {
	var r restoreReceipt
	f, err := safefs.OpenAt(entry, "restore.json", unix.O_RDONLY, 0)
	if err != nil {
		return r, err
	}
	defer f.Close()
	err = json.NewDecoder(io.LimitReader(f, 4096)).Decode(&r)
	return r, err
}
