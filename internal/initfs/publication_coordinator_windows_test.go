//go:build windows

package initfs

import "os"

// coordinationLockModeDiffers is the Windows image of the Unix test predicate
// `info.Mode().Perm() != 0o600`: 0o600 has the owner-write bit, which Go
// reports on Windows as 0o666. Privacy of the lock file (the Windows meaning
// of 0600) is checked by TestManifestLockFileIsPrivate.
func coordinationLockModeDiffers(info os.FileInfo) bool {
	return info.Mode().Perm() != 0o666
}
