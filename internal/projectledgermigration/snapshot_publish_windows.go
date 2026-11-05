//go:build windows

package projectledgermigration

import "github.com/m0n0x41d/haft/internal/winfs"

// publishServeMigrationSnapshot is the Windows counterpart of
// renameat2(RENAME_NOREPLACE) on Linux and renamex_np(RENAME_EXCL) on Darwin
// (ADR-092 G4): winfs.RenameNoReplace, which is MoveFileEx with
// MOVEFILE_WRITE_THROUGH and without MOVEFILE_REPLACE_EXISTING or
// MOVEFILE_COPY_ALLOWED. An existing final snapshot is never replaced (the
// error satisfies errors.Is(err, fs.ErrExist)), and a move across volumes
// fails instead of copying. The caller syncs the directory afterwards, as on
// Unix.
func publishServeMigrationSnapshot(
	partialPath string,
	finalPath string,
) error {
	return winfs.RenameNoReplace(partialPath, finalPath)
}
