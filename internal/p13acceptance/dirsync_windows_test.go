package p13acceptance

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/m0n0x41d/haft/internal/winfs"
)

// syncEvidenceDirectory is the Windows twin of the Unix helper (ADR-093 E3).
// The directory is opened through root, as on Unix, so a missing or escaping
// path fails the same way. Windows cannot flush that read-only handle
// (ERROR_ACCESS_DENIED), so the flush goes through winfs.SyncDirectory on the
// absolute path of the same directory, with the Unix error messages.
func syncEvidenceDirectory(root *os.Root, path string, label string) error {
	directory, err := root.Open(path)
	if err != nil {
		return fmt.Errorf("open P13 %s for sync: %w", label, err)
	}
	syncErr := syncRootDirectory(root, path)
	closeErr := directory.Close()
	if syncErr != nil {
		return fmt.Errorf("sync P13 %s: %w", label, syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close P13 %s: %w", label, closeErr)
	}
	return nil
}

func syncRootDirectory(root *os.Root, path string) error {
	absolute, err := filepath.Abs(filepath.Join(root.Name(), filepath.FromSlash(path)))
	if err != nil {
		return err
	}
	_, err = winfs.SyncDirectory(absolute)
	return err
}
