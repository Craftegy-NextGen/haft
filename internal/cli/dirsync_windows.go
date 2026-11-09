package cli

import (
	"fmt"

	"github.com/m0n0x41d/haft/internal/winfs"
)

// syncPublicExactFileDirectory is fsync on a directory (ADR-092 O3). Windows
// cannot flush the read-only handle that os.Open returns
// (ERROR_ACCESS_DENIED), so the flush goes through winfs.SyncDirectory. Its
// open and flush errors cannot be told apart, so both carry the sync message
// of the Unix version.
func syncPublicExactFileDirectory(path string) error {
	if _, err := winfs.SyncDirectory(path); err != nil {
		return fmt.Errorf("sync public exact file directory %s: %w", path, err)
	}
	return nil
}

// syncProfileReviewDirectory is fsync on a directory through
// winfs.SyncDirectory, as syncPublicExactFileDirectory. Like the Unix version
// it returns the error unwrapped.
func syncProfileReviewDirectory(path string) error {
	_, err := winfs.SyncDirectory(path)
	return err
}
