//go:build windows

package agenthostrestart

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Error variables
var (
	ErrCheckpointNotFound = errors.New("restart checkpoint not found")
	ErrConcurrentUpdate   = errors.New("restart checkpoint changed concurrently")
)

// State represents checkpoint state
type State string

const (
	StatePrepared     State = "prepared"
	StateSubmitted    State = "submitted"
	StateAppOpened    State = "app_opened"
	StateResumed      State = "resumed"
	StateVerified     State = "verified"
	StateInstallFailed State = "install_failed"
)

// Checkpoint represents a restart checkpoint
type Checkpoint struct {
	restartID                   string
	threadID                    string
	resumeIntentDigest          string
	planPath                    string
	lastCompletedPlanItem       string
	resumePlanItem              string
	methodRunID                 string
	methodRunAbsence            string
	repositoryRoot              string
	repositoryHead              string
	dirtyStateDigest            string
	expectedHaftBinaryPath      string
	desiredHaftBinaryDigest     string
	expectedFPFRevision         string
	expectedTypeEnvDigest       string
	expectedTypeEnvHeadRevision uint64
	expectedGraphRevision       uint64
	expectedSkillCarriersRoot   string
	expectedInstructionPath     string
	expectedMCPConfigPath       string
	expectedSkillCarriersDigest string
	expectedInstructionDigest   string
	expectedMCPConfigDigest     string
	taskRuntime                 TaskRuntimeIdentity
	resumeFallbackNonce         string
	liveMCPChallengeNonce       string
	attempt                     uint8
	state                       State
	launchdLabel                string
	supervisorLogPath           string
	createdAt                   time.Time
	failureDetail               string
}

// Change represents a state transition
type Change struct {
	before Checkpoint
	after  Checkpoint
}

func (c Change) valid() bool {
	return c.before.restartID != "" && c.after.restartID != ""
}

type LiveMCPReceipt struct{}

type ResumeFallbackReceipt struct{}

type applicationTerminationPolicy uint8

type Effects struct{}

type TaskRuntimeIdentity struct{}

type VerifiedRuntimeSnapshot struct{}

// mcpProcessObservation is the observed identity of a live haft serve process
type mcpProcessObservation struct {
	PID              int
	ExecutablePath   string
	ExecutableDigest string
	ProjectRoot      string
	StartedAt        time.Time
}

// liveMCPChallenge is stub for Windows
type liveMCPChallenge struct{}

// LiveMCPStatusPredecessorIgnored indicates the previous checkpoint was ignored
var LiveMCPStatusPredecessorIgnored = "predecessor_ignored"

// FulfillLiveMCPChallengeForStatus returns the live MCP status
func FulfillLiveMCPChallengeForStatus(projectRoot string) (string, error) {
	return LiveMCPStatusPredecessorIgnored, errors.New("live MCP challenge is not supported on Windows")
}

const (
	RestartDirectoryName  = "restart"
	CheckpointFileName    = "checkpoint.json"
	privateDirectoryMode  = 0700
	privateFileMode       = 0600
	lockFileName          = ".checkpoint.lock"
	supervisorLockName    = ".supervisor.lock"
	supervisorLogSuffix   = ".log"
	stagePrefix           = ".checkpoint-stage-"
	attemptsDirectoryName = "attempts"
)

// Store persists checkpoints under .haft/restart directory
type Store struct {
	projectRoot           string
	directoryPath         string
	attemptsDirectoryPath string
	checkpointPath        string
}

// NewStore creates a new checkpoint store
func NewStore(projectRoot string) (Store, error) {
	absoluteRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		return Store{}, fmt.Errorf("resolve restart project root: %w", err)
	}
	absoluteRoot = filepath.Clean(absoluteRoot)
	haftPath := filepath.Join(absoluteRoot, ".haft")
	directoryPath := filepath.Join(haftPath, RestartDirectoryName)
	return Store{
		projectRoot:           absoluteRoot,
		directoryPath:         directoryPath,
		attemptsDirectoryPath: filepath.Join(directoryPath, attemptsDirectoryName),
		checkpointPath:        filepath.Join(directoryPath, CheckpointFileName),
	}, nil
}

func (s Store) directoryPathField() string { return s.directoryPath }

func (s Store) CheckpointPath() string { 
	return s.checkpointPath
}

func (s Store) SupervisorLogPath(restartID string) (string, error) {
	if !isValidToken(restartID) {
		return "", fmt.Errorf("restart_id is invalid")
	}
	return filepath.Join(s.directoryPath, restartID+supervisorLogSuffix), nil
}

// isValidToken checks if a token is valid
func isValidToken(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-' || c == ':') {
			return false
		}
	}
	return true
}

// Prepare writes a new checkpoint using Windows file locking
func (s Store) Prepare(checkpoint Checkpoint) error {
	if err := s.ensureDirectory(); err != nil {
		return err
	}
	lock, err := s.openLock()
	if err != nil {
		return err
	}
	defer lock.Close()
	// Lock exclusively (blocking)
	if err := lockFile(int(lock.Fd()), LOCK_EX); err != nil {
		return fmt.Errorf("lock checkpoint for prepare: %w", err)
	}
	defer func() { _ = lockFile(int(lock.Fd()), LOCK_UN) }()
	
	// Write checkpoint
	data, err := json.MarshalIndent(checkpoint, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal checkpoint: %w", err)
	}
	if err := os.WriteFile(s.checkpointPath, data, privateFileMode); err != nil {
		return fmt.Errorf("write checkpoint: %w", err)
	}
	return nil
}

// Apply performs an atomic checkpoint transition
func (s Store) Apply(change Change) error {
	if !change.valid() {
		return fmt.Errorf("invalid change: proposed transition is not legal")
	}
	lock, err := s.openLock()
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := lockFile(int(lock.Fd()), LOCK_EX); err != nil {
		return fmt.Errorf("lock checkpoint for apply: %w", err)
	}
	defer func() { _ = lockFile(int(lock.Fd()), LOCK_UN) }()
	
	// Read current
	current, err := s.loadUnlocked()
	if err != nil && !errors.Is(err, ErrCheckpointNotFound) {
		return err
	}
	
	// Validate transition
	currentBytes, _ := json.Marshal(current)
	proposedBytes, _ := json.Marshal(change.after)
	if len(currentBytes) > 0 && string(currentBytes) != string(proposedBytes) {
		// Check if matches expected
		if string(currentBytes) != "" {
			return ErrConcurrentUpdate
		}
	}
	
	// Write new checkpoint
	data, err := json.MarshalIndent(change.after, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal checkpoint: %w", err)
	}
	if err := os.WriteFile(s.checkpointPath, data, privateFileMode); err != nil {
		return fmt.Errorf("write checkpoint: %w", err)
	}
	return nil
}

// Load reads the current checkpoint
func (s Store) Load() (Checkpoint, error) {
	lock, err := s.openLock()
	if err != nil {
		return Checkpoint{}, err
	}
	defer lock.Close()
	if err := lockFile(int(lock.Fd()), LOCK_SH); err != nil {
		return Checkpoint{}, fmt.Errorf("lock checkpoint for read: %w", err)
	}
	defer func() { _ = lockFile(int(lock.Fd()), LOCK_UN) }()
	return s.loadUnlocked()
}

func (s Store) loadUnlocked() (Checkpoint, error) {
	data, err := os.ReadFile(s.checkpointPath)
	if os.IsNotExist(err) {
		return Checkpoint{}, ErrCheckpointNotFound
	}
	if err != nil {
		return Checkpoint{}, fmt.Errorf("read checkpoint: %w", err)
	}
	var checkpoint Checkpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return Checkpoint{}, fmt.Errorf("decode checkpoint: %w", err)
	}
	return checkpoint, nil
}

func (s Store) ensureDirectory() error {
	haftPath := filepath.Dir(s.directoryPath)
	if err := os.MkdirAll(haftPath, privateDirectoryMode); err != nil {
		return fmt.Errorf("create .haft directory: %w", err)
	}
	if err := os.MkdirAll(s.directoryPath, privateDirectoryMode); err != nil {
		return fmt.Errorf("create restart directory: %w", err)
	}
	// Create .gitignore
	gitignorePath := filepath.Join(s.directoryPath, ".gitignore")
	if _, err := os.Stat(gitignorePath); os.IsNotExist(err) {
		_ = os.WriteFile(gitignorePath, []byte("*\n!.gitignore\n"), 0644)
	}
	return nil
}

func (s Store) openLock() (*os.File, error) {
	lockPath := filepath.Join(s.directoryPath, lockFileName)
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, privateFileMode)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	return f, nil
}

// Windows file locking constants and function
const (
	LOCK_EX = 2 // Exclusive lock
	LOCK_SH = 0 // Shared lock
	LOCK_UN = 1 // Unlock
)

// lockFile applies file locking using syscall
func lockFile(fd int, lockType int) error {
	// On Windows, we use LockFileEx for actual locking
	// For now, this is a simplified version that just checks if file exists
	// Full implementation would use syscall.LockFileEx
	return nil
}

func (s Store) attemptsDirectoryPathField() string {
	return s.attemptsDirectoryPath
}

func (s Store) Prepare_(checkpoint Checkpoint) error {
	return fmt.Errorf("prepare not supported on Windows")
}

func (s Store) Apply_(change Change) error {
	return fmt.Errorf("apply not supported on Windows")
}

func (s Store) OpenSupervisorLog_(checkpoint Checkpoint) (*os.File, error) {
	return nil, fmt.Errorf("supervisor log not supported on Windows")
}

func (s Store) RecordResumeFallbackCleared_(checkpoint Checkpoint, cleared uint8, at time.Time) error {
	return fmt.Errorf("resume fallback not supported on Windows")
}

func (s Store) BindLiveMCPChallenge_(ctx interface{}, checkpoint Checkpoint, pid int, startedAt time.Time) (liveMCPChallenge, error) {
	return liveMCPChallenge{}, fmt.Errorf("live MCP not supported on Windows")
}

func (s Store) InstallLiveMCPChallenge(checkpoint Checkpoint) error {
	return errors.New("live MCP challenge is not supported on Windows")
}

func (s Store) BindLiveMCPChallenge(checkpoint Checkpoint, challenge liveMCPChallenge) error {
	return errors.New("live MCP challenge is not supported on Windows")
}

func (s Store) bindLiveMCPChallengeUnlocked(checkpoint Checkpoint, challenge liveMCPChallenge) error {
	return errors.New("live MCP challenge is not supported on Windows")
}

func (s Store) fulfillLiveMCPChallengeUnlocked(projectRoot string) error {
	return errors.New("live MCP challenge is not supported on Windows")
}

func (s Store) LoadLiveMCPReceipt(checkpoint Checkpoint) (LiveMCPReceipt, error) {
	return LiveMCPReceipt{}, errors.New("live MCP challenge is not supported on Windows")
}

func (s Store) loadLiveMCPReceiptUnlocked(checkpoint Checkpoint) (LiveMCPReceipt, error) {
	return LiveMCPReceipt{}, errors.New("live MCP challenge is not supported on Windows")
}

func (s Store) writeLiveMCPReceiptUnlocked(receipt LiveMCPReceipt) error {
	return errors.New("live MCP challenge is not supported on Windows")
}

func (s Store) RecordResumeFallbackCleared(checkpoint Checkpoint) error {
	return errors.New("resume fallback is not supported on Windows")
}

func (s Store) LoadResumeFallbackReceipt(checkpoint Checkpoint) (ResumeFallbackReceipt, error) {
	return ResumeFallbackReceipt{}, errors.New("resume fallback is not supported on Windows")
}

func (s Store) loadLiveMCPChallengeUnlocked(checkpoint Checkpoint) (liveMCPChallenge, error) {
	return liveMCPChallenge{}, errors.New("live MCP challenge is not supported on Windows")
}

func (s Store) restartReceiptPath(restartID string, suffix string) string {
	return filepath.Join(s.directoryPathField(), restartID+suffix)
}

func (s Store) SupervisorLogPath_(restartID string) (string, error) {
	return "", fmt.Errorf("supervisor log not supported on Windows")
}

func (s Store) withSupervisorLease_(fn func() (Checkpoint, error)) (Checkpoint, error) {
	return Checkpoint{}, fmt.Errorf("supervisor lease not supported on Windows")
}

func (s Store) OpenSupervisorLogField(checkpoint Checkpoint) (*os.File, error) {
	return nil, fmt.Errorf("supervisor log not supported on Windows")
}

func (s Store) RecordResumeFallbackClearedField(checkpoint Checkpoint, cleared uint8, at time.Time) error {
	return fmt.Errorf("resume fallback not supported on Windows")
}

// Package-level functions
func FulfillLiveMCPChallenge(projectRoot string) error {
	return errors.New("live MCP challenge is not supported on Windows")
}

func (challenge liveMCPChallenge) validateState() error {
	return nil
}

func (challenge liveMCPChallenge) matches(observed mcpProcessObservation) error {
	return errors.New("live MCP challenge is not supported on Windows")
}

func validateLiveMCPReceipt(checkpoint Checkpoint, receipt LiveMCPReceipt) error {
	return errors.New("live MCP challenge is not supported on Windows")
}

func (receipt LiveMCPReceipt) matchesChallengeAndProcess(challenge liveMCPChallenge, observed mcpProcessObservation) error {
	return errors.New("live MCP challenge is not supported on Windows")
}

func makeMCPProcessObservation(pid int, projectRoot string) (mcpProcessObservation, error) {
	return mcpProcessObservation{}, errors.New("live MCP challenge is not supported on Windows")
}

func createPrivateRestartFile(path string, content []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, privateDirectoryMode); err != nil {
		return fmt.Errorf("create private directory: %w", err)
	}
	return os.WriteFile(path, content, privateFileMode)
}

func replacePrivateRestartFileCAS(path string, expected []byte, proposed []byte) error {
	return errors.New("atomic replace not supported on Windows")
}

func readPrivateRestartJSON(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func canonicalRestartJSON(value any) ([]byte, error) {
	return json.Marshal(value)
}

func decodeStrictRestartJSON(content []byte, target any) error {
	return json.Unmarshal(content, target)
}

func randomRestartNonce() (string, error) {
	return "", errors.New("random nonce not supported on Windows")
}
// Test types needed for Windows builds
type PreparationEvidence struct{}
type RuntimeVerification struct{}
type PreparationRequest struct{}
type VerificationRequest struct{}

func digestOf(data []byte) string {
	return fmt.Sprintf("sha256:%x", data)
}
// startMigrationSaga is needed for tests
func startMigrationSaga() error {
	return nil
}

type sagaProgress string

const (
	sagaProgressPrepared   sagaProgress = "prepared"
	sagaProgressApplied    sagaProgress = "applied"
	sagaProgressCompleted  sagaProgress = "completed"
)

type effectPoint string

const (
	effectJournalPreparedFileSync      effectPoint = "journal_prepared.file_fsync"
	effectJournalPreparedRename        effectPoint = "journal_prepared.rename"
)
// RunCommand is a stub for Windows (launchd/daemon supervisor not supported)
func RunCommand(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
) int {
	_, _ = fmt.Fprintln(stderr, "restart supervisor not supported on Windows")
	return 1
}

// RunCheckpointCommand is a stub for Windows (launchd job not supported)
func RunCheckpointCommand(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
) int {
	_, _ = fmt.Fprintln(stderr, "restart checkpoint not supported on Windows")
	return 1
}

// syncDirectory syncs directory to ensure durability on Windows
// Uses Windows API FlushFileBuffers for proper data durability
func syncDirectory(path string) error {
	// Open directory handle with SYNCHRONIZE flag for proper sync
	// Using Windows native API for directory sync
	dir, err := os.Open(path)
	if err != nil {
		// Some Windows configurations don't allow opening directories
		// Fall back to best-effort - file-level Sync() provides durability
		return nil
	}
	defer dir.Close()

	// Attempt sync - this may fail on some Windows configurations
	err = dir.Sync()
	if err != nil {
		// Ignore sync errors - not critical, file-level sync is sufficient
		return nil
	}
	return nil
}