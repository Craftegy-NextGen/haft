package winfs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"weak"

	"golang.org/x/sys/windows"
)

// LockMode selects a shared or an exclusive file lock (flock LOCK_SH, LOCK_EX).
type LockMode int

const (
	// SharedLock may be held through several handles at once.
	SharedLock LockMode = iota + 1
	// ExclusiveLock excludes every other handle.
	ExclusiveLock
)

// allBytes is the lock range length, as in Go's cmd/go lockedfile: the whole
// 64-bit range from offset 0, so every handle locks the same range.
const allBytes = ^uint32(0)

// heldFileLocks remembers the mode each handle holds. LockFileEx cannot
// convert a lock: an exclusive request over the handle's own shared lock
// fails (or waits forever), and a shared request over its own exclusive lock
// stacks a second lock. Both are refused with ErrLockConversion instead.
// Entries refer to their *os.File weakly; an entry whose file was closed
// without UnlockFile and whose handle value was reused is stale and replaced.
var heldFileLocks = struct {
	sync.Mutex
	locks map[windows.Handle]heldFileLock
}{locks: map[windows.Handle]heldFileLock{}}

type heldFileLock struct {
	file     weak.Pointer[os.File]
	mode     LockMode
	acquired bool
}

// errLockPending reports an operation on a handle whose LockFile is still
// waiting.
var errLockPending = errors.New("winfs: a lock request on this handle is still waiting")

// LockFile waits for a lock on the whole of file (flock without LOCK_NB).
// Windows byte-range locks are mandatory for I/O through other handles, so
// lock only content-free lock files. Locking again in the mode already held
// is a no-op; locking in the other mode fails with ErrLockConversion. The
// kernel releases the lock when the handle closes or the process exits. While
// it waits, LockFile holds a reference on the handle, so closing file waits
// until LockFile returns.
func LockFile(file *os.File, mode LockMode) error {
	return lockFile(file, mode, true)
}

// TryLockFile is LockFile with LOCK_NB: a lock held through another handle
// yields an error satisfying errors.Is(err, ErrBusy).
func TryLockFile(file *os.File, mode LockMode) error {
	return lockFile(file, mode, false)
}

// UnlockFile releases the lock that LockFile or TryLockFile took on file.
// Unlike flock(LOCK_UN), which succeeds on a descriptor that holds no lock,
// it fails with an error wrapping ERROR_NOT_LOCKED when the handle holds
// none: UnlockFileEx reports it, and an unbalanced unlock is a bookkeeping
// error worth seeing. While a LockFile on the same handle is still waiting it
// fails and changes nothing.
func UnlockFile(file *os.File) error {
	err := withFileHandle(file, func(handle windows.Handle) error {
		if err := releaseFileLock(handle, file); err != nil {
			return err
		}
		return windows.UnlockFileEx(handle, 0, allBytes, allBytes, new(windows.Overlapped))
	})
	if err != nil {
		return &os.PathError{Op: "unlock", Path: nameOf(file), Err: err}
	}
	return nil
}

func lockFile(file *os.File, mode LockMode, wait bool) error {
	op := "trylock"
	if wait {
		op = "lock"
	}
	flags, err := lockFlags(mode, wait)
	if err == nil {
		err = withFileHandle(file, func(handle windows.Handle) error {
			return lockHandle(handle, file, mode, flags, wait)
		})
	}
	if err != nil {
		return &os.PathError{Op: op, Path: nameOf(file), Err: err}
	}
	return nil
}

func lockHandle(handle windows.Handle, file *os.File, mode LockMode, flags uint32, wait bool) error {
	held, err := reserveFileLock(handle, file, mode)
	if err != nil || held {
		return err
	}
	// LockFileEx needs an OVERLAPPED even on a synchronous handle: it carries
	// the start offset of the range (zero here).
	err = windows.LockFileEx(handle, flags, 0, allBytes, allBytes, new(windows.Overlapped))
	if err != nil {
		abandonFileLock(handle, file)
		if !wait && errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			err = fmt.Errorf("%w: %w", ErrBusy, err)
		}
		return err
	}
	confirmFileLock(handle, file)
	return nil
}

func lockFlags(mode LockMode, wait bool) (uint32, error) {
	var flags uint32
	switch mode {
	case SharedLock:
	case ExclusiveLock:
		flags = windows.LOCKFILE_EXCLUSIVE_LOCK
	default:
		return 0, fmt.Errorf("winfs: unknown lock mode %d: %w", mode, fs.ErrInvalid)
	}
	if !wait {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	return flags, nil
}

// reserveFileLock records the mode as pending before LockFileEx runs. It
// reports true when the handle already holds that mode. It first drops the
// entries whose file was garbage collected.
func reserveFileLock(handle windows.Handle, file *os.File, mode LockMode) (bool, error) {
	heldFileLocks.Lock()
	defer heldFileLocks.Unlock()
	for key, entry := range heldFileLocks.locks {
		if entry.file.Value() == nil {
			delete(heldFileLocks.locks, key)
		}
	}
	entry, ok := heldFileLocks.locks[handle]
	if ok && entry.file.Value() == file {
		switch {
		case !entry.acquired:
			return false, errLockPending
		case entry.mode == mode:
			return true, nil
		default:
			return false, ErrLockConversion
		}
	}
	heldFileLocks.locks[handle] = heldFileLock{file: weak.Make(file), mode: mode}
	return false, nil
}

// confirmFileLock marks the pending entry acquired. Only the LockFile call
// that reserved it can reach this point: UnlockFile refuses pending entries.
func confirmFileLock(handle windows.Handle, file *os.File) {
	heldFileLocks.Lock()
	defer heldFileLocks.Unlock()
	entry, ok := heldFileLocks.locks[handle]
	if ok && entry.file.Value() == file {
		entry.acquired = true
		heldFileLocks.locks[handle] = entry
	}
}

// abandonFileLock drops the pending entry of a LockFileEx call that failed.
func abandonFileLock(handle windows.Handle, file *os.File) {
	heldFileLocks.Lock()
	defer heldFileLocks.Unlock()
	entry, ok := heldFileLocks.locks[handle]
	if ok && entry.file.Value() == file && !entry.acquired {
		delete(heldFileLocks.locks, handle)
	}
}

// releaseFileLock drops the acquired entry before UnlockFileEx runs, and
// refuses while a LockFile on the handle is still waiting.
func releaseFileLock(handle windows.Handle, file *os.File) error {
	heldFileLocks.Lock()
	defer heldFileLocks.Unlock()
	entry, ok := heldFileLocks.locks[handle]
	if !ok || entry.file.Value() != file {
		return nil
	}
	if !entry.acquired {
		return errLockPending
	}
	delete(heldFileLocks.locks, handle)
	return nil
}
