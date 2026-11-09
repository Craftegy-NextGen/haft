//go:build windows

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/m0n0x41d/haft/internal/fpfrefresh"
)

// windowsTokenGateScript is the uv-based wrapper of the token gate. The Unix
// script builds its virtual environment with python -m venv and calls
// $env/bin/python, which does not exist on Windows (the interpreter is
// Scripts/python.exe).
//
//nolint:gosec // G101: the value is a script file name, not a credential.
const windowsTokenGateScript = "fpf_query_token_gate_uv.sh"

// tokenGateVerifier turns the token-gate coordinates the check names into the
// verifier it runs. On Windows the script is the uv wrapper and the shell is
// the bash of Git for Windows, found from PATH or beside git. When no bash is
// found the verifier is one that fails with that explanation, so the check
// reports it as the token_gate_failed diagnostic instead of inventing a path.
func tokenGateVerifier(gate fpfrefresh.ExecutableCandidateTokenGate) fpfrefresh.CandidateTokenGate {
	gate.ScriptPath = filepath.Join(filepath.Dir(gate.ScriptPath), windowsTokenGateScript)
	shell, err := resolveWindowsTokenGateShell(os.Getenv("SystemRoot"))
	if err != nil {
		return unavailableTokenGate{err: err}
	}
	gate.ShellPath = shell
	return gate
}

// unavailableTokenGate is a token gate that cannot run and says why.
type unavailableTokenGate struct {
	err error
}

func (gate unavailableTokenGate) VerifyCandidate(
	_ context.Context,
	_ fpfrefresh.CandidateTokenGateInput,
) error {
	return gate.err
}

// resolveWindowsTokenGateShell returns the canonical absolute path of a bash:
// the first bash on PATH, else <Git>\bin\bash.exe of the installation that
// holds the git on PATH. The bash launcher of the Windows Subsystem for Linux
// (in the system directory under systemRoot) is not accepted, since it runs
// Linux and cannot run a Windows script path.
func resolveWindowsTokenGateShell(systemRoot string) (string, error) {
	if found, err := exec.LookPath("bash"); err == nil {
		shell, ok := canonicalRegularFile(found)
		if ok && !isWindowsSubsystemLauncher(shell, systemRoot) {
			return shell, nil
		}
	}
	if gitPath, err := exec.LookPath("git"); err == nil {
		for _, root := range gitInstallationRoots(gitPath) {
			shell, ok := canonicalRegularFile(filepath.Join(root, "bin", "bash.exe"))
			if ok {
				return shell, nil
			}
		}
	}
	return "", errors.New(
		"FPF query token gate needs bash (Git for Windows): no bash.exe on PATH and none in the " +
			"installation of the git on PATH; install Git for Windows or put its bin directory on " +
			"PATH (see WINDOWS.md)",
	)
}

// gitInstallationRoots derives the installation folder of Git for Windows
// from the location of git.exe: <Git>\cmd, <Git>\bin and <Git>\mingw64\bin
// (also mingw32 and clangarm64). Any other location (a shim folder, for
// example) yields no root.
func gitInstallationRoots(gitPath string) []string {
	directory := filepath.Dir(filepath.Clean(gitPath))
	switch strings.ToLower(filepath.Base(directory)) {
	case "cmd":
		return []string{filepath.Dir(directory)}
	case "bin":
		parent := filepath.Dir(directory)
		switch strings.ToLower(filepath.Base(parent)) {
		case "mingw64", "mingw32", "clangarm64":
			return []string{filepath.Dir(parent)}
		}
		return []string{parent}
	}
	return nil
}

// canonicalRegularFile returns path as a cleaned absolute path when it names
// an existing regular file.
func canonicalRegularFile(path string) (string, bool) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	absolute = filepath.Clean(absolute)
	info, err := os.Stat(absolute)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return absolute, true
}

func isWindowsSubsystemLauncher(shell string, systemRoot string) bool {
	if strings.TrimSpace(systemRoot) == "" {
		return false
	}
	for _, name := range []string{"System32", "SysWOW64", "Sysnative"} {
		if strings.EqualFold(filepath.Dir(shell), filepath.Join(systemRoot, name)) {
			return true
		}
	}
	return false
}
