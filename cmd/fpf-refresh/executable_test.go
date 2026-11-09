package main

import (
	"path/filepath"
	"runtime"
	"testing"
)

// TestIndexerExecutablePathIsIdentityOutsideWindows: Linux and macOS build the
// indexer to exactly the base path; only Windows adds the .exe extension that
// PATHEXT needs to run it.
func TestIndexerExecutablePathIsIdentityOutsideWindows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "haft-fpf-indexer")
	want := path
	if runtime.GOOS == "windows" {
		want = path + ".exe"
	}
	if got := indexerExecutablePath(path); got != want {
		t.Fatalf("indexerExecutablePath(%q) = %q, want %q", path, got, want)
	}
}
