package store

import (
	"os"
	"path/filepath"

	"github.com/m0n0x41d/haft/internal/winfs"
)

// syncDir is fsync on the directory p inside root (ADR-092 O3). The directory
// is opened through root first, as on Unix, so a missing directory or one
// that escapes root fails the same way. That handle is read-only, and Windows
// cannot flush a read-only directory handle (ERROR_ACCESS_DENIED), so the
// flush goes through winfs.SyncDirectory on the absolute path of the same
// directory. Errors are returned unchanged, as on Unix.
func syncDir(root *os.Root, p string) error {
	f, err := root.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	path, err := filepath.Abs(filepath.Join(root.Name(), filepath.FromSlash(p)))
	if err != nil {
		return err
	}
	_, err = winfs.SyncDirectory(path)
	return err
}
