package winfs

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// ValidateName accepts exactly one path component: not empty, "." or "..", and
// without "\", "/" (separators), ":" (alternate data streams) or NUL. It also
// rejects names that Win32 path APIs resolve differently from the NT API used
// here: a trailing "." or " " (Win32 strips it) and DOS device names (CON,
// PRN, AUX, NUL, COM0-9, LPT0-9 and the superscript digits, CONIN$, CONOUT$),
// with or without an extension, as Go's filepath.IsLocal and os.Root do.
// Every handle-relative operation applies it, so a name can never walk out of
// the held directory or create an entry other tools cannot reach.
func ValidateName(name string) error {
	switch name {
	case "", ".", "..":
		return ErrInvalidName
	}
	if strings.ContainsAny(name, "\\/:\x00") {
		return ErrInvalidName
	}
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return ErrInvalidName
	}
	if isReservedName(name) {
		return ErrInvalidName
	}
	return nil
}

// isReservedName reports a DOS device name, ignoring an extension and the
// spaces before it. Go's filepathlite asks RtlIsDosDeviceName_U whether a name
// with an extension is still reserved (Windows 11 no longer reserves
// "CON.txt"); winfs rejects it on every version, which is stricter and does
// not depend on the Windows release.
func isReservedName(name string) bool {
	base := name
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		base = base[:dot]
	}
	return isReservedBaseName(asciiUpper(strings.TrimRight(base, " ")))
}

func isReservedBaseName(upper string) bool {
	switch upper {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return true
	}
	if len(upper) < 4 || (upper[:3] != "COM" && upper[:3] != "LPT") {
		return false
	}
	switch upper[3:] {
	case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³":
		return true
	}
	return false
}

// asciiUpper upper-cases ASCII letters only, as Windows device-name matching
// does; Unicode case mapping would turn, for example, a dotless i into "I".
func asciiUpper(text string) string {
	upper := []byte(text)
	for index, char := range upper {
		if 'a' <= char && char <= 'z' {
			upper[index] = char - ('a' - 'A')
		}
	}
	return string(upper)
}

// extendedPath prefixes a long absolute path with \\?\ (or \\?\UNC\) so that
// CreateFile and MoveFileEx accept it without the MAX_PATH limit. Such paths
// skip Win32 normalization, so the path is cleaned first.
func extendedPath(path string) string {
	const directoryLimit = 248 // MAX_PATH minus room for an 8.3 name
	if len(path) < directoryLimit || !filepath.IsAbs(path) {
		return path
	}
	if strings.HasPrefix(path, `\\?\`) || strings.HasPrefix(path, `\\.\`) {
		return path
	}
	clean := filepath.Clean(path)
	if strings.HasPrefix(clean, `\\`) {
		return `\\?\UNC\` + clean[2:]
	}
	return `\\?\` + clean
}

// createFile is windows.CreateFile for a Go path string.
func createFile(
	path string,
	access uint32,
	share uint32,
	disposition uint32,
	flags uint32,
	attributes *windows.SecurityAttributes,
) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(extendedPath(path))
	if err != nil {
		return windows.InvalidHandle, err
	}
	return windows.CreateFile(name, access, share, attributes, disposition, flags, 0)
}

func requireAbsolute(path string) error {
	if !filepath.IsAbs(path) {
		return ErrRelativePath
	}
	return nil
}
