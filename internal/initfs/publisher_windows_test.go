//go:build windows

package initfs

import (
	"os"
	"path/filepath"
	"testing"
)

// assertPublishedContent is the Windows equivalent (ADR-093 E3) of the helper
// in publisher_unix_test.go. The published mode 0o644 has the owner-write
// bit, which Go's Windows Chmod maps to a cleared read-only attribute, and
// Go's Windows Stat reports that as 0o666: the Windows image of 0o644.
func assertPublishedContent(
	t *testing.T,
	root string,
	content map[string][]byte,
) {
	t.Helper()
	for relative, want := range content {
		path := filepath.Join(root, filepath.FromSlash(relative))
		assertFileBytes(t, path, want)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat published path %s: %v", path, err)
		}
		if info.Mode().Perm() != 0o666 {
			t.Fatalf("published path %s mode = %o, want 0666 (Windows image of 0644)", path, info.Mode().Perm())
		}
	}
}
