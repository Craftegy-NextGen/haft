//go:build !windows

package checkrunner

import "os/exec"

// exitCodeReported reports whether the waited command exited with a code. A
// process ended by a signal has none, which is how a run ended by its deadline
// reports no exit code.
func exitCodeReported(command *exec.Cmd) bool {
	return command.ProcessState.ExitCode() >= 0
}
