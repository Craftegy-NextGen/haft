//go:build windows

package agenthostrestart

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ensureExecutablePath is the Windows counterpart of the function in
// submission_executable_unix.go (ADR-092 fact 3; ADR-091 R-b).
//
// Windows has no execute permission bit, and Go reports Windows file modes as
// 0o666 or 0o444 only, so the Unix predicate `Perm()&0o111 == 0` holds for
// every file and every executable would be rejected. What makes a file
// executable by name on Windows is its extension: CreateProcess and the shell
// run the extensions listed in PATHEXT, the rule os/exec LookPath applies
// (lp_windows.go): PATHEXT split at ';', lower-cased, a missing leading dot
// added, and .com, .exe, .bat and .cmd when PATHEXT is unset; the comparison
// ignores case. The check still requires an existing regular file.
func ensureExecutablePath(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || !hasExecutableExtension(path) {
		return fmt.Errorf("%s is not executable", filepath.Clean(path))
	}
	return nil
}

func hasExecutableExtension(path string) bool {
	extension := filepath.Ext(path)
	if extension == "" {
		return false
	}
	for _, executable := range executableExtensions() {
		if strings.EqualFold(extension, executable) {
			return true
		}
	}
	return false
}

// executableExtensions returns the PATHEXT extensions as os/exec reads them.
func executableExtensions() []string {
	pathext := os.Getenv("PATHEXT")
	if pathext == "" {
		return []string{".com", ".exe", ".bat", ".cmd"}
	}
	var extensions []string
	for _, extension := range strings.Split(strings.ToLower(pathext), ";") {
		if extension == "" {
			continue
		}
		if extension[0] != '.' {
			extension = "." + extension
		}
		extensions = append(extensions, extension)
	}
	return extensions
}
