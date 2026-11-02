//go:build !windows

package initfs

import (
	"os"
	"path/filepath"
	"testing"
)

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
		if info.Mode().Perm() != 0o644 {
			t.Fatalf("published path %s mode = %o", path, info.Mode().Perm())
		}
	}
}
