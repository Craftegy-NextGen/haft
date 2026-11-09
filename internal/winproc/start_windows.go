package winproc

import (
	"errors"
	"io"
	"os/exec"
	"sync"
	"syscall"

	"golang.org/x/sys/windows"
)

// Start starts cmd as the first process of a new job: suspended, in a new
// console process group, assigned to the job, then resumed. It is the Windows
// counterpart of setting SysProcAttr.Setpgid before exec.Cmd.Start. When the
// child cannot be placed in the job it is ended before it runs, reaped with
// cmd.Wait, and Start returns the error, as a failed setpgid fails the start
// on Unix. The caller owns the returned job: Terminate, Release or Close it.
func Start(cmd *exec.Cmd) (*Job, error) {
	job, err := newJob()
	if err != nil {
		return nil, err
	}
	suspendInNewGroup(cmd)
	if err := cmd.Start(); err != nil {
		_ = job.Close()
		return nil, err
	}
	if err := job.s.adopt(cmd.Process.Pid); err != nil {
		_ = cmd.Wait()
		_ = job.Close()
		return nil, err
	}
	return job, nil
}

// Contain prepares cmd, before code it does not own calls cmd.Start, so that
// the started process joins a new job before it runs: Contain sets the same
// creation flags as Start and installs a start hook as cmd.Stdin. exec.Cmd
// copies a non-file Stdin on a goroutine that Start launches after it has
// created the process; the hook's first Read places that process in the job,
// resumes it and returns io.EOF, so the child sees an empty standard input
// (on Unix exec.Cmd gives it the null device). cmd.Stdin must be nil.
//
// [ForCommand] returns the job later from cmd. When the process cannot be
// placed in the job it is ended before it runs and [Job.Terminate] returns
// the error. If Start fails, the job holds nothing and is closed when it is
// garbage-collected.
func Contain(cmd *exec.Cmd) (*Job, error) {
	if cmd.Process != nil {
		return nil, errors.New("winproc: Contain must be called before Start")
	}
	if cmd.Stdin != nil {
		return nil, errors.New("winproc: Contain needs cmd.Stdin to be unset")
	}
	job, err := newJob()
	if err != nil {
		return nil, err
	}
	suspendInNewGroup(cmd)
	cmd.Stdin = &startHook{job: job, cmd: cmd}
	return job, nil
}

// ForCommand returns the job that Contain installed on cmd, or nil.
func ForCommand(cmd *exec.Cmd) *Job {
	hook, ok := cmd.Stdin.(*startHook)
	if !ok {
		return nil
	}
	return hook.job
}

// startHook is the Stdin that Contain installs.
type startHook struct {
	job  *Job
	cmd  *exec.Cmd
	once sync.Once
}

func (h *startHook) Read([]byte) (int, error) {
	h.once.Do(func() {
		// exec.Cmd.Start assigned cmd.Process before it launched the
		// goroutine that calls Read, so this read is ordered after it.
		if h.cmd.Process == nil {
			h.job.s.adoptFailed(errors.New("winproc: start hook ran without a started process"))
			return
		}
		_ = h.job.s.adopt(h.cmd.Process.Pid)
	})
	return 0, io.EOF
}

// suspendInNewGroup adds CREATE_SUSPENDED and CREATE_NEW_PROCESS_GROUP to a
// copy of cmd.SysProcAttr, leaving a caller's shared value untouched. The new
// process group is the counterpart of Setpgid: the child is detached from the
// console's CTRL_C and can be addressed by Job.Interrupt.
func suspendInNewGroup(cmd *exec.Cmd) {
	attributes := syscall.SysProcAttr{}
	if cmd.SysProcAttr != nil {
		attributes = *cmd.SysProcAttr
	}
	attributes.CreationFlags |= windows.CREATE_SUSPENDED | windows.CREATE_NEW_PROCESS_GROUP
	cmd.SysProcAttr = &attributes
}
