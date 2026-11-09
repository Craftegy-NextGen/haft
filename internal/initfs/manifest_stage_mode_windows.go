//go:build windows

package initfs

import "os"

// canonicalCarrierStageModeSafe is the Windows meaning of the Unix predicate
// `info.Mode().Perm() == 0o600 || info.Mode().Perm() == 0o644` in
// reconcileCanonicalCarrierStages (ADR-092 fact 3 and O2; ADR-091 R-b).
//
// On Windows, Go derives the permission bits of an os.FileInfo from the
// read-only attribute alone: 0o444 when it is set, 0o666 otherwise. Both
// modes the Unix code accepts, the stage's create mode 0o600 and its final
// mode 0o644, have the owner-write bit, which Go's Windows os.OpenFile and
// os.Chmod map to "not read-only", reported as 0o666. So the predicate
// accepts exactly the image of the Unix-accepted set: 0o666.
//
// This is the documented weaker check of ADR-092 O2: the call site holds no
// handle (os.Lstat), so winfs.CheckPrivate cannot run, and a stage file is not
// private by construction anyway (shared code creates it with os.OpenFile and
// inherited security, and 0o644 is world-readable on Unix too). Ownership and
// the DACL are therefore not checked here; the shared code still requires a
// regular file that is not a symbolic link (IsRegular, ModeSymlink).
func canonicalCarrierStageModeSafe(info os.FileInfo) bool {
	return info.Mode().Perm() == 0o666
}
