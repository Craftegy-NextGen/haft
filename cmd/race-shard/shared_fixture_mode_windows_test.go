package main

import (
	"os"
	"testing"

	"github.com/m0n0x41d/haft/internal/winfs"
)

// sharedFixtureRootNotPrivate is the Windows sibling of the Unix mode check
// extracted from TestRaceSharedFixtureDirectoryIsPrivateAndRunnerOwned. Go
// reports 0777 for every directory on Windows, so privacy is the explicit
// owner and protected owner-only DACL of winfs.Private (ADR-092 O2, 2a).
func sharedFixtureRootNotPrivate(info os.FileInfo, root string) bool {
	directory, err := os.Open(root)
	if err != nil {
		return true
	}
	defer directory.Close()
	return winfs.CheckPrivate(directory) != nil
}

func TestWindowsFixtureDirectoryIsOwnerOnlyAndUnique(t *testing.T) {
	base := t.TempDir()
	first, err := createFixtureDirectory(base, "fixture-*-x")
	if err != nil {
		t.Fatal(err)
	}
	second, err := createFixtureDirectory(base, "fixture-*-x")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("two fixture directories share the path %s", first)
	}
	for _, path := range []string{first, second} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() {
			t.Fatalf("fixture directory %s: %v, %v", path, info, err)
		}
		if sharedFixtureRootNotPrivate(info, path) {
			t.Fatalf("fixture directory %s is not owner-only", path)
		}
	}
}
