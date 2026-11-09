//go:build windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/m0n0x41d/haft/internal/fpfrefresh"
)

// E4 (ADR-093): how the Windows token gate finds its shell. Each test replaces
// PATH, PATHEXT and SystemRoot with a temporary tree of empty bash.exe and
// git.exe files, so nothing of the real machine is read or run.

func writeFakeExecutable(t *testing.T, path string) string {
	t.Helper()
	writeFixtureFile(t, path, "not a real executable\n")
	return path
}

func isolateShellLookup(t *testing.T, pathEntries ...string) string {
	t.Helper()
	systemRoot := filepath.Join(t.TempDir(), "Windows")
	if err := os.MkdirAll(filepath.Join(systemRoot, "System32"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", strings.Join(pathEntries, string(os.PathListSeparator)))
	t.Setenv("PATHEXT", ".COM;.EXE;.BAT;.CMD")
	t.Setenv("SystemRoot", systemRoot)
	return systemRoot
}

func requireSameFile(t *testing.T, got string, want string) {
	t.Helper()
	if !strings.EqualFold(filepath.Clean(got), filepath.Clean(want)) {
		t.Fatalf("shell = %q, want %q", got, want)
	}
	if !filepath.IsAbs(got) || filepath.Clean(got) != got {
		t.Fatalf("shell %q is not a canonical absolute path", got)
	}
}

func TestResolveWindowsTokenGateShellTakesBashFromPath(t *testing.T) {
	git := t.TempDir()
	bash := writeFakeExecutable(t, filepath.Join(git, "usr", "bin", "bash.exe"))
	systemRoot := isolateShellLookup(t, filepath.Dir(bash))

	got, err := resolveWindowsTokenGateShell(systemRoot)
	if err != nil {
		t.Fatalf("resolveWindowsTokenGateShell() error = %v", err)
	}
	requireSameFile(t, got, bash)
}

func TestResolveWindowsTokenGateShellSkipsTheLinuxSubsystemLauncher(t *testing.T) {
	git := t.TempDir()
	gitExe := writeFakeExecutable(t, filepath.Join(git, "cmd", "git.exe"))
	want := writeFakeExecutable(t, filepath.Join(git, "bin", "bash.exe"))
	systemRoot := isolateShellLookup(t)
	launcher := writeFakeExecutable(t, filepath.Join(systemRoot, "System32", "bash.exe"))
	t.Setenv("PATH", filepath.Dir(launcher)+string(os.PathListSeparator)+filepath.Dir(gitExe))

	got, err := resolveWindowsTokenGateShell(systemRoot)
	if err != nil {
		t.Fatalf("resolveWindowsTokenGateShell() error = %v", err)
	}
	requireSameFile(t, got, want)
}

func TestResolveWindowsTokenGateShellDerivesBashFromTheGitInstallation(t *testing.T) {
	for _, gitRelative := range []string{
		filepath.Join("cmd", "git.exe"),
		filepath.Join("mingw64", "bin", "git.exe"),
	} {
		t.Run(gitRelative, func(t *testing.T) {
			git := t.TempDir()
			gitExe := writeFakeExecutable(t, filepath.Join(git, gitRelative))
			want := writeFakeExecutable(t, filepath.Join(git, "bin", "bash.exe"))
			systemRoot := isolateShellLookup(t, filepath.Dir(gitExe))

			got, err := resolveWindowsTokenGateShell(systemRoot)
			if err != nil {
				t.Fatalf("resolveWindowsTokenGateShell() error = %v", err)
			}
			requireSameFile(t, got, want)
		})
	}
}

func TestResolveWindowsTokenGateShellFailsClearlyWhenNothingIsFound(t *testing.T) {
	empty := t.TempDir()
	systemRoot := isolateShellLookup(t, empty)

	got, err := resolveWindowsTokenGateShell(systemRoot)
	if err == nil {
		t.Fatalf("resolveWindowsTokenGateShell() = %q, want an error", got)
	}
	for _, want := range []string{"bash", "Git for Windows", "WINDOWS.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

func TestResolveWindowsTokenGateShellNeverInventsAPathBesideGit(t *testing.T) {
	git := t.TempDir()
	gitExe := writeFakeExecutable(t, filepath.Join(git, "cmd", "git.exe"))
	shim := writeFakeExecutable(t, filepath.Join(t.TempDir(), "shims", "git.exe"))
	systemRoot := isolateShellLookup(t, filepath.Dir(gitExe))

	if got, err := resolveWindowsTokenGateShell(systemRoot); err == nil {
		t.Fatalf("a Git folder without bin\bash.exe gave %q", got)
	}

	t.Setenv("PATH", filepath.Dir(shim))
	if got, err := resolveWindowsTokenGateShell(systemRoot); err == nil {
		t.Fatalf("git in an unrecognised folder gave %q", got)
	}
}

func TestTokenGateVerifierOnWindowsUsesTheUvWrapperAndTheResolvedShell(t *testing.T) {
	git := t.TempDir()
	bash := writeFakeExecutable(t, filepath.Join(git, "bin", "bash.exe"))
	isolateShellLookup(t, filepath.Dir(bash))
	root := t.TempDir()
	gate := fpfrefresh.ExecutableCandidateTokenGate{
		ShellPath:  "/bin/bash",
		ScriptPath: filepath.Join(root, "scripts", "fpf_query_token_gate.sh"),
	}

	got, ok := tokenGateVerifier(gate).(fpfrefresh.ExecutableCandidateTokenGate)
	if !ok {
		t.Fatalf("tokenGateVerifier() = %T, want an executable gate", tokenGateVerifier(gate))
	}
	requireSameFile(t, got.ShellPath, bash)
	if want := filepath.Join(root, "scripts", "fpf_query_token_gate_uv.sh"); got.ScriptPath != want {
		t.Fatalf("ScriptPath = %q, want %q", got.ScriptPath, want)
	}
}

func TestTokenGateVerifierOnWindowsReportsAMissingShellFromVerifyCandidate(t *testing.T) {
	isolateShellLookup(t, t.TempDir())
	gate := fpfrefresh.ExecutableCandidateTokenGate{
		ShellPath:  "/bin/bash",
		ScriptPath: filepath.Join(t.TempDir(), "scripts", "fpf_query_token_gate.sh"),
	}

	err := tokenGateVerifier(gate).VerifyCandidate(context.Background(), fpfrefresh.CandidateTokenGateInput{})
	if err == nil || !strings.Contains(err.Error(), "needs bash (Git for Windows)") {
		t.Fatalf("VerifyCandidate() error = %v, want the missing-bash diagnostic", err)
	}
}
