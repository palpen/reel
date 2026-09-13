package lockfile

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestSharedReleaseKeepsOtherReadersProtected(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	path := filepath.Join(root, "reel.lock")
	a, e := AcquireShared(path)
	if e != nil {
		t.Fatal(e)
	}
	b, e := AcquireShared(path)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Release()
	a.Release()
	if c, e := acquire(path, syscall.LOCK_EX, 20*time.Millisecond); e == nil {
		c.Release()
		t.Fatal("exclusive writer admitted while reader holds lock")
	}
	before, _ := os.Stat(path)
	b.Release()
	c, e := AcquireExclusive(path)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Release()
	after, _ := os.Stat(path)
	if !os.SameFile(before, after) {
		t.Fatal("lock inode replaced")
	}
}
func TestProcessLockHandoff(t *testing.T) {
	if p := os.Getenv("REEL_TEST_LOCK"); p != "" {
		lk, e := acquire(p, syscall.LOCK_EX, 20*time.Millisecond)
		if e != nil {
			os.Exit(23)
		}
		lk.Release()
		os.Exit(0)
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	path := filepath.Join(root, "reel.lock")
	a, e := AcquireShared(path)
	if e != nil {
		t.Fatal(e)
	}
	b, e := AcquireShared(path)
	if e != nil {
		t.Fatal(e)
	}
	a.Release()
	run := func() error {
		c := exec.Command(os.Args[0], "-test.run=^TestProcessLockHandoff$")
		c.Env = append(os.Environ(), "REEL_TEST_LOCK="+path)
		return c.Run()
	}
	if e := run(); e == nil {
		t.Fatal("child writer bypassed shared holder")
	}
	b.Release()
	if e := run(); e != nil {
		t.Fatal("handoff failed", e)
	}
}

func TestPythonQuarantineHonorsGoArchiveLock(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	lk, err := AcquireExclusive(filepath.Join(root, ".reel-archive.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Release()
	scripts, err := filepath.Abs("../../scripts")
	if err != nil {
		t.Fatal(err)
	}
	code := `import sys, pathlib
sys.path.insert(0,sys.argv[1])
import lrf_quarantine as q
root=pathlib.Path(sys.argv[2])
selected=root/"session"
selected.mkdir()
p=dict(root=str(selected),archive_root=str(root),states=[],lock=None)
try:
    with q.transaction_locks(p):
        sys.exit(42)
except BlockingIOError:
    sys.exit(0)
`
	c := exec.Command(python, "-c", code, scripts, root)
	if data, err := c.CombinedOutput(); err != nil {
		t.Fatalf("Python bypassed Go lock: %s %v", data, err)
	}
}
