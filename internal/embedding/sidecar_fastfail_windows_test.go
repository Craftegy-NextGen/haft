//go:build windows

package embedding

import (
	"os"
	"path/filepath"
	"testing"
)

// The Windows variants of the fixtures that sidecar_fastfail_unix_test.go
// writes as bash scripts (ADR-093 E6): the test binary re-executes itself as
// the fake sidecar (sidecar_helper_windows_test.go).

func testExecutable(t *testing.T) string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatalf("test executable: %v", err)
	}
	return path
}

// rejectingSidecarFixture returns a fake sidecar that rejects the daemon flag
// like an old clap binary and otherwise never speaks.
func rejectingSidecarFixture(t *testing.T) string {
	t.Helper()
	t.Setenv(helperModeEnv, "reject")
	return testExecutable(t)
}

// silentSidecarFixture returns a fake sidecar that starts and never handshakes.
func silentSidecarFixture(t *testing.T) string {
	t.Helper()
	t.Setenv(helperModeEnv, "silent")
	return testExecutable(t)
}

// sharedSocketTestDir returns a directory name for the shared sidecar's socket
// and lock that does not exist yet: the sidecar creates it private. It sits
// under a short temporary root because an AF_UNIX path is limited to 107
// bytes, which a t.TempDir path of a long test name can exceed.
func sharedSocketTestDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(shortTempRoot(t), "embed")
}

func shortTempRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("", "hs")
	if err != nil {
		t.Fatalf("create temp root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}
