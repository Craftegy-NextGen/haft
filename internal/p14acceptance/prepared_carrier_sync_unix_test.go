//go:build !windows

package p14acceptance

import (
	"fmt"
	"os"
)

// Unix half of the ADR-093 E3 split of prepared_carrier_test.go: fsync of a
// directory through os.Root. The function is moved verbatim;
// prepared_carrier_sync_windows_test.go holds the Windows twin.

func syncP14Directory(root *os.Root, path string, label string) error {
	directory, err := root.Open(path)
	if err != nil {
		return fmt.Errorf("open P14 %s for sync: %w", label, err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return fmt.Errorf("sync P14 %s: %w", label, syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close P14 %s: %w", label, closeErr)
	}
	return nil
}
