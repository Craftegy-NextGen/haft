//go:build !windows

package projectledgermigration

import (
	"fmt"
	"os"
)

func syncSnapshotDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open serve migration snapshot directory: %w", err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return fmt.Errorf("sync serve migration snapshot directory: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close serve migration snapshot directory: %w", closeErr)
	}
	return nil
}

// openFileForSync opens a snapshot file whose Sync is fsync on it. Outside
// Windows it is exactly os.Open.
func openFileForSync(path string) (*os.File, error) {
	return os.Open(path)
}
