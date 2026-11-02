package store

import (
	"os"
	"path/filepath"
	"testing"
)

func openWriterLockForTest(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func mustTryLock(t *testing.T, f *os.File, write bool, want bool) {
	t.Helper()
	acquired, err := tryLock(f, write)
	if err != nil || acquired != want {
		t.Fatalf("tryLock(write=%v) = %v, %v; want %v, nil", write, acquired, err, want)
	}
}

// The Windows writer lock keeps the flock semantics of lock_unix.go: shared
// locks coexist, a lock held through another handle is busy (false, nil)
// rather than an error, relocking in the held mode succeeds, unlocking a
// handle that holds no lock succeeds, and closing a handle releases its lock.
func TestWindowsWriterLockKeepsFlockSemantics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "writer.lock")
	first := openWriterLockForTest(t, path)
	second := openWriterLockForTest(t, path)
	third := openWriterLockForTest(t, path)

	if err := unlock(first); err != nil {
		t.Fatalf("unlock without a held lock = %v, want nil as flock(LOCK_UN)", err)
	}
	mustTryLock(t, first, false, true)
	mustTryLock(t, second, false, true)
	mustTryLock(t, third, true, false)
	if err := unlock(first); err != nil {
		t.Fatalf("unlock(first) = %v", err)
	}
	mustTryLock(t, third, true, false)
	if err := unlock(second); err != nil {
		t.Fatalf("unlock(second) = %v", err)
	}
	mustTryLock(t, third, true, true)
	mustTryLock(t, third, true, true)
	mustTryLock(t, first, false, false)
	mustTryLock(t, second, true, false)
	if err := third.Close(); err != nil {
		t.Fatal(err)
	}
	mustTryLock(t, first, true, true)
}
