//go:build windows

package fpfrefresh

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/m0n0x41d/haft/internal/winfs"
)

// E4 (ADR-093): Windows-only tests of the owner-only state directory
// (ADR-092 O2). The upstream tests assert the Unix 0o700 image through
// stateDirectoryModeNotPrivate, which on Windows can only reject read-only;
// these check the owner and DACL itself with winfs.CheckPrivate.

func requireStateDirectoryPrivate(t *testing.T, path string) {
	t.Helper()
	directory, err := winfs.OpenDir(path)
	if err != nil {
		t.Fatalf("open state directory %s: %v", path, err)
	}
	defer func() {
		_ = directory.Close()
	}()
	if err := winfs.CheckPrivate(directory.File()); err != nil {
		t.Fatalf("state directory %s is not owner-only: %v", path, err)
	}
}

func TestCreateStateDirectoryIsOwnerOnlyAndCreatesMissingParents(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, ".context", "fpf-refresh")

	if err := createStateDirectory(state); err != nil {
		t.Fatalf("createStateDirectory() error = %v", err)
	}
	requireStateDirectoryPrivate(t, state)
	info, err := os.Lstat(state)
	if err != nil {
		t.Fatal(err)
	}
	if stateDirectoryNotPrivate(info) {
		t.Fatal("the production predicate rejects the directory it was created for")
	}
}

func TestCreateStateDirectoryAcceptsAnExistingDirectoryUnchanged(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := createStateDirectory(state); err != nil {
		t.Fatalf("createStateDirectory() on an existing directory error = %v", err)
	}
	if err := createStateDirectory(state); err != nil {
		t.Fatalf("second createStateDirectory() error = %v", err)
	}
}

func TestCreateStateDirectoryRejectsAFileAtThePath(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(state, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := createStateDirectory(state)
	if !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("createStateDirectory() over a file = %v, want ENOTDIR", err)
	}
}

func TestRepositoryVerificationLockCreatesAnOwnerOnlyStateDirectory(t *testing.T) {
	root := t.TempDir()
	layout, err := ResolveRepositoryLayout(root)
	if err != nil {
		t.Fatal(err)
	}
	release, err := acquireRepositoryVerificationOperationLock(layout)
	if err != nil {
		t.Fatalf("acquireRepositoryVerificationOperationLock() error = %v", err)
	}
	defer release()
	requireStateDirectoryPrivate(t, layout.StateDirectory)
}

func TestCheckCandidateCreatesAnOwnerOnlyStateDirectoryBeforeAnyWork(t *testing.T) {
	root := t.TempDir()
	layout, err := ResolveRepositoryLayout(root)
	if err != nil {
		t.Fatal(err)
	}
	builder := IndexBuilderFunc(func(context.Context, IndexBuildInput) error {
		return errors.New("builder must not run")
	})
	gate := CandidateTokenGateFunc(func(context.Context, CandidateTokenGateInput) error {
		return errors.New("token gate must not run")
	})

	// The fixture has no database, so the check stops after it created the
	// state directory; what matters here is the directory it left.
	_, err = CheckCandidate(context.Background(), CandidateCheckRequest{
		Layout:            layout,
		CandidateRef:      "0000000000000000000000000000000000000000",
		Builder:           builder,
		ToolRevision:      "fpf-refresh-inputs/v1:sha256:fixture",
		TokenGateVerifier: gate,
	})
	if err == nil {
		t.Fatal("CheckCandidate() over an empty repository succeeded")
	}
	requireStateDirectoryPrivate(t, layout.StateDirectory)
}
