//go:build windows

package fpfrefresh

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/m0n0x41d/haft/internal/winfs"
)

// acquireOperationLock is the Windows counterpart of the function in
// apply_unix.go (ADR-091 R-b, ADR-092 mapping table). The Unix calls map as
// follows:
//
//   - open(O_RDWR|O_CREAT|O_NOFOLLOW, 0600): winfs.OpenFile with os.O_RDWR|
//     os.O_CREATE and winfs.Private. A symbolic link or junction at the final
//     component is rejected (G1); a new file gets the token user as owner and
//     a protected owner-only DACL (G6, option 2a).
//   - fstat checks (regular, 0600): winfs.Fstat for the file type,
//     winfs.CheckPrivate for owner, DACL and link count. The error also wraps
//     the winfs cause, so a lock left by an elevated process carries winfs's
//     "created by an elevated process" repair hint (ADR-092 O2).
//   - flock(LOCK_EX|LOCK_NB): winfs.TryLockFile with winfs.ExclusiveLock; a
//     lock held through another handle, in this or another process, is busy.
//     The lock file is content-free, so Windows' mandatory byte-range locking
//     blocks no I/O.
//
// The lock stays held until the returned release runs: the file is not closed
// when this function returns.
func acquireOperationLock(receiptPath string) (func(), error) {
	path := receiptPath + ".operation.lock"
	file, err := winfs.OpenFile(path, os.O_RDWR|os.O_CREATE, winfs.Private)
	if err != nil {
		return nil, fmt.Errorf("create refresh operation lock: %w", err)
	}
	info, err := winfs.Fstat(file)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("stat refresh operation lock %s: %w", path, err)
	}
	if !info.IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf(
			"%w: operation lock carrier %q is not a private regular file",
			ErrReceiptStale,
			path,
		)
	}
	if err := winfs.CheckPrivate(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf(
			"%w: operation lock carrier %q is not a private regular file: %w",
			ErrReceiptStale,
			path,
			err,
		)
	}
	if err := winfs.TryLockFile(file, winfs.ExclusiveLock); err != nil {
		_ = file.Close()
		if errors.Is(err, winfs.ErrBusy) {
			return nil, fmt.Errorf(
				"%w: another refresh operation is active",
				ErrReceiptBusy,
			)
		}
		return nil, fmt.Errorf("lock refresh operation %s: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		_ = winfs.UnlockFile(file)
		_ = file.Close()
		return nil, fmt.Errorf("sync refresh operation lock %s: %w", path, err)
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		_ = winfs.UnlockFile(file)
		_ = file.Close()
		return nil, err
	}
	released := false
	return func() {
		if released {
			return
		}
		released = true
		_ = winfs.UnlockFile(file)
		_ = file.Close()
	}, nil
}
