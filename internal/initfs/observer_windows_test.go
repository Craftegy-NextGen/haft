//go:build windows

package initfs

import "github.com/m0n0x41d/haft/internal/initplanning"

// The fixtures are written with mode 0o640, which has the owner-write bit:
// Go's Windows Chmod and create clear the read-only attribute, and Go's
// Windows Stat reports 0o666. These are the Windows images of the Unix test
// predicates in observer_unix_test.go.

// existingObservationModeDiffers is the Windows image of
// `existing.Mode().Perm() != 0o640`.
func existingObservationModeDiffers(existing initplanning.PathObservation) bool {
	return existing.Mode().Perm() != 0o666
}

// managedCarrierInputModeDiffers is the Windows image of
// `input.Mode() != 0o640`.
func managedCarrierInputModeDiffers(input initplanning.ManagedCarrierInput) bool {
	return input.Mode() != 0o666
}
