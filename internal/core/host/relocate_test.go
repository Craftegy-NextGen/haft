package host

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/m0n0x41d/haft/internal/testpath"
)

type fixtureFile struct {
	Bytes []byte
	Mode  fs.FileMode
}

func projectFiles(t *testing.T, root string) map[string]fixtureFile {
	t.Helper()
	files := map[string]fixtureFile{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = fixtureFile{Bytes: content, Mode: info.Mode().Perm()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func copyProject(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		target := filepath.Join(to, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, content, info.Mode().Perm())
	})
	if err != nil {
		t.Fatal(err)
	}
}

func fakeExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
}

func installedConfig(t *testing.T, root string) managedConfig {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, block, _, err := managedSlice(raw, configStart, configEnd)
	if err != nil {
		t.Fatal(err)
	}
	config, err := parseManagedConfig(block)
	if err != nil {
		t.Fatal(err)
	}
	return config
}

func TestInitRelocatesCopiedProjectWithMovedBinarySourceAndSymlinkRoot(t *testing.T) {
	base := t.TempDir()
	original := filepath.Join(base, "original")
	moved := filepath.Join(base, "copy")
	alias := filepath.Join(base, "copy-link")
	oldBinary := testpath.Exe(filepath.Join(base, "tools-old", "haft10"))
	newBinary := testpath.Exe(filepath.Join(base, "tools-new", "haft10"))
	oldSource := filepath.Join(base, "source-old")
	newSource := filepath.Join(base, "source-new")
	if err := os.MkdirAll(original, 0755); err != nil {
		t.Fatal(err)
	}
	fakeExecutable(t, oldBinary)
	write(t, oldSource, "FPF/PIN", "pinned source bytes")
	write(t, original, ".codex/config.toml", "model = \"selected\"\n[mcp_servers.foreign]\ncommand = \"foreign\"\n")
	write(t, original, ".agents/skills/custom/SKILL.md", "operator skill")
	write(t, original, ".haft/records/data.bin", "exact project data")
	initial := Config{Root: original, Binary: oldBinary, SourceRoot: oldSource, SourceRepository: "pinned://fpf", Codex: true}
	if _, err := Init(initial); err != nil {
		t.Fatal(err)
	}
	oldConfigPath := filepath.Join(original, ".codex", "config.toml")
	raw, err := os.ReadFile(oldConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	startAt, endAt, block, _, err := managedSlice(raw, configStart, configEnd)
	if err != nil {
		t.Fatal(err)
	}
	chosen, err := parseManagedConfig(block)
	if err != nil {
		t.Fatal(err)
	}
	chosen.Profile = "legacy"
	withProfile := bytes.Clone(raw[:startAt])
	withProfile = append(withProfile, chosen.block()...)
	withProfile = append(withProfile, raw[endAt:]...)
	if err := os.WriteFile(oldConfigPath, withProfile, 0600); err != nil {
		t.Fatal(err)
	}
	originalBefore := projectFiles(t, original)
	copyProject(t, original, moved)
	if err := os.Symlink(moved, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(newBinary), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldBinary, newBinary); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldSource, newSource); err != nil {
		t.Fatal(err)
	}
	result, err := Init(Config{Root: alias, Binary: newBinary, SourceRoot: newSource, Codex: true})
	if err != nil {
		t.Fatalf("relocate: %+v, %v", result, err)
	}
	physicalRoot, err := filepath.EvalSymlinks(moved)
	if err != nil {
		t.Fatal(err)
	}
	physicalBinary, err := filepath.EvalSymlinks(newBinary)
	if err != nil {
		t.Fatal(err)
	}
	physicalSource, err := filepath.EvalSymlinks(newSource)
	if err != nil {
		t.Fatal(err)
	}
	if result.Root != physicalRoot || !reflect.DeepEqual(result.Updated, []string{".codex/config.toml"}) {
		t.Fatalf("unexpected relocated result: %+v", result)
	}
	current := installedConfig(t, moved)
	if current.Root != physicalRoot || current.Binary != physicalBinary || current.SourceRoot != physicalSource || current.SourceRepository != "pinned://fpf" || current.Profile != "legacy" {
		t.Fatalf("managed host addresses wrong runtime: %+v", current)
	}
	configRaw, err := os.ReadFile(filepath.Join(moved, ".codex", "config.toml"))
	if err != nil || !bytes.Contains(configRaw, []byte("[mcp_servers.foreign]\ncommand = \"foreign\"")) {
		t.Fatal("foreign config was changed or removed")
	}
	if !reflect.DeepEqual(projectFiles(t, original), originalBefore) {
		t.Fatal("init changed the original project")
	}
	if got := projectFiles(t, moved)[".haft/records/data.bin"]; !bytes.Equal(got.Bytes, []byte("exact project data")) {
		t.Fatal("copied project data changed")
	}
	retry, err := Init(Config{Root: alias, Binary: newBinary, SourceRoot: newSource, Codex: true})
	if err != nil || len(retry.Updated) != 0 || len(retry.Created) != 0 || len(retry.Unchanged) != 6 {
		t.Fatalf("relocation retry was not idempotent: %+v, %v", retry, err)
	}
}

func TestInitRelocationRefusesEditedOrForeignManagedContent(t *testing.T) {
	for _, edit := range []struct {
		name string
		edit func([]byte) []byte
	}{
		{"edited managed key", func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(configEnd), []byte("extra = true\n"+configEnd), 1)
		}},
		{"foreign Haft table", func(raw []byte) []byte {
			return append(raw, []byte("\n[mcp_servers.haft10]\ncommand = \"operator\"\n")...)
		}},
		{"bare key after marker", func(raw []byte) []byte {
			return append(raw, []byte("\noperator_key = \"still in Haft table\"\n")...)
		}},
	} {
		t.Run(edit.name, func(t *testing.T) {
			c := config(t)
			if _, err := Init(c); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(c.Root, ".codex", "config.toml")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			changed := edit.edit(raw)
			if err := os.WriteFile(path, changed, 0600); err != nil {
				t.Fatal(err)
			}
			before := projectFiles(t, c.Root)
			newBinary := testpath.Exe(filepath.Join(t.TempDir(), "new-haft10"))
			fakeExecutable(t, newBinary)
			c.Binary = newBinary
			result, err := Init(c)
			if err == nil || !strings.Contains(err.Error(), "host_conflict") || !reflect.DeepEqual(result.Unresolved, t02rManagedPaths()) || len(result.Unchanged) != 0 {
				t.Fatalf("unknown ownership was overwritten: %+v, %v", result, err)
			}
			if !reflect.DeepEqual(projectFiles(t, c.Root), before) {
				t.Fatal("ownership conflict wrote files")
			}
		})
	}
}

func TestInitRelocationRequiresUsableInheritedSource(t *testing.T) {
	c := config(t)
	if _, err := Init(c); err != nil {
		t.Fatal(err)
	}
	newSource := filepath.Join(t.TempDir(), "moved-source")
	if err := os.Rename(c.SourceRoot, newSource); err != nil {
		t.Fatal(err)
	}
	prior := projectFiles(t, c.Root)
	c.SourceRoot = ""
	result, err := Init(c)
	if err == nil || !strings.HasPrefix(err.Error(), "host_source_unavailable:") || !strings.Contains(err.Error(), "--source-root") || !reflect.DeepEqual(result.Unresolved, t02rManagedPaths()) {
		t.Fatalf("missing inherited source was silently retained: %+v, %v", result, err)
	}
	if !reflect.DeepEqual(projectFiles(t, c.Root), prior) {
		t.Fatal("missing source preflight wrote files")
	}
}

func TestInitRelocationInterruptedPublicationAndRetry(t *testing.T) {
	c := config(t)
	stop := errors.New("test interruption")
	first, err := initWithHooks(c, initHooks{afterPublish: func(string) error { return stop }})
	if !errors.Is(err, stop) || len(first.Created) != 1 || len(first.Unresolved) != 5 {
		t.Fatalf("first publication boundary lost: %+v, %v", first, err)
	}
	completed := filepath.Join(c.Root, filepath.FromSlash(first.Created[0]))
	content, err := os.ReadFile(completed)
	if err != nil || len(content) == 0 {
		t.Fatal("first created file is incomplete")
	}
	retry, err := Init(c)
	if err != nil || len(retry.Created) != 5 || len(retry.Unchanged) != 1 {
		t.Fatalf("interrupted init did not reconcile exact generated bytes: %+v, %v", retry, err)
	}
	newBinary := testpath.Exe(filepath.Join(t.TempDir(), "new-haft10"))
	fakeExecutable(t, newBinary)
	c.Binary = newBinary
	priorConfig, err := os.ReadFile(filepath.Join(c.Root, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	failed, err := initWithHooks(c, initHooks{beforePublish: func(name string) error {
		if name == ".codex/config.toml" {
			return stop
		}
		return nil
	}})
	if !errors.Is(err, stop) || !reflect.DeepEqual(failed.Unresolved, t02rManagedPaths()[4:]) || len(failed.Unchanged) != 4 {
		t.Fatalf("pre-rename interruption was not reported: %+v, %v", failed, err)
	}
	still, err := os.ReadFile(filepath.Join(c.Root, ".codex", "config.toml"))
	if err != nil || !bytes.Equal(still, priorConfig) {
		t.Fatal("pre-rename interruption changed config")
	}
	final, err := Init(c)
	if err != nil || !reflect.DeepEqual(final.Updated, []string{".codex/config.toml"}) {
		t.Fatalf("interrupted update did not retry: %+v, %v", final, err)
	}
}

func TestInitRelocationDetectsConcurrentEditsAndCooperatingInit(t *testing.T) {
	c := config(t)
	if _, err := Init(c); err != nil {
		t.Fatal(err)
	}
	newBinary := testpath.Exe(filepath.Join(t.TempDir(), "new-haft10"))
	fakeExecutable(t, newBinary)
	c.Binary = newBinary
	configPath := filepath.Join(c.Root, ".codex", "config.toml")
	operatorText := []byte("extra = \"operator concurrent edit\"\n")
	result, err := initWithHooks(c, initHooks{beforePublish: func(name string) error {
		if name != ".codex/config.toml" {
			return nil
		}
		second, lockErr := Init(c)
		if lockErr == nil || !strings.Contains(lockErr.Error(), "host_init_busy") || len(second.Updated) != 0 {
			t.Fatalf("cooperating init was not serialized: %+v, %v", second, lockErr)
		}
		current, readErr := os.ReadFile(configPath)
		if readErr != nil {
			return readErr
		}
		edited := bytes.Replace(current, []byte(configEnd), append(operatorText, []byte(configEnd)...), 1)
		return os.WriteFile(configPath, edited, 0600)
	}})
	if err == nil || !strings.Contains(err.Error(), "destination changed") || !reflect.DeepEqual(result.Unresolved, t02rManagedPaths()[4:]) || len(result.Unchanged) != 4 {
		t.Fatalf("concurrent edit was overwritten: %+v, %v", result, err)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil || !bytes.Contains(raw, operatorText) {
		t.Fatal("concurrent config edit was lost")
	}
	if _, err := Init(c); err == nil {
		t.Fatal("retry overwrote changed managed config")
	}
}

func TestInitRechecksUnchangedPaths(t *testing.T) {
	c := config(t)
	if _, err := Init(c); err != nil {
		t.Fatal(err)
	}
	firstSkill := filepath.Join(c.Root, ".agents", "skills", "h-decide", "SKILL.md")
	if err := os.Remove(firstSkill); err != nil {
		t.Fatal(err)
	}
	changedName := ".agents/skills/h-reason/SKILL.md"
	changedPath := filepath.Join(c.Root, filepath.FromSlash(changedName))
	result, err := initWithHooks(c, initHooks{afterPublish: func(name string) error {
		if name == ".agents/skills/h-decide/SKILL.md" {
			return os.WriteFile(changedPath, []byte("operator changed skill"), 0600)
		}
		return nil
	}})
	if err == nil || !strings.Contains(err.Error(), "destination changed") || len(result.Created) != 1 || len(result.Unresolved) == 0 || result.Unresolved[0] != changedName {
		t.Fatalf("unchanged path raced after preflight: %+v, %v", result, err)
	}
	raw, err := os.ReadFile(changedPath)
	if err != nil || string(raw) != "operator changed skill" {
		t.Fatal("unchanged path overwrite lost concurrent edit")
	}
}

func TestInitRejectsConcurrentPathTypeSwap(t *testing.T) {
	c := config(t)
	if _, err := Init(c); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(c.Root, ".codex", "config.toml")
	backup := filepath.Join(c.Root, ".codex", "operator-config.toml")
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	newBinary := testpath.Exe(filepath.Join(t.TempDir(), "new-haft10"))
	fakeExecutable(t, newBinary)
	c.Binary = newBinary
	result, err := initWithHooks(c, initHooks{beforePublish: func(name string) error {
		if name != ".codex/config.toml" {
			return nil
		}
		if err := os.Rename(configPath, backup); err != nil {
			return err
		}
		return os.Symlink(backup, configPath)
	}})
	if err == nil || !strings.HasPrefix(err.Error(), "host_conflict:") || !reflect.DeepEqual(result.Unresolved, t02rManagedPaths()[4:]) || len(result.Unchanged) != 4 {
		t.Fatalf("path-type race was not classified as a conflict: %+v, %v", result, err)
	}
	retained, err := os.ReadFile(backup)
	if err != nil || !bytes.Equal(retained, before) {
		t.Fatal("concurrent path-type swap changed original config")
	}
}
