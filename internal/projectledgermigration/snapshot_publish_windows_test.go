//go:build windows

package projectledgermigration

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// The Windows counterpart of TestPublishServeMigrationSnapshotNeverReplacesExistingBackup
// (snapshot_publish_test.go, darwin || linux), with the same scenario and
// expectations, plus the ErrExist classification of the refusal.
func TestPublishServeMigrationSnapshotNeverReplacesExistingBackupOnWindows(
	t *testing.T,
) {
	directory := t.TempDir()
	partialPath := filepath.Join(directory, "snapshot.partial")
	finalPath := filepath.Join(directory, "snapshot.bak")
	if err := os.WriteFile(partialPath, []byte("new snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(finalPath, []byte("existing backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := publishServeMigrationSnapshot(partialPath, finalPath)
	if err == nil {
		t.Fatal("snapshot publication replaced an existing backup")
	}
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("refused publication error = %v, want fs.ErrExist", err)
	}
	requireSnapshotPublicationContent(t, partialPath, "new snapshot")
	requireSnapshotPublicationContent(t, finalPath, "existing backup")
}

// Publication into an absent name moves the partial file there.
func TestPublishServeMigrationSnapshotMovesIntoAbsentName(t *testing.T) {
	directory := t.TempDir()
	partialPath := filepath.Join(directory, "snapshot.partial")
	finalPath := filepath.Join(directory, "snapshot.bak")
	if err := os.WriteFile(partialPath, []byte("new snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := publishServeMigrationSnapshot(partialPath, finalPath); err != nil {
		t.Fatalf("publish: %v", err)
	}
	requireSnapshotPublicationContent(t, finalPath, "new snapshot")
	if _, err := os.Lstat(partialPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial snapshot still present after publication: %v", err)
	}
}

// The published snapshot passes requireSecureSnapshotFile after the
// os.Chmod(0o600) of createServeMigrationSnapshot, and a read-only snapshot
// (not the image of 0600 on Windows) does not.
func TestRequireSecureSnapshotFileOnWindows(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "snapshot.bak")
	if err := os.WriteFile(path, []byte("snapshot"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := requireSecureSnapshotFile(path); err != nil {
		t.Fatalf("snapshot after chmod 0600 was rejected: %v", err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if err := requireSecureSnapshotFile(path); err == nil {
		t.Fatal("read-only snapshot was accepted")
	}
}

func requireSnapshotPublicationContent(
	t *testing.T,
	path string,
	want string,
) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != want {
		t.Fatalf("snapshot publication content = %q, want %q", content, want)
	}
}
