package embedding

import (
	"path/filepath"
	"runtime"
	"testing"
)

// TestSidecarExecutablePathIsIdentityOutsideWindows: Linux and macOS name the
// sidecar candidates exactly as before; only Windows adds the .exe extension
// that PATHEXT needs to run the file.
func TestSidecarExecutablePathIsIdentityOutsideWindows(t *testing.T) {
	path := filepath.Join(t.TempDir(), sidecarBinaryName)
	want := path
	if runtime.GOOS == "windows" {
		want = path + ".exe"
	}
	if got := sidecarExecutablePath(path); got != want {
		t.Fatalf("sidecarExecutablePath(%q) = %q, want %q", path, got, want)
	}
}
