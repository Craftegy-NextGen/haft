//go:build windows

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
	"golang.org/x/sys/windows"
)

// Windows counterparts of the seven swap tests that ledger_swap_unix_test.go
// keeps for Linux and macOS (ADR-093 E3). Those tests rename an anchored
// directory, or a SQLite file, while it is open. NTFS refuses to rename a
// directory while an entry below it is open, whatever the share mode
// (measured: ERROR_ACCESS_DENIED), and SQLite opens the database and its
// sidecars without FILE_SHARE_DELETE (ERROR_SHARING_VIOLATION). On Windows the
// swap each test performs therefore cannot happen while the ledger holds its
// anchors. Each counterpart shows that refusal and that the guarded outcome
// holds; where Windows does allow a swap (the identity carrier, or the
// database before SQLite connects), it shows the same detection the Unix test
// asserts.

// swapIdentityCarrier replaces the anchored .haft/project.yaml with a new file
// of the same bytes: an inode swap that Windows permits while it is anchored,
// because winfs holds the carrier with FILE_SHARE_DELETE.
func swapIdentityCarrier(root string) error {
	path := filepath.Join(root, ".haft", "project.yaml")
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := os.Rename(path, path+".displaced"); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o644)
}

// swapLedgerDirectory is the directory swap of the Unix tests.
func swapLedgerDirectory(directory string) error {
	if err := os.Rename(directory, directory+".moved"); err != nil {
		return err
	}
	if err := os.Mkdir(directory, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, "haft.db"), []byte("replacement"), 0o644)
}

// Counterpart of TestProjectLedgerRevalidateRejectsAnchoredDirectorySwap: the
// directory swap is refused and the handle stays valid; a carrier swap, which
// Windows allows, is rejected by Revalidate.
func TestWindowsAnchoredLedgerDirectoryCannotBeSwappedWhileOpen(t *testing.T) {
	fixture := newProjectLedgerFixture(t, "qnt_e7f3b2c9")
	if err := BindInitialized(context.Background(), fixture.root, time.Now().UTC()); err != nil {
		t.Fatalf("BindInitialized: %v", err)
	}
	handle, err := OpenExisting(context.Background(), fixture.root, ReadOnly)
	if err != nil {
		t.Fatalf("OpenExisting: %v", err)
	}
	defer handle.Close()
	directory := filepath.Dir(fixture.dbPath)
	err = os.Rename(directory, directory+".moved")
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("rename of the open ledger directory = %v, want ERROR_ACCESS_DENIED", err)
	}
	if err := handle.Revalidate(context.Background()); err != nil {
		t.Fatalf("Revalidate after the refused swap: %v", err)
	}
	if err := swapIdentityCarrier(fixture.root); err != nil {
		t.Fatalf("swap identity carrier: %v", err)
	}
	if err := handle.Revalidate(context.Background()); err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("Revalidate after an anchored carrier swap = %v", err)
	}
}

// Counterpart of
// TestExplicitMigrationOpenRetainsTopologyChecksWithoutRequiringBinding.
func TestWindowsExplicitMigrationOpenRetainsTopologyChecksWithoutRequiringBinding(t *testing.T) {
	fixture := newProjectLedgerFixture(t, "qnt_d6f3b2c9")
	handle, err := OpenForExplicitMigration(context.Background(), fixture.root, ReadOnly)
	if err != nil {
		t.Fatalf("OpenForExplicitMigration: %v", err)
	}
	defer handle.Close()
	if handle.ProjectID().String() != fixture.id {
		t.Fatalf("migration handle project = %s, want %s", handle.ProjectID().String(), fixture.id)
	}
	if handle.DatabasePath() != fixture.dbPath {
		t.Fatalf("migration handle database = %s, want %s", handle.DatabasePath(), fixture.dbPath)
	}
	if err := handle.RequireAttachedIdentity(context.Background()); !errorsIs(err, ErrBindingMissing) {
		t.Fatalf("unbound migration handle identity error = %v", err)
	}
	err = swapLedgerDirectory(filepath.Dir(fixture.dbPath))
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("directory swap under a migration handle = %v, want ERROR_ACCESS_DENIED", err)
	}
	if err := handle.RequireAttachedIdentity(context.Background()); !errorsIs(err, ErrBindingMissing) {
		t.Fatalf("migration handle after the refused swap = %v, want ErrBindingMissing", err)
	}
	if err := swapIdentityCarrier(fixture.root); err != nil {
		t.Fatalf("swap identity carrier: %v", err)
	}
	if err := handle.RequireAttachedIdentity(context.Background()); err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("unbound migration handle accepted a carrier swap: %v", err)
	}
}

// Counterpart of
// TestProjectLedgerRevalidateRejectsReplacedSQLiteSidecarGenerationBeforeSQLiteRead:
// while the handle is open SQLite holds both sidecars without
// FILE_SHARE_DELETE, so neither can be replaced. Detection of a replaced
// generation itself is covered on every OS by
// TestSQLiteSidecarGenerationAdoptsFirstActivationButNotReplacement.
func TestWindowsSQLiteSidecarsCannotBeReplacedWhileOpen(t *testing.T) {
	fixture := newProjectLedgerFixture(t, "qnt_07f3b2c9")
	if err := BindInitialized(context.Background(), fixture.root, time.Now().UTC()); err != nil {
		t.Fatalf("BindInitialized: %v", err)
	}
	handle, err := OpenExisting(context.Background(), fixture.root, ReadWrite)
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
		err := os.Rename(path, path+".prior-generation")
		if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			t.Fatalf("replace open SQLite sidecar %s = %v, want ERROR_SHARING_VIOLATION", suffix, err)
		}
	}
	if err := handle.Revalidate(context.Background()); err != nil {
		t.Fatalf("Revalidate after refused sidecar replacement: %v", err)
	}
}

// Counterpart of TestProjectLedgerBindingRollsBackWhenTopologyChangesBeforeCommit.
func TestWindowsProjectLedgerBindingRollsBackWhenTopologyChangesBeforeCommit(t *testing.T) {
	cases := []struct {
		name  string
		id    string
		swap  func(fixture projectLedgerFixture) error
		check func(t *testing.T, err error)
	}{
		{
			name: "directory swap refused",
			id:   "qnt_37f3b2c8",
			swap: func(fixture projectLedgerFixture) error {
				return swapLedgerDirectory(filepath.Dir(fixture.dbPath))
			},
			check: func(t *testing.T, err error) {
				if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
					t.Fatalf("bind with a refused pre-commit swap = %v, want ERROR_ACCESS_DENIED", err)
				}
			},
		},
		{
			name: "carrier swap detected",
			id:   "qnt_37f3b2c9",
			swap: func(fixture projectLedgerFixture) error {
				return swapIdentityCarrier(fixture.root)
			},
			check: func(t *testing.T, err error) {
				if err == nil || !strings.Contains(err.Error(), "before binding commit") {
					t.Fatalf("bind with pre-commit carrier swap = %v", err)
				}
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectLedgerFixture(t, test.id)
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
			hook := func(stage bindingStage) error {
				if stage != bindingStageBeforeCommit {
					return nil
				}
				return test.swap(fixture)
			}
			err = handle.bindInitializedWithHook(context.Background(), time.Now().UTC(), hook)
			test.check(t, err)
			if errors.Is(err, ErrBindingCommittedTopologyChanged) {
				t.Fatalf("pre-commit topology change reported a committed outcome: %v", err)
			}
			err = requireAttachedIdentity(context.Background(), handle.Database(), identity)
			if !errors.Is(err, ErrBindingMissing) {
				t.Fatalf("pre-commit topology change did not roll binding back: %v", err)
			}
		})
	}
}

// Counterpart of TestMigrationBindingRollsBackVersionAndIdentityWhenTopologyChanges.
func TestWindowsMigrationBindingRollsBackVersionAndIdentityWhenTopologyChanges(t *testing.T) {
	cases := []struct {
		name  string
		id    string
		swap  func(fixture projectLedgerFixture) error
		check func(t *testing.T, err error)
	}{
		{
			name: "directory swap refused",
			id:   "qnt_38f3b2c8",
			swap: func(fixture projectLedgerFixture) error {
				return swapLedgerDirectory(filepath.Dir(fixture.dbPath))
			},
			check: func(t *testing.T, err error) {
				if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
					t.Fatalf("binding migration with a refused swap = %v, want ERROR_ACCESS_DENIED", err)
				}
			},
		},
		{
			name: "carrier swap detected",
			id:   "qnt_38f3b2c9",
			swap: func(fixture projectLedgerFixture) error {
				return swapIdentityCarrier(fixture.root)
			},
			check: func(t *testing.T, err error) {
				if err == nil || !strings.Contains(err.Error(), "identity changed") {
					t.Fatalf("binding migration carrier swap error = %v", err)
				}
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectLedgerFixture(t, test.id)
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
			handle, err := OpenForExplicitMigration(context.Background(), fixture.root, ReadWrite)
			if err != nil {
				t.Fatalf("OpenForExplicitMigration: %v", err)
			}
			defer handle.Close()
			err = db.RunMigrationsThroughProjectLedgerBinding(
				handle.Database(),
				func(transaction db.MigrationTransaction) error {
					return handle.bindDuringFirstDurableSchemaMigration(
						context.Background(),
						transaction,
						time.Now().UTC(),
						func() error { return test.swap(fixture) },
					)
				},
			)
			test.check(t, err)
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
		})
	}
}

// Counterpart of TestProjectLedgerSQLiteIdentityBoundaryRejectsPersistentConnectWindowSwap.
// The directory swap is refused at both stages. Before SQLite connects only
// the anchor holds haft.db (with FILE_SHARE_DELETE), so a file-level swap is
// possible and the connect-window identity check rejects it with the Unix
// message; after the connect SQLite holds haft.db and the swap is refused.
func TestWindowsSQLiteIdentityBoundaryRefusesOrRejectsConnectWindowSwap(t *testing.T) {
	stages := []sqliteOpenStage{
		sqliteOpenStageBeforeConnect,
		sqliteOpenStageAfterConnectBeforeIdentityCheck,
	}
	for index, targetStage := range stages {
		for _, level := range []string{"directory", "file"} {
			t.Run(string(targetStage)+"/"+level, func(t *testing.T) {
				fixture := newProjectLedgerFixture(t, fmt.Sprintf("qnt_47f3b2%s%d", map[string]string{"directory": "d", "file": "f"}[level], index))
				replacementDirectory := prepareReplacementLedgerDirectory(t, fixture, "persistent")
				identity, identityAnchors, err := loadIdentityAnchored(fixture.root)
				if err != nil {
					t.Fatalf("loadIdentityAnchored: %v", err)
				}
				originalDirectory := filepath.Dir(fixture.dbPath)
				hook := func(stage sqliteOpenStage) error {
					if stage != targetStage {
						return nil
					}
					if level == "directory" {
						if err := os.Rename(originalDirectory, originalDirectory+".anchored"); err != nil {
							return err
						}
						return os.Rename(replacementDirectory, originalDirectory)
					}
					if err := os.Rename(fixture.dbPath, fixture.dbPath+".anchored"); err != nil {
						return err
					}
					return os.Rename(filepath.Join(replacementDirectory, "haft.db"), fixture.dbPath)
				}
				handle, err := openTopologyWithHook(context.Background(), identity, identityAnchors, ReadWrite, hook)
				if handle != nil {
					_ = handle.Close()
				}
				_ = closeAnchors(identityAnchors)
				switch {
				case level == "directory":
					if handle != nil || !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
						t.Fatalf("directory swap = %v, want ERROR_ACCESS_DENIED", err)
					}
				case targetStage == sqliteOpenStageBeforeConnect:
					if err == nil || !strings.Contains(err.Error(), "anchored database inode") {
						t.Fatalf("pre-connect file swap error = %v", err)
					}
				default:
					if handle != nil || !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
						t.Fatalf("post-connect file swap = %v, want ERROR_SHARING_VIOLATION", err)
					}
				}
			})
		}
	}
}

// Counterpart of TestProjectLedgerSQLiteObservedPathBoundaryDoesNotClaimSwapAndRestoreProof.
// That Unix test documents a limit: a replacement opened by SQLite and swapped
// back before the post-connect check is not detected. On Windows the swap and
// restore cannot be carried out: the directory swap is refused, and once
// SQLite has opened a swapped-in database file it cannot be renamed back, so
// the open fails instead of silently using the replacement.
func TestWindowsSQLiteObservedPathSwapAndRestoreIsRefused(t *testing.T) {
	for index, level := range []string{"directory", "file"} {
		t.Run(level, func(t *testing.T) {
			fixture := newProjectLedgerFixture(t, fmt.Sprintf("qnt_57f3b2c%d", index+8))
			replacementDirectory := prepareReplacementLedgerDirectory(t, fixture, "replacement")
			identity, identityAnchors, err := loadIdentityAnchored(fixture.root)
			if err != nil {
				t.Fatalf("loadIdentityAnchored: %v", err)
			}
			original := filepath.Dir(fixture.dbPath)
			replacement := replacementDirectory
			if level == "file" {
				original = fixture.dbPath
				replacement = filepath.Join(replacementDirectory, "haft.db")
			}
			hook := func(stage sqliteOpenStage) error {
				switch stage {
				case sqliteOpenStageBeforeConnect:
					if err := os.Rename(original, original+".anchored"); err != nil {
						return err
					}
					return os.Rename(replacement, original)
				case sqliteOpenStageAfterConnectBeforeIdentityCheck:
					if err := os.Rename(original, original+".opened-replacement"); err != nil {
						return err
					}
					return os.Rename(original+".anchored", original)
				default:
					return nil
				}
			}
			handle, err := openTopologyWithHook(context.Background(), identity, identityAnchors, ReadWrite, hook)
			if handle != nil {
				_ = handle.Close()
				t.Fatal("open succeeded through a swap-and-restore")
			}
			_ = closeAnchors(identityAnchors)
			want := windows.ERROR_ACCESS_DENIED
			if level == "file" {
				want = windows.ERROR_SHARING_VIOLATION
			}
			if !errors.Is(err, want) {
				t.Fatalf("%s swap-and-restore = %v, want %v", level, err, want)
			}
		})
	}
}
