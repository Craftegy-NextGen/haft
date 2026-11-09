//go:build !windows

package fpfrefresh

import "os"

func stateDirectoryNotPrivate(info os.FileInfo) bool {
	return info.Mode().Perm()&0o077 != 0
}
