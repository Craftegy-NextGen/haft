//go:build !windows

package main

import "path/filepath"

// cleanMetaPath returns the path an index records in its source metadata.
// Outside Windows it is exactly filepath.Clean.
func cleanMetaPath(path string) string {
	return filepath.Clean(path)
}
