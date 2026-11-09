// Package winproc is the Windows process-tree primitive layer of the haft
// Windows port (ADR-091). It gives the Windows equivalent of guarantee G8 in
// ADR-092: where Unix code puts a child in its own process group (Setpgid) and
// ends the whole group with kill(-pgid, SIGKILL), Windows code puts the child
// in its own Job Object and ends the job with TerminateJobObject.
//
//   - [Start] and [Contain] create the child suspended in a new console process
//     group (CREATE_SUSPENDED | CREATE_NEW_PROCESS_GROUP), assign it to a new
//     job that has JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE, and only then let it
//     run. Every descendant it starts is in the same job, and none can leave
//     it (breakaway is not allowed). Because the child runs no code before it
//     is in the job, the window between CreateProcess and
//     AssignProcessToJobObject that ADR-092 documents is closed.
//   - [Job.Terminate] ends every process in the job and returns nil only when
//     the job reports no active process (JOBOBJECT_BASIC_ACCOUNTING_INFORMATION
//     ActiveProcesses == 0), so a caller can confirm cleanup of the tree.
//   - [Job.Interrupt] sends CTRL_BREAK_EVENT to the child's console process
//     group, the graceful step before termination (ADR-092 G8,
//     Weaker-documented: it reaches console processes that share the caller's
//     console only).
//   - [Job.Release] closes the job without ending its processes, as a Unix
//     process group outlives its leader; [Job.Close] ends them
//     (kill-on-close). A job that is never closed is closed, killing what
//     remains in it, when the garbage collector reclaims it.
//
// Every implementation file is Windows-only (the _windows file-name suffix is
// its build constraint), so on Linux and macOS the package is empty apart from
// this documentation. Only golang.org/x/sys/windows and the standard library
// are used; no cgo.
package winproc
