//go:build windows

package projectledgermigration

import (
	"os"
	"path/filepath"

	"github.com/m0n0x41d/haft/internal/winfs"
)

// insecureSnapshotFileMode is the Windows meaning of the Unix predicate
// `info.Mode().Perm() != 0o600` in requireSecureSnapshotFile (ADR-092 fact 3
// and O2; ADR-091 R-b).
//
// On Windows, Go derives the permission bits of an os.FileInfo from the
// read-only attribute alone: 0o444 when it is set, 0o666 otherwise. The
// snapshot's os.Chmod(partialPath, 0o600) keeps the owner-write bit, which Go
// maps to clearing the read-only attribute, reported as 0o666. So the first
// part of the predicate rejects exactly what cannot be the image of 0o600: a
// read-only snapshot.
//
// The second part is the private-file check of ADR-092 O2 (2a):
// winfs.CheckPrivate on a handle opened without following a link. The snapshot
// is private by construction, because reserveSnapshotPartialPath creates the
// partial file owner-only before VACUUM INTO writes into it
// (snapshot_partial_windows.go), and a rename keeps its security descriptor.
// So the owner is the token user, the DACL is protected and owner-only, and
// the file has one link. Every failure to open or to read the facts rejects
// the snapshot (fail closed).
//
// The caller reports a rejection as a mode failure; on Windows it can also
// mean an owner, DACL or link-count failure.
func insecureSnapshotFileMode(info os.FileInfo, path string) bool {
	if info.Mode().Perm() != 0o666 {
		return true
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return true
	}
	file, err := winfs.OpenFile(absolute, os.O_RDONLY, winfs.Private)
	if err != nil {
		return true
	}
	defer file.Close()
	return winfs.CheckPrivate(file) != nil
}
