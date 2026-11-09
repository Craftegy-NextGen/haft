package winfs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
	"weak"

	"golang.org/x/sys/windows"
)

// openLockFile opens one more handle to a shared lock file; LockFileEx locks
// belong to handles, so two handles in one process contend like two processes.
func openLockFile(t *testing.T, path string) *os.File {
	t.Helper()
	file, err := OpenFile(path, os.O_RDWR|os.O_CREATE, Inherited)
	if err != nil {
		t.Fatalf("open lock file: %v", err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func TestTryLockFileExclusiveExcludesOtherHandles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	first, second := openLockFile(t, path), openLockFile(t, path)
	if err := TryLockFile(first, ExclusiveLock); err != nil {
		t.Fatalf("first exclusive: %v", err)
	}
	for _, mode := range []LockMode{ExclusiveLock, SharedLock} {
		err := TryLockFile(second, mode)
		if !errors.Is(err, ErrBusy) || !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			t.Fatalf("second handle mode %d while exclusive held = %v, want ErrBusy", mode, err)
		}
		var pathErr *os.PathError
		if !errors.As(err, &pathErr) || pathErr.Path != path {
			t.Fatalf("error %v must be an *os.PathError naming the file", err)
		}
	}
	if err := UnlockFile(first); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if err := TryLockFile(second, ExclusiveLock); err != nil {
		t.Fatalf("exclusive after unlock: %v", err)
	}
	if err := UnlockFile(second); err != nil {
		t.Fatal(err)
	}
}

func TestTryLockFileSharedAdmitsShared(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	first, second, third := openLockFile(t, path), openLockFile(t, path), openLockFile(t, path)
	if err := TryLockFile(first, SharedLock); err != nil {
		t.Fatal(err)
	}
	if err := TryLockFile(second, SharedLock); err != nil {
		t.Fatalf("second shared: %v", err)
	}
	if err := TryLockFile(third, ExclusiveLock); !errors.Is(err, ErrBusy) {
		t.Fatalf("exclusive while shared held = %v, want ErrBusy", err)
	}
	_ = UnlockFile(first)
	_ = UnlockFile(second)
	if err := TryLockFile(third, ExclusiveLock); err != nil {
		t.Fatalf("exclusive after shared released: %v", err)
	}
	_ = UnlockFile(third)
}

func TestLockConversionIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	file, other := openLockFile(t, path), openLockFile(t, path)
	if err := TryLockFile(file, SharedLock); err != nil {
		t.Fatal(err)
	}
	if err := TryLockFile(file, ExclusiveLock); !errors.Is(err, ErrLockConversion) {
		t.Fatalf("shared to exclusive = %v, want ErrLockConversion", err)
	}
	// Without the refusal this blocking call would wait on its own lock forever.
	if err := LockFile(file, ExclusiveLock); !errors.Is(err, ErrLockConversion) {
		t.Fatalf("blocking shared to exclusive = %v, want ErrLockConversion", err)
	}
	if err := UnlockFile(file); err != nil {
		t.Fatal(err)
	}
	if err := TryLockFile(file, ExclusiveLock); err != nil {
		t.Fatal(err)
	}
	if err := TryLockFile(file, SharedLock); !errors.Is(err, ErrLockConversion) {
		t.Fatalf("exclusive to shared = %v, want ErrLockConversion", err)
	}
	if err := TryLockFile(file, ExclusiveLock); err != nil {
		t.Fatalf("same-mode relock must be a no-op like flock: %v", err)
	}
	if err := UnlockFile(file); err != nil {
		t.Fatal(err)
	}
	if err := TryLockFile(other, ExclusiveLock); err != nil {
		t.Fatalf("one unlock must release a relocked handle: %v", err)
	}
	_ = UnlockFile(other)
}

func TestLockFileWaitsForRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	holder, waiter := openLockFile(t, path), openLockFile(t, path)
	if err := LockFile(holder, ExclusiveLock); err != nil {
		t.Fatal(err)
	}
	acquired := make(chan error, 1)
	go func() { acquired <- LockFile(waiter, ExclusiveLock) }()
	select {
	case err := <-acquired:
		t.Fatalf("LockFile returned while the lock was held: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	if err := UnlockFile(holder); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("LockFile after release: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("LockFile did not return after the holder released")
	}
	_ = UnlockFile(waiter)
}

func TestCloseReleasesFileLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	holder, err := OpenFile(path, os.O_RDWR|os.O_CREATE, Inherited)
	if err != nil {
		t.Fatal(err)
	}
	other := openLockFile(t, path)
	if err := TryLockFile(holder, ExclusiveLock); err != nil {
		t.Fatal(err)
	}
	_ = holder.Close()
	elapsed, err := eventually(5*time.Second, func() error { return TryLockFile(other, ExclusiveLock) })
	if err != nil {
		t.Fatalf("lock not released by close after %v: %v", elapsed, err)
	}
	_ = UnlockFile(other)
}

func TestLockFileErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	file := openLockFile(t, path)
	if err := TryLockFile(file, LockMode(9)); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("unknown mode = %v, want fs.ErrInvalid", err)
	}
	if err := UnlockFile(file); !errors.Is(err, windows.ERROR_NOT_LOCKED) {
		t.Fatalf("unlock without lock = %v, want ERROR_NOT_LOCKED", err)
	}
	closed, err := OpenFile(path, os.O_RDWR, Inherited)
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	if err := TryLockFile(closed, ExclusiveLock); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("lock on a closed file = %v, want os.ErrClosed", err)
	}
}

// An entry left by a file that was closed without UnlockFile, but is still
// referenced, must not block a new file that reuses the same handle value.
func TestLockEntryOfReusedHandleIsReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	file := openLockFile(t, path)
	previous, err := OpenFile(path, os.O_RDWR, Inherited)
	if err != nil {
		t.Fatal(err)
	}
	_ = previous.Close()
	handle := windows.Handle(file.Fd())
	heldFileLocks.Lock()
	heldFileLocks.locks[handle] = heldFileLock{file: weak.Make(previous), mode: SharedLock, acquired: true}
	heldFileLocks.Unlock()
	if err := TryLockFile(file, ExclusiveLock); err != nil {
		t.Fatalf("the entry of the previous owner blocked a new file: %v", err)
	}
	heldFileLocks.Lock()
	entry := heldFileLocks.locks[handle]
	heldFileLocks.Unlock()
	if entry.file.Value() != file || entry.mode != ExclusiveLock || !entry.acquired {
		t.Fatalf("entry not replaced: %+v", entry)
	}
	_ = UnlockFile(file)
	runtime.KeepAlive(previous)
}

// Entries whose *os.File was garbage collected are pruned.
func TestCollectedLockEntryIsPruned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	pointer := closedFilePointer(t, path)
	for attempt := 0; attempt < 20 && pointer.Value() != nil; attempt++ {
		runtime.GC()
	}
	if pointer.Value() != nil {
		t.Fatal("the unreachable file was not collected after 20 GC cycles")
	}
	const unusedHandle = windows.Handle(0x7ffffff0)
	heldFileLocks.Lock()
	heldFileLocks.locks[unusedHandle] = heldFileLock{file: pointer, mode: SharedLock, acquired: true}
	heldFileLocks.Unlock()
	file := openLockFile(t, path)
	if err := TryLockFile(file, ExclusiveLock); err != nil {
		t.Fatal(err)
	}
	_ = UnlockFile(file)
	heldFileLocks.Lock()
	_, stale := heldFileLocks.locks[unusedHandle]
	heldFileLocks.Unlock()
	if stale {
		t.Fatal("the entry of a collected file was not pruned")
	}
}

// closedFilePointer returns a weak pointer to a file that is closed and no
// longer referenced once this function returns.
func closedFilePointer(t *testing.T, path string) weak.Pointer[os.File] {
	t.Helper()
	file, err := OpenFile(path, os.O_RDWR|os.O_CREATE, Inherited)
	if err != nil {
		t.Fatal(err)
	}
	pointer := weak.Make(file)
	_ = file.Close()
	return pointer
}

// UnlockFile on a handle whose LockFile still waits must refuse and leave the
// pending request tracked, so the lock it later acquires is still known.
func TestUnlockWhileLockWaitsIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	holder, waiter := openLockFile(t, path), openLockFile(t, path)
	if err := LockFile(holder, ExclusiveLock); err != nil {
		t.Fatal(err)
	}
	acquired := make(chan error, 1)
	go func() { acquired <- LockFile(waiter, ExclusiveLock) }()
	waiterHandle := windows.Handle(waiter.Fd())
	if _, err := eventually(5*time.Second, func() error {
		heldFileLocks.Lock()
		entry, ok := heldFileLocks.locks[waiterHandle]
		heldFileLocks.Unlock()
		if !ok || entry.acquired {
			return errors.New("waiter not pending yet")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := UnlockFile(waiter); !errors.Is(err, errLockPending) {
		t.Fatalf("UnlockFile while LockFile waits = %v, want errLockPending", err)
	}
	if err := UnlockFile(holder); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("waiting LockFile: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("waiting LockFile did not return")
	}
	if err := TryLockFile(waiter, SharedLock); !errors.Is(err, ErrLockConversion) {
		t.Fatalf("the acquired lock must be tracked; TryLockFile(shared) = %v, want ErrLockConversion", err)
	}
	if err := UnlockFile(waiter); err != nil {
		t.Fatal(err)
	}
}

// Two processes: the helper holds the lock, this process sees busy, and the
// lock becomes available after the helper exits without unlocking.
func TestFileLockTwoProcesses(t *testing.T) {
	for _, ending := range []string{"exit", "kill"} {
		t.Run(ending, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "lock")
			helper := startHelper(t, "file-exclusive", path)
			probe := openLockFile(t, path)
			for _, mode := range []LockMode{ExclusiveLock, SharedLock} {
				if err := TryLockFile(probe, mode); !errors.Is(err, ErrBusy) {
					t.Fatalf("mode %d while another process holds exclusive = %v, want ErrBusy", mode, err)
				}
			}
			endHelper(t, helper, ending)
			elapsed, err := eventually(5*time.Second, func() error { return TryLockFile(probe, ExclusiveLock) })
			if err != nil {
				t.Fatalf("lock not released %v after the holder ended: %v", elapsed, err)
			}
			t.Logf("file lock acquirable %v after the holder ended by %s", elapsed, ending)
			_ = UnlockFile(probe)
		})
	}
	t.Run("shared", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "lock")
		helper := startHelper(t, "file-shared", path)
		probe := openLockFile(t, path)
		if err := TryLockFile(probe, ExclusiveLock); !errors.Is(err, ErrBusy) {
			t.Fatalf("exclusive while another process holds shared = %v, want ErrBusy", err)
		}
		if err := TryLockFile(probe, SharedLock); err != nil {
			t.Fatalf("shared beside another process's shared lock: %v", err)
		}
		_ = UnlockFile(probe)
		endHelper(t, helper, "exit")
	})
}

func endHelper(t *testing.T, helper *helperProcess, ending string) {
	t.Helper()
	if ending == "kill" {
		_ = helper.kill()
		return
	}
	if err := helper.exit(); err != nil {
		t.Fatalf("helper exit: %v", err)
	}
}
