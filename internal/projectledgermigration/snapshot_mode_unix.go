//go:build !windows

package projectledgermigration

import "os"

// insecureSnapshotFileMode is the Unix predicate of requireSecureSnapshotFile.
// The path is only used by the Windows sibling, which reads the owner and DACL
// through a handle.
func insecureSnapshotFileMode(info os.FileInfo, path string) bool {
	return info.Mode().Perm() != 0o600
}
