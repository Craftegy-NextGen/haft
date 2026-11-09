//go:build windows

package initfs

import (
	"io/fs"
	"os"

	"github.com/m0n0x41d/haft/internal/initplanning"
)

// The two predicates below are the Windows meaning of the Unix mode
// comparisons in HostPublisher (ADR-092 fact 3; ADR-091 R-b): the stage
// checks in stageOutput and in the stage recheck before install, and the
// terminal reread in stepTerminalMatches.
//
// The only permission state a Windows file carries through Go is the
// read-only attribute. os.Chmod and File.Chmod use only the owner-write bit
// (0o200) of the requested mode: set means "clear read-only", unset means
// "set read-only". os.Stat and os.Lstat report 0o666 when the attribute is
// clear and 0o444 when it is set; every other bit is a constant. So after
// writeStagedOutput's file.Chmod(output.Mode().Perm()) the observed mode is
// 0o666 or 0o444, never the planned Unix mode (for example 0o644), and the
// Unix comparisons would reject every stage and every published path.
//
// On Windows an observed mode therefore matches the planned mode when it is
// that mode or its Windows image (0o666 if the planned mode has 0o200, else
// 0o444). For a real observation, always 0o666 or 0o444, this is exactly "the
// owner-write bits agree", which is all Chmod can set and Stat can report; it
// is the same rule as initplanning's currentness and batch checks. A file
// whose read-only attribute was changed after it was written is still
// rejected, as a changed mode is on Unix.

// publicationStageModeDiffers is the Windows meaning of
// `mode.Perm() != output.Mode().Perm()`.
func publicationStageModeDiffers(mode os.FileMode, output initplanning.RenderedOutput) bool {
	return !windowsModeMatches(mode, output.Mode())
}

// publishedModeMatches is the Windows meaning of
// `observation.Mode() == output.Mode().Perm()`.
func publishedModeMatches(observation initplanning.PathObservation, output initplanning.RenderedOutput) bool {
	return windowsModeMatches(observation.Mode(), output.Mode())
}

// windowsModeMatches reports whether an observed mode is the planned mode
// itself or its Windows image.
func windowsModeMatches(observed fs.FileMode, planned fs.FileMode) bool {
	image := fs.FileMode(0o444)
	if planned.Perm()&0o200 != 0 {
		image = 0o666
	}
	return observed.Perm() == planned.Perm() || observed.Perm() == image
}
