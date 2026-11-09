//go:build windows

package embedding

import (
	"os"
	"path/filepath"
	"testing"
)

// E4 (ADR-093): Windows decides that a file is executable by its extension
// (PATHEXT), so HAFT_EMBED_BIN and the managed locations were never accepted
// while isExecutableFile tested Unix permission bits.
func TestIsExecutableFileAcceptsPathextExtensionsOnly(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}

	for _, name := range []string{"haft-embed.exe", "UPPER.EXE"} {
		if !isExecutableFile(write(name)) {
			t.Errorf("isExecutableFile(%s) = false, want true", name)
		}
	}
	for _, name := range []string{"haft-embed", "notes.txt"} {
		if isExecutableFile(write(name)) {
			t.Errorf("isExecutableFile(%s) = true, want false", name)
		}
	}
	if isExecutableFile(dir) {
		t.Error("isExecutableFile(directory) = true, want false")
	}
	if isExecutableFile(filepath.Join(dir, "missing.exe")) {
		t.Error("isExecutableFile(missing file) = true, want false")
	}
}

func TestSidecarCandidatesNameTheExecutableWithItsExtension(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)

	candidates := sidecarCandidates()
	if len(candidates) != 4 {
		t.Fatalf("sidecarCandidates() = %v, want 4 candidates", candidates)
	}
	for _, candidate := range candidates {
		if filepath.Base(candidate) != sidecarBinaryName+".exe" {
			t.Errorf("candidate %q does not end in %s.exe", candidate, sidecarBinaryName)
		}
	}
}

func TestLocateSidecarAcceptsAnExecutableNamedByTheOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fake-haft-embed.exe")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write fake sidecar: %v", err)
	}
	t.Setenv(sidecarBinaryEnv, path)

	got, found := locateSidecar()
	if !found || got != path {
		t.Fatalf("locateSidecar() = %q, %v; want %q, true", got, found, path)
	}
}
