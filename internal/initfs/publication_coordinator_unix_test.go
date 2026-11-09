//go:build !windows

package initfs

import "os"

func coordinationLockModeDiffers(info os.FileInfo) bool {
	return info.Mode().Perm() != 0o600
}
