//go:build !windows

package fpfrefresh

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func acquireOperationLock(receiptPath string) (func(), error) {
	path := receiptPath + ".operation.lock"
	fd, err := unix.Open(
		path,
		unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return nil, fmt.Errorf("create refresh operation lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), path) // #nosec G115 -- unix.Open returned a valid nonnegative descriptor.
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("adopt refresh operation lock %s", path)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("stat refresh operation lock %s: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		_ = file.Close()
		return nil, fmt.Errorf(
			"%w: operation lock carrier %q is not a private regular file",
			ErrReceiptStale,
			path,
		)
	}
	if err := unix.Flock(
		int(file.Fd()), // #nosec G115 -- file is an open lock descriptor.
		unix.LOCK_EX|unix.LOCK_NB,
	); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, fmt.Errorf(
				"%w: another refresh operation is active",
				ErrReceiptBusy,
			)
		}
		return nil, fmt.Errorf("lock refresh operation %s: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN) // #nosec G115
		_ = file.Close()
		return nil, fmt.Errorf("sync refresh operation lock %s: %w", path, err)
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN) // #nosec G115
		_ = file.Close()
		return nil, err
	}
	released := false
	return func() {
		if released {
			return
		}
		released = true
		_ = unix.Flock(
			int(file.Fd()), // #nosec G115 -- file remains open until this release.
			unix.LOCK_UN,
		)
		_ = file.Close()
	}, nil
}
