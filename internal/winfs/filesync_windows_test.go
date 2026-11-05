package winfs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// OpenFileForSync gives shared code a file whose Sync flushes, where the
// read-only handle of os.Open is refused (the projectledgermigration snapshot
// failure). The contents are untouched.
func TestOpenFileForSyncFlushes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot")
	if err := os.WriteFile(path, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	readOnly, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	syncErr := readOnly.Sync()
	_ = readOnly.Close()
	if !errors.Is(syncErr, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("Sync on the os.Open handle = %v, want ERROR_ACCESS_DENIED (the reason for OpenFileForSync)", syncErr)
	}
	file, err := OpenFileForSync(path)
	if err != nil {
		t.Fatalf("OpenFileForSync: %v", err)
	}
	if file.Name() != path {
		t.Fatalf("Name() = %q, want %q", file.Name(), path)
	}
	if err := file.Sync(); err != nil {
		t.Fatalf("Sync on OpenFileForSync = %v, want nil", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := readFile(t, path); got != "content" {
		t.Fatalf("content after sync = %q", got)
	}
}

func TestOpenFileForSyncErrors(t *testing.T) {
	var pathErr *os.PathError
	_, err := OpenFileForSync(filepath.Join("relative", "file"))
	if !errors.Is(err, ErrRelativePath) || !errors.As(err, &pathErr) || pathErr.Op != "open" {
		t.Fatalf("OpenFileForSync(relative) = %v, want *os.PathError open wrapping ErrRelativePath", err)
	}
	_, err = OpenFileForSync(filepath.Join(t.TempDir(), "missing"))
	if !errors.Is(err, fs.ErrNotExist) || !errors.As(err, &pathErr) {
		t.Fatalf("OpenFileForSync(missing) = %v, want *os.PathError wrapping fs.ErrNotExist", err)
	}
	readOnly := filepath.Join(t.TempDir(), "read-only")
	if err := os.WriteFile(readOnly, []byte("x"), 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnly, 0o600) })
	if _, err := OpenFileForSync(readOnly); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("OpenFileForSync(read-only attribute) = %v, want a permission error", err)
	}
}
