//go:build windows

package profileprojection

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Windows-only tests of the twin (ADR-093 E4). Junctions need no privilege, so
// they cover the no-follow boundary even where the symbolic-link contract
// tests cannot create their fixtures.

func createProjectionJunction(t *testing.T, link string, target string) {
	t.Helper()
	output, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Fatalf("create junction %s: %v: %s", link, err, output)
	}
}

func TestWindowsProjectionRejectsJunctionedRootAndHaftDirectory(t *testing.T) {
	outside := t.TempDir()
	root := t.TempDir()
	createProjectionJunction(t, filepath.Join(root, ".haft"), outside)
	if _, err := openProjectionDirectory(root); err == nil {
		t.Fatal("projection boundary followed a junctioned .haft directory")
	}
	junctionRoot := filepath.Join(t.TempDir(), "root")
	createProjectionJunction(t, junctionRoot, t.TempDir())
	if _, err := openProjectionDirectory(junctionRoot); err == nil {
		t.Fatal("projection boundary followed a junctioned project root")
	}
	if _, err := os.Stat(filepath.Join(outside, projectionFileName)); !os.IsNotExist(err) {
		t.Fatalf("writer escaped through the junction: %v", err)
	}
}

// The parent-swap contract test with a junction in place of the symbolic link.
func TestWindowsHeldDirectoryIdentityPreventsJunctionSwapWriteEscape(t *testing.T) {
	root := t.TempDir()
	directory, err := openProjectionDirectory(root)
	if err != nil {
		t.Fatalf("open projection directory: %v", err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	if err := os.Rename(filepath.Join(root, ".haft"), filepath.Join(root, ".haft-held")); err != nil {
		t.Fatalf("move held .haft directory: %v", err)
	}
	outside := t.TempDir()
	createProjectionJunction(t, filepath.Join(root, ".haft"), outside)
	if err := directory.writeAtomic([]byte("must-not-escape\n"), "stage-junction-swap"); err == nil {
		t.Fatal("writer accepted a changed .haft parent identity")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("writer escaped through the swapped parent: %v, %v", entries, err)
	}
}
