//go:build windows

package initfs

import (
	"os"
	"path/filepath"
	"testing"
)

// The modes the Unix predicate accepts (a stage created 0600, then chmod
// 0644) are reported as 0666 on Windows and accepted; a read-only stage is
// not the image of either and is rejected.
func TestCanonicalCarrierStageModeSafeOnWindows(t *testing.T) {
	directory := t.TempDir()
	stage := filepath.Join(directory, "stage")
	file, err := os.OpenFile(stage, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0o600, 0o644} {
		if err := os.Chmod(stage, mode); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(stage)
		if err != nil {
			t.Fatal(err)
		}
		if !canonicalCarrierStageModeSafe(info) {
			t.Fatalf("stage chmod %04o (reported %04o) was rejected", mode, info.Mode().Perm())
		}
	}
	if err := os.Chmod(stage, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stage, 0o600) })
	info, err := os.Lstat(stage)
	if err != nil {
		t.Fatal(err)
	}
	if canonicalCarrierStageModeSafe(info) {
		t.Fatalf("read-only stage (reported %04o) was accepted", info.Mode().Perm())
	}
}
