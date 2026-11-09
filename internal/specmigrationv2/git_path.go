package specmigrationv2

import "path/filepath"

// hostPathFromGit returns a path that Git printed in the host's own spelling.
// Git reports paths with forward slashes on every OS (`rev-parse
// --show-toplevel` prints C:/work/repo on Windows); the canonical project-root
// form is the host's separator. filepath.FromSlash is the identity where the
// separator is '/', so every Unix input is returned unchanged.
func hostPathFromGit(text string) string {
	return filepath.FromSlash(text)
}
