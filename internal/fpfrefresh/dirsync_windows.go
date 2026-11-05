package fpfrefresh

import (
	"os"
	"path/filepath"

	"github.com/m0n0x41d/haft/internal/winfs"
)

// openDirectoryForSync opens a directory whose Sync is fsync on it (ADR-092
// O3). Windows cannot flush the read-only handle that os.Open returns
// (ERROR_ACCESS_DENIED), so the directory is opened with write access through
// winfs.OpenDirectoryForSync, which needs an absolute path.
func openDirectoryForSync(path string) (*os.File, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return winfs.OpenDirectoryForSync(absolute)
}
