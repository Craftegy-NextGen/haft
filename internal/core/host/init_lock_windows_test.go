//go:build windows

package host

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
)

const (
	initLockHelperRootEnv = "HAFT_HOST_INIT_LOCK_HELPER_ROOT"
	initLockHelperReady   = "HOST_INIT_LOCK_HELPER_READY"
	initLockHelperFailed  = "HOST_INIT_LOCK_HELPER_FAILED"
	initLockBusyMessage   = "host_init_busy: another init holds the project lock"
)

// initLockHelperRelease keeps the helper's release function reachable until
// the process exits; the helper never calls it, so only the kernel's cleanup
// at process exit releases the lock.
var initLockHelperRelease func()

// TestInitLockHelperProcess is the body of the helper process that the
// two-process tests start by re-executing the test binary. It takes the
// project-root lock, flushes the locked root through winfs, reports ready
// with the flush outcome, and holds the lock until its stdin closes.
func TestInitLockHelperProcess(t *testing.T) {
	root := os.Getenv(initLockHelperRootEnv)
	if root == "" {
		t.Skip("entry point for re-executed helper processes; runs only with " + initLockHelperRootEnv + " set")
	}
	release, err := lockInitRoot(root)
	if err != nil {
		fmt.Println(initLockHelperFailed, err)
		return
	}
	initLockHelperRelease = release
	outcome, err := winfs.SyncDirectory(root)
	if err != nil {
		fmt.Println(initLockHelperFailed, "holder flush:", err)
		return
	}
	fmt.Println(initLockHelperReady, int(outcome))
	_, _ = io.Copy(io.Discard, os.Stdin)
}

type initLockHelper struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	done    chan struct{}
	outcome winfs.SyncOutcome
	exited  bool
}

// startInitLockHelper starts a helper process that holds the project-root
// lock on root and waits until it reports ready.
func startInitLockHelper(t *testing.T, root string) *initLockHelper {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestInitLockHelperProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(), initLockHelperRootEnv+"="+root)
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
	helper := &initLockHelper{cmd: cmd, stdin: stdin, done: make(chan struct{})}
	ready := make(chan error, 1)
	go func() {
		defer close(helper.done)
		reported := false
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if reported {
				continue
			}
			switch {
			case strings.HasPrefix(line, initLockHelperReady+" "):
				var outcome int
				_, err := fmt.Sscan(strings.TrimPrefix(line, initLockHelperReady+" "), &outcome)
				helper.outcome = winfs.SyncOutcome(outcome)
				reported = true
				ready <- err
			case strings.HasPrefix(line, initLockHelperFailed):
				reported = true
				ready <- errors.New(line)
			}
		}
		if !reported {
			ready <- errors.New("helper exited before it reported ready")
		}
	}()
	t.Cleanup(func() {
		if !helper.exited {
			_ = cmd.Process.Kill()
			_ = stdin.Close()
			<-helper.done
			_ = cmd.Wait()
		}
	})
	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("init lock helper: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("init lock helper did not report ready within 60s")
	}
	return helper
}

// exit closes the helper's stdin; it returns from its test without releasing
// the lock, so the kernel releases it when the process exits.
func (helper *initLockHelper) exit(t *testing.T) {
	t.Helper()
	_ = helper.stdin.Close()
	<-helper.done
	helper.exited = true
	if err := helper.cmd.Wait(); err != nil {
		t.Fatalf("init lock helper exit: %v", err)
	}
}

// kill terminates the helper (TerminateProcess) while it holds the lock.
func (helper *initLockHelper) kill(t *testing.T) {
	t.Helper()
	if err := helper.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill init lock helper: %v", err)
	}
	_ = helper.stdin.Close()
	<-helper.done
	helper.exited = true
	_ = helper.cmd.Wait()
}

// lockAfterRelease retries lockInitRoot until the lock released at the
// helper's exit is available; Microsoft documents that release as prompt but
// not immediate.
func lockAfterRelease(t *testing.T, root string) func() {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		release, err := lockInitRoot(root)
		if err == nil {
			return release
		}
		if time.Now().After(deadline) {
			t.Fatalf("lockInitRoot after the holder exited: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func lockTestRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// The project-root lock held by another process yields the busy result of
// init_lock_unix.go (EWOULDBLOCK), both from lockInitRoot and from Init, and
// Init then writes nothing. After the holder exits without unlocking, the
// kernel has released the lock.
func TestInitLockReportsBusyAcrossProcesses(t *testing.T) {
	root := lockTestRoot(t)
	helper := startInitLockHelper(t, root)

	release, err := lockInitRoot(root)
	if err == nil {
		release()
		t.Fatal("lockInitRoot succeeded while another process holds the project lock")
	}
	if err.Error() != initLockBusyMessage {
		t.Fatalf("lockInitRoot under contention = %v, want %q", err, initLockBusyMessage)
	}
	result, err := Init(Config{Root: root})
	if err == nil || err.Error() != initLockBusyMessage {
		t.Fatalf("Init under contention = %v, want %q", err, initLockBusyMessage)
	}
	if len(result.Created)+len(result.Updated)+len(result.Unchanged) != 0 {
		t.Fatalf("Init under contention reported effects: %+v", result)
	}
	if _, err := os.Lstat(filepath.Join(root, "AGENTS.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Init under contention wrote AGENTS.md: %v", err)
	}

	helper.exit(t)
	lockAfterRelease(t, root)()
}

// Init publishes AGENTS.md into the project root while it holds the root
// lock, by a hard link (new file) or a rename (existing file) into the locked
// directory. The share-mode lock of option 4a refused both (measured in WS7);
// the identity lock (4c) leaves the root usable, so Init creates AGENTS.md
// and a second Init finds it unchanged (upstream contract, also covered with
// --codex by TestInitPreservesConfigAndIsIdempotent).
func TestInitPublishesIntoItsLockedRoot(t *testing.T) {
	root := lockTestRoot(t)
	result, err := Init(Config{Root: root})
	if err != nil || len(result.Created) != 1 || result.Created[0] != "AGENTS.md" {
		t.Fatalf("Init = %+v, %v; want AGENTS.md created", result, err)
	}
	result, err = Init(Config{Root: root})
	if err != nil || len(result.Unchanged) != 1 || result.Unchanged[0] != "AGENTS.md" {
		t.Fatalf("second Init = %+v, %v; want AGENTS.md unchanged", result, err)
	}
}

// While another process holds the project-root lock, this process can flush
// the root (winfs.SyncDirectory and an explicit write open for sync), as
// under flock on Unix. The share-mode lock of option 4a refused this with
// ERROR_SHARING_VIOLATION (the Weaker-documented finding that the amended O4
// removes). The holder flushes the root too.
func TestInitLockAllowsDirectoryFlushFromAnotherProcess(t *testing.T) {
	root := lockTestRoot(t)
	helper := startInitLockHelper(t, root)
	if helper.outcome != winfs.SyncFlushed {
		t.Fatalf("holder flush outcome = %d, want SyncFlushed (%d)", helper.outcome, winfs.SyncFlushed)
	}

	outcome, err := winfs.SyncDirectory(root)
	if err != nil || outcome != winfs.SyncFlushed {
		t.Fatalf("flush of the locked root from another process = %d, %v; want SyncFlushed", outcome, err)
	}
	file, err := winfs.OpenDirectoryForSync(root)
	if err != nil {
		t.Fatalf("write open of the locked root from another process: %v", err)
	}
	if err := file.Sync(); err != nil {
		t.Fatalf("flush through the write open: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if release, err := lockInitRoot(root); err == nil || err.Error() != initLockBusyMessage {
		if release != nil {
			release()
		}
		t.Fatalf("lockInitRoot after the flushes = %v, want %q", err, initLockBusyMessage)
	}
	helper.exit(t)
}

// A holder that is killed (TerminateProcess) releases the lock, as a killed
// process releases its flock on Unix.
func TestInitLockAvailableAfterHolderIsKilled(t *testing.T) {
	root := lockTestRoot(t)
	helper := startInitLockHelper(t, root)
	if release, err := lockInitRoot(root); err == nil || err.Error() != initLockBusyMessage {
		if release != nil {
			release()
		}
		t.Fatalf("lockInitRoot while held = %v, want %q", err, initLockBusyMessage)
	}
	helper.kill(t)
	lockAfterRelease(t, root)()
	result, err := Init(Config{Root: root})
	if err != nil || len(result.Created) != 1 || result.Created[0] != "AGENTS.md" {
		t.Fatalf("Init after the holder was killed = %+v, %v", result, err)
	}
}

// Two cooperating init calls in one process are serialized too, as two
// flock open file descriptions are on Unix.
func TestInitLockReportsBusyWithinProcess(t *testing.T) {
	root := lockTestRoot(t)
	release, err := lockInitRoot(root)
	if err != nil {
		t.Fatalf("first lockInitRoot: %v", err)
	}
	if second, err := lockInitRoot(root); err == nil || err.Error() != initLockBusyMessage {
		if second != nil {
			second()
		}
		t.Fatalf("second lockInitRoot = %v, want %q", err, initLockBusyMessage)
	}
	release()
	again, err := lockInitRoot(root)
	if err != nil {
		t.Fatalf("lockInitRoot after release: %v", err)
	}
	again()
}

// The lock writes nothing into the project root (init_lock_unix.go: "Locking
// the existing project directory needs no preflight-side write") and leaves
// it readable and writable for the holder's own publication.
func TestInitLockWritesNothing(t *testing.T) {
	root := lockTestRoot(t)
	release, err := lockInitRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("locked root entries = %v, %v; want empty", entries, err)
	}
	if err := os.WriteFile(filepath.Join(root, "probe"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write inside the locked root: %v", err)
	}
}

// A root that is not an existing directory is an error, not busy.
func TestInitLockRejectsMissingRoot(t *testing.T) {
	missing := filepath.Join(lockTestRoot(t), "missing")
	release, err := lockInitRoot(missing)
	if err == nil {
		release()
		t.Fatal("lockInitRoot of a missing root succeeded")
	}
	if strings.HasPrefix(err.Error(), "host_init_busy") || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lockInitRoot of a missing root = %v, want a not-exist error", err)
	}
}
