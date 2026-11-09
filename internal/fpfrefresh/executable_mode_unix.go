//go:build !windows

package fpfrefresh

import "os"

func executableModeMissing(path string, info os.FileInfo) bool {
	return info.Mode().Perm()&0o111 == 0
}
