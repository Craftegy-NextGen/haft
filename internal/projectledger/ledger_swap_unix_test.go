//go:build !windows

package projectledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/m0n0x41d/haft/db"
)

func TestExplicitMigrationOpenRetainsTopologyChecksWithoutRequiringBinding(
	t *testing.T,
) {
	fixture := newProjectLedgerFixture(t, "qnt_d6f3b2c1")
	handle, err := OpenForExplicitMigration(
		context.Background(),
		fixture.root,
		ReadOnly,
	)
	if err != nil {
		t.Fatalf("OpenForExplicitMigration: %v", err)
	}
	defer handle.Close()
	if handle.ProjectID().String() != fixture.id {
		t.Fatalf(
			"migration handle project = %s, want %s",
			handle.ProjectID().String(),
			fixture.id,
		)
	}
	if handle.DatabasePath() != fixture.dbPath {
		t.Fatalf(
			"migration handle database = %s, want %s",
			handle.DatabasePath(),
			fixture.dbPath,
		)
	}
	if err := handle.RequireAttachedIdentity(
		context.Background(),
	); !errorsIs(err, ErrBindingMissing) {
		t.Fatalf("unbound migration handle identity error = %v", err)
	}
	directory := filepath.Dir(fixture.dbPath)
	moved := directory + ".moved"
	if err := os.Rename(directory, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(directory, "haft.db"),
		[]byte("replacement"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := handle.RequireAttachedIdentity(
		context.Background(),
	); err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf(
			"unbound migration handle accepted a topology swap: %v",
			err,
		)
	}
}

func TestProjectLedgerRevalidateRejectsAnchoredDirectorySwap(t *testing.T) {
	fixture := newProjectLedgerFixture(t, "qnt_e7f3b2c1")
	if err := BindInitialized(context.Background(), fixture.root, time.Now().UTC()); err != nil {
		t.Fatalf("BindInitialized: %v", err)
	}
	handle, err := OpenExisting(context.Background(), fixture.root, ReadOnly)
	if err != nil {
		t.Fatalf("OpenExisting: %v", err)
	}
	defer handle.Close()

	directory := filepath.Dir(fixture.dbPath)
	moved := directory + ".moved"
	if err := os.Rename(directory, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "haft.db"), []byte("replacement"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := handle.Revalidate(context.Background()); err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("Revalidate after anchored directory swap = %v", err)
	}
}

func TestProjectLedgerRevalidateRejectsReplacedSQLiteSidecarGenerationBeforeSQLiteRead(
	t *testing.T,
) {
	fixture := newProjectLedgerFixture(t, "qnt_07f3b2c1")
	if err := BindInitialized(
		context.Background(),
		fixture.root,
		time.Now().UTC(),
	); err != nil {
		t.Fatalf("BindInitialized: %v", err)
	}
	handle, err := OpenExisting(
		context.Background(),
		fixture.root,
		ReadWrite,
	)
	if err != nil {
		t.Fatalf("OpenExisting: %v", err)
	}
	defer handle.Close()

	_, err = handle.Database().Exec(
		`CREATE TABLE project_ledger_sidecar_generation_probe (
			probe_id TEXT PRIMARY KEY
		) WITHOUT ROWID`,
	)
	if err != nil {
		t.Fatalf("create sidecar generation probe: %v", err)
	}
	if err := handle.Revalidate(context.Background()); err != nil {
		t.Fatalf("capture live sidecar generation: %v", err)
	}

	for _, suffix := range []string{"-wal", "-shm"} {
		path := fixture.dbPath + suffix
		priorPath := path + ".prior-generation"
		if err := os.Rename(path, priorPath); err != nil {
			t.Fatalf("replace SQLite sidecar %s: %v", suffix, err)
		}
		content, err := os.ReadFile(priorPath)
		if err != nil {
			t.Fatalf("read prior SQLite sidecar %s: %v", suffix, err)
		}
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatalf("write replacement SQLite sidecar %s: %v", suffix, err)
		}
	}

	err = handle.Revalidate(context.Background())
	if err == nil ||
		!errors.Is(err, ErrSQLiteSidecarGenerationChanged) ||
		!strings.Contains(err.Error(), "SQLite sidecar generation changed") ||
		strings.Contains(strings.ToLower(err.Error()), "malformed") {
		t.Fatalf("Revalidate after SQLite sidecar replacement = %v", err)
	}
}

func TestProjectLedgerBindingRollsBackWhenTopologyChangesBeforeCommit(t *testing.T) {
	fixture := newProjectLedgerFixture(t, "qnt_37f3b2c1")
	identity, identityAnchors, err := loadIdentityAnchored(fixture.root)
	if err != nil {
		t.Fatalf("loadIdentityAnchored: %v", err)
	}
	handle, err := openTopology(context.Background(), identity, identityAnchors, ReadWrite)
	if err != nil {
		closeAnchors(identityAnchors)
		t.Fatalf("openTopology: %v", err)
	}
	defer handle.Close()
	directory := filepath.Dir(fixture.dbPath)
	moved := directory + ".moved"
	hook := func(stage bindingStage) error {
		if stage != bindingStageBeforeCommit {
			return nil
		}
		if err := os.Rename(directory, moved); err != nil {
			return err
		}
		if err := os.Mkdir(directory, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(directory, "haft.db"), []byte("replacement"), 0o644)
	}
	err = handle.bindInitializedWithHook(
		context.Background(),
		time.Now().UTC(),
		hook,
	)
	if err == nil || !strings.Contains(err.Error(), "before binding commit") {
		t.Fatalf("bind with pre-commit topology swap = %v", err)
	}
	if errors.Is(err, ErrBindingCommittedTopologyChanged) {
		t.Fatalf("pre-commit topology swap reported a committed outcome: %v", err)
	}
	err = requireAttachedIdentity(context.Background(), handle.Database(), identity)
	if !errors.Is(err, ErrBindingMissing) {
		t.Fatalf("pre-commit topology drift did not roll binding back: %v", err)
	}
}

func TestMigrationBindingRollsBackVersionAndIdentityWhenTopologyChanges(
	t *testing.T,
) {
	if db.ProjectLedgerBindingSchemaVersion !=
		firstDurableBindingPredecessorSchema+1 {
		t.Fatalf(
			"binding schema boundary = %d, want predecessor %d plus one",
			db.ProjectLedgerBindingSchemaVersion,
			firstDurableBindingPredecessorSchema,
		)
	}
	fixture := newProjectLedgerFixture(t, "qnt_38f3b2c1")
	database, err := sql.Open("sqlite", fixture.dbPath)
	if err != nil {
		t.Fatalf("open project ledger fixture: %v", err)
	}
	if _, err := database.Exec(
		`DELETE FROM schema_version WHERE version > ?`,
		firstDurableBindingPredecessorSchema,
	); err != nil {
		_ = database.Close()
		t.Fatalf("rewind migration frontier fixture: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close rewound migration fixture: %v", err)
	}
	handle, err := OpenForExplicitMigration(
		context.Background(),
		fixture.root,
		ReadWrite,
	)
	if err != nil {
		t.Fatalf("OpenForExplicitMigration: %v", err)
	}
	defer handle.Close()
	directory := filepath.Dir(fixture.dbPath)
	moved := directory + ".moved"
	err = db.RunMigrationsThroughProjectLedgerBinding(
		handle.Database(),
		func(transaction db.MigrationTransaction) error {
			return handle.bindDuringFirstDurableSchemaMigration(
				context.Background(),
				transaction,
				time.Now().UTC(),
				func() error {
					if err := os.Rename(directory, moved); err != nil {
						return err
					}
					if err := os.Mkdir(directory, 0o755); err != nil {
						return err
					}
					return os.WriteFile(
						filepath.Join(directory, "haft.db"),
						[]byte("replacement"),
						0o644,
					)
				},
			)
		},
	)
	if err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("binding migration topology swap error = %v", err)
	}
	if errors.Is(err, ErrBindingCommittedTopologyChanged) {
		t.Fatalf("rolled-back binding reported committed outcome: %v", err)
	}
	var versionCount int
	if err := handle.Database().QueryRow(
		"SELECT COUNT(*) FROM schema_version WHERE version = ?",
		db.ProjectLedgerBindingSchemaVersion,
	).Scan(&versionCount); err != nil {
		t.Fatalf("inspect rolled-back binding version: %v", err)
	}
	if versionCount != 0 {
		t.Fatalf("rolled-back binding version count = %d, want 0", versionCount)
	}
	if err := requireAttachedIdentity(
		context.Background(),
		handle.Database(),
		handle.identity,
	); !errors.Is(err, ErrBindingMissing) {
		t.Fatalf("rolled-back migration retained a binding: %v", err)
	}
}

func TestProjectLedgerSQLiteIdentityBoundaryRejectsPersistentConnectWindowSwap(t *testing.T) {
	stages := []sqliteOpenStage{
		sqliteOpenStageBeforeConnect,
		sqliteOpenStageAfterConnectBeforeIdentityCheck,
	}
	for index, targetStage := range stages {
		t.Run(string(targetStage), func(t *testing.T) {
			fixture := newProjectLedgerFixture(t, fmt.Sprintf("qnt_47f3b2c%d", index))
			replacementDirectory := prepareReplacementLedgerDirectory(t, fixture, "persistent")
			identity, identityAnchors, err := loadIdentityAnchored(fixture.root)
			if err != nil {
				t.Fatalf("loadIdentityAnchored: %v", err)
			}
			originalDirectory := filepath.Dir(fixture.dbPath)
			movedDirectory := originalDirectory + ".anchored"
			hook := func(stage sqliteOpenStage) error {
				if stage != targetStage {
					return nil
				}
				if err := os.Rename(originalDirectory, movedDirectory); err != nil {
					return err
				}
				return os.Rename(replacementDirectory, originalDirectory)
			}
			handle, err := openTopologyWithHook(
				context.Background(),
				identity,
				identityAnchors,
				ReadWrite,
				hook,
			)
			if handle != nil {
				_ = handle.Close()
			}
			_ = closeAnchors(identityAnchors)
			if err == nil || !strings.Contains(err.Error(), "anchored database inode") {
				t.Fatalf("persistent %s swap error = %v", targetStage, err)
			}
		})
	}
}

// This test is the explicit limit of the process-local observed-path boundary.
// A replacement opened by SQLite and restored before the post-connect pathname
// observation cannot be distinguished without a descriptor-aware SQLite VFS.
// The production contract therefore detects drift visible at either boundary;
// it does not claim immunity to an adversarial swap-and-restore wholly inside
// that gap.
func TestProjectLedgerSQLiteObservedPathBoundaryDoesNotClaimSwapAndRestoreProof(t *testing.T) {
	fixture := newProjectLedgerFixture(t, "qnt_57f3b2c1")
	replacementDirectory := prepareReplacementLedgerDirectory(t, fixture, "replacement")
	identity, identityAnchors, err := loadIdentityAnchored(fixture.root)
	if err != nil {
		t.Fatalf("loadIdentityAnchored: %v", err)
	}
	originalDirectory := filepath.Dir(fixture.dbPath)
	anchoredDirectory := originalDirectory + ".anchored"
	openedReplacementDirectory := originalDirectory + ".opened-replacement"
	hook := func(stage sqliteOpenStage) error {
		switch stage {
		case sqliteOpenStageBeforeConnect:
			if err := os.Rename(originalDirectory, anchoredDirectory); err != nil {
				return err
			}
			return os.Rename(replacementDirectory, originalDirectory)
		case sqliteOpenStageAfterConnectBeforeIdentityCheck:
			if err := os.Rename(originalDirectory, openedReplacementDirectory); err != nil {
				return err
			}
			return os.Rename(anchoredDirectory, originalDirectory)
		default:
			return nil
		}
	}
	handle, err := openTopologyWithHook(
		context.Background(),
		identity,
		identityAnchors,
		ReadWrite,
		hook,
	)
	if err != nil {
		closeAnchors(identityAnchors)
		t.Fatalf("openTopologyWithHook at documented identity limit: %v", err)
	}
	defer handle.Close()
	marker := ""
	if err := handle.Database().QueryRow(`SELECT marker FROM project_ledger_identity_probe`).Scan(&marker); err != nil {
		t.Fatalf("read opened replacement marker: %v", err)
	}
	if marker != "replacement" {
		t.Fatalf("opened database marker = %q", marker)
	}
}
