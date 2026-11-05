//go:build windows

package initplanning

import "io/fs"

// The Windows mode predicates of this package (ADR-092 fact 3; ADR-091 R-b)
// compare an observed file mode with a planned or recorded Unix mode.
//
// Go derives Windows permissions from the read-only attribute alone: Stat
// reports 0o666 when it is clear and 0o444 when it is set, and Chmod uses only
// the owner-write bit (0o200) of a requested mode. So a file published with
// the planned mode 0o644 is observed as 0o666, and the Unix equality would
// classify every owned path as modified. On Windows an observed mode matches
// a planned or recorded mode when it equals that mode or its Windows image
// (windowsModeImage). For a real Windows observation, always 0o666 or 0o444,
// this is exactly "the owner-write bits agree", the rule of initfs's
// publication checks; a synthetic observation that no Windows file can have
// (0o600, say, in a pure-logic test) still has to match exactly, so mode
// drift stays visible. A read-only flip after publication is a modification.

// windowsModeImage returns the mode Go reports on Windows for a file created
// or chmodded with mode: 0o666 when it has the owner-write bit, else 0o444.
func windowsModeImage(mode fs.FileMode) fs.FileMode {
	if mode.Perm()&0o200 != 0 {
		return 0o666
	}
	return 0o444
}

// windowsModeMatches reports whether an observed mode is the planned or
// recorded mode itself or its Windows image.
func windowsModeMatches(observed fs.FileMode, planned fs.FileMode) bool {
	return observed.Perm() == planned.Perm() || observed.Perm() == windowsModeImage(planned)
}

// observedModeDiffersFromManifest is the Windows meaning of
// `uint32(observation.mode.Perm()) != manifest.Mode` in
// classifyManifestOwnedPath.
func observedModeDiffersFromManifest(observation PathObservation, manifest ManifestPath) bool {
	return !windowsModeMatches(observation.mode, fs.FileMode(manifest.Mode))
}
