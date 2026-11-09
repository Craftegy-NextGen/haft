package main

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/m0n0x41d/haft/internal/testpath"
)

// Exercise project relocation through the installed executable. The copied
// tree contains both Haft assets and operator content, while the original is
// retained as a byte-for-byte control after its binary and source are moved.
func TestT02ActualBinaryRelocatesCopiedProject(t *testing.T) {
	oldBinary := testpath.Exe(filepath.Join(t.TempDir(), "haft10"))
	build := exec.Command("go", "build", "-o", oldBinary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	oldSource := filepath.Join(t.TempDir(), "source")
	t02Mkdir(t, oldSource)
	oldRoot := filepath.Join(t.TempDir(), "project")
	t02Mkdir(t, oldRoot)
	hostHome := t.TempDir()
	t02Write(t, filepath.Join(oldRoot, ".codex", "config.toml"), []byte("model = \"operator\"\n[mcp_servers.foreign]\ncommand = \"foreign-tool\"\n"))
	t02Write(t, filepath.Join(oldRoot, ".haft", "operator-data.txt"), []byte("keep project data\n"))
	t02Write(t, filepath.Join(oldRoot, ".agents", "skills", "operator", "SKILL.md"), []byte("keep custom skill\n"))
	initialArgs := []string{"init", "--codex", "--root", oldRoot, "--source-root", oldSource, "--source-repository", "fixture-source"}
	initialCode, initial, initialLog := t02RunBinary(t, oldBinary, hostHome, initialArgs...)
	if initialCode != 0 || initial["result_kind"] != "initialized" {
		t.Fatal(initialCode, initial, initialLog)
	}
	original := t02Snapshot(t, oldRoot)

	defaultCopy := filepath.Join(t.TempDir(), "default-copy")
	legacyCopy := filepath.Join(t.TempDir(), "legacy-copy")
	conflictCopy := filepath.Join(t.TempDir(), "conflict-copy")
	for _, path := range []string{defaultCopy, legacyCopy, conflictCopy} {
		t02CopyTree(t, oldRoot, path)
	}
	t02SelectLegacy(t, filepath.Join(legacyCopy, ".codex", "config.toml"))
	conflictingSkill := filepath.Join(conflictCopy, ".agents", "skills", "h-spec", "SKILL.md")
	beforeSkill := t02Read(t, conflictingSkill)
	t02Write(t, conflictingSkill, append(beforeSkill, []byte("operator edit\n")...))
	conflictBefore := t02Snapshot(t, conflictCopy)

	newBinary := testpath.Exe(filepath.Join(t.TempDir(), "haft10"))
	if err := os.Rename(oldBinary, newBinary); err != nil {
		t.Fatal(err)
	}
	newSource := filepath.Join(t.TempDir(), "source")
	if err := os.Rename(oldSource, newSource); err != nil {
		t.Fatal(err)
	}
	rootLink := filepath.Join(t.TempDir(), "project-link")
	if err := os.Symlink(defaultCopy, rootLink); err != nil {
		t.Fatal(err)
	}
	canonicalRoot := t02Resolve(t, defaultCopy)
	canonicalBinary := t02Resolve(t, newBinary)
	canonicalSource := t02Resolve(t, newSource)
	currentArgs := []string{"--source-root", newSource, "--source-repository", "fixture-source"}

	defaultArgs := append([]string{"init", "--codex", "--root", rootLink}, currentArgs...)
	defaultCode, defaultResult, defaultLog := t02RunBinary(t, newBinary, hostHome, defaultArgs...)
	if defaultCode != 0 || defaultResult["result_kind"] != "initialized" {
		t.Fatal(defaultCode, defaultResult, defaultLog)
	}
	defaultState := defaultResult["data"].(map[string]any)
	if defaultState["root"] != canonicalRoot || !t02HasPath(defaultState, "updated", ".codex/config.toml") {
		t.Fatal("relocation result omitted physical root or updated config", defaultState)
	}
	t02CheckManagedConfig(t, defaultCopy, canonicalRoot, canonicalBinary, canonicalSource, false)
	defaultAgainCode, defaultAgain, defaultAgainLog := t02RunBinary(t, newBinary, hostHome, defaultArgs...)
	if defaultAgainCode != 0 || defaultAgain["result_kind"] != "initialized" {
		t.Fatal(defaultAgainCode, defaultAgain, defaultAgainLog)
	}
	if t02HasPath(defaultAgain["data"].(map[string]any), "updated", ".codex/config.toml") {
		t.Fatal("relocation rerun updated config", defaultAgain)
	}

	legacyArgs := append([]string{"init", "--codex", "--root", legacyCopy}, currentArgs...)
	legacyCode, legacyResult, legacyLog := t02RunBinary(t, newBinary, hostHome, legacyArgs...)
	if legacyCode != 0 || legacyResult["result_kind"] != "initialized" {
		t.Fatal(legacyCode, legacyResult, legacyLog)
	}
	t02CheckManagedConfig(t, legacyCopy, t02Resolve(t, legacyCopy), canonicalBinary, canonicalSource, true)

	conflictArgs := append([]string{"init", "--codex", "--root", conflictCopy}, currentArgs...)
	conflictCode, conflictResult, conflictLog := t02RunBinary(t, newBinary, hostHome, conflictArgs...)
	if conflictCode != 2 || conflictResult["result_kind"] != "init_failed" || conflictLog == "" {
		t.Fatal(conflictCode, conflictResult, conflictLog)
	}
	diagnostics := conflictResult["diagnostics"].([]any)
	if diagnostics[0].(map[string]any)["code"] != "host_conflict" {
		t.Fatal("CLI lost ownership conflict classification", diagnostics)
	}
	conflictState := conflictResult["data"].(map[string]any)
	if !t02HasPath(conflictState, "unresolved", ".agents/skills/h-spec/SKILL.md") {
		t.Fatal("CLI omitted unresolved owned path", conflictState)
	}
	if !reflect.DeepEqual(conflictBefore, t02Snapshot(t, conflictCopy)) {
		t.Fatal("preflight conflict changed copied project")
	}
	if !reflect.DeepEqual(original, t02Snapshot(t, oldRoot)) {
		t.Fatal("relocation changed original project bytes")
	}
}

func t02RunBinary(t *testing.T, binary, home string, args ...string) (int, map[string]any, string) {
	t.Helper()
	command := exec.Command(binary, args...)
	env := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "HOME=") || strings.HasPrefix(entry, "XDG_CONFIG_HOME=") {
			continue
		}
		env = append(env, entry)
	}
	command.Env = append(env, "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"))
	var output, log bytes.Buffer
	command.Stdout = &output
	command.Stderr = &log
	err := command.Run()
	exitCode := 0
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err, log.String())
		}
		exitCode = exit.ExitCode()
	}
	var result map[string]any
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err, output.String(), log.String())
	}
	return exitCode, result, log.String()
}

func t02Mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
}

func t02Write(t *testing.T, path string, data []byte) {
	t.Helper()
	t02Mkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

func t02Read(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func t02Resolve(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func t02Snapshot(t *testing.T, root string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[name] = data
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func t02CopyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		target := filepath.Join(to, name)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
	if err != nil {
		t.Fatal(err)
	}
}

func t02SelectLegacy(t *testing.T, path string) {
	t.Helper()
	lines := strings.SplitAfter(string(t02Read(t, path)), "\n")
	for index, line := range lines {
		if !strings.HasPrefix(line, "args = ") {
			continue
		}
		value := strings.TrimPrefix(line, "args = ")
		value = strings.TrimSpace(value)
		var args []string
		if err := json.Unmarshal([]byte(value), &args); err != nil {
			t.Fatal(err)
		}
		args = append(args, "--profile", "legacy")
		encoded, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		lines[index] = "args = " + string(encoded) + "\n"
		t02Write(t, path, []byte(strings.Join(lines, "")))
		return
	}
	t.Fatal("managed config args missing")
}

func t02CheckManagedConfig(t *testing.T, root, wantRoot, wantBinary, wantSource string, legacy bool) {
	t.Helper()
	config := string(t02Read(t, filepath.Join(root, ".codex", "config.toml")))
	if !strings.HasPrefix(config, "model = \"operator\"\n[mcp_servers.foreign]\ncommand = \"foreign-tool\"\n") {
		t.Fatal("foreign config changed", config)
	}
	operatorAssets := map[string]string{
		filepath.Join(root, ".haft", "operator-data.txt"):                "keep project data\n",
		filepath.Join(root, ".agents", "skills", "operator", "SKILL.md"): "keep custom skill\n",
	}
	for path, want := range operatorAssets {
		if got := string(t02Read(t, path)); got != want {
			t.Fatal("operator asset changed", path, got)
		}
	}
	var command string
	var args []string
	for _, line := range strings.Split(config, "\n") {
		if strings.HasPrefix(line, "command = ") {
			value := strings.TrimPrefix(line, "command = ")
			if err := json.Unmarshal([]byte(value), &command); err != nil {
				t.Fatal(err)
			}
		}
		if strings.HasPrefix(line, "args = ") {
			value := strings.TrimPrefix(line, "args = ")
			if err := json.Unmarshal([]byte(value), &args); err != nil {
				t.Fatal(err)
			}
		}
	}
	if command != wantBinary {
		t.Fatal("candidate binary address", command, wantBinary)
	}
	wantArgs := []string{"serve", "--root", wantRoot, "--source-root", wantSource, "--source-repository", "fixture-source"}
	if legacy {
		wantArgs = append(wantArgs, "--profile", "legacy")
	}
	if !slices.Equal(args, wantArgs) {
		t.Fatal("managed addresses or profile", args, wantArgs)
	}
}

func t02HasPath(state map[string]any, field, path string) bool {
	items, ok := state[field].([]any)
	if !ok {
		return false
	}
	return slices.ContainsFunc(items, func(item any) bool { return item == path })
}
