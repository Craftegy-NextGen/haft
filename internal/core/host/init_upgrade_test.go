package host

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var b1Fixtures = map[string]string{
	"AGENTS.md":                        "AGENTS.md",
	".agents/skills/h-decide/SKILL.md": "h-decide.md",
	".agents/skills/h-reason/SKILL.md": "h-reason.md",
	".agents/skills/h-spec/SKILL.md":   "h-spec.md",
	".agents/skills/h-verify/SKILL.md": "h-verify.md",
}

func b1Content(t *testing.T, name string) []byte {
	t.Helper()
	fixture, ok := b1Fixtures[name]
	if !ok {
		t.Fatalf("unknown B1 fixture %s", name)
	}
	path := filepath.Join("testdata", "b1", fixture)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !knownB1Generated(name, raw) {
		t.Fatalf("B1 fixture digest changed: %s", name)
	}
	return raw
}

func TestInitUpgradesExactB1AssetsAndPreservesUserText(t *testing.T) {
	c := config(t)
	prefix := "# Operator instructions\n\nKeep this prelude.\n\n"
	suffix := "\nKeep this suffix.\n"
	oldAgents := b1Content(t, "AGENTS.md")
	write(t, c.Root, "AGENTS.md", prefix+string(oldAgents)+suffix)
	for name := range b1Fixtures {
		if name == "AGENTS.md" {
			continue
		}
		write(t, c.Root, name, string(b1Content(t, name)))
	}
	first, err := Init(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Created) != 1 || len(first.Updated) != 5 {
		t.Fatalf("expected config create and five exact upgrades: %+v", first)
	}
	installedAgents, err := os.ReadFile(filepath.Join(c.Root, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	wantAgents := prefix + agents + suffix
	if string(installedAgents) != wantAgents {
		t.Fatal("managed AGENTS upgrade changed operator text or missed task-tool block")
	}
	for name, want := range Skills() {
		path := filepath.Join(c.Root, ".agents", "skills", name, "SKILL.md")
		installed, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(installed) != want {
			t.Fatalf("skill %s did not advance to task tools", name)
		}
		info, err := os.Stat(path)
		if err != nil || existingSkillModeChanged(info) {
			t.Fatalf("skill %s lost existing file mode", name)
		}
	}
	second, err := Init(c)
	if err != nil || len(second.Created) != 0 || len(second.Updated) != 0 || len(second.Unchanged) != 6 {
		t.Fatalf("upgraded rerun changed files: %+v, %v", second, err)
	}
}

func TestInitRejectsEditedB1AssetsBeforeAnyWrite(t *testing.T) {
	for _, edited := range []string{"AGENTS.md", ".agents/skills/h-spec/SKILL.md"} {
		t.Run(edited, func(t *testing.T) {
			c := config(t)
			before := map[string][]byte{}
			for name := range b1Fixtures {
				raw := b1Content(t, name)
				if name == edited {
					raw = bytes.Replace(raw, []byte("haft.api/2"), []byte("haft.api/3"), 1)
				}
				before[name] = raw
				write(t, c.Root, name, string(raw))
			}
			_, err := Init(c)
			if err == nil || !strings.Contains(err.Error(), "host_conflict") {
				t.Fatalf("edited %s was upgraded: %v", edited, err)
			}
			for name, want := range before {
				got, err := os.ReadFile(filepath.Join(c.Root, name))
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("preflight changed %s after edited %s", name, edited)
				}
			}
			configPath := filepath.Join(c.Root, ".codex", "config.toml")
			if _, err := os.Stat(configPath); !os.IsNotExist(err) {
				t.Fatal("created MCP config before complete preflight")
			}
		})
	}
}

func TestInitB1UpgradeKeepsExistingMCPConfig(t *testing.T) {
	c := config(t)
	if _, err := Init(c); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(c.Root, ".codex", "config.toml")
	priorConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for name := range b1Fixtures {
		write(t, c.Root, name, string(b1Content(t, name)))
	}
	result, err := Init(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Created) != 0 || len(result.Updated) != 5 || len(result.Unchanged) != 1 {
		t.Fatalf("unexpected re-init result: %+v", result)
	}
	currentConfig, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(currentConfig, priorConfig) {
		t.Fatal("re-init changed existing MCP config")
	}
}
