//go:build windows

package projectledgermigration

import (
	"errors"
	"fmt"
	"os"

	"github.com/m0n0x41d/haft/internal/winfs"
)

type migrationFileLease interface {
	release() error
}

// windowsMigrationFileLease is the Windows counterpart of the Unix lease in
// lease_supported.go (ADR-091 R-a, ADR-092 mapping table). The Unix calls map
// as follows:
//
//   - open(directory, O_DIRECTORY|O_NOFOLLOW): winfs.OpenDir, which rejects a
//     symbolic link or junction at the directory's final component (G1).
//   - openat(directoryFD, name, O_RDWR|O_CREAT|O_NOFOLLOW, 0600): Dir.OpenFile
//     relative to the held directory (G2) with winfs.Private, so a new lease
//     gets the token user as owner and a protected owner-only DACL (G6, 2a).
//   - fstat checks of safeMigrationLeaseMetadata (regular, 0600, no special
//     bits, one link, owner = euid): winfs.Fstat for the file type,
//     winfs.CheckPrivate for owner, DACL and link count. Special bits have no
//     Windows analog (vacuously satisfied).
//   - fstatat(AT_SYMLINK_NOFOLLOW) plus (st_dev, st_ino): Dir.Lstat compared by
//     FILE_ID_INFO (O5).
//   - flock(LOCK_EX|LOCK_NB): winfs.TryLockFile with winfs.ExclusiveLock; a
//     lease held through another handle, in this or another process, is busy
//     (nil, false, nil), and the coordinator retries until its deadline. The
//     lease file is content-free, so mandatory byte-range locking blocks no I/O.
//
// The Darwin-only ENOENT retry is not mirrored: the open is one handle-relative
// NtCreateFile with FILE_OPEN_IF, and no transient negative lookup is known
// for it on Windows, so every open error is returned, as on Linux.
type windowsMigrationFileLease struct {
	file *os.File
}

func tryAcquireMigrationFileLease(
	directory string,
	name string,
) (migrationFileLease, bool, error) {
	held, err := winfs.OpenDir(directory)
	if err != nil {
		return nil, false, fmt.Errorf(
			"open project ledger directory without following symlinks: %w",
			err,
		)
	}
	defer func() {
		_ = held.Close()
	}()
	file, err := held.OpenFile(name, os.O_RDWR|os.O_CREATE, winfs.Private)
	if err != nil {
		return nil, false, fmt.Errorf(
			"open project ledger migration lease without following symlinks: %w",
			err,
		)
	}
	if err := requireRegularAttachedMigrationLease(
		held,
		name,
		file,
	); err != nil {
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
		return nil, false, fmt.Errorf(
			"lock project ledger migration lease: %w",
			err,
		)
	}
	if err := requireRegularAttachedMigrationLease(
		held,
		name,
		file,
	); err != nil {
		_ = winfs.UnlockFile(file)
		_ = file.Close()
		return nil, false, err
	}
	return &windowsMigrationFileLease{file: file}, true, nil
}

// requireRegularAttachedMigrationLease keeps the Unix error texts. The
// ownership error also wraps the winfs cause, so a lease left by an elevated
// process carries winfs's "created by an elevated process" repair hint
// (ADR-092 O2).
func requireRegularAttachedMigrationLease(
	directory *winfs.Dir,
	name string,
	file *os.File,
) error {
	if file == nil {
		return fmt.Errorf("project ledger migration lease file is nil")
	}
	held, err := winfs.Fstat(file)
	if err != nil {
		return fmt.Errorf("inspect held project ledger migration lease: %w", err)
	}
	if !held.IsRegular() {
		return fmt.Errorf(
			"project ledger migration lease ownership or mode is unsafe",
		)
	}
	if err := winfs.CheckPrivate(file); err != nil {
		return fmt.Errorf(
			"project ledger migration lease ownership or mode is unsafe: %w",
			err,
		)
	}
	attached, err := directory.Lstat(name)
	if err != nil {
		return fmt.Errorf(
			"inspect attached project ledger migration lease: %w",
			err,
		)
	}
	if !attached.IsRegular() {
		return fmt.Errorf(
			"attached project ledger migration lease is not a regular file",
		)
	}
	if held.ID != attached.ID {
		return fmt.Errorf(
			"project ledger migration lease identity changed while opening",
		)
	}
	return nil
}

func (lease *windowsMigrationFileLease) release() error {
	if lease == nil || lease.file == nil {
		return nil
	}
	unlockErr := winfs.UnlockFile(lease.file)
	closeErr := lease.file.Close()
	lease.file = nil
	return errors.Join(unlockErr, closeErr)
}
