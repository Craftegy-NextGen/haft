package projectledgermigration

import (
	"fmt"

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
