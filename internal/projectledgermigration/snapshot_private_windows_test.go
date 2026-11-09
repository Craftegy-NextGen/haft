//go:build windows

package projectledgermigration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/m0n0x41d/haft/internal/winfs"
)

// requirePrivateSnapshotFile fails unless path is owner-only by
// winfs.CheckPrivate (ADR-092 O2, 2a): the token user owns it, the DACL is
// protected and allows only that user, SYSTEM and Administrators, and it has
// one link.
func requirePrivateSnapshotFile(t *testing.T, path string) {
	t.Helper()
	file, err := winfs.OpenFile(path, os.O_RDONLY, winfs.Private)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := winfs.CheckPrivate(file); err != nil {
		t.Fatalf("snapshot %s is not private: %v", path, err)
	}
}

// notPrivateSnapshotFile reports whether winfs.CheckPrivate rejects path.
func notPrivateSnapshotFile(t *testing.T, path string) bool {
	t.Helper()
	file, err := winfs.OpenFile(path, os.O_RDONLY, winfs.Private)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	err = winfs.CheckPrivate(file)
	return errors.Is(err, winfs.ErrNotPrivate)
}

// The serve-migration snapshot, as published by EnsureCurrentForServe, is
// owner-only on Windows: VACUUM INTO writes into the private partial file that
// reserveSnapshotPartialPath created, and the publication rename keeps its
// security descriptor. The Unix counterpart is the 0600 assertion of
// assertSecureServeSnapshot.
func TestServeMigrationSnapshotIsOwnerOnlyOnWindows(t *testing.T) {
	fixture, databasePath := newSchema57ServeFixture(t, false)
	result, err := EnsureCurrentForServe(
		context.Background(),
		serveFixtureRequest(t, fixture),
		serveMigrationTestTime,
	)
	if err != nil {
		t.Fatalf("EnsureCurrentForServe: %v", err)
	}
	if result.BackupPath == "" {
		t.Fatalf("activation produced no snapshot: %#v", result)
	}
	requirePrivateSnapshotFile(t, result.BackupPath)
	if err := requireSecureSnapshotFile(result.BackupPath); err != nil {
		t.Fatalf("published snapshot rejected: %v", err)
	}
	partialPath, _ := serveMigrationSnapshotPaths(
		databasePath,
		57,
		58,
		serveMigrationTestTime,
	)
	if _, err := os.Lstat(partialPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial snapshot still present after publication: %v", err)
	}
	content, err := os.ReadFile(result.BackupPath)
	if err != nil || len(content) == 0 {
		t.Fatalf("snapshot content = %d bytes, %v", len(content), err)
	}
}

// A partial path that is taken is refused as on Unix
// (requireSnapshotPathAbsent), and the existing file is left as it was: not
// replaced by a private file, not truncated.
func TestReserveSnapshotPartialPathRefusesExistingFileOnWindows(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "snapshot.partial")
	if err := os.WriteFile(path, []byte("operator-owned partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !notPrivateSnapshotFile(t, path) {
		t.Skip("the test directory does not give new files an inherited ACL")
	}
	err := reserveSnapshotPartialPath(path)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing partial path error = %v, want already exists", err)
	}
	content, readErr := os.ReadFile(path)
	if readErr != nil || string(content) != "operator-owned partial" {
		t.Fatalf("existing partial content = %q, %v", content, readErr)
	}
	if !notPrivateSnapshotFile(t, path) {
		t.Fatal("existing partial file was replaced by a private file")
	}
}

// A free partial path becomes an empty owner-only file, and a second
// reservation of it is refused.
func TestReserveSnapshotPartialPathCreatesPrivateFileOnWindows(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "snapshot.partial")
	if err := reserveSnapshotPartialPath(path); err != nil {
		t.Fatalf("reserve free path: %v", err)
	}
	requirePrivateSnapshotFile(t, path)
	info, err := os.Lstat(path)
	if err != nil || info.Size() != 0 || !info.Mode().IsRegular() {
		t.Fatalf("reserved file = %v, %v; want an empty regular file", info, err)
	}
	if err := reserveSnapshotPartialPath(path); err == nil {
		t.Fatal("second reservation of the same path succeeded")
	}
	requirePrivateSnapshotFile(t, path)
}

// An existing final snapshot blocks the activation before any migration, as on
// Unix, and is left untouched.
func TestEnsureCurrentForServeLeavesFinalCollisionUntouchedOnWindows(
	t *testing.T,
) {
	fixture, databasePath := newSchema57ServeFixture(t, false)
	_, finalPath := serveMigrationSnapshotPaths(
		databasePath,
		57,
		58,
		serveMigrationTestTime,
	)
	if err := os.WriteFile(finalPath, []byte("operator-owned backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := EnsureCurrentForServe(
		context.Background(),
		serveFixtureRequest(t, fixture),
		serveMigrationTestTime,
	)
	if err == nil || result.Blocker != ServeBlockerSnapshot {
		t.Fatalf("final collision activation = %#v, %v", result, err)
	}
	content, readErr := os.ReadFile(finalPath)
	if readErr != nil || string(content) != "operator-owned backup" {
		t.Fatalf("final collision content = %q, %v", content, readErr)
	}
	if frontier := readSchemaFrontierForTest(t, databasePath); frontier != 57 {
		t.Fatalf("schema after final collision = %d, want 57", frontier)
	}
}
