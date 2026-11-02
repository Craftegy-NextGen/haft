//go:build !windows

package initplanning

// parseCanonicalProjectRoot validates an init project root. Outside Windows it
// is exactly parseCanonicalAbsolutePath, so a filesystem root is rejected.
func parseCanonicalProjectRoot(raw string) (string, error) {
	return parseCanonicalAbsolutePath(raw)
}
