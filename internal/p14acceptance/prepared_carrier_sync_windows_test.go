//go:build windows

package p14acceptance

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/m0n0x41d/haft/internal/winfs"
)

// Windows half of the ADR-093 E3 split of prepared_carrier_test.go (see
// prepared_carrier_sync_unix_test.go). os.Open returns a read-only handle
// whose Sync fails with ERROR_ACCESS_DENIED on a directory, so the directory
// is opened through winfs.OpenDirectoryForSync (FILE_WRITE_DATA, probe P-c),
// which flushes with FlushFileBuffers and reports every failure as on Unix.
// The path is joined to the root directory name, which is absolute.

func syncP14Directory(root *os.Root, path string, label string) error {
	directory, err := winfs.OpenDirectoryForSync(filepath.Join(root.Name(), path))
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
