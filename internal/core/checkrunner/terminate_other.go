//go:build !darwin && !linux && !windows

package checkrunner

import (
	"errors"
	"os"
	"os/exec"
)

// Other platforms retain CommandContext's direct-child cancellation. The
// runner reports no sandbox or detached-child containment guarantee there.
func configureTermination(command *exec.Cmd, terminate func(*exec.Cmd) error) {}

func terminateProcessGroup(command *exec.Cmd) error {
	if command.Process == nil {
		return nil
	}
	err := command.Process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}
