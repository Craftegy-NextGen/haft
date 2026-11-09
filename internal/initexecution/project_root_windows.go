package initexecution

import "path/filepath"

// canonicalProjectRootPath validates a project, store or coordinator root. On
// Windows a volume root such as `D:\` is also accepted (see
// initplanning.parseCanonicalProjectRoot). Companion paths such as the
// database and lock keep using canonicalNonRootPath.
func canonicalProjectRootPath(path string) bool {
	return path != "" &&
		filepath.IsAbs(path) &&
		filepath.Clean(path) == path
}
