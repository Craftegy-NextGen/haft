package overseer

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/m0n0x41d/haft/internal/winproc"
)

// runReviewerShellCommand is the Windows sibling of runReviewerShellCommand in
// runner_unix.go. The reviewer command runs under cmd.exe instead of /bin/sh,
// and the shell and every process it starts share a job object instead of a
// process group (ADR-092 G8), so a timeout ends the reviewer itself, not only
// the shell.
func runReviewerShellCommand(
	ctx context.Context,
	command string,
	projectRoot string,
	env []string,
	timeout time.Duration,
) (string, error) {
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	shell := reviewerShell()
	cmd := exec.Command(shell)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: reviewerShellCommandLine(shell, command)}
	cmd.Dir = projectRoot
	cmd.Env = env

	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output

	job, err := winproc.Start(cmd)
	if err != nil {
		return "", err
	}
	// Descendants of a reviewer that exits by itself keep running, as members
	// of a Unix process group outlive its leader.
	defer func() { _ = job.Release() }()

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		return strings.TrimSpace(output.String()), err
	case <-execCtx.Done():
		_ = terminateProcessGroup(job, done)
		return strings.TrimSpace(output.String()), execCtx.Err()
	}
}

// terminateProcessGroup is the Windows sibling of SIGTERM, a 2 second grace
// period and SIGKILL to the reviewer's process group: CTRL_BREAK_EVENT to the
// reviewer's console process group, the same grace period, then the whole job
// ends. CTRL_BREAK reaches only console processes that share haft's console
// (ADR-092 G8, Weaker-documented); the job termination reaches every process.
func terminateProcessGroup(job *winproc.Job, done <-chan error) error {
	_ = job.Interrupt()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
	}

	_ = job.Terminate()
	return <-done
}

// reviewerShell is the command interpreter that runs reviewer_command.
func reviewerShell() string {
	if shell := os.Getenv("ComSpec"); shell != "" {
		return shell
	}
	return "cmd.exe"
}

// reviewerShellCommandLine passes command to the shell unchanged: /s /c
// "<command>" makes cmd.exe remove only the outer quotes, so the command keeps
// its own quoting (exec.Cmd's argument escaping is not the one cmd.exe reads).
// /d skips AutoRun commands from the registry.
func reviewerShellCommandLine(shell, command string) string {
	return syscall.EscapeArg(shell) + ` /d /s /c "` + command + `"`
}
