//go:build darwin || linux

package agenthostrestart

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
	info, err := osStat(store.CheckpointPath())
	if err != nil {
		t.Fatalf("stat checkpoint: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("checkpoint mode = %o", info.Mode().Perm())
	}
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
	if err := os.Chmod(fallbackPath, 0o644); err != nil {
		t.Fatalf("weaken fallback mode: %v", err)
	}
	if _, err := store.LoadResumeFallbackReceipt(resumed); err == nil {
		t.Fatal("world-readable fallback receipt was accepted")
	}
}

func assertPrivateRegularMode(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("inspect %s: %v", filepath.Base(path), err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("%s mode = %s", filepath.Base(path), info.Mode())
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
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat private directory %s: %v", path, err)
		}
		if info.Mode().Perm() != privateDirectoryMode {
			t.Fatalf("private directory %s mode = %04o", path, info.Mode().Perm())
		}
	}
	files := []string{
		store.CheckpointPath(),
		attemptMarkerPath(t, store, prepared.DesiredHaftBinaryDigest()),
		prepared.SupervisorLogPath(),
	}
	for _, path := range files {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat private file %s: %v", path, err)
		}
		if info.Mode().Perm() != privateFileMode {
			t.Fatalf("private file %s mode = %04o", path, info.Mode().Perm())
		}
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
				return os.Chmod(path, 0o644)
			},
			want: "mode is 0644",
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
		if err := os.Chmod(store.CheckpointPath(), 0o644); err != nil {
			t.Fatalf("chmod checkpoint: %v", err)
		}
		if _, err := store.Load(); err == nil || !strings.Contains(err.Error(), "mode is 0644") {
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
		if err := os.Chmod(prepared.SupervisorLogPath(), 0o644); err != nil {
			t.Fatalf("chmod supervisor log: %v", err)
		}
		if _, err := store.OpenSupervisorLog(prepared); err == nil || !strings.Contains(err.Error(), "mode is 0644") {
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
