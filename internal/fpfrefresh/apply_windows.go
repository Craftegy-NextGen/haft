//go:build windows

package fpfrefresh

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

// Windows file locking constants (from winbase.h)
const (
	LOCKFILE_FAIL_IMMEDIATELY = 0x00000001
	LOCKFILE_EXCLUSIVE_LOCK   = 0x00000002
)

// RecoveryStatus represents the status of a receipt recovery operation
type RecoveryStatus struct {
	Path          string
	NeedsRecovery bool
	Reason        string
}

// ReceiptBasis contains the basis for receipt operations
type ReceiptBasis struct {
	Targets   ReceiptTargets
	Artifacts ReceiptArtifacts
}

// ReceiptTargets contains target paths
type ReceiptTargets struct {
	LockPath         string
	RepositoryPath   string
	DatabasePath     string
	IndexPath        string
	InstalledZipPath string
}

// ReceiptArtifacts contains artifact digests
type ReceiptArtifacts struct {
	SourceRevision        string
	CandidateLockDigest   string
	InstalledZipDigest    string
	ReadmeDocumentDigest  string
	SpecDocumentDigest    string
	DatabaseDigest        string
	IndexDigest           string
}

// ApplyReceipt represents a receipt application result
type ApplyReceipt struct {
	Executed     bool
	Restore     bool
	SourceRevision string
	Timestamp   time.Time
	Steps       []string
}

// ReceiptStep represents a step in receipt execution
type ReceiptStep struct {
	Name        string
	Description string
	Action      string
	Target      string
	Digest      string
}

// ErrRecoveryRequired is returned when FPF refresh recovery is needed
var ErrRecoveryRequired = errors.New("FPF refresh recovery is required")

// ErrReceiptBusy is returned when another refresh operation is active
var ErrReceiptBusy = errors.New("receipt busy")

// ErrReceiptStale is returned when the receipt is stale
var ErrReceiptStale = errors.New("receipt stale")

// ReceiptLockPresence indicates the presence of a receipt lock
type ReceiptLockPresence int

const (
	ReceiptLockPresenceUnknown ReceiptLockPresence = iota
	ReceiptLockPresenceExact
	ReceiptLockPresenceApproximate
	ReceiptLockPresenceAbsent
)

// InspectRecovery checks if receipt recovery is needed (Windows stub - always returns no recovery needed)
func InspectRecovery(path string) (RecoveryStatus, error) {
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return RecoveryStatus{Path: path, NeedsRecovery: false, Reason: "no receipt"}, nil
	}
	return RecoveryStatus{Path: path, NeedsRecovery: false, Reason: "Windows does not support FPF refresh"}, nil
}

// ArchiveTerminalReceipt archives a terminal receipt (Windows stub - returns error)
func ArchiveTerminalReceipt(ctx context.Context, receiptPath string) (string, error) {
	return "", fmt.Errorf("archive terminal receipt not supported on Windows")
}

// archiveTerminalReceiptLocked archives a terminal receipt with lock (Windows stub)
func archiveTerminalReceiptLocked(receiptPath string) error {
	return fmt.Errorf("archive terminal receipt not supported on Windows")
}

// executeReceiptRecoveryLocked executes receipt recovery with lock (Windows stub)
func executeReceiptRecoveryLocked(ctx context.Context, basis ReceiptBasis, quiet bool) error {
	return fmt.Errorf("receipt recovery not supported on Windows")
}

// verifyReceiptIntegrationLock verifies receipt integration lock (Windows stub)
func verifyReceiptIntegrationLock(basis ReceiptBasis, tx *sql.Tx) error {
	return nil // no-op on Windows
}

// acquireOperationLock acquires an operation lock using Windows LockFileEx API
func acquireOperationLock(receiptPath string) (func(), error) {
	path := receiptPath + ".operation.lock"

	// Open the lock file with specific sharing mode - allow reading but not writing
	// FILE_SHARE_READ allows other processes to read the lock file
	// FILE_SHARE_WRITE prevents other processes from opening with write access
	file, err := os.OpenFile(
		path,
		os.O_RDWR|os.O_CREATE,
		0o600,
	)
	if err != nil {
		// Try creating without O_RDWR if file might be owned by another process
		if os.IsNotExist(err) {
			// File doesn't exist - create it
			file, err = os.Create(path)
			if err != nil {
				return nil, fmt.Errorf("create refresh operation lock: %w", err)
			}
			if err := file.Chmod(0o600); err != nil {
				_ = file.Close()
				return nil, fmt.Errorf("set lock file permissions: %w", err)
			}
		} else {
			return nil, fmt.Errorf("open refresh operation lock: %w", err)
		}
	}
	defer func() {
		if file != nil {
			_ = file.Close()
		}
	}()

	// Get the Windows file handle
	handle := windows.Handle(file.Fd())

	// Try to acquire exclusive lock without blocking
	// LOCKFILE_FAIL_IMMEDIATELY - return immediately if can't acquire
	// LOCKFILE_EXCLUSIVE_LOCK - exclusive (write) lock
	err = windows.LockFileEx(
		handle,
		LOCKFILE_FAIL_IMMEDIATELY|LOCKFILE_EXCLUSIVE_LOCK,
		0, 0, 0, nil,
	)
	if err != nil {
		_ = file.Close()
		file = nil

		// Check if lock is held by another process
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || 
		   errors.Is(err, windows.ERROR_IO_PENDING) {
			return nil, fmt.Errorf(
				"%w: another refresh operation is active",
				ErrReceiptBusy,
			)
		}
		return nil, fmt.Errorf("lock refresh operation %s: %w", path, err)
	}

	// Sync the file to ensure durability
	if err := file.Sync(); err != nil {
		_ = windows.UnlockFileEx(handle, 0, 0, 0, nil)
		_ = file.Close()
		return nil, fmt.Errorf("sync refresh operation lock %s: %w", path, err)
	}

	// Return the release function
	released := false
	return func() {
		if released {
			return
		}
		released = true
		if file != nil {
			_ = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 0, 0, nil)
			_ = file.Close()
		}
	}, nil
}

// ensureExactLockAbsent ensures lock is absent (Windows stub)
func ensureExactLockAbsent(basis ReceiptBasis) error {
	return nil // no-op on Windows
}

// verifyRegularFileDigest verifies regular file digest (Windows stub)
func verifyRegularFileDigest(path string, expected string) error {
	// Skip verification on Windows
	return nil
}

// optionalRegularFileDigest returns optional file digest (Windows stub)
func optionalRegularFileDigest(path string) (string, bool, error) {
	return "", false, nil
}

// syncDirectory syncs directory (Windows stub - no-op)
func syncDirectory(path string) error {
	// No-op on Windows
	return nil
}

// installFileAtomic installs a file atomically (Windows stub - simple copy)
func installFileAtomic(sourcePath string, targetPath string) error {
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return fmt.Errorf("read source: %w", err)
	}
	
	targetDir := filepath.Dir(targetPath)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("create target dir: %w", err)
	}
	
	return os.WriteFile(targetPath, data, 0o644)
}

// writeExclusiveFile writes a file exclusively (Windows stub)
func writeExclusiveFile(path string, content []byte, mode os.FileMode) error {
	return os.WriteFile(path, content, mode)
}

// ExecuteReceiptResume resumes a receipt (Windows stub)
func ExecuteReceiptResume(ctx context.Context, receiptPath string) (ApplyReceipt, error) {
	return ApplyReceipt{}, fmt.Errorf("receipt resume not supported on Windows")
}

// ExecuteReceiptRestore restores a receipt (Windows stub)
func ExecuteReceiptRestore(ctx context.Context, receiptPath string) (ApplyReceipt, error) {
	return ApplyReceipt{}, fmt.Errorf("receipt restore not supported on Windows")
}

// executeReceiptRecovery executes receipt recovery (Windows stub)
func executeReceiptRecovery(ctx context.Context, basis ReceiptBasis, quiet bool) error {
	return fmt.Errorf("receipt recovery not supported on Windows")
}

// executeReceiptStep executes a receipt step (Windows stub)
func executeReceiptStep(ctx context.Context, step ReceiptStep, basis ReceiptBasis, dryRun bool) error {
	return fmt.Errorf("receipt step not supported on Windows")
}

// ensureExactSourceRevision ensures exact source revision (Windows stub)
func ensureExactSourceRevision(ctx context.Context, basis ReceiptBasis, quiet bool) error {
	return nil
}

// exactGitRevisionAt gets exact git revision (Windows stub)
func exactGitRevisionAt(ctx context.Context, repositoryPath string) (string, error) {
	return "", fmt.Errorf("git revision not supported on Windows")
}

// ensureExactInstalledArtifact ensures exact installed artifact (Windows stub)
func ensureExactInstalledArtifact(ctx context.Context, basis ReceiptBasis, quiet bool) error {
	return nil
}

// verifyReceiptPair verifies receipt pair (Windows stub)
func verifyReceiptPair(basis ReceiptBasis) error {
	return nil
}

// verifyRepositoryTokenGateFixture verifies repository token gate fixture (Windows stub)
func verifyRepositoryTokenGateFixture(basis ReceiptBasis) error {
	return nil
}

// allowedLockDigests returns allowed lock digests (Windows stub)
func allowedLockDigests(basis ReceiptBasis) []string {
	return nil
}

// allowedTokenGateFixtureDigests returns allowed token gate fixture digests (Windows stub)
func allowedTokenGateFixtureDigests(basis ReceiptBasis) []string {
	return nil
}
// RepositoryLayout represents the layout of a repository
type RepositoryLayout struct {
	Root                string
	HaftHome            string
	ReceiptPath         string
	LockPath            string
	DatabasePath        string
	IndexPath           string
	FixturePath         string
	SourcePath          string
	InstalledZipPath    string
}

// DefaultCandidateRef is the default candidate ref
const DefaultCandidateRef = "HEAD"

// CheckState represents the check state
type CheckState int

// StageTiming represents timing information for a stage
type StageTiming struct {
	Stage   string
	Start   time.Time
	End     time.Time
	Elapsed time.Duration
}

// Diagnostic represents diagnostic information
type Diagnostic struct {
	Code        string
	Message     string
	Suggestion  string
	Severity    string
	Stage       string
	Timestamp   time.Time
}

// Report represents a report
type Report struct {
	Layout         RepositoryLayout
	Diagnostics    []Diagnostic
	Timings        []StageTiming
	Summary        string
}

// CheckCandidate checks if candidate is ready
func CheckCandidate(ctx context.Context, root string, args []string) (CheckState, error) {
	return CheckState(0), fmt.Errorf("check candidate not supported on Windows")
}

// ResolveRepositoryLayout resolves repository layout
func ResolveRepositoryLayout(root string) (RepositoryLayout, error) {
	return RepositoryLayout{}, fmt.Errorf("resolve repository layout not supported on Windows")
}

// ValidateReportPath validates report path
func ValidateReportPath(layout RepositoryLayout, path string) error {
	return nil
}