package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/m0n0x41d/haft/internal/testpath"
)

func TestT02R2ActualCLIRecoverySurvivesLongPaths(t *testing.T) {
	binary := testpath.Exe(filepath.Join(t.TempDir(), "haft10"))
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}

	for _, profile := range []string{"default", "legacy"} {
		t.Run(profile, func(t *testing.T) {
			base := t.TempDir()
			home := t.TempDir()
			longBase := t02r2LongPath(base, 2)
			original := filepath.Join(longBase, testpath.Name("Original-é\"Root"))
			copied := filepath.Join(longBase, testpath.Name("Copied-é\"Root"))
			oldSource := filepath.Join(original, "FPF")
			newSource := filepath.Join(copied, "FPF")
			t02Mkdir(t, oldSource)
			t02Write(t, filepath.Join(oldSource, "PIN"), []byte("original publication\n"))

			code, initial, log := t02r2RunCLI(t, binary, home, "init", "--codex", "--root", original, "--source-root", oldSource, "--source-repository", "fixture-source")
			if code != 0 || initial["result_kind"] != "initialized" {
				t.Fatal("initial CLI init failed", code, initial, log)
			}
			if profile == "legacy" {
				t02SelectLegacy(t, filepath.Join(original, ".codex", "config.toml"))
			}
			t02CopyTree(t, original, copied)
			t02Write(t, filepath.Join(newSource, "PIN"), []byte("distinct copied publication\n"))
			before := t02Snapshot(t, copied)

			code, refused, log := t02r2RunCLI(t, binary, home, "init", "--codex", "--root", copied)
			t02r2RequireRecovery(t, code, refused, log, "host_source_selection_required")
			if !slices.Equal(t02rResultPaths(t, refused, "unresolved"), t02rCLIPaths()) {
				t.Fatal("source refusal omitted unresolved paths", refused)
			}
			if !reflect.DeepEqual(before, t02Snapshot(t, copied)) {
				t.Fatal("source refusal changed copied project")
			}

			code, recovered, log := t02r2RunCLI(t, binary, home, "init", "--codex", "--root", copied, "--source-root", newSource)
			if code != 0 || recovered["result_kind"] != "initialized" {
				t.Fatal("explicit copied source did not recover init", code, recovered, log)
			}
			t02r2RequireSourceArgs(t, copied, newSource, profile)
			if string(t02Read(t, filepath.Join(newSource, "PIN"))) != "distinct copied publication\n" {
				t.Fatal("explicit source did not select copied publication")
			}

			movedOriginal := filepath.Join(longBase, "original-moved")
			if err := os.Rename(original, movedOriginal); err != nil {
				t.Fatal(err)
			}
			code, retry, log := t02r2RunCLI(t, binary, home, "init", "--codex", "--root", copied)
			if code != 0 || retry["result_kind"] != "initialized" || !slices.Equal(t02rResultPaths(t, retry, "unchanged"), t02rCLIPaths()) {
				t.Fatal("copied source did not survive original move", code, retry, log)
			}

			missing := filepath.Join(t02r2LongPath(base, 4), testpath.Name("missing-é\"source"))
			beforeMissing := t02Snapshot(t, copied)
			code, unavailable, log := t02r2RunCLI(t, binary, home, "init", "--codex", "--root", copied, "--source-root", missing)
			t02r2RequireRecovery(t, code, unavailable, log, "host_source_unavailable")
			if !slices.Equal(t02rResultPaths(t, unavailable, "unresolved"), t02rCLIPaths()) {
				t.Fatal("unavailable source omitted unresolved paths", unavailable)
			}
			if !reflect.DeepEqual(beforeMissing, t02Snapshot(t, copied)) {
				t.Fatal("unavailable explicit source changed copied project")
			}
			regularFile := filepath.Join(t02r2LongPath(base, 4), testpath.Name("file-é\"source.txt"))
			t02Write(t, regularFile, []byte("not a source directory\n"))
			beforeFile := t02Snapshot(t, copied)
			code, fileRefusal, log := t02r2RunCLI(t, binary, home, "init", "--codex", "--root", copied, "--source-root", regularFile)
			t02r2RequireRecovery(t, code, fileRefusal, log, "host_source_unavailable")
			message := fileRefusal["diagnostics"].([]any)[0].(map[string]any)["message"].(string)
			if !strings.Contains(message[:min(240, len(message))], "not an existing directory") || !strings.Contains(log[:min(240, len(log))], "not an existing directory") {
				t.Fatalf("regular-file reason was hidden after the long address: JSON=%q stderr=%q", message, log)
			}
			if !slices.Equal(t02rResultPaths(t, fileRefusal, "unresolved"), t02rCLIPaths()) {
				t.Fatal("regular-file refusal omitted unresolved paths", fileRefusal)
			}
			if !reflect.DeepEqual(beforeFile, t02Snapshot(t, copied)) {
				t.Fatal("regular-file refusal changed copied project")
			}

			externalRoot := filepath.Join(t02r2LongPath(base, 4), testpath.Name("shared-é\"source"))
			externalOriginal := filepath.Join(base, "external-original")
			externalCopy := filepath.Join(base, "external-copy")
			t02Mkdir(t, externalRoot)
			t02Mkdir(t, externalOriginal)
			code, initialExternal, log := t02r2RunCLI(t, binary, home, "init", "--codex", "--root", externalOriginal, "--source-root", externalRoot)
			if code != 0 || initialExternal["result_kind"] != "initialized" {
				t.Fatal("external source setup failed", code, initialExternal, log)
			}
			t02CopyTree(t, externalOriginal, externalCopy)
			if err := os.Rename(externalRoot, externalRoot+"-moved"); err != nil {
				t.Fatal(err)
			}
			beforeExternal := t02Snapshot(t, externalCopy)
			code, unavailableInherited, log := t02r2RunCLI(t, binary, home, "init", "--codex", "--root", externalCopy)
			t02r2RequireRecovery(t, code, unavailableInherited, log, "host_source_unavailable")
			if !reflect.DeepEqual(beforeExternal, t02Snapshot(t, externalCopy)) {
				t.Fatal("unavailable inherited source changed copied project")
			}
		})
	}
}

func t02r2LongPath(base string, depth int) string {
	parts := []string{base}
	for index := 0; index < depth; index++ {
		parts = append(parts, strings.Repeat("route-", 12))
	}
	return filepath.Join(parts...)
}

func t02r2RunCLI(t *testing.T, binary, home string, args ...string) (int, map[string]any, string) {
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
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	code := 0
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err, stderr.String())
		}
		code = exit.ExitCode()
	}
	if stdout.Len() > 8192 {
		t.Fatalf("actual CLI stdout exceeds 8192 bytes: %d", stdout.Len())
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err, stdout.String(), stderr.String())
	}
	return code, result, stderr.String()
}

func t02r2RequireRecovery(t *testing.T, code int, result map[string]any, log, wantCode string) {
	t.Helper()
	if code != 2 || result["result_kind"] != "init_failed" || t02rResultCode(t, result) != wantCode {
		t.Fatal("source recovery classification changed", code, result, log)
	}
	diagnostics := result["diagnostics"].([]any)
	message := diagnostics[0].(map[string]any)["message"].(string)
	for _, output := range []string{message, log} {
		instruction := output[:min(240, len(output))]
		if !strings.Contains(instruction, "--root <selected-project-root>") || !strings.Contains(instruction, "--source-root <existing-source-directory>") || !strings.Contains(instruction, "for the selected project") {
			t.Fatalf("selected-project recovery instruction was lost before long addresses: JSON=%q stderr=%q", message, log)
		}
	}
	if !utf8.ValidString(message) || !utf8.ValidString(log) {
		t.Fatal("CLI recovery output contains invalid UTF-8")
	}
	for _, field := range []string{"created", "updated", "unchanged"} {
		if len(t02rResultPaths(t, result, field)) != 0 {
			t.Fatalf("source refusal claimed completed %s paths: %v", field, result)
		}
	}
}

func t02r2RequireSourceArgs(t *testing.T, root, source, profile string) {
	t.Helper()
	physicalRoot := t02Resolve(t, root)
	physicalSource := t02Resolve(t, source)
	config := string(t02Read(t, filepath.Join(root, ".codex", "config.toml")))
	for _, line := range strings.Split(config, "\n") {
		if !strings.HasPrefix(line, "args = ") {
			continue
		}
		var args []string
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "args = ")), &args); err != nil {
			t.Fatal(err)
		}
		want := []string{"serve", "--root", physicalRoot, "--source-root", physicalSource, "--source-repository", "fixture-source"}
		if profile == "legacy" {
			want = append(want, "--profile", "legacy")
		}
		if !slices.Equal(args, want) {
			t.Fatalf("source recovery changed managed address or profile: got %v, want %v", args, want)
		}
		return
	}
	t.Fatal("managed config args missing")
}
