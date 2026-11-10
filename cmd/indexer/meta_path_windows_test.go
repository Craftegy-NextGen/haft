//go:build windows

package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// E4/E5 (ADR-093): the Windows form of TestBuildSourceIndexMetadata_RecordsBothSourceCarriers.
// The index is a committed generated carrier pinned by digest, so the recorded
// paths are the slash form on every OS.

func TestCleanMetaPathKeepsTheSlashFormOnWindows(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{`data\FPF\FPF-Spec.md`, "data/FPF/FPF-Spec.md"},
		{"data/FPF/FPF-Spec.md", "data/FPF/FPF-Spec.md"},
		{`data\FPF\..\FPF\Readme.md`, "data/FPF/Readme.md"},
		{`..\..\data\FPF\Readme.md`, "../../data/FPF/Readme.md"},
	} {
		if got := cleanMetaPath(tc.input); got != tc.want {
			t.Errorf("cleanMetaPath(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestBuildSourceIndexMetadata_RecordsSlashFormSourceCarriersOnWindows(t *testing.T) {
	buildTime := time.Date(2026, time.March, 26, 12, 34, 56, 0, time.UTC)
	dir := filepath.Join("data", "FPF")
	specPath := filepath.Join(dir, "FPF-Spec.md")
	readmePath := filepath.Join(dir, "Readme.md")
	metadata := buildSourceIndexMetadata(specPath, readmePath, 77, "revision", buildTime)

	if metadata["spec_path"] != "data/FPF/FPF-Spec.md" || metadata["readme_path"] != "data/FPF/Readme.md" {
		t.Fatalf("source carrier paths are not in slash form: %#v", metadata)
	}
	for key, value := range metadata {
		if strings.Contains(value, `\`) {
			t.Fatalf("metadata %q = %q contains a backslash", key, value)
		}
	}
}
