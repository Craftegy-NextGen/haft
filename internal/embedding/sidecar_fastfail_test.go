//go:build !windows

package embedding

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
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

// withinDeadline runs fn and fails the test if it does not return in time —
// the regression guard for the hang this fix removes.
func withinDeadline(t *testing.T, d time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { fn(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("call did not return within %s — sidecar resolution hung", d)
	}
}

// TestNewSidecar_IncompatibleBinaryDegradesFast pins fix (1): when the shared
// daemon attempt resolves an OLD binary that rejects --serve-socket, New must
// degrade to ErrSidecarUnavailable (FTS5+PPR) quickly — it must NOT fall back to
// driving the same binary over stdio (which would hang on a missing handshake).
func TestNewSidecar_IncompatibleBinaryDegradesFast(t *testing.T) {
	// Rejects the daemon flag like an old clap binary; in stdio mode it would
	// hang without ever sending a handshake. If New wrongly falls back to stdio,
	// the deadline guard trips.
	script := "#!/usr/bin/env bash\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$a\" = \"--serve-socket\" ]; then echo 'unexpected argument --serve-socket' >&2; exit 2; fi\n" +
		"done\n" +
		"sleep 30\n"
	path := writeFakeSidecar(t, script)

	t.Setenv(sidecarBinaryEnv, path)
	t.Setenv(sharedSidecarEnv, "1")           // exercise the shared path
	t.Setenv(sharedSocketDirEnv, t.TempDir()) // isolate socket/lock from any real daemon

	var gotErr error
	withinDeadline(t, 15*time.Second, func() {
		_, gotErr = New(Config{Provider: ProviderLocal})
	})
	if !errors.Is(gotErr, ErrSidecarUnavailable) {
		t.Fatalf("New err = %v, want ErrSidecarUnavailable (degrade to FTS)", gotErr)
	}
}

// TestNewSidecar_StdioHandshakeTimesOut pins fix (2): on the stdio path
// (shared disabled), a binary that never emits a handshake must time out and
// return an error rather than block forever.
func TestNewSidecar_StdioHandshakeTimesOut(t *testing.T) {
	script := "#!/usr/bin/env bash\nsleep 30\n" // starts, never handshakes
	path := writeFakeSidecar(t, script)

	t.Setenv(sidecarBinaryEnv, path)
	t.Setenv(sharedSidecarEnv, "0")         // force the stdio path
	t.Setenv(stdioHandshakeTimeoutEnv, "1") // 1s handshake budget

	var gotErr error
	withinDeadline(t, 15*time.Second, func() {
		_, gotErr = New(Config{Provider: ProviderLocal})
	})
	if gotErr == nil {
		t.Fatal("New err = nil, want a handshake-timeout error")
	}
}
