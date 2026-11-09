//go:build !windows

package fpfrefresh

import "os"

func receiptModeNotPrivate(got os.FileMode) bool {
	return got != 0o600
}

func operationCarrierNotPrivate(path string, info os.FileInfo) bool {
	return info.Mode().Perm() != 0o600
}

func candidateRootModeNotPrivate(permissions os.FileMode) bool {
	return permissions != 0o700
}

func receiptLockNotPrivate(path string, lockInfo os.FileInfo) bool {
	return lockInfo.Mode().Perm() != 0o600
}

func stateDirectoryModeNotPrivate(info os.FileInfo) bool {
	return info.Mode().Perm()&0o077 != 0
}

func effectsCacheDirectoryNotPrivate(path string, info os.FileInfo) bool {
	return info.Mode().Perm()&0o077 != 0
}

func writeReceiptLockCarrier(path string) error {
	return os.WriteFile(path+".lock", nil, 0o600)
}
