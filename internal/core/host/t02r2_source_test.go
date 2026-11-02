package host

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/m0n0x41d/haft/internal/testpath"
)

func TestT02R2HistoricalLinkedExternalSourceSurvivesOriginalMove(t *testing.T) {
	cases := []struct {
		name   string
		digest string
	}{
		{"t01ar", "661c2ef3731f6eec595a34b0de1dc01d58cbdd9c625bdf2feb3d2f2ce4377ec1"},
		{"t01br", "2465ac9eb71579c3ecff4f79d7683757e1df62526b669ed2e0cb76da3be135fa"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", "t02r", tc.name+"-config.toml"))
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(raw)
			if hex.EncodeToString(digest[:]) != tc.digest {
				t.Fatal("historical generated config digest changed")
			}
			_, _, block, outsideBefore, err := managedSlice(raw, configStart, configEnd)
			if err != nil {
				t.Fatal(err)
			}
			prior, err := parseManagedConfig(block)
			if err != nil {
				t.Fatal(err)
			}

			base := t.TempDir()
			original := filepath.Join(base, "original")
			copied := filepath.Join(base, "copied")
			shared := filepath.Join(base, "shared", "FPF")
			linked := filepath.Join(original, "linked-source")
			oldBinary := filepath.Join(base, "old-bin", "haft10")
			newBinary := filepath.Join(base, "new-bin", "haft10")
			fakeExecutable(t, oldBinary)
			fakeExecutable(t, newBinary)
			write(t, shared, "PIN", "shared publication")
			if err := os.MkdirAll(original, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(shared, linked); err != nil {
				t.Fatal(err)
			}
			addressed := historicalConfigAddress(t, raw, prior, managedConfig{
				Root: original, Binary: oldBinary, SourceRoot: linked,
			})
			write(t, original, ".codex/config.toml", string(addressed))
			write(t, original, "operator.txt", "keep original")
			write(t, copied, ".codex/config.toml", string(addressed))
			write(t, copied, "operator.txt", "keep copied")
			originalConfigBefore, err := os.ReadFile(filepath.Join(original, ".codex", "config.toml"))
			if err != nil {
				t.Fatal(err)
			}
			copiedBefore := projectFiles(t, copied)
			result, err := Init(Config{Root: copied, Binary: newBinary, Codex: true})
			if err != nil || !reflect.DeepEqual(result.Updated, []string{".codex/config.toml"}) {
				t.Fatalf("historical linked source was not inherited: %+v, %v", result, err)
			}
			installed := installedConfig(t, copied)
			physicalShared, err := filepath.EvalSymlinks(shared)
			if err != nil {
				t.Fatal(err)
			}
			if installed.SourceRoot != physicalShared || installed.SourceRoot == linked || installed.SourceRepository != prior.SourceRepository || installed.Profile != prior.Profile {
				t.Fatalf("inherited source identity or metadata changed: %+v", installed)
			}
			updated, err := os.ReadFile(filepath.Join(copied, ".codex", "config.toml"))
			if err != nil {
				t.Fatal(err)
			}
			_, _, _, outsideAfter, err := managedSlice(updated, configStart, configEnd)
			if err != nil || !bytes.Equal(outsideBefore, outsideAfter) {
				t.Fatal("foreign config bytes changed", err)
			}
			originalConfigAfter, err := os.ReadFile(filepath.Join(original, ".codex", "config.toml"))
			if err != nil || !bytes.Equal(originalConfigBefore, originalConfigAfter) {
				t.Fatal("original config changed", err)
			}
			if !bytes.Equal(copiedBefore["operator.txt"].Bytes, projectFiles(t, copied)["operator.txt"].Bytes) {
				t.Fatal("copied operator file changed")
			}

			if err := os.Rename(original, filepath.Join(base, "original-moved")); err != nil {
				t.Fatal(err)
			}
			retry, err := Init(Config{Root: copied, Binary: newBinary, Codex: true})
			if err != nil || len(retry.Created) != 0 || len(retry.Updated) != 0 || len(retry.Unchanged) != 6 {
				t.Fatalf("physical source identity did not survive original move: %+v, %v", retry, err)
			}
		})
	}
}

func TestT02R2T02PhysicalSourceControlAndMissingSource(t *testing.T) {
	base := t.TempDir()
	original := filepath.Join(base, "original")
	copied := filepath.Join(base, "copied")
	shared := filepath.Join(base, "shared", "FPF")
	binary := testpath.Exe(filepath.Join(base, "bin", "haft10"))
	fakeExecutable(t, binary)
	write(t, original, "operator.txt", "keep")
	write(t, shared, "PIN", "shared publication")
	if _, err := Init(Config{Root: original, Binary: binary, SourceRoot: shared, SourceRepository: "pinned://shared", Codex: true}); err != nil {
		t.Fatal(err)
	}
	copyProject(t, original, copied)
	result, err := Init(Config{Root: copied, Binary: binary, Codex: true})
	if err != nil || !reflect.DeepEqual(result.Updated, []string{".codex/config.toml"}) {
		t.Fatalf("T02 physical source was not inherited: %+v, %v", result, err)
	}
	installed := installedConfig(t, copied)
	if installed.SourceRoot != physicalOrRecordedPath(shared) || installed.SourceRepository != "pinned://shared" {
		t.Fatalf("T02 source identity changed: %+v", installed)
	}
	if err := os.Rename(original, filepath.Join(base, "original-moved")); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(Config{Root: copied, Binary: binary, Codex: true}); err != nil {
		t.Fatalf("inherited physical source did not survive original move: %v", err)
	}
	if err := os.Rename(shared, filepath.Join(base, "shared-moved")); err != nil {
		t.Fatal(err)
	}
	before := projectFiles(t, copied)
	missing, err := Init(Config{Root: copied, Binary: binary, Codex: true})
	if err == nil || !strings.HasPrefix(err.Error(), "host_source_unavailable:") || !strings.Contains(err.Error()[:min(240, len(err.Error()))], "--source-root") {
		t.Fatalf("missing source recovery was not actionable: %+v, %v", missing, err)
	}
	if len(missing.Created) != 0 || len(missing.Updated) != 0 || !reflect.DeepEqual(projectFiles(t, copied), before) {
		t.Fatalf("missing inherited source wrote to project: %+v", missing)
	}
}

func TestT02R2AvailableRootIdentityUsesSameFile(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ProjectCase")
	otherSpelling := filepath.Join(base, "projectcase")
	binary := testpath.Exe(filepath.Join(base, "bin", "haft10"))
	write(t, root, "FPF/PIN", "pinned")
	fakeExecutable(t, binary)
	rootInfo, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	otherInfo, err := os.Stat(otherSpelling)
	if err != nil {
		t.Skip("filesystem does not resolve case-variant paths to the same directory")
	}
	if !os.SameFile(rootInfo, otherInfo) {
		t.Fatal("case-variant spelling unexpectedly resolved to a different directory")
	}
	if _, err := Init(Config{Root: root, Binary: binary, SourceRoot: filepath.Join(root, "FPF"), Codex: true}); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, ".codex", "config.toml")
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	_, _, block, _, err := managedSlice(raw, configStart, configEnd)
	if err != nil {
		t.Fatal(err)
	}
	prior, err := parseManagedConfig(block)
	if err != nil {
		t.Fatal(err)
	}
	if prior.Root != physicalOrRecordedPath(root) || prior.SourceRoot != physicalOrRecordedPath(filepath.Join(root, "FPF")) {
		t.Fatalf("recorded root and source must start with the original spelling: %+v", prior)
	}
	result, err := Init(Config{Root: otherSpelling, Binary: binary, Codex: true})
	if err != nil || !reflect.DeepEqual(result.Updated, []string{".codex/config.toml"}) {
		t.Fatalf("same inode with alternate case was refused: %+v, %v", result, err)
	}
	installed := installedConfig(t, otherSpelling)
	if installed.Root != physicalOrRecordedPath(otherSpelling) || installed.SourceRoot != prior.SourceRoot {
		t.Fatalf("same-inode retry lost the source or new root spelling: %+v", installed)
	}
	retry, err := Init(Config{Root: otherSpelling, Binary: binary, Codex: true})
	if err != nil || len(retry.Created) != 0 || len(retry.Updated) != 0 || len(retry.Unchanged) != 6 {
		t.Fatalf("same-inode retry was not unchanged: %+v, %v", retry, err)
	}
}

func TestT02R2DistinctCaseVariantRootsStayDistinct(t *testing.T) {
	base := t.TempDir()
	original := filepath.Join(base, "ProjectCase")
	copied := filepath.Join(base, "projectcase")
	binary := filepath.Join(base, "bin", "haft10")
	write(t, original, "FPF/PIN", "pinned")
	fakeExecutable(t, binary)
	if _, err := os.Stat(copied); err == nil {
		t.Skip("filesystem aliases case-variant paths")
	}
	if _, err := Init(Config{Root: original, Binary: binary, SourceRoot: filepath.Join(original, "FPF"), Codex: true}); err != nil {
		t.Fatal(err)
	}
	copyProject(t, original, copied)
	requireSourceSelectionWithoutWrite(t, Config{Root: copied, Binary: binary, Codex: true})
}

func TestT02R2UnavailableOldRootUsesRecordedPathFallback(t *testing.T) {
	base := t.TempDir()
	old := filepath.Join(base, "missing-old")
	current := filepath.Join(base, "current")
	if err := os.MkdirAll(current, 0700); err != nil {
		t.Fatal(err)
	}
	if sameProjectRoot(old, current) {
		t.Fatal("missing old root was treated as the current directory")
	}
	if !sameProjectRoot(filepath.Join(base, "no-such", "..", "current"), current) {
		t.Fatal("recorded-path fallback did not normalize exact spelling")
	}
}
