//go:build windows

package initfs

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/m0n0x41d/haft/internal/winfs"
	"golang.org/x/sys/windows"
)

const (
	manifestLockHelperPathEnv = "HAFT_INITFS_MANIFEST_LOCK_HELPER_PATH"
	manifestLockHelperReady   = "INITFS_MANIFEST_LOCK_HELPER_READY"
	manifestLockHelperFailed  = "INITFS_MANIFEST_LOCK_HELPER_FAILED"
)

// manifestLockHelperHeld keeps the helper's lock reachable until the process
// exits; the helper never releases it, so only the kernel's cleanup at
// process exit does.
var manifestLockHelperHeld *manifestLock

// TestManifestLockHelperProcess is the body of the helper process that the
// two-process test starts by re-executing the test binary. It acquires the
// manifest lock, reports ready and holds the lock until its stdin closes.
func TestManifestLockHelperProcess(t *testing.T) {
	path := os.Getenv(manifestLockHelperPathEnv)
	if path == "" {
		t.Skip("entry point for re-executed helper processes; runs only with " + manifestLockHelperPathEnv + " set")
	}
	lock, acquired, err := tryAcquireManifestLock(path)
	if err != nil || !acquired {
		fmt.Println(manifestLockHelperFailed, acquired, err)
		return
	}
	manifestLockHelperHeld = lock
	fmt.Println(manifestLockHelperReady)
	_, _ = io.Copy(io.Discard, os.Stdin)
}

// startManifestLockHelper starts a helper process that holds the manifest
// lock at path and waits until it reports ready. The returned function closes
// the helper's stdin and waits for it to exit without releasing the lock.
func startManifestLockHelper(t *testing.T, path string) func() {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestManifestLockHelperProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(), manifestLockHelperPathEnv+"="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("helper stdin: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("helper stdout: %v", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	done := make(chan struct{})
	ready := make(chan error, 1)
	go func() {
		defer close(done)
		reported := false
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case reported:
			case line == manifestLockHelperReady:
				reported = true
				ready <- nil
			case strings.HasPrefix(line, manifestLockHelperFailed):
				reported = true
				ready <- errors.New(line)
			}
		}
		if !reported {
			ready <- errors.New("helper exited before it reported ready")
		}
	}()
	exited := false
	t.Cleanup(func() {
		if !exited {
			_ = cmd.Process.Kill()
			_ = stdin.Close()
			<-done
			_ = cmd.Wait()
		}
	})
	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("manifest lock helper: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("manifest lock helper did not report ready within 60s")
	}
	return func() {
		t.Helper()
		_ = stdin.Close()
		<-done
		exited = true
		if err := cmd.Wait(); err != nil {
			t.Fatalf("manifest lock helper exit: %v", err)
		}
	}
}

// A manifest lock held by another process is busy: (nil, false, nil), the
// result of EWOULDBLOCK in manifest_lock_supported.go, also through
// ManifestStore.TryAcquire. After the holder exits, the lock is free again.
func TestManifestLockReportsBusyAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json.lock")
	exitHelper := startManifestLockHelper(t, path)

	lock, acquired, err := tryAcquireManifestLock(path)
	if err != nil || acquired || lock != nil {
		if lock != nil {
			_ = lock.release()
		}
		t.Fatalf("tryAcquireManifestLock under contention = %v, %t, %v; want nil, false, nil", lock, acquired, err)
	}

	exitHelper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		lock, acquired, err = tryAcquireManifestLock(path)
		if err != nil {
			t.Fatalf("tryAcquireManifestLock after the holder exited: %v", err)
		}
		if acquired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("manifest lock was not released when the holder exited")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := lock.release(); err != nil {
		t.Fatalf("release: %v", err)
	}
}

// Two handles in one process exclude each other, as two flock open file
// descriptions do on Unix; release makes the lock available again and is
// idempotent.
func TestManifestLockReportsBusyWithinProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json.lock")
	first, acquired, err := tryAcquireManifestLock(path)
	if err != nil || !acquired {
		t.Fatalf("first tryAcquireManifestLock = %t, %v", acquired, err)
	}
	second, acquired, err := tryAcquireManifestLock(path)
	if err != nil || acquired || second != nil {
		t.Fatalf("second tryAcquireManifestLock = %v, %t, %v; want nil, false, nil", second, acquired, err)
	}
	if err := first.release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := first.release(); err != nil {
		t.Fatalf("second release: %v", err)
	}
	third, acquired, err := tryAcquireManifestLock(path)
	if err != nil || !acquired {
		t.Fatalf("tryAcquireManifestLock after release = %t, %v", acquired, err)
	}
	_ = third.release()
}

// The Windows meaning of the Unix 0600/owner/nlink check (ADR-092 O2, 2a): a
// lock file created by tryAcquireManifestLock is private (token user owner,
// protected owner-only DACL, one link).
func TestManifestLockFileIsPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json.lock")
	lock, acquired, err := tryAcquireManifestLock(path)
	if err != nil || !acquired {
		t.Fatalf("tryAcquireManifestLock = %t, %v", acquired, err)
	}
	if err := lock.release(); err != nil {
		t.Fatal(err)
	}
	file, err := winfs.OpenFile(path, os.O_RDONLY, winfs.Inherited)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := winfs.CheckPrivate(file); err != nil {
		t.Fatalf("manifest lock file is not private: %v", err)
	}
}

// An existing lock file that is not private (created with inherited security,
// the Windows counterpart of a 0644 file) is rejected, fail closed, before it
// is locked.
func TestManifestLockRejectsInheritedSecurityCarrier(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json.lock")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	lock, acquired, err := tryAcquireManifestLock(path)
	if err == nil || acquired || lock != nil {
		t.Fatalf("inherited-security lock = %v, %t, %v; want an error", lock, acquired, err)
	}
	if !strings.Contains(err.Error(), "held manifest lock ownership or mode is unsafe") ||
		!errors.Is(err, winfs.ErrNotPrivate) {
		t.Fatalf("inherited-security lock error = %v", err)
	}
}

// A junction at the lock path is not followed (O_NOFOLLOW).
func TestManifestLockRejectsJunction(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "manifest.json.lock")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", path, target).CombinedOutput(); err != nil {
		t.Skipf("create junction: %v: %s", err, out)
	}
	lock, acquired, err := tryAcquireManifestLock(path)
	if err == nil || acquired || lock != nil {
		t.Fatalf("junction lock = %v, %t, %v; want an error", lock, acquired, err)
	}
	if !strings.Contains(err.Error(), "open manifest lock without following symlinks") {
		t.Fatalf("junction lock error = %v", err)
	}
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		t.Fatalf("junction lock reported as contention: %v", err)
	}
}
