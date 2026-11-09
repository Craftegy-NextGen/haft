//go:build windows

package fpf

import "path/filepath"

// cleanSourcePath returns the path a publication document is recorded under.
// The path is part of every SourceUnit provenance and is compared with the
// logical slash path of the same document ("data/FPF/FPF-Spec.md") by
// fpfrefresh, so Windows keeps the slash form that filepath.Clean would turn
// into backslashes. The cleaned path stays usable to open the file.
func cleanSourcePath(path string) string {
	return filepath.ToSlash(filepath.Clean(path))
}
