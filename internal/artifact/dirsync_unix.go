//go:build !windows

package artifact

import "os"

// openDirectoryForSync opens a directory whose Sync is fsync on it. Outside
// Windows it is exactly os.Open.
func openDirectoryForSync(path string) (*os.File, error) {
	return os.Open(path)
}
