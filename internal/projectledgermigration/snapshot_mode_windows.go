//go:build windows

package projectledgermigration

import "os"

// insecureSnapshotFileMode is the Windows meaning of the Unix predicate
// `info.Mode().Perm() != 0o600` in requireSecureSnapshotFile (ADR-092 fact 3
// and O2; ADR-091 R-b).
//
// On Windows, Go derives the permission bits of an os.FileInfo from the
// read-only attribute alone: 0o444 when it is set, 0o666 otherwise. The
// snapshot's os.Chmod(partialPath, 0o600) keeps the owner-write bit, which Go
// maps to clearing the read-only attribute, reported as 0o666. So the
// predicate rejects exactly what cannot be the image of 0o600: a read-only
// snapshot.
//
// This is the documented weaker check of ADR-092 O2: the call site holds no
// handle (os.Lstat), so winfs.CheckPrivate cannot run, and the snapshot is not
// private by construction (SQLite's VACUUM INTO creates it with the inherited
// security of the ledger directory; Chmod cannot change a DACL on Windows).
// Owner, DACL and link count are therefore not checked here; access to the
// snapshot follows the ledger directory's ACL.
func insecureSnapshotFileMode(info os.FileInfo) bool {
	return info.Mode().Perm() != 0o666
}
