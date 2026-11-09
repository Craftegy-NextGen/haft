package checkrunner

import (
	"errors"
	"os"
	"os/exec"
	"strings"

	"github.com/m0n0x41d/haft/internal/winproc"
)

// errNotContained reports a started process that is not in a job object, so
// its descendants cannot be found or confirmed ended.
var errNotContained = errors.New("checkrunner: process is not in a job object")

// Put the test and ordinary descendants in one job object so a deadline bounds
// both the Go test command and child binaries retaining its output pipes
// (terminate_unix.go uses a process group). The child joins the job before it
// runs; its standard input is an empty pipe instead of the null device.
func configureTermination(command *exec.Cmd, terminate func(*exec.Cmd) error) {
	passLocalAppData(command)
	// Without a job, terminateProcessGroup reports errNotContained, so the
	// run is marked cleanup-uncertain instead of failing here.
	_, _ = winproc.Contain(command)
	command.Cancel = func() error { return terminate(command) }
}

// terminateProcessGroup ends every process in the command's job and returns
// nil only when the job is confirmed empty, also after the command exited.
func terminateProcessGroup(command *exec.Cmd) error {
	if command.Process == nil {
		return os.ErrProcessDone
	}
	job := winproc.ForCommand(command)
	if job == nil {
		_ = command.Process.Kill()
		return errNotContained
	}
	return job.Terminate()
}

// passLocalAppData completes the child environment with the one host variable
// the Go tool needs on Windows that processEnvironment's allowlist omits. With
// GOENV=off and no GOCACHE, go finds its build cache only through LocalAppData
// and otherwise fails every build with "build cache is required". Run calls
// configureTermination right after it sets command.Env, so this is where a
// Windows-only addition fits without changing the shared Unix body (ADR-091).
// The value is a host path and, like HOME, is not part of the returned
// Result.Environment.
func passLocalAppData(command *exec.Cmd) {
	value, present := os.LookupEnv("LocalAppData")
	if !present || value == "" {
		return
	}
	for _, entry := range command.Env {
		if strings.HasPrefix(strings.ToLower(entry), "localappdata=") {
			return
		}
	}
	command.Env = append(command.Env, "LocalAppData="+value)
}
