package winfs

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"sync/atomic"

	"golang.org/x/sys/windows"
)

// IdentityLock is an exclusive lock on a directory's identity: a named Win32
// mutex whose name is derived from the directory's FILE_ID_INFO (ADR-092 O4,
// option 4c). It is the Windows counterpart of flock(LOCK_EX|LOCK_NB) on a
// directory descriptor:
//
//   - it touches neither the directory nor its contents: no handle stays open
//     on the directory, so renames and hard links into it and directory
//     flushes from any process keep working (the share-mode directory lock of
//     option 4a refused both and was removed in WS7);
//   - it is keyed by identity, not by path, so every path that resolves to the
//     same directory (through intermediate junctions, subst drives or
//     different spellings) names the same lock;
//   - the kernel releases it when the owning thread or process ends. Usually
//     the holder's handle was the last one, so the named object is destroyed
//     with it and the next acquirer creates a fresh mutex, exactly like a
//     flock released at exit. If another handle kept the object alive (a
//     concurrent TryLockIdentity in the same instant), the next acquirer
//     takes the abandoned mutex and the lock reports Abandoned, counted by
//     AbandonedIdentityLocks; a dead holder is then visible, never silent;
//   - like flock on a second descriptor, a second TryLockIdentity in the same
//     process is busy, even on the same goroutine (a Win32 mutex alone would
//     be recursive for its owning thread).
//
// The mutex lives in the session namespace (Local\), so it excludes the
// processes of one logon session; processes in other sessions (another user,
// a service) do not see it. A process of the same session that holds or
// squats the name makes TryLockIdentity report busy or fail closed.
//
// Mutex ownership belongs to a thread, so TryLockIdentity wires the calling
// goroutine to its OS thread (runtime.LockOSThread) until Unlock. Unlock must
// run on the acquiring goroutine; on any other goroutine it fails with
// ErrWrongGoroutine and changes nothing. If the acquiring goroutine exits
// without Unlock, Go terminates its thread, the mutex is abandoned for other
// processes, and this process keeps reporting the identity busy (the leaked
// lock fails safe in-process).
type IdentityLock struct {
	handle    windows.Handle
	thread    uint32
	id        FileID
	path      string
	abandoned bool
}

// ErrWrongGoroutine reports an Unlock of an IdentityLock on a goroutine other
// than the one that acquired it.
var ErrWrongGoroutine = errors.New("winfs: identity lock must be unlocked on the goroutine that acquired it")

// heldIdentityLocks maps directory identities to the IdentityLocks this
// process holds, the in-process half of the flock semantics.
var heldIdentityLocks = struct {
	sync.Mutex
	locks map[FileID]*IdentityLock
}{locks: map[FileID]*IdentityLock{}}

// abandonedIdentityLocks counts acquisitions of abandoned mutexes in this
// process.
var abandonedIdentityLocks atomic.Uint64

// AbandonedIdentityLocks returns how many TryLockIdentity calls in this
// process acquired a lock whose previous holder ended without unlocking.
func AbandonedIdentityLocks() uint64 {
	return abandonedIdentityLocks.Load()
}

// identityLockName returns the session-namespace mutex name for a directory
// identity: Local\haft-lock-<volume serial, 16 hex digits>-<file ID, 32 hex
// digits>.
func identityLockName(id FileID) string {
	return fmt.Sprintf(`Local\haft-lock-%016x-%x`, id.VolumeSerial, id.ID)
}

// TryLockIdentity locks the absolute directory path without waiting. A
// symbolic link or junction at the final component is not followed (the open
// fails with an error wrapping syscall.ELOOP); intermediate components resolve
// as usual. A lock held by another process, or already held in this process,
// yields an error satisfying errors.Is(err, ErrBusy).
func TryLockIdentity(path string) (*IdentityLock, error) {
	id, err := directoryIdentity(path)
	if err != nil {
		return nil, &os.PathError{Op: "lock", Path: path, Err: err}
	}
	heldIdentityLocks.Lock()
	defer heldIdentityLocks.Unlock()
	if _, held := heldIdentityLocks.locks[id]; held {
		return nil, &os.PathError{Op: "lock", Path: path, Err: fmt.Errorf("%w: held in this process", ErrBusy)}
	}
	lock, err := acquireIdentityMutex(id, path)
	if err != nil {
		return nil, &os.PathError{Op: "lock", Path: path, Err: err}
	}
	heldIdentityLocks.locks[id] = lock
	return lock, nil
}

// directoryIdentity opens path as a directory without following a final
// reparse point and returns its FILE_ID_INFO identity. The handle is closed
// again: the lock holds no handle on the directory.
func directoryIdentity(path string) (FileID, error) {
	dir, err := OpenDir(path)
	if err != nil {
		return FileID{}, unwrapPathError(err)
	}
	defer dir.Close()
	id, err := Identity(dir.File())
	if err != nil {
		return FileID{}, unwrapPathError(err)
	}
	return id, nil
}

// acquireIdentityMutex creates or opens the identity's mutex and takes it
// without waiting, on the calling goroutine's locked OS thread.
func acquireIdentityMutex(id FileID, path string) (*IdentityLock, error) {
	name, err := windows.UTF16PtrFromString(identityLockName(id))
	if err != nil {
		return nil, err
	}
	runtime.LockOSThread()
	handle, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) && handle != 0 {
		// Opening an existing mutex is the normal case under contention;
		// CreateMutex reports it through the last error with a valid handle.
		err = nil
	}
	if err != nil {
		if handle != 0 {
			closeHandle(handle)
		}
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("create identity mutex: %w", err)
	}
	event, err := windows.WaitForSingleObject(handle, 0)
	lock := &IdentityLock{handle: handle, id: id, path: path}
	switch {
	case err != nil:
		err = fmt.Errorf("wait for identity mutex: %w", err)
	case event == windows.WAIT_OBJECT_0:
	case event == windows.WAIT_ABANDONED:
		lock.abandoned = true
		abandonedIdentityLocks.Add(1)
	case event == uint32(windows.WAIT_TIMEOUT):
		err = fmt.Errorf("%w: identity mutex is owned by another thread", ErrBusy)
	default:
		err = fmt.Errorf("wait for identity mutex: unexpected result %#x", event)
	}
	if err != nil {
		closeHandle(handle)
		runtime.UnlockOSThread()
		return nil, err
	}
	lock.thread = windows.GetCurrentThreadId()
	return lock, nil
}

// Abandoned reports whether the previous holder of the lock ended without
// unlocking it (WAIT_ABANDONED), as a crashed or killed process does.
func (lock *IdentityLock) Abandoned() bool {
	return lock.abandoned
}

// Unlock releases the mutex, closes its handle and unwires the goroutine from
// its OS thread. It must run on the goroutine that acquired the lock; on
// another goroutine it returns ErrWrongGoroutine and the lock stays held.
// A second Unlock fails with os.ErrClosed.
func (lock *IdentityLock) Unlock() error {
	heldIdentityLocks.Lock()
	defer heldIdentityLocks.Unlock()
	if lock.handle == windows.InvalidHandle {
		return &os.PathError{Op: "unlock", Path: lock.path, Err: os.ErrClosed}
	}
	if windows.GetCurrentThreadId() != lock.thread {
		return &os.PathError{Op: "unlock", Path: lock.path, Err: ErrWrongGoroutine}
	}
	releaseErr := windows.ReleaseMutex(lock.handle)
	closeErr := windows.CloseHandle(lock.handle)
	lock.handle = windows.InvalidHandle
	if heldIdentityLocks.locks[lock.id] == lock {
		delete(heldIdentityLocks.locks, lock.id)
	}
	runtime.UnlockOSThread()
	if err := errors.Join(releaseErr, closeErr); err != nil {
		return &os.PathError{Op: "unlock", Path: lock.path, Err: err}
	}
	return nil
}

func unwrapPathError(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}
