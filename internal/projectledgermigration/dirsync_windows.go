package projectledgermigration

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/m0n0x41d/haft/internal/winfs"
)

// syncSnapshotDirectory is fsync on a directory (ADR-092 O3). Windows cannot
// flush the read-only handle that os.Open returns (ERROR_ACCESS_DENIED), so
// the flush goes through winfs.SyncDirectory. Its open and flush errors cannot
// be told apart, so both carry the sync message of the Unix version.
func syncSnapshotDirectory(path string) error {
	if _, err := winfs.SyncDirectory(path); err != nil {
		return fmt.Errorf("sync serve migration snapshot directory: %w", err)
	}
	return nil
}

// openFileForSync opens a snapshot file whose Sync is fsync on it (ADR-092,
// G7). Windows refuses FlushFileBuffers on the read-only handle that os.Open
// returns (ERROR_ACCESS_DENIED), so the file is opened with write access
// through winfs.OpenFileForSync, which needs an absolute path. Every open and
// flush error still reaches syncSnapshotFile, as on Unix.
func openFileForSync(path string) (*os.File, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return winfs.OpenFileForSync(absolute)
}
