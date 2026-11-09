//go:build darwin || linux || windows

package agenthostrestart

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/m0n0x41d/haft/internal/testpath"
)

func newRestartProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".haft"), 0o755); err != nil {
		t.Fatalf("create .haft: %v", err)
	}
	return root
}

func newPreparedFixture(
	t *testing.T,
	root string,
	restartID string,
	desiredDigest string,
) Checkpoint {
	t.Helper()
	store, err := NewStore(root)
	if err != nil {
		t.Fatalf("NewStore fixture: %v", err)
	}
	logPath, err := store.SupervisorLogPath(restartID)
	if err != nil {
		t.Fatalf("SupervisorLogPath: %v", err)
	}
	checkpoint, err := NewPreparedCheckpoint(Draft{
		RestartID:                   restartID,
		ThreadID:                    "019f5a6e-fba1-7cd3-8421-677d5431bd12",
		ResumeIntentDigest:          digestOf('3'),
		PlanPath:                    filepath.Join(".context", "haft-v9-typed-memory-e2e-master-plan.md"),
		LastCompletedPlanItem:       "P13 acceptance",
		ResumePlanItem:              "P14 installed runtime proof",
		MethodRunAbsence:            "no Haft MethodRun was opened for the host acceptance mechanism",
		RepositoryRoot:              root,
		RepositoryHead:              gitRevisionOf('4'),
		DirtyStateDigest:            digestOf('5'),
		ExpectedHaftBinaryPath:      filepath.Join(root, "bin", "haft"),
		DesiredHaftBinaryDigest:     desiredDigest,
		ExpectedFPFRevision:         gitRevisionOf('6'),
		ExpectedTypeEnvDigest:       digestOf('7'),
		ExpectedTypeEnvHeadRevision: 12,
		ExpectedGraphRevision:       34,
		ExpectedSkillCarriersRoot:   filepath.Join(root, ".agents", "skills"),
		ExpectedInstructionPath:     filepath.Join(root, "AGENTS.md"),
		ExpectedMCPConfigPath:       filepath.Join(root, ".codex", "config.toml"),
		ExpectedSkillCarriersDigest: digestOf('8'),
		ExpectedInstructionDigest:   digestOf('9'),
		ExpectedMCPConfigDigest:     digestOf('a'),
		TaskRuntime: TaskRuntimeIdentity{
			PID:             4242,
			StartedAt:       time.Date(2026, 7, 19, 11, 0, 0, 0, time.UTC),
			ExecutablePath:  testpath.Abs("/Applications/ChatGPT.app/Contents/Resources/codex"),
			ArgumentsDigest: digestOf('d'),
		},
		ResumeFallbackNonce:   digestOf('b'),
		LiveMCPChallengeNonce: digestOf('c'),
		LaunchdLabel:          "com.openai.codex.haft-restart." + restartID,
		SupervisorLogPath:     logPath,
		CreatedAt:             time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("NewPreparedCheckpoint: %v", err)
	}
	return checkpoint
}

func digestOf(value byte) string {
	return "sha256:" + repeatByte(value, 64)
}

func gitRevisionOf(value byte) string {
	return repeatByte(value, 40)
}

func repeatByte(value byte, count int) string {
	result := make([]byte, count)
	for index := range result {
		result[index] = value
	}
	return string(result)
}

func osStat(path string) (os.FileInfo, error) {
	return os.Stat(path)
}

func createSymlink(oldPath string, newPath string) error {
	return os.Symlink(oldPath, newPath)
}

func applyStoreState(
	t *testing.T,
	store Store,
	checkpoint Checkpoint,
	next State,
	failureDetail string,
) Checkpoint {
	t.Helper()
	change, err := transition(checkpoint, checkpoint.State(), next, failureDetail)
	if err != nil {
		t.Fatalf("transition %s -> %s: %v", checkpoint.State().String(), next.String(), err)
	}
	if err := store.Apply(change); err != nil {
		t.Fatalf("Apply %s -> %s: %v", checkpoint.State().String(), next.String(), err)
	}
	return change.After()
}

func applyVerifiedStoreState(t *testing.T, store Store, prepared Checkpoint) Checkpoint {
	t.Helper()
	submitted := applyStoreState(t, store, prepared, StateSubmitted, "")
	opened := applyStoreState(t, store, submitted, StateAppOpened, "")
	resumed := applyStoreState(t, store, opened, StateResumed, "")
	return applyStoreState(t, store, resumed, StateVerified, "")
}

func attemptMarkerPath(t *testing.T, store Store, desiredDigest string) string {
	t.Helper()
	name, _, err := attemptMarker(desiredDigest)
	if err != nil {
		t.Fatalf("attemptMarker: %v", err)
	}
	return filepath.Join(store.attemptsDirectoryPath, name)
}

func TestStoreAttemptHistoryRejectsVerifiedAAfterTerminalB(t *testing.T) {
	root := newRestartProject(t)
	store, err := NewStore(root)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	digestA := digestOf('d')
	digestB := digestOf('e')
	preparedA := newPreparedFixture(t, root, "restart-history-a", digestA)
	if err := store.Prepare(preparedA); err != nil {
		t.Fatalf("Prepare A: %v", err)
	}
	applyVerifiedStoreState(t, store, preparedA)
	markerA := attemptMarkerPath(t, store, digestA)
	if err := os.Remove(markerA); err != nil {
		t.Fatalf("remove A marker to simulate legacy checkpoint: %v", err)
	}
	if err := os.Remove(store.attemptsDirectoryPath); err != nil {
		t.Fatalf("remove attempts directory to simulate legacy checkpoint: %v", err)
	}

	preparedB := newPreparedFixture(t, root, "restart-history-b", digestB)
	if err := store.Prepare(preparedB); err != nil {
		t.Fatalf("Prepare B after legacy A: %v", err)
	}
	if _, err := os.Stat(markerA); err != nil {
		t.Fatalf("A marker was not backfilled before B: %v", err)
	}
	submittedB := applyStoreState(t, store, preparedB, StateSubmitted, "")
	applyStoreState(t, store, submittedB, StateInstallFailed, "terminal B failure")

	replayA := newPreparedFixture(t, root, "restart-history-a-replay", digestA)
	if err := store.Prepare(replayA); !errors.Is(err, ErrLoopGuard) {
		t.Fatalf("A -> B -> A replay error = %v", err)
	}
}

func TestStoreRejectsSymlinkedAttemptHistory(t *testing.T) {
	t.Run("marker", func(t *testing.T) {
		root := newRestartProject(t)
		store, err := NewStore(root)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		prepared := newPreparedFixture(t, root, "restart-marker-symlink", digestOf('3'))
		if err := store.Prepare(prepared); err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		applyVerifiedStoreState(t, store, prepared)
		marker := attemptMarkerPath(t, store, prepared.DesiredHaftBinaryDigest())
		if err := os.Remove(marker); err != nil {
			t.Fatalf("remove marker: %v", err)
		}
		target := filepath.Join(t.TempDir(), "marker")
		if err := os.WriteFile(target, []byte(prepared.DesiredHaftBinaryDigest()+"\n"), privateFileMode); err != nil {
			t.Fatalf("write marker target: %v", err)
		}
		if err := os.Symlink(target, marker); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		changed := newPreparedFixture(t, root, "restart-marker-symlink-next", digestOf('4'))
		if err := store.Prepare(changed); err == nil {
			t.Fatalf("symlinked marker was accepted")
		}
	})

	t.Run("directory", func(t *testing.T) {
		root := newRestartProject(t)
		store, err := NewStore(root)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		prepared := newPreparedFixture(t, root, "restart-attempts-symlink", digestOf('5'))
		if err := store.Prepare(prepared); err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		applyVerifiedStoreState(t, store, prepared)
		marker := attemptMarkerPath(t, store, prepared.DesiredHaftBinaryDigest())
		if err := os.Remove(marker); err != nil {
			t.Fatalf("remove marker: %v", err)
		}
		if err := os.Remove(store.attemptsDirectoryPath); err != nil {
			t.Fatalf("remove attempts directory: %v", err)
		}
		if err := os.Symlink(t.TempDir(), store.attemptsDirectoryPath); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		changed := newPreparedFixture(t, root, "restart-attempts-symlink-next", digestOf('6'))
		if err := store.Prepare(changed); err == nil {
			t.Fatalf("symlinked attempts directory was accepted")
		}
	})
}

func TestStoreRejectsDuplicateResumeWriterButKeepsOtherReplayIdempotent(t *testing.T) {
	root := newRestartProject(t)
	store, err := NewStore(root)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	prepared := newPreparedFixture(t, root, "restart-resume-writer", digestOf('a'))
	if err := store.Prepare(prepared); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	submission, err := transition(prepared, StatePrepared, StateSubmitted, "")
	if err != nil {
		t.Fatalf("submitted transition: %v", err)
	}
	if err := store.Apply(submission); err != nil {
		t.Fatalf("Apply submitted: %v", err)
	}
	if err := store.Apply(submission); err != nil {
		t.Fatalf("submitted replay lost idempotence: %v", err)
	}
	opening, err := transition(submission.After(), StateSubmitted, StateAppOpened, "")
	if err != nil {
		t.Fatalf("opened transition: %v", err)
	}
	if err := store.Apply(opening); err != nil {
		t.Fatalf("Apply opened: %v", err)
	}
	resumption, err := transition(opening.After(), StateAppOpened, StateResumed, "")
	if err != nil {
		t.Fatalf("resumed transition: %v", err)
	}
	if err := store.Apply(resumption); err != nil {
		t.Fatalf("Apply resumed: %v", err)
	}
	if err := store.Apply(resumption); !errors.Is(err, ErrDuplicateResumeWriter) {
		t.Fatalf("duplicate resume writer error = %v", err)
	}
}
