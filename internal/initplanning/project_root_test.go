package initplanning

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func volumeRootOfTempDir(t *testing.T) string {
	t.Helper()
	return filepath.VolumeName(t.TempDir()) + string(filepath.Separator)
}

func TestParseCanonicalAbsolutePathRejectsTheVolumeRootOnEveryOS(t *testing.T) {
	root := volumeRootOfTempDir(t)
	_, err := parseCanonicalAbsolutePath(root)
	if err == nil || !strings.Contains(err.Error(), "filesystem root cannot be an initialization target") {
		t.Fatalf("parseCanonicalAbsolutePath(%q) error = %v, want the filesystem root rejection", root, err)
	}
}

// A volume root is a project root on Windows only; elsewhere the upstream rule applies.
func TestParseCanonicalProjectRootVolumeRootRule(t *testing.T) {
	root := volumeRootOfTempDir(t)
	got, err := parseCanonicalProjectRoot(root)
	if runtime.GOOS == "windows" {
		if err != nil || got != root {
			t.Fatalf("parseCanonicalProjectRoot(%q) = %q, %v; want the volume root accepted on Windows", root, got, err)
		}
		return
	}
	if err == nil || !strings.Contains(err.Error(), "filesystem root cannot be an initialization target") {
		t.Fatalf("parseCanonicalProjectRoot(%q) error = %v, want the filesystem root rejection", root, err)
	}
}

func TestParseCanonicalProjectRootRequiresTheCanonicalForm(t *testing.T) {
	root := canonicalTempRoot(t)
	for _, raw := range []string{"", " " + root, root + string(filepath.Separator), "relative"} {
		if _, err := parseCanonicalProjectRoot(raw); err == nil {
			t.Fatalf("parseCanonicalProjectRoot(%q) accepted a non-canonical root", raw)
		}
	}
	got, err := parseCanonicalProjectRoot(root)
	if err != nil || got != root {
		t.Fatalf("parseCanonicalProjectRoot(%q) = %q, %v; want the root unchanged", root, got, err)
	}
}
