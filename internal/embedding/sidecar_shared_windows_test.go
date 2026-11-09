//go:build windows

package embedding

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/m0n0x41d/haft/internal/testpath"
	"github.com/m0n0x41d/haft/internal/winfs"
)

// E4 (ADR-093): the Windows body of the shared embedding sidecar (ADR-095
// option A). The fake sidecar and the separate haft processes are the test
// binary itself (sidecar_helper_windows_test.go).

// sharedWindowsEnv points the sidecar at the test binary as its fake haft-embed
// and at a fresh private socket directory, and returns the start log path and
// the socket directory.
func sharedWindowsEnv(t *testing.T) (startLog string, socketDir string) {
	t.Helper()
	testpath.SetHome(t, filepath.Join(t.TempDir(), "home"))
	socketDir = sharedSocketTestDir(t)
	startLog = filepath.Join(t.TempDir(), "starts.log")

	t.Setenv(sidecarBinaryEnv, testExecutable(t))
	t.Setenv(sharedSidecarEnv, "1")
	t.Setenv(sharedSocketDirEnv, socketDir)
	t.Setenv(sharedIdleTimeoutEnv, "30")
	t.Setenv(sharedStartupTimeoutEnv, "20")
	t.Setenv(helperStartsEnv, startLog)
	return startLog, socketDir
}

// runHelperRole runs the test binary as a separate haft process in the given
// role and returns the pid of the daemon that answered it.
func runHelperRole(t *testing.T, role string) int {
	t.Helper()
	command := exec.Command(testExecutable(t))
	command.Env = append(os.Environ(), helperRoleEnv+"="+role)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	done := make(chan error, 1)
	if err := command.Start(); err != nil {
		t.Fatalf("start %s process: %v", role, err)
	}
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s process failed: %v\n%s", role, err, output.String())
		}
	case <-time.After(60 * time.Second):
		_ = command.Process.Kill()
		t.Fatalf("%s process did not finish; output:\n%s", role, output.String())
	}
	return daemonPidFromOutput(t, output.String())
}

func daemonPidFromOutput(t *testing.T, output string) int {
	t.Helper()
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		if rest, found := strings.CutPrefix(scanner.Text(), helperResultPrefix); found {
			pid, err := strconv.Atoi(strings.TrimSpace(rest))
			if err != nil {
				t.Fatalf("daemon pid %q: %v", rest, err)
			}
			return pid
		}
	}
	t.Fatalf("no %q line in helper output:\n%s", helperResultPrefix, output)
	return 0
}

func startedDaemons(t *testing.T, startLog string) []int {
	t.Helper()
	data, err := os.ReadFile(startLog)
	if err != nil {
		t.Fatalf("read start log: %v", err)
	}
	var pids []int
	for _, field := range strings.Fields(string(data)) {
		pid, err := strconv.Atoi(field)
		if err != nil {
			t.Fatalf("start log entry %q: %v", field, err)
		}
		pids = append(pids, pid)
	}
	return pids
}

// killDaemonAtCleanup stops a daemon that a test started, so that no fake
// sidecar outlives the test run (the daemon would otherwise leave only after
// its idle timeout).
func killDaemonAtCleanup(t *testing.T, startLog string) {
	t.Helper()
	t.Cleanup(func() {
		data, err := os.ReadFile(startLog)
		if err != nil {
			return
		}
		for _, field := range strings.Fields(string(data)) {
			if pid, err := strconv.Atoi(field); err == nil {
				if process, err := os.FindProcess(pid); err == nil {
					_ = process.Kill()
				}
			}
		}
	})
}

func processIsAlive(pid int) bool {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	event, err := windows.WaitForSingleObject(handle, 0)
	return err == nil && event == uint32(windows.WAIT_TIMEOUT)
}

func TestSharedSidecarDirIsCreatedPrivateAndChecked(t *testing.T) {
	dir := filepath.Join(shortTempRoot(t), "embed")
	t.Setenv(sharedSocketDirEnv, dir)

	created, err := sharedSidecarDir()
	if err != nil {
		t.Fatalf("sharedSidecarDir() error = %v", err)
	}
	if !strings.EqualFold(created, dir) {
		t.Fatalf("sharedSidecarDir() = %q, want %q", created, dir)
	}
	assertPrivateDirectory(t, created)

	again, err := sharedSidecarDir()
	if err != nil || again != created {
		t.Fatalf("second sharedSidecarDir() = %q, %v; want %q, nil", again, err, created)
	}
}

func TestSharedSidecarDirDefaultsToTheLocalAppDataHaftEmbedDirectory(t *testing.T) {
	local := shortTempRoot(t)
	t.Setenv(sharedSocketDirEnv, "")
	t.Setenv("LocalAppData", local)

	dir, err := sharedSidecarDir()
	if err != nil {
		t.Fatalf("sharedSidecarDir() error = %v", err)
	}
	want := filepath.Join(local, "haft", "embed")
	if !strings.EqualFold(dir, want) {
		t.Fatalf("sharedSidecarDir() = %q, want %q", dir, want)
	}
	assertPrivateDirectory(t, dir)
}

// The endpoint's only protection on Windows is its directory (AF_UNIX has no
// peer credentials, ADR-095), so a directory that is not private is refused,
// never repaired.
func TestSharedSidecarDirRefusesADirectoryThatIsNotPrivate(t *testing.T) {
	dir := filepath.Join(shortTempRoot(t), "embed")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("create the inherited-ACL directory: %v", err)
	}
	t.Setenv(sharedSocketDirEnv, dir)

	_, err := sharedSidecarDir()
	if !errors.Is(err, winfs.ErrNotPrivate) {
		t.Fatalf("sharedSidecarDir() error = %v, want winfs.ErrNotPrivate", err)
	}
}

func TestSharedSidecarDirRefusesAJunction(t *testing.T) {
	root := shortTempRoot(t)
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("create junction target: %v", err)
	}
	junction := filepath.Join(root, "embed")
	if output, err := exec.Command("cmd", "/c", "mklink", "/J", junction, target).CombinedOutput(); err != nil {
		t.Fatalf("create junction: %v: %s", err, output)
	}
	t.Setenv(sharedSocketDirEnv, junction)

	if _, err := sharedSidecarDir(); err == nil {
		t.Fatal("sharedSidecarDir() accepted a junction as the private socket directory")
	}
}

func TestSharedSidecarSecondProcessReachesTheSameDaemon(t *testing.T) {
	startLog, _ := sharedWindowsEnv(t)
	killDaemonAtCleanup(t, startLog)

	first, err := New(Config{Provider: ProviderLocal, Model: "fake", Dim: 2})
	if err != nil {
		t.Fatalf("first New(local): %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	vectors, err := first.Embed(context.Background(), RoleQuery, []string{"alpha"})
	if err != nil {
		t.Fatalf("first embed: %v", err)
	}
	firstDaemon := int(vectors[0][0])

	// A separate haft process, connecting over AF_UNIX to the running daemon.
	secondDaemon := runHelperRole(t, "client")
	if secondDaemon != firstDaemon {
		t.Fatalf("second process reached daemon %d, want the running daemon %d", secondDaemon, firstDaemon)
	}
	if starts := startedDaemons(t, startLog); len(starts) != 1 || starts[0] != firstDaemon {
		t.Fatalf("daemon starts = %v, want exactly [%d]", starts, firstDaemon)
	}
}

func TestSharedSidecarConcurrentProcessesStartOneDaemon(t *testing.T) {
	startLog, _ := sharedWindowsEnv(t)
	killDaemonAtCleanup(t, startLog)

	const processes = 3
	results := make([]int, processes)
	var group sync.WaitGroup
	for i := range results {
		group.Add(1)
		go func() {
			defer group.Done()
			results[i] = runHelperRole(t, "client")
		}()
	}
	group.Wait()

	starts := startedDaemons(t, startLog)
	if len(starts) != 1 {
		t.Fatalf("daemon starts = %v, want exactly one (the start lock serializes the processes)", starts)
	}
	for i, pid := range results {
		if pid != starts[0] {
			t.Fatalf("process %d reached daemon %d, want %d", i, pid, starts[0])
		}
	}
}

func TestSharedSidecarDaemonOutlivesTheProcessThatStartedIt(t *testing.T) {
	startLog, _ := sharedWindowsEnv(t)
	killDaemonAtCleanup(t, startLog)

	daemon := runHelperRole(t, "starter")
	// The starter exited without closing anything.
	if !processIsAlive(daemon) {
		t.Fatalf("daemon %d ended with the process that started it", daemon)
	}

	embedder, err := New(Config{Provider: ProviderLocal, Model: "fake", Dim: 2})
	if err != nil {
		t.Fatalf("New(local) after the starter exited: %v", err)
	}
	t.Cleanup(func() { _ = embedder.Close() })
	vectors, err := embedder.Embed(context.Background(), RoleQuery, []string{"beta"})
	if err != nil {
		t.Fatalf("embed after the starter exited: %v", err)
	}
	if got := int(vectors[0][0]); got != daemon {
		t.Fatalf("answered by %d, want the surviving daemon %d", got, daemon)
	}
	if starts := startedDaemons(t, startLog); len(starts) != 1 {
		t.Fatalf("daemon starts = %v, want exactly one", starts)
	}
}

func TestSharedSidecarReplacesAStaleSocketFile(t *testing.T) {
	startLog, _ := sharedWindowsEnv(t)
	killDaemonAtCleanup(t, startLog)

	binary, found := locateSidecar()
	if !found {
		t.Fatal("the test binary was not located as the sidecar")
	}
	socket, lock, err := sharedSidecarPaths(binary, sidecarSpecFromConfig(Config{Provider: ProviderLocal, Model: "fake", Dim: 2}))
	if err != nil {
		t.Fatalf("sharedSidecarPaths: %v", err)
	}

	// A daemon that died without cleaning up leaves its socket file and its
	// lock file behind.
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen for the stale socket: %v", err)
	}
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatalf("close the stale listener: %v", err)
	}
	if _, err := os.Lstat(socket); err != nil {
		t.Fatalf("the stale socket file is missing: %v", err)
	}
	staleLock, err := winfs.OpenFile(lock, os.O_CREATE|os.O_RDWR, winfs.Private)
	if err != nil {
		t.Fatalf("create the stale lock file: %v", err)
	}
	_ = staleLock.Close()

	embedder, err := New(Config{Provider: ProviderLocal, Model: "fake", Dim: 2})
	if err != nil {
		t.Fatalf("New(local) over a stale socket: %v", err)
	}
	t.Cleanup(func() { _ = embedder.Close() })
	vectors, err := embedder.Embed(context.Background(), RoleQuery, []string{"gamma"})
	if err != nil {
		t.Fatalf("embed over a stale socket: %v", err)
	}
	starts := startedDaemons(t, startLog)
	if len(starts) != 1 || int(vectors[0][0]) != starts[0] {
		t.Fatalf("answered by %v, daemon starts = %v; want one fresh daemon to answer", vectors[0], starts)
	}
}

// A socket path over the AF_UNIX limit cannot be bound by anybody, so the shared
// attempt fails without blaming the binary and haft uses a per-process stdio
// sidecar (ADR-095: the stdio fallback stays).
func TestSharedSidecarFallsBackToStdioWhenTheSocketPathIsTooLong(t *testing.T) {
	t.Setenv(helperModeEnv, "stdio")
	t.Setenv(sidecarBinaryEnv, testExecutable(t))
	t.Setenv(sharedSidecarEnv, "1")
	t.Setenv(sharedSocketDirEnv, filepath.Join(shortTempRoot(t), strings.Repeat("d", 100)))
	testpath.SetHome(t, filepath.Join(t.TempDir(), "home"))

	embedder, err := New(Config{Provider: ProviderLocal, Model: "fake", Dim: 2})
	if err != nil {
		t.Fatalf("New(local) with an over-long socket path: %v", err)
	}
	t.Cleanup(func() { _ = embedder.Close() })
	if _, shared := embedder.(*sharedSidecarAdapter); shared {
		t.Fatal("New returned the shared adapter for a socket path over the AF_UNIX limit")
	}
	vectors, err := embedder.Embed(context.Background(), RoleQuery, []string{"alpha"})
	if err != nil || len(vectors) != 1 {
		t.Fatalf("stdio embed = %v, %v", vectors, err)
	}
}

func assertPrivateDirectory(t *testing.T, dir string) {
	t.Helper()
	held, err := winfs.OpenDir(dir)
	if err != nil {
		t.Fatalf("open %s: %v", dir, err)
	}
	defer held.Close()
	if err := winfs.CheckPrivate(held.File()); err != nil {
		t.Fatalf("%s is not private: %v", dir, err)
	}
}
