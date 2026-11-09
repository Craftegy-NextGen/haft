//go:build !windows

package p13acceptance

import (
	"fmt"
	"os"
)

func syncEvidenceDirectory(root *os.Root, path string, label string) error {
	directory, err := root.Open(path)
	if err != nil {
		return fmt.Errorf("open P13 %s for sync: %w", label, err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return fmt.Errorf("sync P13 %s: %w", label, syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close P13 %s: %w", label, closeErr)
	}
	return nil
}
