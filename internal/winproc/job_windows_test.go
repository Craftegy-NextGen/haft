package winproc

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const helperEnv = "WINPROC_TEST_HELPER"

// TestWinprocHelperProcess is not a test: the tests re-execute the test binary
// with helperEnv set to run one of these modes as a child or grandchild.
func TestWinprocHelperProcess(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		return
	}
	index := slices.Index(os.Args, "--")
	if index < 0 || index+1 >= len(os.Args) {
		os.Exit(2)
	}
	args := os.Args[index+2:]
	switch os.Args[index+1] {
	case "exit":
	case "leaf":
		time.Sleep(30 * time.Second)
	case "tree", "tree-exit":
		// The grandchild of the test inherits this process's job.
		leaf := helperCommand("leaf")
		if err := leaf.Start(); err != nil {
			os.Exit(4)
		}
		writeAtomically(args[0], fmt.Sprintf("%d %d", os.Getpid(), leaf.Process.Pid))
		if os.Args[index+1] == "tree-exit" {
			os.Exit(0)
		}
		time.Sleep(30 * time.Second)
	case "stdin":
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			os.Exit(5)
		}
		fmt.Print(len(data))
	case "interrupt":
		interrupted := make(chan os.Signal, 1)
		signal.Notify(interrupted, os.Interrupt)
		writeAtomically(args[0], "ready")
		select {
		case <-interrupted:
			writeAtomically(args[1], "interrupted")
			os.Exit(0)
		case <-time.After(30 * time.Second):
			os.Exit(6)
		}
	default:
		os.Exit(3)
	}
	os.Exit(0)
}

func helperCommand(mode string, args ...string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestWinprocHelperProcess$", "--", mode}, args...)...)
	cmd.Env = append(os.Environ(), helperEnv+"=1")
	return cmd
}

func writeAtomically(path, content string) {
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, []byte(content), 0o600); err != nil {
		os.Exit(7)
	}
	if err := os.Rename(temporary, path); err != nil {
		os.Exit(7)
	}
}

func waitForFile(t *testing.T, path string) string {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; {
		data, err := os.ReadFile(path)
		if err == nil {
			return string(data)
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("helper did not write %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// treePIDs returns the child and grandchild process ids that a "tree" helper
// wrote, each with a handle that is signaled when that process has ended.
func treePIDs(t *testing.T, path string) (child, grandchild int, childHandle, grandchildHandle windows.Handle) {
	t.Helper()
	fields := strings.Fields(waitForFile(t, path))
	if len(fields) != 2 {
		t.Fatalf("pid file = %q", fields)
	}
	child, err := strconv.Atoi(fields[0])
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err = strconv.Atoi(fields[1])
	if err != nil {
		t.Fatal(err)
	}
	return child, grandchild, openProcess(t, child), openProcess(t, grandchild)
}

func openProcess(t *testing.T, pid int) windows.Handle {
	t.Helper()
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		t.Fatalf("open process %d: %v", pid, err)
	}
	t.Cleanup(func() {
		_ = windows.TerminateProcess(handle, 1)
		_ = windows.CloseHandle(handle)
	})
	return handle
}

func exited(t *testing.T, handle windows.Handle, wait time.Duration) bool {
	t.Helper()
	event, err := windows.WaitForSingleObject(handle, uint32(wait.Milliseconds()))
	if err != nil {
		t.Fatal(err)
	}
	return event == windows.WAIT_OBJECT_0
}

func waitActive(t *testing.T, job *Job, want int) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; {
		active, err := job.ActiveProcesses()
		if err != nil {
			t.Fatal(err)
		}
		if active == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("active processes = %d, want %d", active, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitCommand(cmd *exec.Cmd) <-chan error {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return done
}

func TestStartTerminateKillsChildAndGrandchild(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pids")
	cmd := helperCommand("tree", pidFile)
	job, err := Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = job.Close() })
	done := waitCommand(cmd)
	child, _, childHandle, grandchildHandle := treePIDs(t, pidFile)
	if child != cmd.Process.Pid {
		t.Fatalf("helper pid %d, started pid %d", child, cmd.Process.Pid)
	}
	waitActive(t, job, 2)

	if job.Killed() {
		t.Fatal("Killed before Terminate")
	}
	if err := job.Terminate(); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	if !job.Killed() {
		t.Fatal("Terminate ended a running child but Killed is false")
	}
	// Terminate confirmed an empty job, so both processes have already ended.
	if !exited(t, childHandle, 0) || !exited(t, grandchildHandle, 0) {
		t.Fatal("Terminate returned before the child and grandchild ended")
	}
	var exitErr *exec.ExitError
	if err := <-done; !errors.As(err, &exitErr) || exitErr.ExitCode() != terminatedExitCode {
		t.Fatalf("child wait = %v, want exit code %d", err, terminatedExitCode)
	}
	if active, err := job.ActiveProcesses(); err != nil || active != 0 {
		t.Fatalf("after Terminate: active=%d err=%v", active, err)
	}
	if err := job.Terminate(); err != nil {
		t.Fatalf("second Terminate: %v", err)
	}
}

func TestContainTerminateKillsChildAndGrandchild(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pids")
	cmd := helperCommand("tree", pidFile)
	job, err := Contain(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = job.Close() })
	if ForCommand(cmd) != job {
		t.Fatal("ForCommand did not return the contained job")
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := waitCommand(cmd)
	_, _, childHandle, grandchildHandle := treePIDs(t, pidFile)
	waitActive(t, job, 2)

	if err := job.Terminate(); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	if !exited(t, childHandle, 0) || !exited(t, grandchildHandle, 0) {
		t.Fatal("Terminate returned before the child and grandchild ended")
	}
	<-done
}

func TestContainTerminateRightAfterStartEndsChild(t *testing.T) {
	cmd := helperCommand("leaf")
	job, err := Contain(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = job.Close() })
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	handle := openProcess(t, cmd.Process.Pid)
	// Terminate waits for the start hook, so the child cannot escape the job.
	if err := job.Terminate(); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	if !exited(t, handle, 0) {
		t.Fatal("child survived Terminate issued right after Start")
	}
	_ = cmd.Wait()
}

func TestTerminateAfterChildExitKillsOrphanedGrandchild(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pids")
	cmd := helperCommand("tree-exit", pidFile)
	job, err := Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = job.Close() })
	_, _, _, grandchildHandle := treePIDs(t, pidFile)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("child: %v", err)
	}
	if exited(t, grandchildHandle, 0) {
		t.Fatal("grandchild ended with its parent; the test needs it running")
	}
	waitActive(t, job, 1)
	if err := job.Terminate(); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	if !exited(t, grandchildHandle, 0) {
		t.Fatal("orphaned grandchild survived Terminate")
	}
	if job.Killed() {
		t.Fatal("Killed is true although the child had exited before Terminate")
	}
}

func TestCloseKillsTree(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pids")
	cmd := helperCommand("tree", pidFile)
	job, err := Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	done := waitCommand(cmd)
	_, _, childHandle, grandchildHandle := treePIDs(t, pidFile)
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	if !exited(t, childHandle, 5*time.Second) || !exited(t, grandchildHandle, 5*time.Second) {
		t.Fatal("kill-on-close left a process running")
	}
	<-done
	if err := job.Terminate(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Terminate after Close = %v, want ErrClosed", err)
	}
}

func TestReleaseLeavesDescendantsRunning(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pids")
	cmd := helperCommand("tree-exit", pidFile)
	job, err := Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, grandchildHandle := treePIDs(t, pidFile)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("child: %v", err)
	}
	if err := job.Release(); err != nil {
		t.Fatal(err)
	}
	if exited(t, grandchildHandle, 300*time.Millisecond) {
		t.Fatal("Release ended a descendant; a Unix group outlives its leader")
	}
	// openProcess's cleanup ends the grandchild.
}

func TestContainGivesEmptyStdinAndConfirmsAfterExit(t *testing.T) {
	cmd := helperCommand("stdin")
	job, err := Contain(cmd)
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "0" {
		t.Fatalf("child read %q bytes of standard input, want 0", output)
	}
	if err := job.Terminate(); err != nil {
		t.Fatalf("Terminate after the child exited: %v", err)
	}
}

func TestContainRejectsStdinAndStartedCommand(t *testing.T) {
	withStdin := helperCommand("stdin")
	withStdin.Stdin = strings.NewReader("data")
	if _, err := Contain(withStdin); err == nil {
		t.Fatal("Contain accepted a command with Stdin")
	}
	started := helperCommand("exit")
	if err := started.Start(); err != nil {
		t.Fatal(err)
	}
	_ = started.Wait()
	if _, err := Contain(started); err == nil {
		t.Fatal("Contain accepted a started command")
	}
	if ForCommand(started) != nil {
		t.Fatal("ForCommand found a job on a command Contain did not prepare")
	}
}

func TestStartReportsStartFailure(t *testing.T) {
	cmd := exec.Command(filepath.Join(t.TempDir(), "missing.exe"))
	if job, err := Start(cmd); err == nil || job != nil {
		t.Fatalf("Start of a missing program: job=%v err=%v", job, err)
	}
}

func TestStartKeepsCallerCreationFlags(t *testing.T) {
	shared := &syscall.SysProcAttr{HideWindow: true}
	cmd := helperCommand("exit")
	cmd.SysProcAttr = shared
	job, err := Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if shared.CreationFlags != 0 || !cmd.SysProcAttr.HideWindow {
		t.Fatalf("shared attributes changed or caller fields lost: %+v", cmd.SysProcAttr)
	}
	want := uint32(windows.CREATE_SUSPENDED | windows.CREATE_NEW_PROCESS_GROUP)
	if cmd.SysProcAttr.CreationFlags&want != want {
		t.Fatalf("creation flags = %#x", cmd.SysProcAttr.CreationFlags)
	}
	if err := job.Terminate(); err != nil {
		t.Fatal(err)
	}
}

func TestInterruptReachesConsoleProcessGroup(t *testing.T) {
	directory := t.TempDir()
	ready := filepath.Join(directory, "ready")
	marker := filepath.Join(directory, "interrupted")
	cmd := helperCommand("interrupt", ready, marker)
	job, err := Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = job.Close() })
	done := waitCommand(cmd)
	waitForFile(t, ready)
	if err := job.Interrupt(); err != nil {
		t.Skipf("the test process has no console to share with the child: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("interrupted child: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("CTRL_BREAK did not reach the child's process group")
	}
	if waitForFile(t, marker) != "interrupted" {
		t.Fatal("child did not observe os.Interrupt")
	}
}
