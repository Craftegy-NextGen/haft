//go:build !windows

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeGoVersionShim installs the toolchain probe fixture of
// TestCaptureRefusesCodeChangedByToolchainProbeBeforeClaim in shimDir (ADR-093
// E3, E6, extracted verbatim from the test body): a POSIX shell script named
// go that rewrites answer.go when it is asked for its version and otherwise
// runs the real toolchain. Its Windows sibling re-executes the test binary.
func writeGoVersionShim(t *testing.T, shimDir string, realGo string) {
	t.Helper()
	quotedGo := "'" + strings.ReplaceAll(realGo, "'", "'\"'\"'") + "'"
	shim := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "version" ]; then
cat > answer.go <<'GO'
package answer
func Answer() int { return 2 }
GO
fi
exec %s "$@"
`, quotedGo)
	if err := os.WriteFile(filepath.Join(shimDir, "go"), []byte(shim), 0700); err != nil {
		t.Fatal(err)
	}
}
