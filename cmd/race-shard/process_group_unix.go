//go:build !windows

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
)

func (executor localProcessExecutor) run(
	ctx context.Context,
	stdout io.Writer,
	stderr io.Writer,
	name string,
	args ...string,
) processResult {
	if err := ctx.Err(); err != nil {
		return processResult{exitCode: -1, err: err}
	}
	command := exec.Command(name, args...)
	command.Dir = executor.directory
	if len(executor.environment) > 0 {
		command.Env = environmentWithOverrides(
			os.Environ(),
			executor.environment,
		)
	}
	command.Stdout = stdout
	command.Stderr = stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		return processResultFromError(err)
	}

	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
	}()
	select {
	case err := <-done:
		return processResultFromError(err)
	case <-ctx.Done():
		waitErr := terminateProcessGroup(command.Process.Pid, done)
		if waitErr == nil {
			return processResultFromError(ctx.Err())
		}
		return processResultFromError(fmt.Errorf(
			"%w: %v",
			ctx.Err(),
			waitErr,
		))
	}
}

func terminateProcessGroup(pid int, done <-chan error) error {
	if pid <= 0 {
		return nil
	}
	// A graceful signal can let a test descendant outlive the `go` group
	// leader and perform work after a timed-out/interrupted observation. The
	// runner owns this isolated process group, so terminate the complete group
	// atomically at the observation boundary.
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	return <-done
}
