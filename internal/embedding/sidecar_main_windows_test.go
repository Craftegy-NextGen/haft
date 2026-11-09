//go:build windows

package embedding

import (
	"os"
	"testing"
)

// TestMain lets the test binary act as the fake sidecar and as the separate
// haft processes of the shared sidecar tests (sidecar_helper_windows_test.go).
func TestMain(m *testing.M) {
	if code, handled := runHelperProcess(os.Args[1:]); handled {
		os.Exit(code)
	}
	os.Exit(m.Run())
}
