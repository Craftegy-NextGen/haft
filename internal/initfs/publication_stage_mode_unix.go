//go:build !windows

package initfs

import (
	"os"

	"github.com/m0n0x41d/haft/internal/initplanning"
)

func publicationStageModeDiffers(mode os.FileMode, output initplanning.RenderedOutput) bool {
	return mode.Perm() != output.Mode().Perm()
}

func publishedModeMatches(observation initplanning.PathObservation, output initplanning.RenderedOutput) bool {
	return observation.Mode() == output.Mode().Perm()
}
