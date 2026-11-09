//go:build darwin || linux

package agenthostrestart

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// LoadVerifiedRuntimeSnapshot reads one completed restart under a shared store
// lock and returns only its secret-free evidence projection.
func LoadVerifiedRuntimeSnapshot(
	projectRoot string,
) (VerifiedRuntimeSnapshot, error) {
	store, err := NewStore(projectRoot)
	if err != nil {
		return VerifiedRuntimeSnapshot{}, err
	}
	if err := store.requireExistingDirectory(); err != nil {
		return VerifiedRuntimeSnapshot{}, err
	}
	lock, err := store.openExistingLock()
	if err != nil {
		return VerifiedRuntimeSnapshot{}, err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_SH); err != nil { // #nosec G115 -- lock is an open file returned by the restart store.
		return VerifiedRuntimeSnapshot{}, fmt.Errorf(
			"lock verified restart evidence for read: %w",
			err,
		)
	}
	defer func() {
		_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) // #nosec G115 -- lock remains open until this deferred unlock runs.
	}()
	return store.loadVerifiedRuntimeSnapshotUnlocked()
}

func (store Store) validateReservedCandidateDigest(
	desiredDigest string,
) error {
	flags := unix.O_RDONLY |
		unix.O_DIRECTORY |
		unix.O_CLOEXEC |
		unix.O_NOFOLLOW
	fd, err := unix.Open(store.attemptsDirectoryPath, flags, 0)
	if err != nil {
		return fmt.Errorf(
			"open existing restart attempts directory: %w",
			err,
		)
	}
	directory := os.NewFile(uintptr(fd), store.attemptsDirectoryPath) // #nosec G115 -- unix.Open returned a valid nonnegative descriptor.
	defer directory.Close()
	if err := requirePrivateDirectory(
		directory,
		"restart attempts directory",
	); err != nil {
		return err
	}
	return validateAttemptMarker(directory, desiredDigest)
}
