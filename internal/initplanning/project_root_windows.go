package initplanning

import (
	"fmt"
	"path/filepath"
	"strings"
)

// parseCanonicalProjectRoot validates an init project root. On Windows a
// volume root such as `D:\` is also accepted, because workspaces are often
// checked out at a drive root (a subst or mapped drive). Companion paths keep
// using parseCanonicalAbsolutePath, which still rejects a volume root.
func parseCanonicalProjectRoot(raw string) (string, error) {
	if raw == "" || raw != strings.TrimSpace(raw) {
		return "", fmt.Errorf("path is required in exact form")
	}
	if !filepath.IsAbs(raw) || filepath.Clean(raw) != raw {
		return "", fmt.Errorf("path must be canonical and absolute")
	}
	return raw, nil
}
