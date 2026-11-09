//go:build windows

package embedding

import (
	"os"
	"path/filepath"
	"strings"
)

// modeIsExecutable is the Windows meaning of the Unix predicate
// `info.Mode().Perm()&0o111 != 0` in isExecutableFile (ADR-092 fact 3; ADR-091
// R-b).
//
// Windows has no execute permission bit, and Go reports Windows file modes as
// 0o666 or 0o444 only, so the Unix predicate is false for every file and a
// sidecar named by HAFT_EMBED_BIN or found in a managed location would never
// be accepted. What makes a file executable by name on Windows is its
// extension: CreateProcess and the shell run the extensions listed in
// PATHEXT. This is the rule Go's os/exec LookPath applies on Windows: PATHEXT
// split at ';', lower-cased, a missing leading dot added, and .com, .exe, .bat
// and .cmd when PATHEXT is unset; the comparison ignores case.
func modeIsExecutable(info os.FileInfo) bool {
	extension := filepath.Ext(info.Name())
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

// sidecarExecutablePath names the sidecar file at path. Windows runs a file by
// its extension (PATHEXT), and the Rust build writes haft-embed.exe, so the
// managed and development candidates, which the Unix code names without an
// extension, get the .exe that makes them found.
func sidecarExecutablePath(path string) string {
	return path + ".exe"
}
