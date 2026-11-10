//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// E4/E5 (ADR-093): the Windows form of replaceDatabasePath (see main_unix_test.go).
//
// The two TestComparePreviousTypeEnv_PathReplacement* tests swap the database file at the
// probed path while the previous index is open in a read-only SQLite transaction, then
// check that the comparison still uses the opened snapshot. Windows does not allow that
// swap: SQLite opens the file without FILE_SHARE_DELETE, so renaming it fails with a
// sharing violation. The replacement race cannot happen, which is what the tests protect.
//
// This helper therefore requires the OS refusal and leaves the files as they are; the
// test assertions then hold against the unreplaced, still opened predecessor. If the
// rename is ever allowed (the handle was closed or opened with delete sharing), it stages
// the replacement exactly as the Unix form does, so the tests keep their full meaning.
func replaceDatabasePath(activePath, replacementPath, archivedPath string) error {
	err := os.Rename(activePath, archivedPath)
	if err == nil {
		if err := os.Rename(replacementPath, activePath); err != nil {
			_ = os.Rename(archivedPath, activePath)
			return fmt.Errorf("install predecessor replacement: %w", err)
		}
		return nil
	}
	if errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		return nil
	}
	return fmt.Errorf("archive opened predecessor: %w", err)
}
