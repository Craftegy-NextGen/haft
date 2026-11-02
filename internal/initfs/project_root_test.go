package initfs

import (
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// A volume root is a publication resource on Windows only; elsewhere the upstream rule applies.
func TestCanonicalPublicationResourceSetVolumeRootRule(t *testing.T) {
	root := filepath.VolumeName(t.TempDir()) + string(filepath.Separator)
	child := filepath.Join(root, "project")
	resources, _, err := canonicalPublicationResourceSet([]string{child, root})
	if runtime.GOOS == "windows" {
		if err != nil || !slices.Contains(resources, root) || !slices.Contains(resources, child) {
			t.Fatalf("resources = %v, %v; want both %q and %q on Windows", resources, err, root, child)
		}
		return
	}
	if err == nil || !strings.Contains(err.Error(), "filesystem root cannot be a publication resource") {
		t.Fatalf("canonicalPublicationResourceSet with %q: error = %v, want the filesystem root rejection", root, err)
	}
}
