//go:build darwin || linux

package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// createPrivateTestDirectory makes the restart directory of the status
// restart compatibility test: mkdir with mode 0700, extracted from the test's
// inline os.Mkdir call (ADR-093 E3). Its Windows sibling creates the directory
// with an owner-only descriptor, which the restart store requires there.
func createPrivateTestDirectory(path string) error {
	return os.Mkdir(path, 0o700)
}

func writePrivateTestFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write %s: %v", filepath.Base(path), err)
	}
}
