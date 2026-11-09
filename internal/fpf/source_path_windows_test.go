//go:build windows

package fpf

import (
	"path/filepath"
	"strings"
	"testing"
)

// E4 (ADR-093): Windows-only tests of cleanSourcePath. The upstream tests run
// on every OS and assert the cleaned input path; these pin the slash form that
// fpfrefresh compares with its logical document paths.

func TestCleanSourcePathKeepsTheSlashFormOnWindows(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{`data\FPF\FPF-Spec.md`, "data/FPF/FPF-Spec.md"},
		{"data/FPF/FPF-Spec.md", "data/FPF/FPF-Spec.md"},
		{`data\FPF\..\FPF\Readme.md`, "data/FPF/Readme.md"},
		{`..\..\data\FPF\Readme.md`, "../../data/FPF/Readme.md"},
	} {
		if got := cleanSourcePath(tc.input); got != tc.want {
			t.Errorf("cleanSourcePath(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestLoadPublicationSnapshotRecordsSlashProvenancePathsOnWindows(t *testing.T) {
	readmePath := filepath.Join("..", "..", "data", "FPF", "Readme.md")
	specPath := filepath.Join("..", "..", "data", "FPF", "FPF-Spec.md")

	snapshot, err := LoadPublicationSnapshot(readmePath, specPath, "")
	if err != nil {
		t.Fatalf("LoadPublicationSnapshot() error: %v", err)
	}
	units := snapshot.SourceUnits()
	if len(units) == 0 {
		t.Fatal("snapshot has no source units")
	}
	for _, unit := range units {
		if strings.Contains(unit.Provenance.SourcePath, `\`) {
			t.Fatalf(
				"unit %q provenance path %q contains a backslash",
				unit.UnitID,
				unit.Provenance.SourcePath,
			)
		}
	}
	if got := snapshot.Spec().Path; got != "../../data/FPF/FPF-Spec.md" {
		t.Fatalf("spec path = %q, want the slash form of the cleaned input", got)
	}
}
