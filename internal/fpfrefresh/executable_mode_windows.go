//go:build windows

package fpfrefresh

import (
	"os"
	"path/filepath"
	"strings"
)

// executableModeMissing is the Windows meaning of the Unix predicate
// `info.Mode().Perm()&0o111 == 0` in ExecutableIndexBuilder.BuildIndex and
// verifyCandidateTokenGateFile (ADR-092 fact 3; ADR-091 R-b). It is the rule
// of core/host's candidateBinaryNotExecutable.
//
// Windows has no execute permission bit, and Go reports Windows file modes as
// 0o666 or 0o444 only, so the Unix predicate would reject every indexer and
// shell. What makes a file executable by name on Windows is its extension: the
// ones listed in PATHEXT, the list os/exec applies in LookPath (split at ';',
// lower-cased, a missing leading dot added, and .com, .exe, .bat and .cmd when
// PATHEXT is unset). The comparison ignores case. A script such as .sh is
// therefore not executable by name, and neither is a file without extension.
// The call sites have already required an existing regular file, which is why
// info is unused.
func executableModeMissing(path string, info os.FileInfo) bool {
	extension := filepath.Ext(path)
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
