//go:build windows

package projectledgermigration

import (
	"os"

	"github.com/m0n0x41d/haft/internal/winfs"
)

// serveSnapshotNotPrivate is the Windows sibling of the Unix regular 0600 check
// extracted from assertSecureServeSnapshot (serve_snapshot_mode_unix_test.go).
// The serve-migration snapshot is created owner-only (ADR-092 O2, 2a;
// reserveSnapshotPartialPath), so the assertion is winfs.CheckPrivate on a
// no-follow handle: the token user owns it, the DACL is protected and
// owner-only, and it has one link.
func serveSnapshotNotPrivate(info os.FileInfo, path string) bool {
	if !info.Mode().IsRegular() {
		return true
	}
	file, err := winfs.OpenFile(path, os.O_RDONLY, winfs.Private)
	if err != nil {
		return true
	}
	defer file.Close()
	return winfs.CheckPrivate(file) != nil
}
