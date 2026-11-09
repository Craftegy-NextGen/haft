package host

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/m0n0x41d/haft/internal/testpath"
)

var t02GeneratedFixtures = map[string]string{
	"AGENTS.md":                        "host-agents.txt",
	".agents/skills/h-decide/SKILL.md": "h-decide.md",
	".agents/skills/h-reason/SKILL.md": "h-reason.md",
	".agents/skills/h-spec/SKILL.md":   "h-spec.md",
	".agents/skills/h-verify/SKILL.md": "h-verify.md",
}

var t02GeneratedVersions = map[string]map[string]string{
	"t01ar": t01arGeneratedDigests,
	"t01r2": t01r2GeneratedDigests,
	"t01br": t01brGeneratedDigests,
}

func t02GeneratedContent(t *testing.T, version, name string) []byte {
	t.Helper()
	digests, ok := t02GeneratedVersions[version]
	if !ok {
		t.Fatalf("unknown generated version %s", version)
	}
	fixture, ok := t02GeneratedFixtures[name]
	if !ok {
		t.Fatalf("unknown generated asset %s", name)
	}
	path := filepath.Join("testdata", "t02", version, fixture)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !generatedDigestMatches(digests, name, raw) {
		t.Fatalf("%s generated fixture digest changed: %s", version, name)
	}
	return raw
}

func TestT02KnownGeneratedAssetsUpgradeAtSameLocation(t *testing.T) {
	for _, version := range []string{"t01ar", "t01r2", "t01br"} {
		t.Run(version, func(t *testing.T) {
			c := config(t)
			if _, err := Init(c); err != nil {
				t.Fatal(err)
			}
			configPath := filepath.Join(c.Root, ".codex", "config.toml")
			oldConfig, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			prefix := "# Operator instructions\n\nKeep this prelude.\n\n"
			suffix := "\nKeep this suffix.\n"
			for name := range t02GeneratedFixtures {
				content := string(t02GeneratedContent(t, version, name))
				if name == "AGENTS.md" {
					content = prefix + content + suffix
				}
				write(t, c.Root, name, content)
			}
			if _, err := Init(c); err != nil {
				t.Fatalf("%s unchanged generated assets did not upgrade: %v", version, err)
			}
			gotConfig, err := os.ReadFile(configPath)
			if err != nil || !bytes.Equal(gotConfig, oldConfig) {
				t.Fatal("same-location upgrade changed the MCP address")
			}
			gotAgents, err := os.ReadFile(filepath.Join(c.Root, "AGENTS.md"))
			if err != nil || string(gotAgents) != prefix+agents+suffix {
				t.Fatalf("%s upgrade changed operator instructions: %v", version, err)
			}
			for name, want := range Skills() {
				path := filepath.Join(c.Root, ".agents", "skills", name, "SKILL.md")
				got, err := os.ReadFile(path)
				if err != nil || string(got) != want {
					t.Fatalf("%s upgrade missed %s: %v", version, name, err)
				}
			}
			second, err := Init(c)
			if err != nil || len(second.Created) != 0 || len(second.Updated) != 0 {
				t.Fatalf("%s retry changed upgraded assets: %+v, %v", version, second, err)
			}
		})
	}
}

func TestT02CopiedProjectUpgradesT01ARAssetsAndAddresses(t *testing.T) {
	parent := t.TempDir()
	oldRoot := filepath.Join(parent, "original")
	copyRoot := filepath.Join(parent, "copy")
	oldSource := filepath.Join(parent, "source-old")
	newSource := filepath.Join(parent, "source-new")
	oldBinary := testpath.Exe(filepath.Join(parent, "binary-old"))
	newBinary := testpath.Exe(filepath.Join(parent, "binary-new"))
	for _, path := range []string{oldRoot, copyRoot, oldSource} {
		if err := os.Mkdir(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(oldBinary, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	prior := Config{Root: oldRoot, Binary: oldBinary, SourceRoot: oldSource, Codex: true}
	if _, err := Init(prior); err != nil {
		t.Fatal(err)
	}
	for name := range t02GeneratedFixtures {
		write(t, oldRoot, name, string(t02GeneratedContent(t, "t01ar", name)))
	}
	original := map[string][]byte{}
	for name := range t02GeneratedFixtures {
		path := filepath.Join(oldRoot, filepath.FromSlash(name))
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		original[name] = raw
	}
	configName := ".codex/config.toml"
	oldConfig, err := os.ReadFile(filepath.Join(oldRoot, configName))
	if err != nil {
		t.Fatal(err)
	}
	original[configName] = oldConfig
	for name, raw := range original {
		write(t, copyRoot, name, string(raw))
	}
	if err := os.Rename(oldBinary, newBinary); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldSource, newSource); err != nil {
		t.Fatal(err)
	}
	current := Config{Root: copyRoot, Binary: newBinary, SourceRoot: newSource, Codex: true}
	first, err := Init(current)
	if err != nil || len(first.Created) != 0 || len(first.Updated) != 6 {
		t.Fatalf("copied T01AR project did not update six owned files: %+v, %v", first, err)
	}
	newConfig, err := os.ReadFile(filepath.Join(copyRoot, configName))
	if err != nil {
		t.Fatal(err)
	}
	_, _, block, _, err := managedSlice(newConfig, configStart, configEnd)
	if err != nil {
		t.Fatal(err)
	}
	addresses, err := parseManagedConfig(block)
	if err != nil {
		t.Fatal(err)
	}
	wantRoot, err := filepath.EvalSymlinks(copyRoot)
	if err != nil {
		t.Fatal(err)
	}
	wantSource, err := filepath.EvalSymlinks(newSource)
	if err != nil {
		t.Fatal(err)
	}
	wantBinary, err := filepath.EvalSymlinks(newBinary)
	if err != nil {
		t.Fatal(err)
	}
	if addresses.Root != wantRoot || addresses.Binary != wantBinary || addresses.SourceRoot != wantSource {
		t.Fatalf("copied project retained old managed addresses: %+v", addresses)
	}
	for name, raw := range original {
		got, err := os.ReadFile(filepath.Join(oldRoot, filepath.FromSlash(name)))
		if err != nil || !bytes.Equal(got, raw) {
			t.Fatalf("upgrading copy changed original %s: %v", name, err)
		}
	}
	second, err := Init(current)
	if err != nil || len(second.Updated) != 0 || len(second.Created) != 0 {
		t.Fatalf("copied project retry changed managed files: %+v, %v", second, err)
	}
}

func TestT02EditedKnownGeneratedAssetRefusesBeforeWrite(t *testing.T) {
	for _, version := range []string{"t01ar", "t01r2", "t01br"} {
		t.Run(version, func(t *testing.T) {
			c := config(t)
			before := map[string][]byte{}
			for name := range t02GeneratedFixtures {
				raw := t02GeneratedContent(t, version, name)
				if name == ".agents/skills/h-decide/SKILL.md" {
					raw = append(bytes.Clone(raw), []byte("\n# User change\n")...)
				}
				before[name] = raw
				write(t, c.Root, name, string(raw))
			}
			_, err := Init(c)
			if err == nil || !strings.Contains(err.Error(), "host_conflict") {
				t.Fatalf("%s edited asset was accepted: %v", version, err)
			}
			for name, want := range before {
				got, err := os.ReadFile(filepath.Join(c.Root, filepath.FromSlash(name)))
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("%s refusal changed %s: %v", version, name, err)
				}
			}
			configPath := filepath.Join(c.Root, ".codex", "config.toml")
			if _, err := os.Stat(configPath); !os.IsNotExist(err) {
				t.Fatalf("%s refusal created config before preflight", version)
			}
		})
	}
}
