//go:build !windows

package initfs

import "github.com/m0n0x41d/haft/internal/initplanning"

func existingObservationModeDiffers(existing initplanning.PathObservation) bool {
	return existing.Mode().Perm() != 0o640
}

func managedCarrierInputModeDiffers(input initplanning.ManagedCarrierInput) bool {
	return input.Mode() != 0o640
}
