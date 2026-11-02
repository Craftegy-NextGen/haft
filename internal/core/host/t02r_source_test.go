package host

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/m0n0x41d/haft/internal/testpath"
)

func requireSourceSelectionWithoutWrite(t *testing.T, c Config) {
	t.Helper()
	physical, err := filepath.EvalSymlinks(c.Root)
	if err != nil {
		t.Fatal(err)
	}
	before := projectFiles(t, physical)
	result, err := Init(c)
	if err == nil || !strings.HasPrefix(err.Error(), "host_source_selection_required:") || !strings.Contains(err.Error(), "--source-root") {
		t.Fatalf("omitted source selection was accepted or misclassified: %+v, %v", result, err)
	}
	if len(result.Created) != 0 || len(result.Updated) != 0 || !containsPath(result.Unresolved, ".codex/config.toml") {
		t.Fatalf("source refusal reported completed files or omitted config: %+v", result)
	}
	if !reflect.DeepEqual(projectFiles(t, physical), before) {
		t.Fatal("source refusal changed the copied project")
	}
}

func containsPath(paths []string, target string) bool {
	for _, path := range paths {
		if path == target {
			return true
		}
	}
	return false
}

func TestT02RSourceInsideCopiedProjectNeedsExplicitSelection(t *testing.T) {
	base := t.TempDir()
	original := filepath.Join(base, "original")
	copyForNew := filepath.Join(base, "copy-new")
	copyForOld := filepath.Join(base, "copy-old")
	oldSource := filepath.Join(original, "FPF")
	oldBinary := testpath.Exe(filepath.Join(base, "old-bin", "haft10"))
	newBinary := testpath.Exe(filepath.Join(base, "new-bin", "haft10"))
	write(t, original, "FPF/PIN", "old pinned publication")
	fakeExecutable(t, oldBinary)
	fakeExecutable(t, newBinary)
	if _, err := Init(Config{Root: original, Binary: oldBinary, SourceRoot: oldSource, SourceRepository: "pinned://publication", Codex: true}); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(original, ".codex", "config.toml")
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	start, end, block, _, err := managedSlice(raw, configStart, configEnd)
	if err != nil {
		t.Fatal(err)
	}
	prior, err := parseManagedConfig(block)
	if err != nil {
		t.Fatal(err)
	}
	prior.Profile = "legacy"
	legacy := bytes.Clone(raw[:start])
	legacy = append(legacy, prior.block()...)
	legacy = append(legacy, raw[end:]...)
	if err := os.WriteFile(configPath, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	originalBefore := projectFiles(t, original)
	copyProject(t, original, copyForNew)
	copyProject(t, original, copyForOld)
	write(t, copyForNew, "FPF/PIN", "distinct copied publication")

	newChoice := Config{Root: copyForNew, Binary: newBinary, Codex: true}
	requireSourceSelectionWithoutWrite(t, newChoice)
	oldChoice := Config{Root: copyForOld, Binary: newBinary, Codex: true}
	requireSourceSelectionWithoutWrite(t, oldChoice)
	if !reflect.DeepEqual(projectFiles(t, original), originalBefore) {
		t.Fatal("source refusal changed original project")
	}

	oldChoice.SourceRoot = oldSource
	oldChoice.SourceRepository = "explicit://repository"
	oldSelected, err := Init(oldChoice)
	if err != nil || !reflect.DeepEqual(oldSelected.Updated, []string{".codex/config.toml"}) {
		t.Fatalf("explicit old source was not selected: %+v, %v", oldSelected, err)
	}
	oldInstalled := installedConfig(t, copyForOld)
	if oldInstalled.SourceRoot != physicalOrRecordedPath(oldSource) || oldInstalled.SourceRepository != "explicit://repository" || oldInstalled.Profile != "legacy" {
		t.Fatalf("explicit old source, repository or profile was lost: %+v", oldInstalled)
	}

	movedOriginal := filepath.Join(base, "original-moved")
	if err := os.Rename(original, movedOriginal); err != nil {
		t.Fatal(err)
	}
	oldCopyBefore := projectFiles(t, copyForOld)
	oldRetry, err := Init(Config{Root: copyForOld, Binary: newBinary, Codex: true})
	if err == nil || !strings.HasPrefix(err.Error(), "host_source_unavailable:") || !strings.Contains(err.Error(), "--source-root") {
		t.Fatalf("explicit old source dependency was hidden after its move: %+v, %v", oldRetry, err)
	}
	if !reflect.DeepEqual(projectFiles(t, copyForOld), oldCopyBefore) {
		t.Fatal("missing old source retry changed copied project")
	}
	requireSourceSelectionWithoutWrite(t, newChoice)
	newChoice.SourceRoot = filepath.Join(copyForNew, "FPF")
	newSelected, err := Init(newChoice)
	if err != nil || !reflect.DeepEqual(newSelected.Updated, []string{".codex/config.toml"}) {
		t.Fatalf("explicit copied source was not selected: %+v, %v", newSelected, err)
	}
	newInstalled := installedConfig(t, copyForNew)
	if newInstalled.SourceRoot != physicalOrRecordedPath(newChoice.SourceRoot) || newInstalled.SourceRepository != "pinned://publication" || newInstalled.Profile != "legacy" {
		t.Fatalf("source repository inheritance or profile was lost: %+v", newInstalled)
	}
	newChoice.SourceRoot = ""
	retry, err := Init(newChoice)
	if err != nil || len(retry.Created) != 0 || len(retry.Updated) != 0 || len(retry.Unchanged) != 6 {
		t.Fatalf("same-root inherited source retry failed: %+v, %v", retry, err)
	}
}

func TestT02RRootEqualSourceAndPhysicalSymlinkComparison(t *testing.T) {
	base := t.TempDir()
	original := filepath.Join(base, "original")
	copyRoot := filepath.Join(base, "copy")
	alias := filepath.Join(base, "original-link")
	binary := testpath.Exe(filepath.Join(base, "bin", "haft10"))
	write(t, original, "FPF/PIN", "pinned publication")
	fakeExecutable(t, binary)
	if _, err := Init(Config{Root: original, Binary: binary, SourceRoot: original, Codex: true}); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(original, alias); err != nil {
		t.Fatal(err)
	}
	same, err := Init(Config{Root: alias, Binary: binary, Codex: true})
	if err != nil || len(same.Updated) != 0 || len(same.Unchanged) != 6 {
		t.Fatalf("same physical root through symlink required a new source: %+v, %v", same, err)
	}
	copyProject(t, original, copyRoot)
	if err := os.Symlink(copyRoot, filepath.Join(base, "copy-link")); err != nil {
		t.Fatal(err)
	}
	requireSourceSelectionWithoutWrite(t, Config{Root: filepath.Join(base, "copy-link"), Binary: binary, Codex: true})
}

func TestT02RExternalAndPrefixSiblingSourceCanBeInherited(t *testing.T) {
	for _, name := range []string{"shared", "original-neighbor"} {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			original := filepath.Join(base, "original")
			copyRoot := filepath.Join(base, "copy")
			source := filepath.Join(base, name, "FPF")
			binary := testpath.Exe(filepath.Join(base, "bin", "haft10"))
			write(t, original, "operator.txt", "keep")
			write(t, source, "PIN", "shared publication")
			fakeExecutable(t, binary)
			if _, err := Init(Config{Root: original, Binary: binary, SourceRoot: source, SourceRepository: "shared://source", Codex: true}); err != nil {
				t.Fatal(err)
			}
			copyProject(t, original, copyRoot)
			result, err := Init(Config{Root: copyRoot, Binary: binary, Codex: true})
			if err != nil || !reflect.DeepEqual(result.Updated, []string{".codex/config.toml"}) {
				t.Fatalf("external source was not inherited: %+v, %v", result, err)
			}
			installed := installedConfig(t, copyRoot)
			if installed.SourceRoot != physicalOrRecordedPath(source) || installed.SourceRepository != "shared://source" {
				t.Fatalf("external source identity changed: %+v", installed)
			}
			before := projectFiles(t, copyRoot)
			if err := os.Rename(source, filepath.Join(base, "source-moved")); err != nil {
				t.Fatal(err)
			}
			missing, err := Init(Config{Root: copyRoot, Binary: binary, Codex: true})
			if err == nil || !strings.HasPrefix(err.Error(), "host_source_unavailable:") || !strings.Contains(err.Error(), "--source-root") {
				t.Fatalf("missing inherited source was misclassified: %+v, %v", missing, err)
			}
			if len(missing.Created) != 0 || len(missing.Updated) != 0 || !containsPath(missing.Unresolved, ".codex/config.toml") {
				t.Fatalf("missing source reported completed files or omitted config: %+v", missing)
			}
			if !reflect.DeepEqual(projectFiles(t, copyRoot), before) {
				t.Fatal("missing source refusal changed the project")
			}
		})
	}
}
