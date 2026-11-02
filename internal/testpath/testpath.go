// Package testpath makes test fixtures that are written with Unix paths and a
// Unix home directory portable to Windows. It is imported only by tests.
package testpath

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Abs returns an absolute path for the running OS from a Unix-style absolute
// fixture path such as "/tmp/haft-project". Where the path separator is "/"
// (Linux, macOS) the fixture is returned unchanged. On Windows the slashes
// become backslashes and the volume of the temporary directory is prefixed,
// so "/tmp/haft-project" becomes `D:\tmp\haft-project` when TEMP is on D:.
func Abs(unixPath string) string {
	return absFor(unixPath, filepath.Separator, filepath.VolumeName(os.TempDir()))
}

// absFor is Abs with the separator and volume supplied, so both branches can be
// tested on every OS.
func absFor(unixPath string, separator rune, volume string) string {
	if separator == '/' {
		return unixPath
	}
	converted := []rune(unixPath)
	for i, r := range converted {
		if r == '/' {
			converted[i] = separator
		}
	}
	return volume + string(converted)
}

// Exe returns the path of a fixture executable for the running OS. Where the
// path separator is "/" (Linux, macOS) the path is returned unchanged. On
// Windows ".exe" is appended, because Windows decides that a file is
// executable by its extension (PATHEXT), not by a permission bit. Use it for
// fixture binaries whose path is checked but which are never run.
func Exe(path string) string {
	return exeFor(path, filepath.Separator)
}

// exeFor is Exe with the separator supplied, so both branches can be tested
// on every OS.
func exeFor(path string, separator rune) string {
	if separator == '/' {
		return path
	}
	return path + ".exe"
}

// SetHome points the user home directory at dir for the rest of the test. It
// sets HOME, which os.UserHomeDir reads on Linux and macOS, and USERPROFILE,
// which it reads on Windows.
func SetHome(t testing.TB, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

// JSONString returns s as a JSON string literal, quotes included, for fixtures
// that assemble JSON text around a path (a Windows path contains backslashes).
func JSONString(s string) string {
	encoded, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
