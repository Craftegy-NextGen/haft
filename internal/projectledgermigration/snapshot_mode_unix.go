//go:build !windows

package projectledgermigration

import "os"

func insecureSnapshotFileMode(info os.FileInfo) bool {
	return info.Mode().Perm() != 0o600
}
