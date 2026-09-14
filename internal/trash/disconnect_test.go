package trash

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pspenano/reel/internal/fault"
	"github.com/pspenano/reel/internal/safefs"
)

func TestMountedRecoveryFullVolume(t *testing.T) {
	root := os.Getenv("REEL_TEST_VOLUME_ROOT")
	if root == "" {
		t.Skip("requires disposable image")
	}
	probe, err := safefs.OpenDir(root, false)
	if err != nil {
		t.Fatal(err)
	}
	copies, err := safefs.RecoveryCopies(probe)
	probe.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !copies {
		t.Skip("exclusive native rename does not require media-sized free space")
	}
	// Never fill an arbitrary filesystem: the coordinator supplies a small
	// image, and we verify that bound before allocating any filler bytes.
	var fs syscall.Statfs_t
	if err := syscall.Statfs(root, &fs); err != nil {
		t.Fatal(err)
	}
	if uint64(fs.Blocks)*uint64(fs.Bsize) > 300*1024*1024 {
		t.Fatal("refusing to fill a volume over 300 MiB")
	}
	dir, err := os.MkdirTemp(root, "full-volume-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	src := filepath.Join(dir, "clip.MP4")
	data := bytes.Repeat([]byte("original"), 262144)
	if err := os.WriteFile(src, data, 0600); err != nil {
		t.Fatal(err)
	}
	filler, err := os.Create(filepath.Join(dir, "disposable-filler"))
	if err != nil {
		t.Fatal(err)
	}
	defer filler.Close()
	block := make([]byte, 1024*1024)
	for {
		_, err = filler.Write(block)
		if err != nil {
			break
		}
	}
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatal(err)
	}
	if _, err := MoveOnVolume(src, dir, time.Now()); err == nil {
		t.Fatal("full volume accepted recovery")
	}
	assertData(t, src, data)
	// Free only our disposable filler, never media or recovery artifacts.
	if err := filler.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filler.Name()); err != nil {
		t.Fatal(err)
	}
	dst, err := MoveOnVolume(src, dir, time.Now())
	if err != nil {
		t.Fatal("retry after full volume", err)
	}
	assertData(t, dst, data)
}

// The Python disk-image harness owns mounting/detaching its disposable image.
// It resumes this helper after force-detaching the filesystem at a checkpoint,
// then invokes the recovery phase only after attaching that same image again.
func TestMountedRecoveryDisconnect(t *testing.T) {
	root := os.Getenv("REEL_DISCONNECT_ROOT")
	if root == "" {
		t.Skip("requires disposable image coordinator")
	}
	src := filepath.Join(root, "clip.MP4")
	data := bytes.Repeat([]byte("disconnected camera recording\n"), 80000)
	step := os.Getenv("REEL_DISCONNECT_STEP")
	if os.Getenv("REEL_DISCONNECT_PHASE") == "recover" {
		journal := oneJournal(t, root)
		if err := Restore(journal); err != nil {
			t.Fatal("restore after reattachment", err)
		}
		assertData(t, src, data)
		if err := Restore(journal); err != nil {
			t.Fatal("repeat restore after reattachment", err)
		}
		return
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, data, 0600); err != nil {
		t.Fatal(err)
	}
	f, err := safefs.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := safefs.DurableSync(f); err != nil {
		t.Fatal(err)
	}
	f.Close()
	journal := ""
	if strings.HasPrefix(step, "restore") {
		if _, err := MoveOnVolume(src, root, time.Now()); err != nil {
			t.Fatal(err)
		}
		journal = oneJournal(t, root)
	}
	control := os.Getenv("REEL_DISCONNECT_CONTROL")
	fault.Hook = func(name string) error {
		if name != step {
			return nil
		}
		if err := os.WriteFile(control+".ready", []byte(step), 0600); err != nil {
			return err
		}
		deadline := time.Now().Add(45 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(control + ".resume"); err == nil {
				return nil
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatal("detach coordinator timed out")
		return nil
	}
	defer func() { fault.Hook = nil }()
	if journal == "" {
		_, err = MoveOnVolume(src, root, time.Now())
	} else {
		err = Restore(journal)
	}
	if err == nil {
		t.Fatal("disconnected operation reported success")
	}
	t.Logf("disconnection reported: %v", err)
}
