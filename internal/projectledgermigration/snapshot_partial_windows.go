//go:build windows

package projectledgermigration

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/m0n0x41d/haft/internal/winfs"
)

// reserveSnapshotPartialPath checks that the partial snapshot path is free,
// as on Unix, and then creates it empty and owner-only (ADR-092 O2, 2a):
// winfs.OpenFile with os.O_CREATE|os.O_EXCL and winfs.Private, closed at once.
//
// SQLite's VACUUM INTO accepts an existing empty file and writes into it
// without touching its security descriptor, whereas a file it creates itself
// gets the DACL inherited from the ledger directory and an os.Chmod cannot
// change a DACL. So the snapshot is private by construction, and the
// winfs.CheckPrivate in insecureSnapshotFileMode holds for it.
//
// The exclusive create refuses a file that appears between the absence check
// and here, the same way VACUUM INTO refuses a non-empty target on Unix. When
// the later absence check of the final path fails, the empty private partial
// file stays, as every partial file does after a failure on Unix.
func reserveSnapshotPartialPath(path string) error {
	if err := requireSnapshotPathAbsent(path); err != nil {
		return err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve serve migration snapshot path %s: %w", path, err)
	}
	file, err := winfs.OpenFile(
		absolute,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		winfs.Private,
	)
	if err != nil {
		return fmt.Errorf("create private serve migration snapshot %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close private serve migration snapshot %s: %w", path, err)
	}
	return nil
}
