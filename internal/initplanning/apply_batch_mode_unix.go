//go:build !windows

package initplanning

func predecessorModeDiffers(expectation PathExpectation, output RenderedOutput) bool {
	return expectation.mode != output.mode
}

func predecessorModeMatches(expectation PathExpectation, output RenderedOutput) bool {
	return expectation.mode == output.mode
}
