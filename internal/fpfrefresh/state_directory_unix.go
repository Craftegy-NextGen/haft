//go:build !windows

package fpfrefresh

import "os"

func stateDirectoryNotPrivate(info os.FileInfo) bool {
	return info.Mode().Perm()&0o077 != 0
}

// createStateDirectory creates the refresh state directory and its missing
// parents. Outside Windows it is exactly os.MkdirAll(path, 0o700).
func createStateDirectory(path string) error {
	return os.MkdirAll(path, 0o700)
}
