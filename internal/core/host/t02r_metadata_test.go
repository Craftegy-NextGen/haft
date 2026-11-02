package host

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/m0n0x41d/haft/internal/testpath"
)

func t02rManagedPaths() []string {
	return []string{
		".agents/skills/h-decide/SKILL.md",
		".agents/skills/h-reason/SKILL.md",
		".agents/skills/h-spec/SKILL.md",
		".agents/skills/h-verify/SKILL.md",
		".codex/config.toml",
		"AGENTS.md",
	}
}

func TestT02RInitPreflightKeepsEveryUncompletedPath(t *testing.T) {
	c := config(t)
	if _, err := Init(c); err != nil {
		t.Fatal(err)
	}
	newBinary := testpath.Exe(filepath.Join(t.TempDir(), "new-haft10"))
	fakeExecutable(t, newBinary)
	c.Binary = newBinary
	skill := filepath.Join(c.Root, ".agents", "skills", "h-spec", "SKILL.md")
	if err := os.WriteFile(skill, []byte("operator edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before := projectFiles(t, c.Root)
	result, err := Init(c)
	if err == nil || !strings.HasPrefix(err.Error(), "host_conflict:") {
		t.Fatalf("edited ownership not refused: %+v, %v", result, err)
	}
	if len(result.Created)+len(result.Updated)+len(result.Unchanged) != 0 {
		t.Fatalf("preflight claimed completed paths: %+v", result)
	}
	if !slices.Equal(result.Unresolved, t02rManagedPaths()) {
		t.Fatalf("stale config and other unassessed paths omitted: %+v", result)
	}
	if !reflect.DeepEqual(projectFiles(t, c.Root), before) {
		t.Fatal("preflight refusal changed project bytes or modes")
	}

	missing := filepath.Join(t.TempDir(), "missing-source")
	c.SourceRoot = missing
	result, err = Init(c)
	if err == nil || !strings.HasPrefix(err.Error(), "host_source_unavailable:") {
		t.Fatalf("source failure not distinguished: %+v, %v", result, err)
	}
	if !slices.Equal(result.Unresolved, t02rManagedPaths()) || len(result.Unchanged) != 0 {
		t.Fatalf("source preflight claimed completed paths: %+v", result)
	}
	if !reflect.DeepEqual(projectFiles(t, c.Root), before) {
		t.Fatal("source refusal changed project bytes or modes")
	}
}

func TestT02RInitRecheckAndRetryKeepExactRemainder(t *testing.T) {
	c := config(t)
	if _, err := Init(c); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(c.Root, ".agents", "skills", "h-decide", "SKILL.md")
	if err := os.Remove(first); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(c.Root, ".agents", "skills", "h-reason", "SKILL.md")
	original, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	stop := errors.New("test interruption")
	partial, err := initWithHooks(c, initHooks{afterPublish: func(name string) error {
		if name != ".agents/skills/h-decide/SKILL.md" {
			return nil
		}
		if err := os.WriteFile(second, []byte("operator concurrent edit\n"), 0600); err != nil {
			return err
		}
		return stop
	}})
	if !errors.Is(err, stop) {
		t.Fatalf("publication interruption missing: %+v, %v", partial, err)
	}
	if !slices.Equal(partial.Created, []string{t02rManagedPaths()[0]}) {
		t.Fatalf("published path not completed: %+v", partial)
	}
	if !slices.Equal(partial.Unresolved, t02rManagedPaths()[1:]) || len(partial.Unchanged) != 0 {
		t.Fatalf("unrechecked planned-identical paths omitted: %+v", partial)
	}
	blocked, err := Init(c)
	if err == nil || !strings.HasPrefix(err.Error(), "host_conflict:") {
		t.Fatalf("concurrent edit was accepted: %+v, %v", blocked, err)
	}
	if !slices.Equal(blocked.Unresolved, t02rManagedPaths()) || len(blocked.Unchanged) != 0 {
		t.Fatalf("fresh preflight overclaimed completion: %+v", blocked)
	}
	if err := os.WriteFile(second, original, 0600); err != nil {
		t.Fatal(err)
	}
	retry, err := Init(c)
	if err != nil || len(retry.Created)+len(retry.Updated) != 0 {
		t.Fatalf("exact retry failed: %+v, %v", retry, err)
	}
	if !slices.Equal(retry.Unchanged, t02rManagedPaths()) || len(retry.Unresolved) != 0 {
		t.Fatalf("retry did not recheck each exact path: %+v", retry)
	}
	restored, err := os.ReadFile(second)
	if err != nil || !bytes.Equal(restored, original) {
		t.Fatal("retry changed restored generated content")
	}
}
