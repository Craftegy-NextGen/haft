//go:build windows

package specmigrationv2

import (
	"errors"
	"fmt"

	"github.com/m0n0x41d/haft/internal/winfs"
)

// migrationLock is the Windows counterpart of apply_lock_unix.go: flock(LOCK_EX |
// LOCK_NB) on a no-follow descriptor of the .haft directory. winfs.TryLockIdentity
// opens the directory without following a symbolic link or junction at the final
// component, rejects a non-directory, and takes a named mutex keyed by the
// directory's FILE_ID_INFO (ADR-092 O4, option 4c). Like flock it holds no handle
// on the directory, so the migration's own renames into .haft and directory
// flushes keep working, and the kernel releases it when the process ends. A
// second lock on the same directory, in this or another process, is busy.
//
// The mutex belongs to the acquiring OS thread, so close must run on the
// goroutine that called acquireMigrationLock; ApplyMigration and RecoverMigration
// defer it there.
type migrationLock struct {
	lock *winfs.IdentityLock
}

func acquireMigrationLock(path string) (migrationLock, error) {
	lock, err := winfs.TryLockIdentity(path)
	if errors.Is(err, winfs.ErrBusy) {
		return migrationLock{}, fmt.Errorf("migration apply is already locked: %w", err)
	}
	if err != nil {
		return migrationLock{}, fmt.Errorf("open migration lock directory: %w", err)
	}
	return migrationLock{lock: lock}, nil
}

func (lock migrationLock) close() error {
	if lock.lock == nil {
		return nil
	}
	return lock.lock.Unlock()
}
