//go:build windows

package fpfrefresh

import (
	"os"
	"testing"

	"github.com/m0n0x41d/haft/internal/winfs"
)

// lockEffectsQueryDatabaseCache is the Windows counterpart of the function in
// repository_unix_test.go (ADR-093 E3): flock(LOCK_EX) on the cache lock file
// becomes a blocking winfs.LockFile over the whole content-free file.
func lockEffectsQueryDatabaseCache(t *testing.T, path string) func() {
	t.Helper()
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := winfs.LockFile(lock, winfs.ExclusiveLock); err != nil {
		_ = lock.Close()
		t.Fatal(err)
	}
	return func() {
		if err := winfs.UnlockFile(lock); err != nil {
			t.Errorf("unlock shared query fixture: %v", err)
		}
		if err := lock.Close(); err != nil {
			t.Errorf("close shared query fixture lock: %v", err)
		}
	}
}
