//go:build !windows

package initexecution

// canonicalProjectRootPath validates a project, store or coordinator root.
// Outside Windows it is exactly canonicalNonRootPath, so a filesystem root is
// rejected.
func canonicalProjectRootPath(path string) bool {
	return canonicalNonRootPath(path)
}
