//go:build !windows

package fpf

import "path/filepath"

// cleanSourcePath returns the path a publication document is recorded under.
// Outside Windows it is exactly filepath.Clean.
func cleanSourcePath(path string) string {
	return filepath.Clean(path)
}
