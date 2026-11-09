//go:build windows

package store

import (
	"errors"
	"os"

	"github.com/m0n0x41d/haft/internal/winfs"
	"golang.org/x/sys/windows"
)

// tryLock is flock(LOCK_SH or LOCK_EX | LOCK_NB) on the writer lock file, as in
// lock_unix.go (ADR-092: LockFileEx over the whole range with an OVERLAPPED,
// through winfs). A lock held through another handle, in this or another
// process, is busy: (false, nil), and Store.open retries until its deadline.
// Every other failure is returned as the bare OS error, as flock's errno is.
// writer.lock is content-free, so Windows' mandatory byte-range locking
// blocks no I/O that the store performs.
func tryLock(f *os.File, write bool) (bool, error) {
	mode := winfs.SharedLock
	if write {
		mode = winfs.ExclusiveLock
	}
	err := winfs.TryLockFile(f, mode)
	if errors.Is(err, winfs.ErrBusy) {
		return false, nil
	}
	if err != nil {
		return false, bareError(err)
	}
	return true, nil
}

// unlock is flock(LOCK_UN). Like flock, it succeeds on a handle that holds no
// lock, which Store.open's failure path does before a lock was acquired. The
// kernel also releases the lock when the handle is closed.
func unlock(f *os.File) error {
	err := winfs.UnlockFile(f)
	if errors.Is(err, windows.ERROR_NOT_LOCKED) {
		return nil
	}
	if err != nil {
		return bareError(err)
	}
	return nil
}

// bareError drops the *os.PathError that winfs adds, so callers see the same
// error shape as the Unix flock errno.
func bareError(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}
