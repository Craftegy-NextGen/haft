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
