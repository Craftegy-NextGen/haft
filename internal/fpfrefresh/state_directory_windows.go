//go:build windows

package fpfrefresh

import "os"

// stateDirectoryNotPrivate is the Windows meaning of the Unix predicate
// `info.Mode().Perm()&0o077 != 0` in acquireRepositoryVerificationOperationLock
// (ADR-092 fact 3 and O2; ADR-091 R-b).
//
// On Windows, Go derives the permission bits of an os.FileInfo from the
// read-only attribute alone: 0o555 when it is set, 0o777 otherwise. The
// os.MkdirAll(path, 0o700) that creates the directory keeps the owner-write
// bit, which Go maps to a directory without the read-only attribute, reported
// as 0o777. So the predicate rejects exactly what cannot be the image of
// 0o700: a read-only directory.
//
// This is the documented weaker check of ADR-092 O2: the call site holds no
// handle (os.Lstat), so winfs.CheckPrivate cannot run, and the directory is
// created with the inherited security of its parent, which Mkdir cannot
// change. Owner and DACL are therefore not checked here. The lock files that
// guard the refresh are private by construction (apply_lock_windows.go and
// receipt_lock_windows.go).
func stateDirectoryNotPrivate(info os.FileInfo) bool {
	return info.Mode().Perm()&0o200 == 0
}
