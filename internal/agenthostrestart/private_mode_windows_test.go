//go:build windows

package agenthostrestart

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/m0n0x41d/haft/internal/winfs"
)

// Windows counterparts of the tests in private_mode_unix_test.go (ADR-093 E3).
// Those tests read Unix permission bits and weaken them with chmod; Windows
// reports 0o666 for every file, so the same contract is checked through the
// owner and protected owner-only DACL that the store creates (ADR-092 O2, 2a),
// and a file is weakened by replacing it with a copy that has inherited
// security, which winfs.CheckPrivate rejects.

func assertPrivateRegularMode(t *testing.T, path string) {
	t.Helper()
	file, err := winfs.OpenFile(path, os.O_RDONLY, winfs.Inherited)
	if err != nil {
		t.Fatalf("inspect %s: %v", filepath.Base(path), err)
	}
	defer file.Close()
	info, err := winfs.Fstat(file)
	if err != nil {
		t.Fatalf("stat %s: %v", filepath.Base(path), err)
	}
	if !info.IsRegular() {
		t.Fatalf("%s is not a regular file", filepath.Base(path))
	}
	if err := winfs.CheckPrivate(file); err != nil {
		t.Fatalf("%s is not private: %v", filepath.Base(path), err)
	}
}

func assertPrivateDirectoryMode(t *testing.T, path string) {
	t.Helper()
	directory, err := winfs.OpenDir(path)
	if err != nil {
		t.Fatalf("inspect directory %s: %v", path, err)
	}
	defer directory.Close()
	if err := winfs.CheckPrivate(directory.File()); err != nil {
		t.Fatalf("directory %s is not private: %v", path, err)
	}
}

// weakenToInheritedSecurity is the Windows meaning of chmod 0644 on a private
// file: the same bytes under the same name with the security that os.WriteFile
// gives a new file, which is not owner-only and not protected from inheritance.
func weakenToInheritedSecurity(path string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o644)
}

func TestStoreWritesPrivateGitignoredCheckpointAndExactCAS(t *testing.T) {
	root := newRestartProject(t)
	store, err := NewStore(root)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	prepared := newPreparedFixture(t, root, "restart-store", digestOf('e'))
	if err := store.Prepare(prepared); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	assertPrivateRegularMode(t, store.CheckpointPath())
	ignoreBytes, err := readRegularNoFollow(filepath.Join(root, ".haft", "restart", ".gitignore"), MaximumCheckpointBytes)
	if err != nil {
		t.Fatalf("read restart .gitignore: %v", err)
	}
	if !ignoreProtectsRestartFiles(ignoreBytes) {
		t.Fatalf("restart .gitignore does not protect checkpoint/log: %q", ignoreBytes)
	}
	entries, err := os.ReadDir(filepath.Join(root, ".haft", "restart"))
	if err != nil {
		t.Fatalf("read restart directory: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), stagePrefix) {
			t.Fatalf("atomic checkpoint stage was not cleaned up: %s", entry.Name())
		}
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.RestartID() != prepared.RestartID() {
		t.Fatalf("loaded restart = %q", loaded.RestartID())
	}
	submission, err := MarkSubmitted(prepared)
	if err != nil {
		t.Fatalf("MarkSubmitted: %v", err)
	}
	submitted := submission.After()
	if err := store.Apply(submission); err != nil {
		t.Fatalf("Apply submitted: %v", err)
	}
	if err := store.Apply(submission); err != nil {
		t.Fatalf("idempotent Apply: %v", err)
	}
	failure, err := MarkInstallFailed(submitted, "install failed safely")
	if err != nil {
		t.Fatalf("MarkInstallFailed: %v", err)
	}
	if err := store.Apply(Change{before: prepared, after: failure.After()}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("manufactured skipped transition error = %v", err)
	}
}

func TestLiveMCPAndFallbackReceiptsRejectTamperAndDuplicateWake(t *testing.T) {
	root := newRestartProject(t)
	store, err := NewStore(root)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	prepared := newPreparedFixture(t, root, "restart-receipt-tamper", digestOf('e'))
	if err := store.Prepare(prepared); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	submitted := applyStoreState(t, store, prepared, StateSubmitted, "")
	if err := store.InstallLiveMCPChallenge(submitted); err != nil {
		t.Fatalf("InstallLiveMCPChallenge: %v", err)
	}
	opened := applyStoreState(t, store, submitted, StateAppOpened, "")
	resumed := applyStoreState(t, store, opened, StateResumed, "")
	verification := validRuntimeVerification(resumed)
	if err := store.withExclusiveLock(func() error {
		return store.writeLiveMCPReceiptUnlocked(resumed, verification.LiveMCPReceipt)
	}); err != nil {
		t.Fatalf("write live receipt: %v", err)
	}
	livePath := store.restartReceiptPath(resumed.restartID, liveMCPReceiptSuffix)
	content, err := os.ReadFile(livePath)
	if err != nil {
		t.Fatalf("read live receipt: %v", err)
	}
	content = bytes.Replace(
		content,
		[]byte(resumed.liveMCPChallengeNonce),
		[]byte(digestOf('0')),
		1,
	)
	if err := os.WriteFile(livePath, content, 0o600); err != nil {
		t.Fatalf("tamper live receipt: %v", err)
	}
	if _, err := store.LoadLiveMCPReceipt(resumed); err == nil {
		t.Fatal("tampered live MCP receipt was accepted")
	}

	if _, err := store.RecordResumeFallbackCleared(
		resumed,
		2,
		resumed.createdAt.Add(time.Minute),
	); err == nil {
		t.Fatal("fallback accepted more than one wake")
	}
	if _, err := store.RecordResumeFallbackCleared(
		resumed,
		1,
		resumed.createdAt.Add(2*time.Minute),
	); err != nil {
		t.Fatalf("record valid fallback receipt: %v", err)
	}
	fallbackPath := store.restartReceiptPath(resumed.restartID, fallbackReceiptSuffix)
	if err := weakenToInheritedSecurity(fallbackPath); err != nil {
		t.Fatalf("weaken fallback receipt security: %v", err)
	}
	if _, err := store.LoadResumeFallbackReceipt(resumed); err == nil {
		t.Fatal("world-readable fallback receipt was accepted")
	}
}

func TestStoreAttemptMarkerAndRestartFilesStayPrivate(t *testing.T) {
	root := newRestartProject(t)
	store, err := NewStore(root)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	prepared := newPreparedFixture(t, root, "restart-private", digestOf('f'))
	if err := store.Prepare(prepared); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	log, err := store.OpenSupervisorLog(prepared)
	if err != nil {
		t.Fatalf("OpenSupervisorLog: %v", err)
	}
	if err := log.Close(); err != nil {
		t.Fatalf("close supervisor log: %v", err)
	}

	directories := []string{store.directoryPath, store.attemptsDirectoryPath}
	for _, path := range directories {
		assertPrivateDirectoryMode(t, path)
	}
	files := []string{
		store.CheckpointPath(),
		attemptMarkerPath(t, store, prepared.DesiredHaftBinaryDigest()),
		prepared.SupervisorLogPath(),
	}
	for _, path := range files {
		assertPrivateRegularMode(t, path)
	}
}

func TestStoreRejectsTamperedAttemptMarker(t *testing.T) {
	tests := []struct {
		name   string
		tamper func(string) error
		want   string
	}{
		{
			name: "content",
			tamper: func(path string) error {
				return os.WriteFile(path, []byte("tampered\n"), privateFileMode)
			},
			want: "does not match desired digest",
		},
		{
			name: "mode",
			tamper: func(path string) error {
				return weakenToInheritedSecurity(path)
			},
			want: "ownership or mode is unsafe",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := newRestartProject(t)
			store, err := NewStore(root)
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			prepared := newPreparedFixture(t, root, "restart-marker-"+test.name, digestOf('1'))
			if err := store.Prepare(prepared); err != nil {
				t.Fatalf("Prepare: %v", err)
			}
			applyVerifiedStoreState(t, store, prepared)
			marker := attemptMarkerPath(t, store, prepared.DesiredHaftBinaryDigest())
			if err := test.tamper(marker); err != nil {
				t.Fatalf("tamper marker: %v", err)
			}
			changed := newPreparedFixture(t, root, "restart-marker-next-"+test.name, digestOf('2'))
			err = store.Prepare(changed)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("tampered marker error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestStoreRejectsPublicOrSymlinkedCheckpointAndLog(t *testing.T) {
	t.Run("checkpoint mode", func(t *testing.T) {
		root := newRestartProject(t)
		store, err := NewStore(root)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		prepared := newPreparedFixture(t, root, "restart-checkpoint-mode", digestOf('7'))
		if err := store.Prepare(prepared); err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		if err := weakenToInheritedSecurity(store.CheckpointPath()); err != nil {
			t.Fatalf("weaken checkpoint security: %v", err)
		}
		if _, err := store.Load(); err == nil || !strings.Contains(err.Error(), "ownership or mode is unsafe") {
			t.Fatalf("public checkpoint Load error = %v", err)
		}
	})

	t.Run("checkpoint symlink", func(t *testing.T) {
		root := newRestartProject(t)
		store, err := NewStore(root)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		prepared := newPreparedFixture(t, root, "restart-checkpoint-symlink", digestOf('8'))
		if err := store.Prepare(prepared); err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		content, err := os.ReadFile(store.CheckpointPath())
		if err != nil {
			t.Fatalf("read checkpoint: %v", err)
		}
		target := filepath.Join(t.TempDir(), "checkpoint.json")
		if err := os.WriteFile(target, content, privateFileMode); err != nil {
			t.Fatalf("write checkpoint target: %v", err)
		}
		if err := os.Remove(store.CheckpointPath()); err != nil {
			t.Fatalf("remove checkpoint: %v", err)
		}
		if err := os.Symlink(target, store.CheckpointPath()); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, err := store.Load(); err == nil {
			t.Fatalf("symlinked checkpoint was loaded")
		}
	})

	t.Run("log mode and symlink", func(t *testing.T) {
		root := newRestartProject(t)
		store, err := NewStore(root)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		prepared := newPreparedFixture(t, root, "restart-log-tamper", digestOf('9'))
		if err := store.Prepare(prepared); err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		log, err := store.OpenSupervisorLog(prepared)
		if err != nil {
			t.Fatalf("OpenSupervisorLog: %v", err)
		}
		if err := log.Close(); err != nil {
			t.Fatalf("close supervisor log: %v", err)
		}
		if err := weakenToInheritedSecurity(prepared.SupervisorLogPath()); err != nil {
			t.Fatalf("weaken supervisor log security: %v", err)
		}
		if _, err := store.OpenSupervisorLog(prepared); err == nil || !strings.Contains(err.Error(), "ownership or mode is unsafe") {
			t.Fatalf("public log open error = %v", err)
		}
		if err := os.Remove(prepared.SupervisorLogPath()); err != nil {
			t.Fatalf("remove supervisor log: %v", err)
		}
		target := filepath.Join(t.TempDir(), "supervisor.log")
		if err := os.WriteFile(target, nil, privateFileMode); err != nil {
			t.Fatalf("write supervisor target: %v", err)
		}
		if err := os.Symlink(target, prepared.SupervisorLogPath()); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, err := store.OpenSupervisorLog(prepared); err == nil {
			t.Fatalf("symlinked log was opened")
		}
	})
}
