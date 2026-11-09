//go:build !windows

package embedding

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// writeFakeSidecar drops an executable bash script and returns its path.
func writeFakeSidecar(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake sidecar script needs a POSIX shell")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available for the fake sidecar")
	}
	path := filepath.Join(t.TempDir(), "fake-haft-embed")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake sidecar: %v", err)
	}
	return path
}

// rejectingSidecarFixture returns a fake sidecar that rejects the daemon flag
// like an old clap binary and otherwise never speaks.
func rejectingSidecarFixture(t *testing.T) string {
	t.Helper()
	script := "#!/usr/bin/env bash\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$a\" = \"--serve-socket\" ]; then echo 'unexpected argument --serve-socket' >&2; exit 2; fi\n" +
		"done\n" +
		"sleep 30\n"
	return writeFakeSidecar(t, script)
}

// silentSidecarFixture returns a fake sidecar that starts and never handshakes.
func silentSidecarFixture(t *testing.T) string {
	t.Helper()
	script := "#!/usr/bin/env bash\nsleep 30\n"
	return writeFakeSidecar(t, script)
}

// sharedSocketTestDir returns the directory the shared sidecar keeps its socket
// and lock in: a fresh temp directory, which is already private (mode 0700).
func sharedSocketTestDir(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}
