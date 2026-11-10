//go:build !windows

package main

import (
	"path/filepath"
	"testing"
	"time"
)

// E3 (ADR-093): moved verbatim from main_test.go. It records the native path of the
// source carriers, which is the slash form only outside Windows; the Windows
// sibling is in meta_path_windows_test.go.

func TestBuildSourceIndexMetadata_RecordsBothSourceCarriers(t *testing.T) {
	buildTime := time.Date(2026, time.March, 26, 12, 34, 56, 0, time.UTC)
	dir := t.TempDir()
	specPath := filepath.Join(dir, "FPF-Spec.md")
	readmePath := filepath.Join(dir, "Readme.md")
	metadata := buildSourceIndexMetadata(specPath, readmePath, 77, "revision", buildTime)

	if metadata["fpf_commit"] != "revision" {
		t.Fatalf("fpf_commit = %q", metadata["fpf_commit"])
	}
	if metadata["indexed_sections"] != "0" || metadata["indexed_source_units"] != "77" {
		t.Fatalf("unexpected counts: %#v", metadata)
	}
	if metadata["spec_path"] != specPath || metadata["readme_path"] != readmePath {
		t.Fatalf("source carrier paths missing: %#v", metadata)
	}
}
