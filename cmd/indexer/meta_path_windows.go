//go:build windows

package main

import "path/filepath"

// cleanMetaPath returns the path an index records in its source metadata.
// The index is a committed generated carrier whose digest the integration lock
// pins, so its bytes must not depend on the OS that built it: Windows keeps the
// slash form that filepath.Clean would turn into backslashes. The recorded
// path is provenance only and is never used to open a file.
func cleanMetaPath(path string) string {
	return filepath.ToSlash(filepath.Clean(path))
}
