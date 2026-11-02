//go:build windows

package host

import (
	"errors"
	"fmt"

	"github.com/m0n0x41d/haft/internal/winfs"
)

// lockInitRoot is the Windows counterpart of init_lock_unix.go: flock(LOCK_EX
// | LOCK_NB) on the project root directory. It uses winfs.TryLockIdentity, a
// named mutex keyed by the root's FILE_ID_INFO (ADR-092 O4 as amended in
// WS7: option 4c). Like the Unix lock it writes nothing into the project
// directory and serializes only cooperating init calls, and it leaves the
// root fully usable: Init's own publication renames and links into the root,
// and other processes may flush it. The kernel releases it when the process
// ends.
//
// A lock held by another process, or already held in this process (as for a
// second flock descriptor), gives the same host_init_busy error as
// EWOULDBLOCK on Unix. Every other failure is returned unchanged, as the Unix
// open and flock errors are. A lock taken over from a holder that died is not
// an error: Init preflights and publishes every file completely, as after a
// crash on Unix; winfs counts such takeovers (AbandonedIdentityLocks).
//
// The mutex belongs to the acquiring OS thread, so the release function must
// run on the goroutine that called lockInitRoot; Init defers it there.
//
// Upstream dev deletes the Unix and unsupported init lock files; delete this
// file at that sync (architecture doc, WS7).
func lockInitRoot(root string) (func(), error) {
	lock, err := winfs.TryLockIdentity(root)
	if errors.Is(err, winfs.ErrBusy) {
		return nil, fmt.Errorf("host_init_busy: another init holds the project lock")
	}
	if err != nil {
		return nil, err
	}
	return func() {
		_ = lock.Unlock()
	}, nil
}
