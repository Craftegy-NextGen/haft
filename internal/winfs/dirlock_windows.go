package winfs

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"golang.org/x/sys/windows"
)

// DirLock is an exclusive lock on a directory by share-mode exclusion
// (ADR-092 O4, option 4a): the directory is opened with FILE_WRITE_DATA access
// and a share mode without FILE_SHARE_WRITE, so a second such open fails with
// ERROR_SHARING_VIOLATION. It writes nothing into the directory. The kernel
// releases it when the handle closes, including at process exit.
//
// Side effects (probe P-b): while it is held, nobody else can open the
// directory with write access, which a directory flush needs. SyncDirectory
// and Dir.Sync in the holding process flush through the lock's handle; other
// processes get the sharing violation as an error. Conversely, any other open
// of the directory with write access, or without FILE_SHARE_WRITE, makes
// TryLockDir report busy while that open lasts. Opens for reading, file
// creation inside the directory, and renaming the directory are unaffected.
//
// Release is explicit: a DirLock that is dropped without Unlock keeps the
// directory locked until the process exits. There is deliberately no
// finalizer (and the in-process registry refers to the lock strongly): a
// leaked lock then fails safe, as extra exclusion, instead of being released
// at a moment chosen by the garbage collector. This differs from a leaked
// *os.File holding flock on Unix, which its finalizer closes.
type DirLock struct {
	handle windows.Handle
	path   string
	id     FileID
}

// heldDirLocks maps directory identities to the DirLocks this process holds,
// so a directory sync can reach the one handle that has write access.
var heldDirLocks = struct {
	sync.Mutex
	locks map[FileID]*DirLock
}{locks: map[FileID]*DirLock{}}

const dirLockAccess = windows.FILE_WRITE_DATA | windows.FILE_READ_ATTRIBUTES | windows.SYNCHRONIZE

// TryLockDir locks the absolute directory path without waiting. It does not
// follow a symbolic link or junction at the final component. A lock held
// through another handle, in this or another process, yields an error
// satisfying errors.Is(err, ErrBusy).
func TryLockDir(path string) (*DirLock, error) {
	if err := requireAbsolute(path); err != nil {
		return nil, &os.PathError{Op: "lock", Path: path, Err: err}
	}
	handle, err := createFile(
		path,
		dirLockAccess,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		nil,
	)
	if errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		return nil, &os.PathError{Op: "lock", Path: path, Err: fmt.Errorf("%w: %w", ErrBusy, err)}
	}
	if err != nil {
		return nil, &os.PathError{Op: "lock", Path: path, Err: err}
	}
	lock, err := newDirLock(handle, path)
	if err != nil {
		closeHandle(handle)
		return nil, &os.PathError{Op: "lock", Path: path, Err: err}
	}
	return lock, nil
}

func newDirLock(handle windows.Handle, path string) (*DirLock, error) {
	if err := checkFinalComponent(handle, true); err != nil {
		return nil, err
	}
	id, err := handleFileID(handle)
	if err != nil {
		return nil, err
	}
	lock := &DirLock{handle: handle, path: path, id: id}
	heldDirLocks.Lock()
	heldDirLocks.locks[id] = lock
	heldDirLocks.Unlock()
	return lock, nil
}

// Unlock releases the lock by closing its handle.
func (lock *DirLock) Unlock() error {
	heldDirLocks.Lock()
	defer heldDirLocks.Unlock()
	if lock.handle == windows.InvalidHandle {
		return &os.PathError{Op: "unlock", Path: lock.path, Err: os.ErrClosed}
	}
	if heldDirLocks.locks[lock.id] == lock {
		delete(heldDirLocks.locks, lock.id)
	}
	err := windows.CloseHandle(lock.handle)
	lock.handle = windows.InvalidHandle
	if err != nil {
		return &os.PathError{Op: "unlock", Path: lock.path, Err: err}
	}
	return nil
}

// flushHeldDirLock flushes the directory id through a DirLock this process
// holds. It reports false when there is none. The registry stays locked during
// the flush so Unlock cannot close the handle underneath it.
func flushHeldDirLock(id FileID) (bool, error) {
	heldDirLocks.Lock()
	defer heldDirLocks.Unlock()
	lock, ok := heldDirLocks.locks[id]
	if !ok {
		return false, nil
	}
	return true, windows.FlushFileBuffers(lock.handle)
}
