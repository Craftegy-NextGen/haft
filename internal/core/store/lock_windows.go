//go:build windows

package store

import (
	"os"

	"golang.org/x/sys/windows"
)

// Windows-specific file locking using LockFileEx
func tryLock(f *os.File, write bool) (bool, error) {
	handle := windows.Handle(f.Fd())
	
	// LOCKFILE_FAIL_IMMEDIATELY - return immediately if can't acquire
	// LOCKFILE_EXCLUSIVE_LOCK - exclusive (write) lock
	var flags uint32 = windows.LOCKFILE_FAIL_IMMEDIATELY
	if write {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	
	err := windows.LockFileEx(handle, flags, 0, 0, 0, nil)
	if err != nil {
		if err == windows.ERROR_LOCK_VIOLATION || err == windows.ERROR_IO_PENDING {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func unlock(f *os.File) error {
	handle := windows.Handle(f.Fd())
	return windows.UnlockFileEx(handle, 0, 0, 0, nil)
}