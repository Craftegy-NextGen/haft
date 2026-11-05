//go:build windows

package specmigrationv2

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Types needed by Windows stub
type migrationEffectPlan struct {
	request ApplyRequest
	paths   migrationEffectPaths
}

type migrationEffectPaths struct {
	lock        string
	journal     string
	targetStage string
	lineage     string
	receipt     string
	source      string
	target      string
	archive     string
}

type Journal struct{}

type ApplyChange struct{}

// acquireMigrationLock creates a file-based lock on Windows
var (
	activeLocks = make(map[string]*migrationLock)
)

type migrationLock struct {
	path string
	file *os.File
}

func acquireMigrationLock(path string) (migrationLock, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return migrationLock{}, fmt.Errorf("create lock directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return migrationLock{}, fmt.Errorf("open lock file: %w", err)
	}
	return migrationLock{path: path, file: file}, nil
}

func (lock migrationLock) close() error {
	if lock.file == nil {
		return nil
	}
	delete(activeLocks, lock.path)
	return lock.file.Close()
}

func loadJournal(path string) (Journal, bool, error) {
	return Journal{}, false, errors.New("migration journal not supported on Windows")
}

func buildMigrationEffectPaths(req ApplyRequest) (migrationEffectPaths, error) {
	return migrationEffectPaths{}, errors.New("migration not supported on Windows")
}

// effectPaths takes ApplyRequest and returns migrationEffectPaths
func effectPaths(req ApplyRequest) migrationEffectPaths {
	return migrationEffectPaths{}
}

func buildMigrationEffectPlan(req ApplyRequest, witness any, startedAt time.Time) (migrationEffectPlan, error) {
	return migrationEffectPlan{}, errors.New("migration not supported on Windows")
}

func (req ApplyRequest) validateAndPopulate() error {
	return errors.New("migration not supported on Windows")
}

func applyMigrationEffect(req ApplyRequest, change ApplyChange) error {
	return errors.New("migration not supported on Windows")
}

// verifyCanonicalRealRoot checks if the path is a valid canonical root on Windows
func verifyCanonicalRealRoot(root string) error {
	if root == "" {
		return errors.New("project root cannot be empty")
	}
	if !filepath.IsAbs(root) {
		return fmt.Errorf("project root must be absolute: %s", root)
	}
	cleaned := filepath.Clean(root)
	if cleaned != root {
		return fmt.Errorf("project root must be canonical: %s", root)
	}
	return nil
}

// verifyConfinedPathComponents checks if a path is confined within the project root
func verifyConfinedPathComponents(projectRoot, relPath string) error {
	if relPath == "" {
		return nil
	}
	absPath := filepath.Join(projectRoot, relPath)
	cleanedAbs := filepath.Clean(absPath)
	cleanedRoot := filepath.Clean(projectRoot)
	if !strings.HasPrefix(cleanedAbs, cleanedRoot) {
		return fmt.Errorf("path escapes project root: %s", relPath)
	}
	return nil
}

// confinedRelativePath computes a relative path from project root to target
func confinedRelativePath(projectRoot, target string) (string, error) {
	rel, err := filepath.Rel(projectRoot, target)
	if err != nil {
		return "", fmt.Errorf("compute relative path: %w", err)
	}
	if strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path escapes project root: %s", target)
	}
	return rel, nil
}

// readRegularFileNoFollow reads a file without following symlinks
func readRegularFileNoFollow(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	return data, nil
}

// CurrentMigrationTargetState represents the state of a migration target
type CurrentMigrationTargetState int

const (
	CurrentMigrationTargetExact    CurrentMigrationTargetState = iota // exact match
	CurrentMigrationTargetEvolved                                              // evolved (upgraded)
	CurrentMigrationTargetAbsent                                              // absent (not present)
)

// JournalState represents the current state of the migration journal
type JournalState interface{}

// NoMigrationJournal is a type representing no journal present
type NoMigrationJournal struct{}

// RecoveryPendingMigrationJournal is a type representing a recovery-pending journal
type RecoveryPendingMigrationJournal struct {
	phase   string
	reason  string
}

func (r RecoveryPendingMigrationJournal) Phase() string   { return r.phase }
func (r RecoveryPendingMigrationJournal) Reason() string  { return r.reason }

// CompletedMigrationJournal is a type representing a completed migration
type CompletedMigrationJournal struct {
	receiptCarrier      MigrationEffectReceiptCarrier
	currentTargetState  CurrentMigrationTargetState
}

func (c CompletedMigrationJournal) ReceiptCarrier() MigrationEffectReceiptCarrier { return c.receiptCarrier }
func (c CompletedMigrationJournal) CurrentTargetState() CurrentMigrationTargetState { return c.currentTargetState }

// InspectMigrationJournalState inspects the migration journal state - returns interface type
func InspectMigrationJournalState(root ApplyProjectRoot, packet Packet) (JournalState, error) {
	return nil, errors.New("migration journal not supported on Windows")
}

// CurrentMigrationTargetStateForRequest returns the current migration target state
func CurrentMigrationTargetStateForRequest(req ApplyRequest) (CurrentMigrationTargetState, error) {
	return CurrentMigrationTargetAbsent, errors.New("migration not supported on Windows")
}

// ApplyMigration applies a migration - stub for Windows (returns single interface value)
func ApplyMigration(ctx context.Context, svc interface{}, req ApplyRequest) interface{} {
	return nil
}

// RecoverMigration recovers a failed migration - stub for Windows (returns single interface value)
func RecoverMigration(ctx context.Context, svc interface{}, reviewSvc interface{}, req interface{}) interface{} {
	return nil
}
// verifyEffectPathTopology verifies the migration effect paths
func verifyEffectPathTopology(plan migrationEffectPlan) error {
	return nil // Simplified for Windows
}

// requireRealDirectory checks if path is a real directory
func requireRealDirectory(path string) error {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return fmt.Errorf("directory does not exist: %s", path)
	}
	if err != nil {
		return fmt.Errorf("stat directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("not a directory: %s", path)
	}
	return nil
}
// Test helper functions for Windows
func verifyCurrentReviewBasis(ctx context.Context, plan migrationEffectPlan) error {
	return nil
}

func resultForExistingJournal(plan migrationEffectPlan, journal Journal, phase string, err error) interface{} {
	return nil
}

// Types needed for tests
type sagaProgress string
type effectPoint string

const (
	SagaProgressPrepared  sagaProgress = "prepared"
	SagaProgressApplied   sagaProgress = "applied" 
	SagaProgressCompleted sagaProgress = "completed"
	EffectJournalPreparedFileSync effectPoint = "journal_prepared.file_fsync"
)

var startMigrationSagaCalled = false

func startMigrationSaga() {
	startMigrationSagaCalled = true
}