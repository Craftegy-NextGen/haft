//go:build windows

package initplanning

// predecessorModeDiffers is the Windows meaning of
// `expectation.mode != output.mode` in publicationOutputStep: the observed
// predecessor mode matches the planned output mode when it is that mode or its
// Windows image (see currentness_mode_windows.go). It keeps batch building
// consistent with classifyManifestOwnedPath, which classified the path.
func predecessorModeDiffers(expectation PathExpectation, output RenderedOutput) bool {
	return !windowsModeMatches(expectation.mode, output.mode)
}

// predecessorModeMatches is the Windows meaning of
// `expectation.mode == output.mode` in publicationOutputStep.
func predecessorModeMatches(expectation PathExpectation, output RenderedOutput) bool {
	return windowsModeMatches(expectation.mode, output.mode)
}
