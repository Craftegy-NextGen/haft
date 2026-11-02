//go:build windows

package host

import (
	"os"
	"path/filepath"
	"strings"
)

// candidateBinaryNotExecutable is the Windows meaning of the Unix predicate
// `bi.Mode()&0111 == 0` in Init (ADR-092 fact 3; ADR-091 R-b).
//
// Windows has no execute permission bit, and Go reports Windows file modes
// as 0o666 or 0o444 only, so the Unix predicate holds for every file and Init
// would reject every candidate binary. What makes a file executable by name
// on Windows is its extension: CreateProcess and the shell run the
// extensions listed in PATHEXT. This is the rule Go's own os/exec LookPath
// applies on Windows (lp_windows.go): PATHEXT split at ';', lower-cased, a
// missing leading dot added, and .com, .exe, .bat and .cmd when PATHEXT is
// unset; the comparison ignores case. The shared code still requires an
// existing regular file (bi.Mode().IsRegular()).
func candidateBinaryNotExecutable(bi os.FileInfo) bool {
	extension := filepath.Ext(bi.Name())
	if extension == "" {
		return true
	}
	for _, executable := range executableExtensions() {
		if strings.EqualFold(extension, executable) {
			return false
		}
	}
	return true
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
