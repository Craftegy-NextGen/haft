//go:build !windows

package projectledgermigration

import "os"

// serveSnapshotNotPrivate is the Unix form of the privacy assertion in
// assertSecureServeSnapshot (ADR-093 E3, extracted verbatim): a private
// snapshot is a regular file with mode 0600. Its Windows sibling checks the
// explicit owner and protected owner-only DACL instead (ADR-092 O2).
func serveSnapshotNotPrivate(info os.FileInfo, path string) bool {
	return !info.Mode().IsRegular() || info.Mode().Perm() != 0o600
}
