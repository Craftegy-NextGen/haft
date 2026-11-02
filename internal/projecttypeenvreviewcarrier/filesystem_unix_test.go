//go:build darwin || linux

package projecttypeenvreviewcarrier

import (
	"os"
	"path/filepath"
	"testing"
)

// installedCarrierNotPrivate is the Unix form of the privacy assertion in
// TestInstallCreatesRereadsAndExactlyReusesCarrier (ADR-093 E3, extracted
// verbatim): a private carrier has mode 0600. Its Windows sibling checks the
// explicit owner and protected owner-only DACL instead (ADR-092 O2).
func installedCarrierNotPrivate(info os.FileInfo, root string) bool {
	return info.Mode().Perm() != 0o600
}

func TestHeldDirectoryRejectsAttachedPathSwap(t *testing.T) {
	root := newProjectRoot(t)
	directory, err := openHeldDirectoryForMutation(root)
	if err != nil {
		t.Fatalf("open held directory: %v", err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	heldPath := filepath.Join(root, ".haft-held")
	if err := os.Rename(filepath.Join(root, DirectoryName), heldPath); err != nil {
		t.Fatalf("move held .haft: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, DirectoryName)); err != nil {
		t.Fatalf("swap .haft path: %v", err)
	}
	if err := directory.lockExclusive(); err == nil {
		t.Fatal("held directory accepted a swapped .haft path")
	}
	if _, err := os.Stat(filepath.Join(outside, FileName)); !os.IsNotExist(err) {
		t.Fatalf("writer escaped through swapped .haft path: %v", err)
	}
}
