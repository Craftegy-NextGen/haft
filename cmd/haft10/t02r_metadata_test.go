package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/m0n0x41d/haft/internal/testpath"
)

func t02rCLIPaths() []string {
	return []string{
		".agents/skills/h-decide/SKILL.md",
		".agents/skills/h-reason/SKILL.md",
		".agents/skills/h-spec/SKILL.md",
		".agents/skills/h-verify/SKILL.md",
		".codex/config.toml",
		"AGENTS.md",
	}
}

func t02rResultPaths(t *testing.T, result map[string]any, field string) []string {
	t.Helper()
	data, ok := result["data"].(map[string]any)
	if !ok {
		t.Fatalf("CLI result has no init data: %v", result)
	}
	raw, ok := data[field].([]any)
	if !ok {
		return nil
	}
	paths := make([]string, 0, len(raw))
	for _, value := range raw {
		path, ok := value.(string)
		if !ok {
			t.Fatalf("non-string %s path: %v", field, value)
		}
		paths = append(paths, path)
	}
	return paths
}

func t02rResultCode(t *testing.T, result map[string]any) string {
	t.Helper()
	diagnostics, ok := result["diagnostics"].([]any)
	if !ok || len(diagnostics) == 0 {
		t.Fatalf("CLI result has no diagnostics: %v", result)
	}
	first, ok := diagnostics[0].(map[string]any)
	if !ok {
		t.Fatalf("CLI diagnostic has invalid shape: %v", diagnostics[0])
	}
	code, ok := first["code"].(string)
	if !ok {
		t.Fatalf("CLI diagnostic has no code: %v", first)
	}
	return code
}

func t02rBoundedResult(t *testing.T, result map[string]any) {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 8192 {
		t.Fatalf("CLI init result exceeds 8192 bytes: %d", len(encoded))
	}
}

func TestT02RActualCLIReportsUnassessedPathsAndPartialRetry(t *testing.T) {
	binary := testpath.Exe(filepath.Join(t.TempDir(), "haft10"))
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	home := t.TempDir()
	source := filepath.Join(t.TempDir(), "external-source")
	t02Mkdir(t, source)
	priorRoot := filepath.Join(t.TempDir(), "prior-project")
	t02Mkdir(t, priorRoot)
	firstCode, first, firstLog := t02RunBinary(t, binary, home, "init", "--codex", "--root", priorRoot, "--source-root", source, "--source-repository", "fixture-source")
	if firstCode != 0 || first["result_kind"] != "initialized" {
		t.Fatal(firstCode, first, firstLog)
	}
	copyRoot := filepath.Join(t.TempDir(), "copied-project")
	t02CopyTree(t, priorRoot, copyRoot)
	firstSkill := filepath.Join(copyRoot, ".agents", "skills", "h-decide", "SKILL.md")
	if err := os.Remove(firstSkill); err != nil {
		t.Fatal(err)
	}
	conflictingSkill := filepath.Join(copyRoot, ".agents", "skills", "h-spec", "SKILL.md")
	if err := os.WriteFile(conflictingSkill, []byte("operator edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before := t02Snapshot(t, copyRoot)
	blockedCode, blocked, blockedLog := t02RunBinary(t, binary, home, "init", "--codex", "--root", copyRoot)
	if blockedCode != 2 || blocked["result_kind"] != "init_failed" || blockedLog == "" {
		t.Fatal(blockedCode, blocked, blockedLog)
	}
	if code := t02rResultCode(t, blocked); code != "host_conflict" {
		t.Fatalf("edited ownership classification changed: %s", code)
	}
	if !slices.Equal(t02rResultPaths(t, blocked, "unresolved"), t02rCLIPaths()) {
		t.Fatalf("copied config or other unassessed paths omitted: %v", blocked)
	}
	for _, field := range []string{"created", "updated", "unchanged"} {
		if len(t02rResultPaths(t, blocked, field)) != 0 {
			t.Fatalf("preflight claimed completed %s paths: %v", field, blocked)
		}
	}
	if blocked["coverage"] != "unavailable" || !reflect.DeepEqual(before, t02Snapshot(t, copyRoot)) {
		t.Fatal("preflight changed bytes or claimed coverage", blocked)
	}
	t02rBoundedResult(t, blocked)

	if err := os.WriteFile(conflictingSkill, t02Read(t, filepath.Join(priorRoot, ".agents", "skills", "h-spec", "SKILL.md")), 0600); err != nil {
		t.Fatal(err)
	}
	retryCode, retry, retryLog := t02RunBinary(t, binary, home, "init", "--codex", "--root", copyRoot)
	if retryCode != 0 || retry["result_kind"] != "initialized" {
		t.Fatal(retryCode, retry, retryLog)
	}
	if !slices.Equal(t02rResultPaths(t, retry, "created"), []string{t02rCLIPaths()[0]}) {
		t.Fatal("partial retry did not publish missing generated asset", retry)
	}
	if !slices.Equal(t02rResultPaths(t, retry, "updated"), []string{".codex/config.toml"}) {
		t.Fatal("partial retry did not relocate stale managed config", retry)
	}
	if len(t02rResultPaths(t, retry, "unchanged")) != 4 || len(t02rResultPaths(t, retry, "unresolved")) != 0 {
		t.Fatal("partial retry did not complete six paths", retry)
	}
	if retry["coverage"] != "complete" {
		t.Fatal("partial retry did not report complete coverage", retry)
	}
	t02rBoundedResult(t, retry)
	nextCode, next, nextLog := t02RunBinary(t, binary, home, "init", "--codex", "--root", copyRoot)
	if nextCode != 0 || next["result_kind"] != "initialized" || !slices.Equal(t02rResultPaths(t, next, "unchanged"), t02rCLIPaths()) {
		t.Fatal("same-root retry was not a no-op", nextCode, next, nextLog)
	}
	t02rBoundedResult(t, next)
}

func TestT02RActualCLIDistinguishesSourceSelectionAndUnavailability(t *testing.T) {
	binary := testpath.Exe(filepath.Join(t.TempDir(), "haft10"))
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	home := t.TempDir()
	priorRoot := filepath.Join(t.TempDir(), "prior-project")
	priorSource := filepath.Join(priorRoot, "FPF")
	t02Mkdir(t, priorSource)
	initialCode, initial, initialLog := t02RunBinary(t, binary, home, "init", "--codex", "--root", priorRoot, "--source-root", priorSource)
	if initialCode != 0 || initial["result_kind"] != "initialized" {
		t.Fatal(initialCode, initial, initialLog)
	}
	copyRoot := filepath.Join(t.TempDir(), "copied-project")
	t02CopyTree(t, priorRoot, copyRoot)
	before := t02Snapshot(t, copyRoot)
	selectionCode, selection, selectionLog := t02RunBinary(t, binary, home, "init", "--codex", "--root", copyRoot)
	if selectionCode != 2 || t02rResultCode(t, selection) != "host_source_selection_required" || selectionLog == "" {
		t.Fatal("implicit old source was not refused distinctly", selectionCode, selection, selectionLog)
	}
	if !slices.Equal(t02rResultPaths(t, selection, "unresolved"), t02rCLIPaths()) || len(t02rResultPaths(t, selection, "unchanged")) != 0 {
		t.Fatal("source choice refusal claimed completed paths", selection)
	}
	if !reflect.DeepEqual(before, t02Snapshot(t, copyRoot)) {
		t.Fatal("source choice refusal changed copied project")
	}
	t02rBoundedResult(t, selection)
	missing := filepath.Join(copyRoot, "missing-source")
	unavailableCode, unavailable, unavailableLog := t02RunBinary(t, binary, home, "init", "--codex", "--root", copyRoot, "--source-root", missing)
	if unavailableCode != 2 || t02rResultCode(t, unavailable) != "host_source_unavailable" || unavailableLog == "" {
		t.Fatal("unavailable explicit source was not refused distinctly", unavailableCode, unavailable, unavailableLog)
	}
	if !slices.Equal(t02rResultPaths(t, unavailable, "unresolved"), t02rCLIPaths()) || len(t02rResultPaths(t, unavailable, "unchanged")) != 0 {
		t.Fatal("unavailable source refusal claimed completed paths", unavailable)
	}
	if !reflect.DeepEqual(before, t02Snapshot(t, copyRoot)) {
		t.Fatal("unavailable source refusal changed copied project")
	}
	t02rBoundedResult(t, unavailable)
}
