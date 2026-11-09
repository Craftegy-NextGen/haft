package winproc

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ErrClosed reports an operation on a job that was released or closed before
// its processes were confirmed ended.
var ErrClosed = errors.New("winproc: job is closed")

const (
	// terminatedExitCode is the exit code TerminateJobObject gives every
	// process it ends; os.Process.Kill uses the same value.
	terminatedExitCode = 1
	// adoptTimeout bounds how long Terminate waits for a started child to be
	// placed in its job (only Contain defers that step, to a goroutine that
	// exec.Cmd.Start has already launched).
	adoptTimeout = 5 * time.Second
	// emptyTimeout bounds how long Terminate waits for the job to report no
	// active process after TerminateJobObject.
	emptyTimeout = 5 * time.Second
)

// The x/sys/windows wrappers of these two functions take the information
// buffer as a uintptr, which does not keep a stack buffer in place while the
// call runs (a stack copy moves it and the kernel writes to the old address).
// LazyProc.Call is marked go:uintptrescapes, so a pointer converted in its
// argument list is moved to the heap and kept alive until the call returns.
var (
	kernel32                      = windows.NewLazySystemDLL("kernel32.dll")
	procQueryInformationJobObject = kernel32.NewProc("QueryInformationJobObject")
	procSetInformationJobObject   = kernel32.NewProc("SetInformationJobObject")
)

// queryJob fills info, a pointer to a fixed-size JOBOBJECT_* structure.
func queryJob(handle windows.Handle, class uintptr, info unsafe.Pointer, size uintptr) error {
	ok, _, err := procQueryInformationJobObject.Call(uintptr(handle), class, uintptr(info), size, 0)
	if ok == 0 {
		return err
	}
	return nil
}

// jobObjectBasicAccountingInformation is JOBOBJECT_BASIC_ACCOUNTING_INFORMATION,
// which golang.org/x/sys/windows does not define.
type jobObjectBasicAccountingInformation struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

// Job is a Windows Job Object that holds one started child and all of its
// descendants. Its methods are safe for concurrent use.
type Job struct {
	s *jobState
}

// jobState is separate from Job so that the garbage-collector cleanup of an
// unreachable Job can close the handle without keeping the Job reachable.
type jobState struct {
	mu       sync.Mutex
	handle   windows.Handle // 0 once closed
	pid      int            // the child, once it is in the job
	adopted  chan struct{}  // closed when the child is in the job or adoption failed
	adoptErr error
	emptied  bool // closed by Terminate after ActiveProcesses reached 0
	killed   bool // Terminate found the started child still running
}

// newJob creates a job whose processes all end when its last handle closes.
func newJob() (*Job, error) {
	handle, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("winproc: create job object: %w", err)
	}
	if err := setKillOnClose(handle, true); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	state := &jobState{handle: handle, adopted: make(chan struct{})}
	job := &Job{s: state}
	runtime.AddCleanup(job, func(s *jobState) { _ = s.close() }, state)
	return job, nil
}

func setKillOnClose(handle windows.Handle, kill bool) error {
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if kill {
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	}
	ok, _, err := procSetInformationJobObject.Call(
		uintptr(handle),
		windows.JobObjectExtendedLimitInformation,
		//nolint:gosec // G103: the pointer is the fixed-size Windows job information struct passed to SetInformationJobObject.
		uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info),
	)
	if ok == 0 {
		return fmt.Errorf("winproc: set job limits: %w", err)
	}
	return nil
}

// adopt places the suspended child pid in the job and resumes it. On failure
// the child is ended before it has run any code, and the error is kept for
// Terminate to report.
func (s *jobState) adopt(pid int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.adoptErr = s.adoptLocked(pid)
	close(s.adopted)
	return s.adoptErr
}

func (s *jobState) adoptFailed(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.adoptErr = err
	close(s.adopted)
}

func (s *jobState) adoptLocked(pid int) error {
	if s.handle == 0 {
		return ErrClosed
	}
	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false,
		//nolint:gosec // G115: pid is a positive process id, which fits a DWORD.
		uint32(pid),
	)
	if err != nil {
		return fmt.Errorf("winproc: open process %d: %w", pid, err)
	}
	defer func() { _ = windows.CloseHandle(process) }()
	if err := windows.AssignProcessToJobObject(s.handle, process); err != nil {
		_ = windows.TerminateProcess(process, terminatedExitCode)
		return fmt.Errorf("winproc: assign process %d to its job: %w", pid, err)
	}
	s.pid = pid
	if err := resumeProcess(pid); err != nil {
		_ = windows.TerminateJobObject(s.handle, terminatedExitCode)
		return err
	}
	return nil
}

// resumeProcess resumes the threads of a process created with
// CREATE_SUSPENDED. exec.Cmd closes the thread handle that CreateProcess
// returns, so the initial thread is found through a thread snapshot; the
// caller holds the process handle, so the process id cannot be reused.
func resumeProcess(pid int) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("winproc: snapshot threads: %w", err)
	}
	defer func() { _ = windows.CloseHandle(snapshot) }()
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	resumed := 0
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		//nolint:gosec // G115: pid is a positive process id, which fits a DWORD.
		if entry.OwnerProcessID != uint32(pid) {
			continue
		}
		thread, openErr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if openErr != nil {
			return fmt.Errorf("winproc: open thread %d of process %d: %w", entry.ThreadID, pid, openErr)
		}
		_, resumeErr := windows.ResumeThread(thread)
		_ = windows.CloseHandle(thread)
		if resumeErr != nil {
			return fmt.Errorf("winproc: resume thread %d of process %d: %w", entry.ThreadID, pid, resumeErr)
		}
		resumed++
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return fmt.Errorf("winproc: list threads: %w", err)
	}
	if resumed == 0 {
		return fmt.Errorf("winproc: process %d has no thread to resume", pid)
	}
	return nil
}

// Terminate ends every process in the job (TerminateJobObject) and waits until
// the job reports no active process and every process it held has ended
// (its process object is signaled). It returns nil only when that is
// confirmed; the job is then closed, and later calls return nil at once. It is
// the equivalent of kill(-pgid, SIGKILL) treating ESRCH as success, with the
// confirmation that kill(2) cannot give.
func (j *Job) Terminate() error {
	s := j.s
	select {
	case <-s.adopted:
	case <-time.After(adoptTimeout):
		return errors.New("winproc: the started process was not placed in its job")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.adoptErr != nil {
		return s.adoptErr
	}
	if s.handle == 0 {
		if s.emptied {
			return nil
		}
		return ErrClosed
	}
	// A terminating process leaves the job's active count before its process
	// object is signaled, so hold a handle to each member and wait for both.
	members := map[uint32]windows.Handle{}
	defer func() {
		for _, handle := range members {
			_ = windows.CloseHandle(handle)
		}
	}()
	if err := s.openMembersLocked(members); err != nil {
		return err
	}
	//nolint:gosec // G115: pid is a positive process id, which fits a DWORD.
	if child, held := members[uint32(s.pid)]; held {
		event, err := windows.WaitForSingleObject(child, 0)
		s.killed = err != nil || event != windows.WAIT_OBJECT_0
	}
	if err := windows.TerminateJobObject(s.handle, terminatedExitCode); err != nil {
		return fmt.Errorf("winproc: terminate job: %w", err)
	}
	deadline := time.Now().Add(emptyTimeout)
	for delay := time.Millisecond; ; delay = min(2*delay, 50*time.Millisecond) {
		if err := s.openMembersLocked(members); err != nil {
			return err
		}
		active, err := s.activeLocked()
		if err != nil {
			return err
		}
		running := 0
		for _, handle := range members {
			if event, err := windows.WaitForSingleObject(handle, 0); err != nil || event != windows.WAIT_OBJECT_0 {
				running++
			}
		}
		if active == 0 && running == 0 {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("winproc: %d processes active and %d not ended %s after job termination", active, running, emptyTimeout)
		}
		time.Sleep(delay)
	}
	s.emptied = true
	return s.closeLocked()
}

// jobObjectBasicProcessIDList is JOBOBJECT_BASIC_PROCESS_ID_LIST with room for
// a bounded number of ids; Terminate lists again until the job is empty.
type jobObjectBasicProcessIDList struct {
	NumberOfAssignedProcesses uint32
	NumberOfProcessIdsInList  uint32
	ProcessIDList             [256]uintptr
}

// openMembersLocked adds a SYNCHRONIZE handle for each process now in the job
// that members does not hold yet. A process that ended between the listing
// and OpenProcess is skipped: it is no longer in the job.
func (s *jobState) openMembersLocked(members map[uint32]windows.Handle) error {
	list := new(jobObjectBasicProcessIDList)
	//nolint:gosec // G103: the pointer is the fixed-size process id list struct filled by QueryInformationJobObject.
	err := queryJob(s.handle, windows.JobObjectBasicProcessIdList, unsafe.Pointer(list), unsafe.Sizeof(*list))
	if err != nil && !errors.Is(err, windows.ERROR_MORE_DATA) {
		return fmt.Errorf("winproc: list job processes: %w", err)
	}
	for _, id := range list.ProcessIDList[:min(int(list.NumberOfProcessIdsInList), len(list.ProcessIDList))] {
		//nolint:gosec // G115: id is a uintptr process id from the job list, which fits a DWORD.
		pid := uint32(id)
		if _, held := members[pid]; held {
			continue
		}
		handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
		if err != nil {
			continue
		}
		members[pid] = handle
	}
	return nil
}

// Killed reports whether Terminate ended the started child while it was still
// running. A child that had already exited on its own, whose descendants
// Terminate then ended, is not killed. Unix reports a child ended by a signal
// as having no exit code; Windows gives every process Terminate ends the exit
// code 1, so callers that need that distinction ask here.
func (j *Job) Killed() bool {
	s := j.s
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.killed
}

// Interrupt sends CTRL_BREAK_EVENT to the child's console process group. It
// reaches only processes attached to the caller's console that have not
// disabled the event; it is a request, so callers follow it with Terminate.
func (j *Job) Interrupt() error {
	s := j.s
	select {
	case <-s.adopted:
	default:
		return errors.New("winproc: no process to interrupt yet")
	}
	s.mu.Lock()
	pid, err := s.pid, s.adoptErr
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if pid <= 0 {
		// Group 0 would signal every process on the console, the caller too.
		return errors.New("winproc: no process group to interrupt")
	}
	//nolint:gosec // G115: pid is a positive process id, which fits a DWORD.
	if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(pid)); err != nil {
		return fmt.Errorf("winproc: send CTRL_BREAK to process group %d: %w", pid, err)
	}
	return nil
}

// ActiveProcesses reports how many processes are in the job now.
func (j *Job) ActiveProcesses() (int, error) {
	s := j.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handle == 0 {
		if s.emptied {
			return 0, nil
		}
		return 0, ErrClosed
	}
	return s.activeLocked()
}

func (s *jobState) activeLocked() (int, error) {
	info := new(jobObjectBasicAccountingInformation)
	//nolint:gosec // G103: the pointer is the fixed-size accounting struct filled by QueryInformationJobObject.
	if err := queryJob(s.handle, windows.JobObjectBasicAccountingInformation, unsafe.Pointer(info), unsafe.Sizeof(*info)); err != nil {
		return 0, fmt.Errorf("winproc: query job accounting: %w", err)
	}
	return int(info.ActiveProcesses), nil
}

// Release closes the job without ending its processes: they keep running
// outside any haft-owned job, as Unix group members outlive the group leader.
func (j *Job) Release() error {
	s := j.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handle == 0 {
		return nil
	}
	clearErr := setKillOnClose(s.handle, false)
	return errors.Join(clearErr, s.closeLocked())
}

// Close closes the job; kill-on-close ends every process still in it.
func (j *Job) Close() error {
	return j.s.close()
}

func (s *jobState) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeLocked()
}

func (s *jobState) closeLocked() error {
	if s.handle == 0 {
		return nil
	}
	err := windows.CloseHandle(s.handle)
	s.handle = 0
	if err != nil {
		return fmt.Errorf("winproc: close job: %w", err)
	}
	return nil
}
