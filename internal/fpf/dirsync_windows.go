package fpf

import (
	"fmt"

	"github.com/m0n0x41d/haft/internal/winfs"
)

// syncDirectory is fsync on a directory (ADR-092 O3). Windows cannot flush the
// read-only handle that os.Open returns (ERROR_ACCESS_DENIED), so the flush
// goes through winfs.SyncDirectory. Its open and flush errors cannot be told
// apart, so both carry the sync message of the Unix version.
func syncDirectory(path string) error {
	if _, err := winfs.SyncDirectory(path); err != nil {
		return fmt.Errorf("sync source query index directory: %w", err)
	}
	return nil
}
