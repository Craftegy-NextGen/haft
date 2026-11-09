//go:build windows

package p14acceptance

import (
	"fmt"
	"io/fs"
	"os"
)

// Windows half of the ADR-093 E3 split of init_matrix_content_validation_test.go
// (see init_regular_file_unix_test.go). The callers compare the reported mode
// with 0o644, the image of what init writes with os.WriteFile(path, content,
// 0o644) and of the rendered output mode.
//
// On Windows, Go derives the permission bits of an os.FileInfo from the
// read-only attribute alone: 0o444 when it is set, 0o666 otherwise
// (ADR-092 fact 3; the same image internal/projectledgermigration uses in
// snapshot_mode_windows.go). A file that init wrote with owner-write is
// therefore reported as 0o666 and could never equal 0o644. The twin reports
// the Unix image of that state: owner-writable maps to 0o644, read-only to
// 0o444.
//
// This is a weaker check than on Unix and the weakness is deliberate: group
// and other write bits do not exist on Windows, and the DACL is not read here
// (the call sites hold no handle, so winfs.CheckPrivate cannot run). What the
// callers still prove on Windows is that the file is regular, has the exact
// bytes and is not read-only; a wrong mode on Unix such as 0o600 or 0o755 is
// not detectable on Windows.

func readP14InitRegularFile(path string) ([]byte, fs.FileMode, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, 0, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("%s is not a regular file", path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	return content, windowsP14InitModeImage(info.Mode().Perm()), nil
}

// windowsP14InitModeImage is the Unix image of the permission bits that Go
// reports on Windows: 0o444 stays read-only, 0o666 (owner-writable) becomes
// 0o644.
func windowsP14InitModeImage(perm fs.FileMode) fs.FileMode {
	if perm&0o200 != 0 {
		return 0o644
	}
	return 0o444
}
