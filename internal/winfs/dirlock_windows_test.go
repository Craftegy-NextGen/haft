package winfs

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestTryLockDirExcludesSecondLock(t *testing.T) {
	path := t.TempDir()
	lock, err := TryLockDir(path)
	if err != nil {
		t.Fatalf("TryLockDir: %v", err)
	}
	_, err = TryLockDir(path)
	if !errors.Is(err, ErrBusy) || !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("second TryLockDir = %v, want ErrBusy", err)
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || pathErr.Path != path {
		t.Fatalf("error %v must be an *os.PathError naming the directory", err)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if err := lock.Unlock(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("second Unlock = %v, want os.ErrClosed", err)
	}
	again, err := TryLockDir(path)
	if err != nil {
		t.Fatalf("TryLockDir after Unlock: %v", err)
	}
	_ = again.Unlock()
}

// The lock writes nothing and leaves ordinary use of the directory alone.
func TestDirLockLeavesDirectoryUsable(t *testing.T) {
	path := t.TempDir()
	lock, err := TryLockDir(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Unlock()
	if entries, err := os.ReadDir(path); err != nil || len(entries) != 0 {
		t.Fatalf("locked directory must stay empty and readable: %v, %v", entries, err)
	}
	if err := os.WriteFile(filepath.Join(path, "file"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write inside the locked directory: %v", err)
	}
	dir, err := OpenDir(path)
	if err != nil {
		t.Fatalf("OpenDir of the locked directory: %v", err)
	}
	writeFile(t, dir, "held", "x")
	_ = dir.Close()
}

func TestTryLockDirRejects(t *testing.T) {
	root := t.TempDir()
	if _, err := TryLockDir("relative"); !errors.Is(err, ErrRelativePath) {
		t.Fatalf("relative = %v, want ErrRelativePath", err)
	}
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	createJunction(t, filepath.Join(root, "junction"), target)
	if _, err := TryLockDir(filepath.Join(root, "junction")); !errors.Is(err, syscall.ELOOP) {
		t.Fatalf("junction = %v, want ELOOP", err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := TryLockDir(file); !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("file = %v, want ENOTDIR", err)
	}
	if _, err := TryLockDir(filepath.Join(root, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing = %v, want os.ErrNotExist", err)
	}
}

// Two processes: the helper holds the directory lock, this process sees busy,
// and the lock becomes available after the helper exits without unlocking.
func TestDirLockTwoProcesses(t *testing.T) {
	for _, ending := range []string{"exit", "kill"} {
		t.Run(ending, func(t *testing.T) {
			path := t.TempDir()
			helper := startHelper(t, "dir", path)
			if _, err := TryLockDir(path); !errors.Is(err, ErrBusy) {
				t.Fatalf("TryLockDir while another process holds it = %v, want ErrBusy", err)
			}
			endHelper(t, helper, ending)
			var lock *DirLock
			elapsed, err := eventually(5*time.Second, func() error {
				var err error
				lock, err = TryLockDir(path)
				return err
			})
			if err != nil {
				t.Fatalf("directory lock not released %v after the holder ended: %v", elapsed, err)
			}
			t.Logf("directory lock acquirable %v after the holder ended by %s", elapsed, ending)
			_ = lock.Unlock()
		})
	}
}
