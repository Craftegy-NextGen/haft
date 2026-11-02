package initexecution

import (
	"path/filepath"
	"runtime"
	"testing"
)

func volumeRootOfTempDir(t *testing.T) string {
	t.Helper()
	return filepath.VolumeName(t.TempDir()) + string(filepath.Separator)
}

func TestCanonicalNonRootPathRejectsTheVolumeRootOnEveryOS(t *testing.T) {
	root := volumeRootOfTempDir(t)
	if canonicalNonRootPath(root) {
		t.Fatalf("canonicalNonRootPath(%q) = true, want companion paths to reject the volume root", root)
	}
}

// A volume root is a project root on Windows only; elsewhere the upstream rule applies.
func TestCanonicalProjectRootPathVolumeRootRule(t *testing.T) {
	root := volumeRootOfTempDir(t)
	if got, want := canonicalProjectRootPath(root), runtime.GOOS == "windows"; got != want {
		t.Fatalf("canonicalProjectRootPath(%q) = %v on %s, want %v", root, got, runtime.GOOS, want)
	}
}

func TestCanonicalProjectRootPathRequiresTheCanonicalForm(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"", root + string(filepath.Separator), "relative"} {
		if canonicalProjectRootPath(path) {
			t.Fatalf("canonicalProjectRootPath(%q) = true, want a non-canonical root rejected", path)
		}
	}
	if !canonicalProjectRootPath(root) {
		t.Fatalf("canonicalProjectRootPath(%q) = false, want a canonical root accepted", root)
	}
}
