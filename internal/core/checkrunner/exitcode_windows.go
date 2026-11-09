package checkrunner

import (
	"os/exec"

	"github.com/m0n0x41d/haft/internal/winproc"
)

// exitCodeReported reports whether the waited command exited with a code of
// its own. Windows has no signal death: ending the job gives the child exit
// code 1, which would read as a real exit, so a child the job ended while it
// was running reports none, as a process ended by a signal does elsewhere.
func exitCodeReported(command *exec.Cmd) bool {
	if job := winproc.ForCommand(command); job != nil && job.Killed() {
		return false
	}
	return command.ProcessState.ExitCode() >= 0
}
