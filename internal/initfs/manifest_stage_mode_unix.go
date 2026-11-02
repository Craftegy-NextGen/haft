//go:build !windows

package initfs

import "os"

func canonicalCarrierStageModeSafe(info os.FileInfo) bool {
	return info.Mode().Perm() == 0o600 || info.Mode().Perm() == 0o644
}
