//go:build windows

package fpfrefresh

import (
	"os"

	"github.com/m0n0x41d/haft/internal/winfs"
)

// The helpers below are the Windows meanings of the Unix mode predicates in
// the upstream tests (ADR-093 E3; ADR-092 fact 3 and O2). Go reports Windows
// file modes as 0o666 or 0o444 only, so a test that holds a path checks the
// owner and DACL through winfs.CheckPrivate instead.

// receiptModeNotPrivate: a receipt is written through os.CreateTemp and
// Chmod(0o600), which keeps the owner-write bit, so Go reports 0o666; the
// predicate rejects what cannot be that image of 0o600. The receipt takes the
// security of its directory (winfs.Private is for the lock carriers).
func receiptModeNotPrivate(got os.FileMode) bool {
	return got != 0o666
}

func operationCarrierNotPrivate(path string, info os.FileInfo) bool {
	return carrierNotPrivate(path)
}

func receiptLockNotPrivate(path string, lockInfo os.FileInfo) bool {
	return carrierNotPrivate(path + ".lock")
}

// carrierNotPrivate reports a lock carrier that does not have the owner-only
// security acquireOperationLock and withReceiptExclusiveLock create it with.
func carrierNotPrivate(path string) bool {
	file, err := winfs.OpenFile(path, os.O_RDONLY, winfs.Inherited)
	if err != nil {
		return true
	}
	defer func() {
		_ = file.Close()
	}()
	return winfs.CheckPrivate(file) != nil
}

// candidateRootModeNotPrivate: the candidate root comes from os.MkdirTemp,
// which keeps the owner-write bit of 0o700 and so is reported as 0o777; the
// predicate rejects what cannot be that image (a read-only directory). The
// root takes the security of the temporary directory, not a private
// descriptor.
func candidateRootModeNotPrivate(permissions os.FileMode) bool {
	return permissions != 0o777
}

// stateDirectoryModeNotPrivate is the production predicate itself: the state
// directory is created with the security of its parent, and the documented
// weaker check of stateDirectoryNotPrivate is all Windows can say about it.
func stateDirectoryModeNotPrivate(info os.FileInfo) bool {
	return stateDirectoryNotPrivate(info)
}

// effectsCacheDirectoryNotPrivate checks the shared fixture root that
// cmd/race-shard creates with an owner-only descriptor.
func effectsCacheDirectoryNotPrivate(path string, info os.FileInfo) bool {
	dir, err := winfs.OpenDir(path)
	if err != nil {
		return true
	}
	defer func() {
		_ = dir.Close()
	}()
	return winfs.CheckPrivate(dir.File()) != nil
}

// writeReceiptLockCarrier creates the prior unlocked carrier with the private
// security that withReceiptExclusiveLock requires of an existing one.
func writeReceiptLockCarrier(path string) error {
	file, err := winfs.OpenFile(path+".lock", os.O_WRONLY|os.O_CREATE, winfs.Private)
	if err != nil {
		return err
	}
	return file.Close()
}
