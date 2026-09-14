package trash

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/pspenano/reel/internal/fault"
)

func copyProtocol(t *testing.T) {
	t.Helper()
	old := recoveryCopies
	recoveryCopies = func(*os.File) (bool, error) { return true, nil }
	t.Cleanup(func() { recoveryCopies = old; fault.Hook = nil })
}

func copyFixture(t *testing.T) (root, src string, data []byte) {
	t.Helper()
	copyProtocol(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	src = filepath.Join(root, "clip.MP4")
	data = bytes.Repeat([]byte("recoverable recording\n"), 110000)
	if err = os.WriteFile(src, data, 0600); err != nil {
		t.Fatal(err)
	}
	return root, src, data
}

func assertData(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("bytes not preserved at %s: %v", path, err)
	}
}

func oneJournal(t *testing.T, root string) string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(root, ".reel-trash", "reel-deleted-*", "recovery.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("journal lost: %v %v", paths, err)
	}
	return paths[0]
}

func TestCopyRecoveryAndRestore(t *testing.T) {
	root, src, data := copyFixture(t)
	dst, err := MoveOnVolume(src, root, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("source not moved", err)
	}
	assertData(t, dst, data)
	journal := oneJournal(t, root)
	r, err := ReadRecord(journal)
	if err != nil || r.Version != 2 || r.Strategy != "verified-copy" {
		t.Fatal(r, err)
	}
	for i := 0; i < 2; i++ {
		if err := Restore(journal); err != nil {
			t.Fatal(err)
		}
		assertData(t, src, data)
		assertData(t, dst, data)
	}
}

func TestCopyRecoveryFailuresAndRetries(t *testing.T) {
	for _, step := range []string{"recovery-intent", "media-move", "recovery-copy", "recovery-copy-sync", "recovery-copied", "recovery-remove", "media-moved"} {
		t.Run(step, func(t *testing.T) {
			root, src, data := copyFixture(t)
			fault.Hook = func(name string) error {
				if name == step {
					return syscall.EIO
				}
				return nil
			}
			dst, err := MoveOnVolume(src, root, time.Now())
			if err == nil {
				t.Fatal("failure ignored")
			}
			fault.Hook = nil
			if step == "media-moved" {
				assertData(t, dst, data)
				if err := Restore(oneJournal(t, root)); err != nil {
					t.Fatal(err)
				}
			} else {
				assertData(t, src, data)
				// Retry creates a fresh entry; it never reuses a partial recovery.
				if _, err := MoveOnVolume(src, root, time.Now()); err != nil {
					t.Fatal("retry", err)
				}
			}
		})
	}
}

func TestCopyRecoveryReplacementAndCollision(t *testing.T) {
	for _, kind := range []string{"occupied", "source-replaced", "source-content", "entry-replaced", "copy-replaced", "backup-changed"} {
		t.Run(kind, func(t *testing.T) {
			root, src, data := copyFixture(t)
			var retained string
			fault.Hook = func(step string) error {
				if (kind == "occupied" && step == "media-move") || (kind != "occupied" && step == "recovery-remove") {
					journal := oneJournal(t, root)
					r, err := ReadRecord(journal)
					if err != nil {
						return err
					}
					switch kind {
					case "occupied":
						return os.WriteFile(r.RecoveryPath, []byte("other media"), 0600)
					case "source-replaced":
						retained = src + ".old"
						if err := os.Rename(src, retained); err != nil {
							return err
						}
						return os.WriteFile(src, data, 0600) // identical bytes, different identity
					case "source-content":
						return os.WriteFile(src, []byte("new recording"), 0600)
					case "entry-replaced":
						retained = filepath.Dir(journal) + ".old"
						if err := os.Rename(filepath.Dir(journal), retained); err != nil {
							return err
						}
						return os.Mkdir(filepath.Dir(journal), 0700)
					case "copy-replaced":
						retained = r.RecoveryPath + ".old"
						if err := os.Rename(r.RecoveryPath, retained); err != nil {
							return err
						}
						return os.WriteFile(r.RecoveryPath, data, 0600)
					}
				}
				return nil
			}
			calls := 0
			_, err := MoveChecked(src, root, time.Now(), func() error {
				calls++
				if kind == "backup-changed" && calls > 1 {
					return fmt.Errorf("backup changed")
				}
				return nil
			})
			if err == nil {
				t.Fatal("changed endpoint accepted")
			}
			if kind == "source-content" {
				assertData(t, src, []byte("new recording"))
			} else {
				assertData(t, src, data)
			}
			if kind == "occupied" {
				r, err := ReadRecord(oneJournal(t, root))
				if err != nil {
					t.Fatal(err)
				}
				assertData(t, r.RecoveryPath, []byte("other media"))
			}
			if kind == "source-replaced" || kind == "copy-replaced" {
				assertData(t, retained, data)
			}
			if kind == "entry-replaced" {
				assertData(t, filepath.Join(retained, "clip.MP4"), data)
			}
		})
	}
}

func TestCopyRestoreRefusesConflicts(t *testing.T) {
	for _, kind := range []string{"different", "identical", "directory", "replaced-partial", "changed-prefix", "created-before-receipt"} {
		t.Run(kind, func(t *testing.T) {
			root, src, data := copyFixture(t)
			dst, err := MoveOnVolume(src, root, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			journal := oneJournal(t, root)
			switch kind {
			case "directory":
				err = os.Mkdir(src, 0700)
			case "different":
				err = os.WriteFile(src, []byte("new recording"), 0600)
			case "identical":
				err = os.WriteFile(src, data, 0600)
			default:
				step := "restore-copy"
				if kind == "created-before-receipt" {
					step = "restore-created"
				}
				fault.Hook = func(name string) error {
					if name == step {
						return syscall.ENOSPC
					}
					return nil
				}
				if e := Restore(journal); e == nil {
					t.Fatal("failure ignored")
				}
				fault.Hook = nil
				if kind == "replaced-partial" {
					partial, e := os.ReadFile(src)
					if e != nil {
						t.Fatal(e)
					}
					if e := os.Rename(src, src+".old"); e != nil {
						t.Fatal(e)
					}
					err = os.WriteFile(src, partial, 0600)
				} else if kind == "changed-prefix" {
					err = os.WriteFile(src, []byte("new recording"), 0600)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(src)
			if err := Restore(journal); err == nil {
				t.Fatal("conflicting destination accepted")
			}
			if kind != "directory" {
				assertData(t, src, before)
			}
			assertData(t, dst, data)
		})
	}
}

func TestCopyProcessInterruption(t *testing.T) {
	if root := os.Getenv("REEL_COPY_CHILD_ROOT"); root != "" {
		copyProtocol(t)
		step := os.Getenv("REEL_COPY_CHILD_STEP")
		fault.Hook = func(name string) error {
			if name == step {
				os.Exit(37)
			}
			return nil
		}
		if journal := os.Getenv("REEL_COPY_CHILD_JOURNAL"); journal != "" {
			Restore(journal)
		} else {
			MoveOnVolume(filepath.Join(root, "clip.MP4"), root, time.Now())
		}
		os.Exit(38)
	}
	for _, step := range []string{"media-move", "recovery-copy", "recovery-copy-sync", "recovery-copied", "recovery-remove", "media-moved", "restore-intent", "restore-copy", "restore-copy-sync", "restored"} {
		t.Run(step, func(t *testing.T) {
			root, src, data := copyFixture(t)
			journal := ""
			if step == "restore-intent" || step == "restore-copy" || step == "restore-copy-sync" || step == "restored" {
				if _, err := MoveOnVolume(src, root, time.Now()); err != nil {
					t.Fatal(err)
				}
				journal = oneJournal(t, root)
			}
			c := exec.Command(os.Args[0], "-test.run=^TestCopyProcessInterruption$")
			c.Env = append(os.Environ(), "REEL_COPY_CHILD_ROOT="+root, "REEL_COPY_CHILD_STEP="+step, "REEL_COPY_CHILD_JOURNAL="+journal)
			err := c.Run()
			if e, ok := err.(*exec.ExitError); !ok || e.ExitCode() != 37 {
				t.Fatalf("checkpoint not reached: %v", err)
			}
			if err := Restore(oneJournal(t, root)); err != nil {
				t.Fatal("restart restore", err)
			}
			assertData(t, src, data)
		})
	}
}

func TestCopyRestoreCaseCollision(t *testing.T) {
	root, src, data := copyFixture(t)
	dst, err := MoveOnVolume(src, root, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "CLIP.mp4")
	if err := os.WriteFile(other, []byte("case collision"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); os.IsNotExist(err) {
		t.Skip("case-sensitive filesystem")
	}
	if err := Restore(oneJournal(t, root)); err == nil {
		t.Fatal("case-insensitive collision accepted")
	}
	assertData(t, other, []byte("case collision"))
	assertData(t, dst, data)
}

func TestCopyRestoreEndpointReplacement(t *testing.T) {
	for _, endpoint := range []string{"recovery", "destination"} {
		t.Run(endpoint, func(t *testing.T) {
			root, src, data := copyFixture(t)
			dst, err := MoveOnVolume(src, root, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			fault.Hook = func(step string) error {
				if step != "restore-intent" {
					return nil
				}
				path := dst
				if endpoint == "destination" {
					path = src
				}
				if err := os.Rename(path, path+".old"); err != nil {
					return err
				}
				return os.WriteFile(path, []byte("replacement"), 0600)
			}
			if err := Restore(oneJournal(t, root)); err == nil {
				t.Fatal("endpoint replacement accepted")
			}
			if endpoint == "recovery" {
				assertData(t, dst+".old", data)
				assertData(t, dst, []byte("replacement"))
			} else {
				assertData(t, src, []byte("replacement"))
				assertData(t, dst, data)
			}
		})
	}
}
