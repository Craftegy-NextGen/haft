package winfs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestSyncDirectoryFlushes(t *testing.T) {
	dir, path := openTempDir(t)
	writeFile(t, dir, "entry", "x")
	outcome, err := SyncDirectory(path)
	if err != nil || outcome != SyncFlushed {
		t.Fatalf("SyncDirectory = %d, %v; want SyncFlushed (probe P-c)", outcome, err)
	}
	outcome, err = dir.Sync()
	if err != nil || outcome != SyncFlushed {
		t.Fatalf("Dir.Sync = %d, %v; want SyncFlushed", outcome, err)
	}
}

func TestSyncDirectoryMissing(t *testing.T) {
	_, err := SyncDirectory(filepath.Join(t.TempDir(), "missing"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("SyncDirectory(missing) = %v, want fs.ErrNotExist", err)
	}
}

// OpenDirectoryForSync gives shared code a directory file whose Sync flushes,
// where the read-only handle of os.Open fails (probe P-c, the R2 failure).
func TestOpenDirectoryForSyncFlushes(t *testing.T) {
	dir, path := openTempDir(t)
	writeFile(t, dir, "entry", "x")
	directory, err := OpenDirectoryForSync(path)
	if err != nil {
		t.Fatalf("OpenDirectoryForSync: %v", err)
	}
	if directory.Name() != path {
		t.Fatalf("Name() = %q, want %q", directory.Name(), path)
	}
	if err := directory.Sync(); err != nil {
		t.Fatalf("Sync on OpenDirectoryForSync = %v, want nil", err)
	}
	if err := directory.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestOpenDirectoryForSyncErrors(t *testing.T) {
	var pathErr *os.PathError
	_, err := OpenDirectoryForSync(filepath.Join("relative", "dir"))
	if !errors.Is(err, ErrRelativePath) || !errors.As(err, &pathErr) || pathErr.Op != "open" {
		t.Fatalf("OpenDirectoryForSync(relative) = %v, want *os.PathError open wrapping ErrRelativePath", err)
	}
	_, err = OpenDirectoryForSync(filepath.Join(t.TempDir(), "missing"))
	if !errors.Is(err, fs.ErrNotExist) || !errors.As(err, &pathErr) {
		t.Fatalf("OpenDirectoryForSync(missing) = %v, want *os.PathError wrapping fs.ErrNotExist", err)
	}
}

// No fallback is hidden here: a directory under a DirLock cannot be opened
// for write, in the holding process too, and the error is returned.
func TestOpenDirectoryForSyncUnderDirLockFails(t *testing.T) {
	path := t.TempDir()
	lock, err := TryLockDir(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Unlock()
	directory, err := OpenDirectoryForSync(path)
	if err == nil {
		_ = directory.Close()
	}
	if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("OpenDirectoryForSync under a DirLock = %v, want ERROR_SHARING_VIOLATION", err)
	}
}

func TestSyncResultFallbackIsCountedAndNarrow(t *testing.T) {
	before := SkippedDirectorySyncs()
	for _, code := range []error{windows.ERROR_ACCESS_DENIED, windows.ERROR_INVALID_FUNCTION} {
		outcome, err := syncResult(code)
		if err != nil || outcome != SyncSkipped {
			t.Fatalf("syncResult(%v) = %d, %v; want SyncSkipped", code, outcome, err)
		}
	}
	if got := SkippedDirectorySyncs() - before; got != 2 {
		t.Fatalf("skipped syncs counted %d, want 2", got)
	}
	for _, code := range []error{windows.ERROR_SHARING_VIOLATION, windows.ERROR_NOT_READY, windows.ERROR_DISK_FULL} {
		_, err := syncResult(code)
		if !errors.Is(err, code) {
			t.Fatalf("syncResult(%v) = %v; other errors must be returned", code, err)
		}
	}
	if outcome, err := syncResult(nil); err != nil || outcome != SyncFlushed {
		t.Fatalf("syncResult(nil) = %d, %v", outcome, err)
	}
}

// A DirLock refuses every other write open of its directory, so a sync in the
// holding process must go through the lock's handle.
func TestSyncThroughOwnDirLock(t *testing.T) {
	dir, path := openTempDir(t)
	lock, err := TryLockDir(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Unlock()
	outcome, err := SyncDirectory(path)
	if err != nil || outcome != SyncFlushed {
		t.Fatalf("SyncDirectory under own lock = %d, %v; want SyncFlushed", outcome, err)
	}
	outcome, err = dir.Sync()
	if err != nil || outcome != SyncFlushed {
		t.Fatalf("Dir.Sync under own lock = %d, %v; want SyncFlushed", outcome, err)
	}
}

// Documented weaker behaviour of option 4a: while another process holds the
// directory lock, this process cannot flush the directory.
func TestSyncUnderForeignDirLockFails(t *testing.T) {
	path := t.TempDir()
	helper := startHelper(t, "dir", path)
	_, err := SyncDirectory(path)
	if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("SyncDirectory under a foreign lock = %v, want ERROR_SHARING_VIOLATION", err)
	}
	if err := helper.exit(); err != nil {
		t.Fatalf("helper exit: %v", err)
	}
	if _, err := eventually(5*time.Second, func() error {
		_, err := SyncDirectory(path)
		return err
	}); err != nil {
		t.Fatalf("SyncDirectory after the holder exited: %v", err)
	}
}
