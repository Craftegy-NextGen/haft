//go:build !windows

package overseer

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

func runReviewerShellCommand(
	ctx context.Context,
	command string,
	projectRoot string,
	env []string,
	timeout time.Duration,
) (string, error) {
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = projectRoot
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output

	if err := cmd.Start(); err != nil {
		return "", err
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		return strings.TrimSpace(output.String()), err
	case <-execCtx.Done():
		_ = terminateProcessGroup(cmd.Process.Pid, done)
		return strings.TrimSpace(output.String()), execCtx.Err()
	}
}

func terminateProcessGroup(pid int, done <-chan error) error {
	if pid <= 0 {
		return nil
	}

	_ = syscall.Kill(-pid, syscall.SIGTERM)
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
	}

	_ = syscall.Kill(-pid, syscall.SIGKILL)
	return <-done
}
