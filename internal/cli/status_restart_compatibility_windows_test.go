package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/m0n0x41d/haft/internal/winfs"
)

// Windows siblings of createPrivateTestDirectory and writePrivateTestFile in
// status_restart_compatibility_unix_test.go (ADR-093 E3). The Windows restart
// store accepts only an owner-only directory and owner-only files (ADR-092 O2),
// and a plain os.Mkdir or os.WriteFile inherits the folder ACL, so both are
// created through winfs.Private.

func createPrivateTestDirectory(path string) error {
	parent, err := winfs.OpenDir(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Mkdir(filepath.Base(path), winfs.Private)
}

func writePrivateTestFile(t *testing.T, path string, content []byte) {
	t.Helper()
	file, err := winfs.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, winfs.Private)
	if err != nil {
		t.Fatalf("write %s: %v", filepath.Base(path), err)
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		t.Fatalf("write %s: %v", filepath.Base(path), err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("write %s: %v", filepath.Base(path), err)
	}
}
