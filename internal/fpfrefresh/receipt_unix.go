//go:build !windows

package fpfrefresh

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func withReceiptExclusiveLock(path string, effect func() error) (result error) {
	lockPath := path + ".lock"
	fd, err := unix.Open(
		lockPath,
		unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return fmt.Errorf("open FPF refresh receipt lock %s: %w", lockPath, err)
	}
	lock := os.NewFile(uintptr(fd), lockPath) // #nosec G115 -- unix.Open returned a valid nonnegative descriptor.
	if lock == nil {
		_ = unix.Close(fd)
		return fmt.Errorf("adopt FPF refresh receipt lock %s", lockPath)
	}
	info, err := lock.Stat()
	if err != nil {
		_ = lock.Close()
		return fmt.Errorf("stat FPF refresh receipt lock %s: %w", lockPath, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		_ = lock.Close()
		return invalidReceiptError(
			"lock carrier %q is not a private regular file",
			lockPath,
		)
	}
	if err := unix.Flock(
		int(lock.Fd()), // #nosec G115 -- lock is an open file descriptor.
		unix.LOCK_EX|unix.LOCK_NB,
	); err != nil {
		_ = lock.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return fmt.Errorf("%w: %s", ErrReceiptBusy, path)
		}
		return fmt.Errorf("lock FPF refresh receipt %s: %w", lockPath, err)
	}
	defer func() {
		unlockErr := unix.Flock(
			int(lock.Fd()), // #nosec G115 -- lock remains open through the effect.
			unix.LOCK_UN,
		)
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
