package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/m0n0x41d/haft/internal/winproc"
)

// run is the Windows sibling of run in process_group_unix.go. The command and
// its descendants share a job object instead of a process group (ADR-092 G8).
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
	// winproc.Start stands for Setpgid followed by Start: the command joins
	// its own job before it runs.
	job, err := winproc.Start(command)
	if err != nil {
		return processResultFromError(err)
	}
	// Descendants that outlive a command which exits by itself keep running,
	// as members of a Unix process group outlive its leader.
	defer func() { _ = job.Release() }()

	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
	}()
	select {
	case err := <-done:
		return processResultFromError(err)
	case <-ctx.Done():
		waitErr := terminateProcessGroup(job, done)
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

// terminateProcessGroup is the Windows sibling of the Unix kill(-pid,
// SIGKILL): a graceful request could let a test descendant outlive the `go`
// command and perform work after a timed-out/interrupted observation, so the
// whole job ends at once at the observation boundary. As on Unix, the result
// is the command's own wait result.
func terminateProcessGroup(job *winproc.Job, done <-chan error) error {
	_ = job.Terminate()
	return <-done
}
