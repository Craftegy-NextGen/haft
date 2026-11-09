//go:build windows

package projectledgermigration

import "os"

// serveSnapshotNotPrivate is the Windows sibling of the Unix regular 0600 check
// extracted from assertSecureServeSnapshot (serve_snapshot_mode_unix_test.go).
// It asserts exactly what the production predicate insecureSnapshotFileMode
// checks on Windows (snapshot_mode_windows.go; ADR-092 fact 3 and O2): a
// regular file whose Go mode is the image 0666 of the snapshot's
// os.Chmod(path, 0o600), that is, not read-only.
//
// It does not check owner and DACL: SQLite's VACUUM INTO creates the snapshot
// with the DACL inherited from the ledger directory, so winfs.CheckPrivate
// rejects it ("DACL is not protected from inheritance"). That is a product gap
// against ADR-092 O2 (2a), not a test fault; the check tightens to
// winfs.CheckPrivate once the snapshot is created private.
func serveSnapshotNotPrivate(info os.FileInfo, path string) bool {
	return !info.Mode().IsRegular() || info.Mode().Perm() != 0o666
}
