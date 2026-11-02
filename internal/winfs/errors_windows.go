package winfs

import (
	"errors"
	"fmt"
	"io/fs"
)

var (
	// ErrBusy reports a lock held through another handle: ERROR_LOCK_VIOLATION
	// for a file lock, WAIT_TIMEOUT or the in-process registry for an
	// IdentityLock.
	ErrBusy = errors.New("winfs: busy: held through another handle")

	// ErrLockConversion reports a lock request of the other mode on a handle
	// that already holds a lock. LockFileEx cannot convert a lock in place, so
	// the caller must unlock first.
	ErrLockConversion = errors.New("winfs: handle already holds a lock of the other mode; unlock it first")

	// ErrInvalidName reports a name that is not exactly one path component.
	ErrInvalidName = fmt.Errorf(
		"winfs: name must be one path component (not empty, '.', '..', and without '\\', '/', ':' or NUL): %w",
		fs.ErrInvalid,
	)

	// ErrRelativePath reports a relative path where an absolute one is required.
	ErrRelativePath = fmt.Errorf("winfs: path must be absolute: %w", fs.ErrInvalid)

	// ErrUnsupportedFilesystem reports a volume that cannot answer FileIdInfo.
	// Identity-held features fail closed on such volumes (ADR-092 O5).
	ErrUnsupportedFilesystem = errors.New("winfs: unsupported filesystem: FILE_ID_INFO is unavailable")

	// ErrNotPrivate reports a file that fails the private-file check (ADR-092 O2).
	ErrNotPrivate = errors.New("winfs: file is not private")

	// ErrElevatedOwner reports a file owned by the Administrators group, which
	// is what an elevated process creates by default. It is always wrapped
	// together with ErrNotPrivate.
	ErrElevatedOwner = errors.New("winfs: created by an elevated process")
)
