package winfs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestIdentityLockName(t *testing.T) {
	id := FileID{VolumeSerial: 0x1a2b}
	for index := range id.ID {
		id.ID[index] = byte(index)
	}
	want := `Local\haft-lock-0000000000001a2b-000102030405060708090a0b0c0d0e0f`
	if got := identityLockName(id); got != want {
		t.Fatalf("identityLockName = %q, want %q", got, want)
	}
}

// A lock held by another process is busy; after the holder unlocks and
// exits, the lock is acquired normally (not abandoned).
func TestIdentityLockExcludesOtherProcessUntilUnlock(t *testing.T) {
	path := t.TempDir()
	helper := startHelper(t, "identity-release", path)
	_, err := TryLockIdentity(path)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("TryLockIdentity while another process holds it = %v, want ErrBusy", err)
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || pathErr.Path != path {
		t.Fatalf("error %v must be an *os.PathError naming the directory", err)
	}
	if err := helper.exit(); err != nil {
		t.Fatalf("helper unlock and exit: %v", err)
	}
	abandoned := AbandonedIdentityLocks()
	lock, err := TryLockIdentity(path)
	if err != nil {
		t.Fatalf("TryLockIdentity after the holder unlocked: %v", err)
	}
	defer lock.Unlock()
	if lock.Abandoned() || AbandonedIdentityLocks() != abandoned {
		t.Fatal("a lock released by Unlock was reported abandoned")
	}
}

// A holder that exits without unlocking releases the lock, as flock is
// released at exit. No other handle kept the mutex alive, so the object was
// destroyed with the holder and the next acquirer gets a fresh mutex.
func TestIdentityLockReleasedWhenHolderExitsWithoutUnlock(t *testing.T) {
	path := t.TempDir()
	helper := startHelper(t, "identity", path)
	if _, err := TryLockIdentity(path); !errors.Is(err, ErrBusy) {
		t.Fatalf("TryLockIdentity while held = %v, want ErrBusy", err)
	}
	if err := helper.exit(); err != nil {
		t.Fatalf("helper exit: %v", err)
	}
	lock := requireAcquireAfterHolderEnded(t, path)
	defer lock.Unlock()
}

// A killed holder (TerminateProcess) releases the lock too.
func TestIdentityLockReleasedWhenHolderIsKilled(t *testing.T) {
	path := t.TempDir()
	helper := startHelper(t, "identity", path)
	if _, err := TryLockIdentity(path); !errors.Is(err, ErrBusy) {
		t.Fatalf("TryLockIdentity while held = %v, want ErrBusy", err)
	}
	if err := helper.kill(); err != nil {
		t.Fatalf("kill helper: %v", err)
	}
	lock := requireAcquireAfterHolderEnded(t, path)
	defer lock.Unlock()
}

// When the mutex outlives a killed holder because another handle to it is
// open (here the test's own, in practice a concurrent TryLockIdentity), the
// next acquirer takes the abandoned mutex and reports it, through Abandoned
// and the process counter.
func TestIdentityLockReportsAbandonedMutex(t *testing.T) {
	path := t.TempDir()
	helper := startHelper(t, "identity", path)
	id, err := directoryIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(identityLockName(id))
	if err != nil {
		t.Fatal(err)
	}
	observer, err := windows.CreateMutex(nil, false, name)
	if !errors.Is(err, windows.ERROR_ALREADY_EXISTS) || observer == 0 {
		t.Fatalf("open the held mutex = %v, %v; want the existing mutex", observer, err)
	}
	defer closeHandle(observer)
	if err := helper.kill(); err != nil {
		t.Fatalf("kill helper: %v", err)
	}
	abandoned := AbandonedIdentityLocks()
	lock := requireAcquireAfterHolderEnded(t, path)
	defer lock.Unlock()
	if !lock.Abandoned() {
		t.Fatal("acquiring the mutex of a killed holder was not reported abandoned")
	}
	if AbandonedIdentityLocks() != abandoned+1 {
		t.Fatalf("abandoned counter = %d, want %d", AbandonedIdentityLocks(), abandoned+1)
	}
}

func requireAcquireAfterHolderEnded(t *testing.T, path string) *IdentityLock {
	t.Helper()
	var lock *IdentityLock
	took, err := eventually(5*time.Second, func() error {
		var err error
		lock, err = TryLockIdentity(path)
		return err
	})
	if err != nil {
		t.Fatalf("TryLockIdentity after the holder ended: %v (after %s)", err, took)
	}
	t.Logf("acquired %s after the holder ended (abandoned: %t)", took, lock.Abandoned())
	return lock
}

// Like flock on a second descriptor, a second lock in the same process is
// busy, on the same goroutine (a Win32 mutex would be recursive there) and on
// another one.
func TestIdentityLockBusyWithinProcess(t *testing.T) {
	path := t.TempDir()
	lock, err := TryLockIdentity(path)
	if err != nil {
		t.Fatalf("TryLockIdentity: %v", err)
	}
	if _, err := TryLockIdentity(path); !errors.Is(err, ErrBusy) {
		t.Fatalf("second TryLockIdentity on the same goroutine = %v, want ErrBusy", err)
	}
	other := make(chan error, 1)
	go func() {
		second, err := TryLockIdentity(path)
		if err == nil {
			_ = second.Unlock()
		}
		other <- err
	}()
	if err := <-other; !errors.Is(err, ErrBusy) {
		t.Fatalf("TryLockIdentity on another goroutine = %v, want ErrBusy", err)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if err := lock.Unlock(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("second Unlock = %v, want os.ErrClosed", err)
	}
	again, err := TryLockIdentity(path)
	if err != nil {
		t.Fatalf("TryLockIdentity after Unlock: %v", err)
	}
	if err := again.Unlock(); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
}

// Unlock on a goroutine other than the acquiring one is refused and changes
// nothing; the acquiring goroutine can still unlock.
func TestIdentityLockRefusesUnlockOnAnotherGoroutine(t *testing.T) {
	path := t.TempDir()
	lock, err := TryLockIdentity(path)
	if err != nil {
		t.Fatalf("TryLockIdentity: %v", err)
	}
	defer lock.Unlock()
	refused := make(chan error, 1)
	go func() {
		refused <- lock.Unlock()
	}()
	if err := <-refused; !errors.Is(err, ErrWrongGoroutine) {
		t.Fatalf("Unlock on another goroutine = %v, want ErrWrongGoroutine", err)
	}
	if _, err := TryLockIdentity(path); !errors.Is(err, ErrBusy) {
		t.Fatalf("lock after a refused Unlock = %v, want still held (ErrBusy)", err)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatalf("Unlock on the acquiring goroutine: %v", err)
	}
}

// The lock follows the directory's identity, not its path: an alias through
// an intermediate junction is the same lock, and other directories are
// independent.
func TestIdentityLockIsKeyedByIdentity(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	locked := filepath.Join(realDir, "project")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	createJunction(t, filepath.Join(root, "alias"), realDir)
	lock, err := TryLockIdentity(locked)
	if err != nil {
		t.Fatalf("TryLockIdentity: %v", err)
	}
	defer lock.Unlock()
	if _, err := TryLockIdentity(filepath.Join(root, "alias", "project")); !errors.Is(err, ErrBusy) {
		t.Fatalf("lock through an intermediate junction = %v, want ErrBusy", err)
	}
	if other, err := TryLockIdentity(realDir); err != nil {
		t.Fatalf("lock of another directory: %v", err)
	} else if err := other.Unlock(); err != nil {
		t.Fatal(err)
	}
}

// The reasons option 4a failed (ADR-092 O4, WS7): while another process
// holds the lock, renames and hard links into the directory, directory
// flushes and even renaming the directory itself all work, and the lock
// follows the renamed directory.
func TestIdentityLockLeavesDirectoryUsableAcrossProcesses(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "locked")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	helper := startHelper(t, "identity", path)
	requireDirectoryUsable(t, root, path)
	renamed := filepath.Join(root, "renamed")
	if err := os.Rename(path, renamed); err != nil {
		t.Fatalf("rename the locked directory: %v", err)
	}
	if _, err := TryLockIdentity(renamed); !errors.Is(err, ErrBusy) {
		t.Fatalf("lock of the renamed directory = %v, want ErrBusy", err)
	}
	if err := helper.exit(); err != nil {
		t.Fatalf("helper exit: %v", err)
	}
}

// The same holds inside the holding process, which is what haft init needs to
// publish into its locked project root.
func TestIdentityLockLeavesDirectoryUsableInProcess(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "locked")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	lock, err := TryLockIdentity(path)
	if err != nil {
		t.Fatalf("TryLockIdentity: %v", err)
	}
	defer lock.Unlock()
	requireDirectoryUsable(t, root, path)
}

func requireDirectoryUsable(t *testing.T, root string, path string) {
	t.Helper()
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s in the locked directory: %v", what, err)
		}
	}
	outside := filepath.Join(root, "outside")
	must("write outside", os.WriteFile(outside, []byte("o"), 0o644))
	must("rename from outside into", os.Rename(outside, filepath.Join(path, "moved-in")))
	must("write", os.WriteFile(filepath.Join(path, "a"), []byte("a"), 0o644))
	must("write", os.WriteFile(filepath.Join(path, "existing"), []byte("e"), 0o644))
	must("rename to a new name", os.Rename(filepath.Join(path, "a"), filepath.Join(path, "b")))
	must("rename over an existing name", os.Rename(filepath.Join(path, "b"), filepath.Join(path, "existing")))
	must("hard link", os.Link(filepath.Join(path, "existing"), filepath.Join(path, "linked")))
	_, err := os.ReadDir(path)
	must("list", err)
	outcome, err := SyncDirectory(path)
	must("flush (SyncDirectory)", err)
	if outcome != SyncFlushed {
		t.Fatalf("flush outcome = %d, want SyncFlushed", outcome)
	}
	file, err := OpenDirectoryForSync(path)
	must("write open for sync", err)
	must("flush (OpenDirectoryForSync)", file.Sync())
	must("close", file.Close())
}

func TestTryLockIdentityRejects(t *testing.T) {
	root := t.TempDir()
	if _, err := TryLockIdentity("relative"); !errors.Is(err, ErrRelativePath) {
		t.Fatalf("relative = %v, want ErrRelativePath", err)
	}
	if _, err := TryLockIdentity(filepath.Join(root, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing = %v, want fs.ErrNotExist", err)
	}
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	createJunction(t, filepath.Join(root, "junction"), target)
	if _, err := TryLockIdentity(filepath.Join(root, "junction")); !errors.Is(err, syscall.ELOOP) {
		t.Fatalf("junction = %v, want ELOOP", err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := TryLockIdentity(file); !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("file = %v, want ENOTDIR", err)
	}
}
