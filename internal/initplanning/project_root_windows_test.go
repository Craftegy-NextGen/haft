package initplanning

import "testing"

func TestParseInitIntentAcceptsAVolumeRootProject(t *testing.T) {
	root := volumeRootOfTempDir(t)
	intent, err := ParseInitIntent(WeakInitIntent{
		InvocationPolicy: string(InvocationExplicit),
		ProjectRoot:      root,
		ProjectID:        "qnt_e3149c17",
	})
	if err != nil {
		t.Fatalf("ParseInitIntent with project root %q: %v", root, err)
	}
	if intent.ProjectRoot() != root {
		t.Fatalf("project root = %q, want %q", intent.ProjectRoot(), root)
	}
}
