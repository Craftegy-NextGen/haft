//go:build windows

package app

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// goShimConfigSuffix names the file beside the shim executable that holds the
// real toolchain path. The shim cannot read it from its environment: the
// capture runner starts the probe with a pinned one.
const goShimConfigSuffix = ".real"

func init() {
	runGoVersionShimIfInstalled()
}

// runGoVersionShimIfInstalled turns a copy of the test binary named go.exe
// into the toolchain probe fixture (ADR-093 E6, the helper-process pattern).
// It is a no-op in every other process: only a go.exe with a config file
// beside it acts, and it never returns.
func runGoVersionShimIfInstalled() {
	self, err := os.Executable()
	if err != nil || !strings.EqualFold(filepath.Base(self), "go.exe") {
		return
	}
	real, err := os.ReadFile(self + goShimConfigSuffix)
	if err != nil {
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "version" {
		source := []byte("package answer\nfunc Answer() int { return 2 }\n")
		if err := os.WriteFile("answer.go", source, 0o600); err != nil {
			os.Exit(4)
		}
	}
	command := exec.Command(string(real), os.Args[1:]...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	err = command.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		os.Exit(exit.ExitCode())
	}
	if err != nil {
		os.Exit(5)
	}
	os.Exit(0)
}

// writeGoVersionShim is the Windows sibling of the POSIX shell script in
// capture_toolchain_shim_unix_test.go: a copy of the test binary named go.exe
// in shimDir that, asked for its version, rewrites answer.go in its working
// directory and otherwise runs the real toolchain.
func writeGoVersionShim(t *testing.T, shimDir string, realGo string) {
	t.Helper()
	self, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer self.Close()
	shim := filepath.Join(shimDir, "go.exe")
	copied, err := os.OpenFile(shim, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(copied, self); err != nil {
		_ = copied.Close()
		t.Fatal(err)
	}
	if err := copied.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shim+goShimConfigSuffix, []byte(realGo), 0o600); err != nil {
		t.Fatal(err)
	}
}
