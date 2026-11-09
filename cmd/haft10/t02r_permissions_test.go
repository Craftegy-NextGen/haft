package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/m0n0x41d/haft/internal/testpath"
)

// Run the installed command in a child shell so its umask cannot affect the
// concurrent Go test process. Both generated files and updates are checked.
func TestT02RActualBinaryKeepsPrivateCreationAndExistingModes(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("a POSIX child shell is unavailable")
	}
	binary := testpath.Exe(filepath.Join(t.TempDir(), "haft10"))
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	for _, mask := range []string{"077", "022"} {
		t.Run(mask, func(t *testing.T) {
			root := t.TempDir()
			source := t.TempDir()
			home := t.TempDir()
			configPath := filepath.Join(root, ".codex", "config.toml")
			agentsPath := filepath.Join(root, "AGENTS.md")
			if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(configPath, []byte("model = \"operator\"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(agentsPath, []byte("# Operator rules\n"), 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{"init", "--codex", "--root", root, "--source-root", source, "--source-repository", "fixture-source"}
			code, result, log := t02RRunWithUmask(t, binary, home, mask, args...)
			if code != 0 || result["result_kind"] != "initialized" {
				t.Fatal(code, result, log)
			}
			state := result["data"].(map[string]any)
			if !t02HasPath(state, "updated", "AGENTS.md") || !t02HasPath(state, "updated", ".codex/config.toml") {
				t.Fatal("existing destinations were not updated", state)
			}
			for _, name := range []string{"AGENTS.md", ".codex/config.toml", ".agents/skills/h-spec/SKILL.md"} {
				t02RRequireMode(t, filepath.Join(root, name), 0600)
			}
			created := state["created"].([]any)
			if len(created) == 0 {
				t.Fatal("no generated file was created", state)
			}
			for _, item := range created {
				t02RRequireMode(t, filepath.Join(root, item.(string)), 0600)
			}
			code, retry, log := t02RRunWithUmask(t, binary, home, mask, args...)
			if code != 0 || retry["result_kind"] != "initialized" {
				t.Fatal(code, retry, log)
			}
			retryState := retry["data"].(map[string]any)
			if len(retryState["created"].([]any)) != 0 || len(retryState["updated"].([]any)) != 0 {
				t.Fatal("retry changed generated files", retryState)
			}
			for _, name := range []string{"AGENTS.md", ".codex/config.toml", ".agents/skills/h-spec/SKILL.md"} {
				t02RRequireMode(t, filepath.Join(root, name), 0600)
			}
		})
	}
	t.Run("existing-0644-under-077", func(t *testing.T) {
		root := t.TempDir()
		source := t.TempDir()
		home := t.TempDir()
		configPath := filepath.Join(root, ".codex", "config.toml")
		agentsPath := filepath.Join(root, "AGENTS.md")
		t02Write(t, configPath, []byte("model = \"operator\"\n"))
		t02Write(t, agentsPath, []byte("# Operator rules\n"))
		for _, path := range []string{configPath, agentsPath} {
			if err := os.Chmod(path, 0644); err != nil {
				t.Fatal(err)
			}
		}
		args := []string{"init", "--codex", "--root", root, "--source-root", source}
		code, result, log := t02RRunWithUmask(t, binary, home, "077", args...)
		if code != 0 || result["result_kind"] != "initialized" {
			t.Fatal(code, result, log)
		}
		state := result["data"].(map[string]any)
		if !t02HasPath(state, "updated", "AGENTS.md") || !t02HasPath(state, "updated", ".codex/config.toml") {
			t.Fatal("existing destinations were not replaced", state)
		}
		t02RRequireMode(t, agentsPath, 0644)
		t02RRequireMode(t, configPath, 0644)
		code, retry, log := t02RRunWithUmask(t, binary, home, "077", args...)
		if code != 0 || retry["result_kind"] != "initialized" {
			t.Fatal(code, retry, log)
		}
		if len(t02rResultPaths(t, retry, "updated")) != 0 || len(t02rResultPaths(t, retry, "created")) != 0 {
			t.Fatal("retry changed generated files", retry)
		}
		t02RRequireMode(t, agentsPath, 0644)
		t02RRequireMode(t, configPath, 0644)
	})
}

func t02RRunWithUmask(t *testing.T, binary, home, mask string, args ...string) (int, map[string]any, string) {
	t.Helper()
	commandArgs := []string{"-c", "umask " + mask + "; exec \"$@\"", "t02r-umask", binary}
	commandArgs = append(commandArgs, args...)
	return t02RunBinary(t, "/bin/sh", home, commandArgs...)
}

func t02RRequireMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("%s mode = %04o, want %04o", path, info.Mode().Perm(), want)
	}
}
