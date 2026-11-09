//go:build windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/m0n0x41d/haft/internal/fpfrefresh"
)

// E4 (ADR-093): the production indexer build, run with the real go command,
// must leave a file that Windows can run, which is what check and apply need
// of the indexer (fpfrefresh.executableModeMissing, ADR-092 fact 3).

func TestBuildIndexerExecutableBuildsARunnableIndexerOnWindows(t *testing.T) {
	root := t.TempDir()
	writeFixtureFile(t, filepath.Join(root, "go.mod"), "module example.invalid/indexer\n\ngo 1.21\n")
	writeFixtureFile(
		t,
		filepath.Join(root, "cmd", "indexer", "main.go"),
		"package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"indexer ran\")\n}\n",
	)

	path, cleanup, err := buildIndexerExecutable(context.Background(), root)
	if err != nil {
		t.Fatalf("buildIndexerExecutable() error = %v", err)
	}
	cleaned := false
	defer func() {
		if !cleaned {
			_ = cleanup()
		}
	}()

	if !strings.EqualFold(filepath.Ext(path), ".exe") {
		t.Fatalf("indexer path %q has no .exe extension", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the build left no file at %q: %v", path, err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		t.Fatalf("indexer %q is not a non-empty regular file", path)
	}
	output, err := exec.Command(path).CombinedOutput()
	if err != nil {
		t.Fatalf("run the built indexer: %v: %s", err, output)
	}
	if strings.TrimSpace(string(output)) != "indexer ran" {
		t.Fatalf("indexer output = %q", output)
	}

	// The library's own check of an indexer (the PATHEXT rule) accepts it.
	revision := commitFixtureRepository(t, root)
	err = fpfrefresh.ExecutableIndexBuilder{
		ExecutablePath:       path,
		SourceRepositoryPath: root,
	}.BuildIndex(context.Background(), fpfrefresh.IndexBuildInput{
		WorkingDirectory:  root,
		ReadmePath:        "data/FPF/Readme.md",
		SpecificationPath: "data/FPF/FPF-Spec.md",
		DatabasePath:      filepath.Join(root, "fpf.db"),
		SourceRevision:    revision,
	})
	if err != nil {
		t.Fatalf("ExecutableIndexBuilder.BuildIndex() rejected the built indexer: %v", err)
	}

	cleaned = true
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("cleanup left the build directory: %v", err)
	}
}

func writeFixtureFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// commitFixtureRepository makes root a git repository with one commit and
// returns its SHA, which BuildIndex needs to read the source commit time.
func commitFixtureRepository(t *testing.T, root string) string {
	t.Helper()
	git := func(args ...string) string {
		command := exec.Command("git", append([]string{
			"-c", "user.name=fixture",
			"-c", "user.email=fixture@example.invalid",
			"-c", "commit.gpgsign=false",
		}, args...)...)
		command.Dir = root
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "-q")
	git("add", "go.mod")
	git("commit", "-q", "-m", "fixture")
	return git("rev-parse", "HEAD")
}
