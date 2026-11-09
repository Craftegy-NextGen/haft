//go:build !windows

package p14acceptance

import (
	"fmt"
	"io/fs"
	"os"
)

// Unix half of the ADR-093 E3 split of init_matrix_content_validation_test.go:
// reading an installed file together with its permission bits, which callers
// compare with 0o644. The function is moved verbatim;
// init_regular_file_windows_test.go holds the Windows twin.

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
	return content, info.Mode().Perm(), nil
}
