//go:build windows

package initfs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/m0n0x41d/haft/internal/winfs"
)

// manifestLock is the Windows counterpart of manifest_lock_supported.go
// (ADR-091 R-a, ADR-092 mapping table). The Unix calls map as follows:
//
//   - open(O_RDWR|O_CREAT|O_NOFOLLOW, 0600): winfs.OpenFile with os.O_RDWR|
//     os.O_CREATE and winfs.Private. A symbolic link or junction at the final
//     component is rejected (G1); a new file gets the token user as owner and a
//     protected owner-only DACL (G6, option 2a).
//   - fstat checks (regular, 0600, no special bits, one link, owner = euid):
//     winfs.Fstat for the file type, winfs.CheckPrivate for owner, DACL and
//     link count. Special bits have no Windows analog (vacuously satisfied).
//   - lstat plus (st_dev, st_ino) comparison: a no-follow lookup of the name in
//     its parent directory (winfs.Dir.Lstat) compared by FILE_ID_INFO (O5).
//   - flock(LOCK_EX|LOCK_NB): winfs.TryLockFile with winfs.ExclusiveLock; a lock
//     held through another handle, in this or another process, is busy
//     (nil, false, nil). The lock file is content-free, so Windows' mandatory
//     byte-range locking blocks no I/O.
type manifestLock struct {
	file *os.File
}

func tryAcquireManifestLock(
	path string,
) (*manifestLock, bool, error) {
	file, err := winfs.OpenFile(path, os.O_RDWR|os.O_CREATE, winfs.Private)
	if err != nil {
		return nil, false, fmt.Errorf("open manifest lock without following symlinks: %w", err)
	}
	if err := requireRegularAttachedManifestLock(path, file); err != nil {
		_ = file.Close()
		return nil, false, err
	}
	err = winfs.TryLockFile(file, winfs.ExclusiveLock)
	if errors.Is(err, winfs.ErrBusy) {
		_ = file.Close()
		return nil, false, nil
	}
	if err != nil {
		_ = file.Close()
		return nil, false, fmt.Errorf("lock manifest publication: %w", err)
	}
	if err := requireRegularAttachedManifestLock(path, file); err != nil {
		_ = winfs.UnlockFile(file)
		_ = file.Close()
		return nil, false, err
	}
	return &manifestLock{file: file}, true, nil
}

// requireRegularAttachedManifestLock keeps the Unix error texts. The
// ownership error also wraps the winfs cause, so a lock file left by an
// elevated process carries winfs's "created by an elevated process" repair
// hint (ADR-092 O2).
func requireRegularAttachedManifestLock(
	path string,
	file *os.File,
) error {
	if file == nil {
		return fmt.Errorf("manifest lock file is nil")
	}
	held, err := winfs.Fstat(file)
	if err != nil {
		return fmt.Errorf("inspect held manifest lock: %w", err)
	}
	if !held.IsRegular() {
		return fmt.Errorf("held manifest lock is not a regular file")
	}
	if err := winfs.CheckPrivate(file); err != nil {
		return fmt.Errorf("held manifest lock ownership or mode is unsafe: %w", err)
	}
	attached, err := lstatManifestLock(path)
	if err != nil {
		return fmt.Errorf("inspect attached manifest lock: %w", err)
	}
	if !attached.IsRegular() {
		return fmt.Errorf("attached manifest lock is not a regular file")
	}
	if held.ID != attached.ID {
		return fmt.Errorf("manifest lock identity changed while opening")
	}
	return nil
}

// lstatManifestLock is lstat(2) on path: the final component is not followed.
// winfs has no path-based lstat, so the name is looked up in its held parent
// directory. Unlike lstat(2), a symbolic link or junction at the parent's own
// final component is rejected rather than followed; both callers have already
// required the lock's directory tree to be free of symlinks
// (ensureDirectoryTreeWithoutSymlinks), so this rejects nothing they accept.
func lstatManifestLock(path string) (winfs.Info, error) {
	parent, err := winfs.OpenDir(filepath.Dir(path))
	if err != nil {
		return winfs.Info{}, err
	}
	defer func() {
		_ = parent.Close()
	}()
	return parent.Lstat(filepath.Base(path))
}

func (lock *manifestLock) release() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	unlockErr := winfs.UnlockFile(lock.file)
	closeErr := lock.file.Close()
	lock.file = nil
	return errors.Join(unlockErr, closeErr)
}
