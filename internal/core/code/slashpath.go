package code

import "path/filepath"

// slashPath returns a captured-file path in the slash spelling of the capture
// map. go/build joins a package directory and a file name with the host
// separator before it calls Context.OpenFile, so on Windows a file below the
// root arrives as `pkg\pkg_test.go` while the capture keys it
// `pkg/pkg_test.go`. filepath.ToSlash is the identity where the separator
// is '/', so every Unix input is returned unchanged.
func slashPath(p string) string {
	return filepath.ToSlash(p)
}
