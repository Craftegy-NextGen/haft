//go:build windows

package agenthostrestart

// Windows counterpart of verified_runtime_snapshot_os_unix.go (ADR-091 R-b).
// The two Unix functions that call the descriptor layer map to internal/winfs
// (ADR-092), with the same order of effects and error texts:
//
//   - flock(LOCK_SH) and flock(LOCK_UN) on the content-free store lock:
//     winfs.LockFile with SharedLock and winfs.UnlockFile (G5).
//   - open(O_DIRECTORY|O_NOFOLLOW) of the attempts directory: winfs.OpenDir;
//     a symbolic link or junction at the final component is ELOOP (G1). The
//     held handle is the *os.File that requirePrivateDirectory and
//     validateAttemptMarker take, as on Unix.

import (
	"fmt"

	"github.com/m0n0x41d/haft/internal/winfs"
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
	if err := winfs.LockFile(lock, winfs.SharedLock); err != nil {
		return VerifiedRuntimeSnapshot{}, fmt.Errorf(
			"lock verified restart evidence for read: %w",
			err,
		)
	}
	defer func() {
		_ = winfs.UnlockFile(lock)
	}()
	return store.loadVerifiedRuntimeSnapshotUnlocked()
}

func (store Store) validateReservedCandidateDigest(
	desiredDigest string,
) error {
	held, err := winfs.OpenDir(store.attemptsDirectoryPath)
	if err != nil {
		return fmt.Errorf(
			"open existing restart attempts directory: %w",
			err,
		)
	}
	defer held.Close()
	directory := held.File()
	if err := requirePrivateDirectory(
		directory,
		"restart attempts directory",
	); err != nil {
		return err
	}
	return validateAttemptMarker(directory, desiredDigest)
}
