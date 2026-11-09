//go:build !windows

package agenthostrestart

import (
	"fmt"
	"os"
	"path/filepath"
)

func ensureExecutablePath(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s is not executable", filepath.Clean(path))
	}
	return nil
}
