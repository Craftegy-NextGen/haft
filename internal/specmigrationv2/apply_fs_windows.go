//go:build windows

package specmigrationv2

import (
	"fmt"
	"os"

	"github.com/m0n0x41d/haft/internal/winfs"
)

// This file is the Windows counterpart of apply_fs_supported.go,
// apply_fs_linux.go and apply_fs_darwin.go (ADR-091 R-a). The Unix calls map to
// internal/winfs (ADR-092):
//
//   - open(O_CREAT|O_EXCL|O_NOFOLLOW): winfs.OpenFile with os.O_CREATE|os.O_EXCL,
//     an exclusive create that never follows a link at the final component (G1,
//     G3); mode 0600 maps to winfs.Private (G6), any other mode to
//     winfs.Inherited.
//   - open(O_RDONLY|O_NOFOLLOW) and the regular-file fstat check: winfs.OpenFile
//     and winfs.Fstat. A symbolic link or junction is opened as the link itself,
//     so it fails the regular-file check instead of being followed.
//   - fsync of a directory opened with O_DIRECTORY|O_NOFOLLOW: winfs.OpenDir and
//     Dir.Sync (G7, ADR-092 O3).
//   - stat and the st_dev comparison: winfs.PathIdentity and FileID.SameVolume,
//     the volume serial number, so carriers on different drives are detected (O5).
//   - renameat2(RENAME_NOREPLACE) and renamex_np(RENAME_EXCL): winfs.RenameNoReplace
//     (G4).
//
// openDirectoryNoFollow and adoptOpenedFile have no Windows counterpart: the
// only Unix callers are the directory lock and the descriptor adoption of
// apply_fs_supported.go, and the lock is apply_lock_windows.go.

func openExclusiveNoFollow(path string, mode os.FileMode) (*os.File, error) {
	protection := winfs.Inherited
	if mode.Perm()&0o077 == 0 {
		protection = winfs.Private
	}
	file, err := winfs.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, protection)
	if err != nil {
		return nil, fmt.Errorf("create exclusive migration file: %w", err)
	}
	return file, nil
}

func openReadOnlyNoFollow(path string) (*os.File, error) {
	file, err := winfs.OpenFile(path, os.O_RDONLY, winfs.Inherited)
	if err != nil {
		return nil, fmt.Errorf("open migration file without following symlinks: %w", err)
	}
	info, err := winfs.Fstat(file)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect migration file: %w", err)
	}
	if !info.IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("migration carrier %s is not a regular file", path)
	}
	return file, nil
}

func syncDirectoryNoFollow(path string) error {
	directory, err := winfs.OpenDir(path)
	if err != nil {
		return fmt.Errorf("open migration directory without following symlinks: %w", err)
	}
	// A volume that refuses the flush reports winfs.SyncSkipped, the documented
	// weaker fallback of ADR-092 O3; winfs counts it (SkippedDirectorySyncs).
	_, syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func sameFilesystem(paths []string) error {
	if len(paths) < 2 {
		return nil
	}
	first, err := winfs.PathIdentity(paths[0])
	if err != nil {
		return fmt.Errorf("inspect migration filesystem topology: %w", err)
	}
	for _, path := range paths[1:] {
		current, err := winfs.PathIdentity(path)
		if err != nil {
			return fmt.Errorf("inspect migration filesystem topology: %w", err)
		}
		if !current.SameVolume(first) {
			return fmt.Errorf("migration carriers cross filesystem devices")
		}
	}
	return nil
}

func renameNoReplace(source string, destination string) error {
	if err := winfs.RenameNoReplace(source, destination); err != nil {
		return fmt.Errorf("install migration carrier without replacement: %w", err)
	}
	return nil
}
