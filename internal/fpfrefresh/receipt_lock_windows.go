//go:build windows

package fpfrefresh

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/m0n0x41d/haft/internal/winfs"
)

// withReceiptExclusiveLock is the Windows counterpart of the function in
// receipt_unix.go (ADR-091 R-b, ADR-092 mapping table). The Unix calls map as
// in acquireOperationLock (apply_lock_windows.go): winfs.OpenFile with
// winfs.Private for the no-follow private open, winfs.Fstat and
// winfs.CheckPrivate for the regular, private check, and winfs.TryLockFile
// with winfs.ExclusiveLock for flock(LOCK_EX|LOCK_NB). The lock file is
// content-free, so Windows' mandatory byte-range locking blocks no I/O.
func withReceiptExclusiveLock(path string, effect func() error) (result error) {
	lockPath := path + ".lock"
	lock, err := winfs.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, winfs.Private)
	if err != nil {
		return fmt.Errorf("open FPF refresh receipt lock %s: %w", lockPath, err)
	}
	info, err := winfs.Fstat(lock)
	if err != nil {
		_ = lock.Close()
		return fmt.Errorf("stat FPF refresh receipt lock %s: %w", lockPath, err)
	}
	if !info.IsRegular() {
		_ = lock.Close()
		return invalidReceiptError(
			"lock carrier %q is not a private regular file",
			lockPath,
		)
	}
	if err := winfs.CheckPrivate(lock); err != nil {
		_ = lock.Close()
		return fmt.Errorf(
			"%w: %w",
			invalidReceiptError(
				"lock carrier %q is not a private regular file",
				lockPath,
			),
			err,
		)
	}
	if err := winfs.TryLockFile(lock, winfs.ExclusiveLock); err != nil {
		_ = lock.Close()
		if errors.Is(err, winfs.ErrBusy) {
			return fmt.Errorf("%w: %s", ErrReceiptBusy, path)
		}
		return fmt.Errorf("lock FPF refresh receipt %s: %w", lockPath, err)
	}
	defer func() {
		unlockErr := winfs.UnlockFile(lock)
		closeErr := lock.Close()
		if unlockErr != nil {
			result = errors.Join(
				result,
				fmt.Errorf("unlock FPF refresh receipt %s: %w", lockPath, unlockErr),
			)
		}
		if closeErr != nil {
			result = errors.Join(
				result,
				fmt.Errorf("close FPF refresh receipt lock %s: %w", lockPath, closeErr),
			)
		}
	}()
	if err := lock.Sync(); err != nil {
		return fmt.Errorf("sync FPF refresh receipt lock %s: %w", lockPath, err)
	}
	if err := syncReceiptDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	return effect()
}
