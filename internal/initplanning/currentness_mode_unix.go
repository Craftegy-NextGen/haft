//go:build !windows

package initplanning

func observedModeDiffersFromManifest(observation PathObservation, manifest ManifestPath) bool {
	return uint32(observation.mode.Perm()) != manifest.Mode
}
