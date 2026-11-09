//go:build !windows

package fpfrefresh

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func lockEffectsQueryDatabaseCache(t *testing.T, path string) func() {
	t.Helper()
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(
		int(lock.Fd()), // #nosec G115 -- Fd is the descriptor of this open lock file.
		unix.LOCK_EX,
	); err != nil {
		_ = lock.Close()
		t.Fatal(err)
	}
	return func() {
		if err := unix.Flock(
			int(lock.Fd()), // #nosec G115 -- Fd is the descriptor of this open lock file.
			unix.LOCK_UN,
		); err != nil {
			t.Errorf("unlock shared query fixture: %v", err)
		}
		if err := lock.Close(); err != nil {
			t.Errorf("close shared query fixture lock: %v", err)
		}
	}
}
