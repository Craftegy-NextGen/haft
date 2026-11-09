//go:build windows

package initfs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/m0n0x41d/haft/internal/winfs"
)

// assertNoManifestStagesAndSafeLock is the Windows equivalent (ADR-093 E3) of
// the helper in manifest_store_unix_test.go. The stage checks are the same.
// For the lock, "safe regular 0600 carrier" means on Windows a regular file
// that is not a link and passes winfs.CheckPrivate (token user owner,
// protected owner-only DACL, one link; ADR-092 O2), because Go reports every
// Windows file mode as 0666 or 0444.
func assertNoManifestStagesAndSafeLock(t *testing.T, manifestPath string) {
	t.Helper()
	stages, err := filepath.Glob(
		filepath.Join(
			filepath.Dir(manifestPath),
			canonicalCarrierStagePrefix(filepath.Base(manifestPath))+"*",
		),
	)
	if err != nil {
		t.Fatalf("glob manifest stages: %v", err)
	}
	if len(stages) != 0 {
		t.Fatalf("manifest stages remain: %v", stages)
	}
	journalStages, err := filepath.Glob(
		filepath.Join(
			filepath.Dir(manifestPath),
			canonicalCarrierStagePrefix(filepath.Base(manifestPath+".pending"))+"*",
		),
	)
	if err != nil {
		t.Fatalf("glob publication journal stages: %v", err)
	}
	if len(journalStages) != 0 {
		t.Fatalf("publication journal stages remain: %v", journalStages)
	}
	info, err := os.Lstat(manifestPath + ".lock")
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatalf("inspect manifest lock: %v", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("manifest lock is not a regular carrier: %v", info.Mode())
	}
	file, err := winfs.OpenFile(manifestPath+".lock", os.O_RDONLY, winfs.Inherited)
	if err != nil {
		t.Fatalf("open manifest lock: %v", err)
	}
	defer file.Close()
	if err := winfs.CheckPrivate(file); err != nil {
		t.Fatalf("manifest lock is not a private carrier: %v", err)
	}
}

// canonicalManifestModeDiffers is the Windows image of the Unix test
// predicate `info.Mode().Perm() != 0o644`: 0o644 has the owner-write bit, so
// Go's Windows Chmod clears the read-only attribute and Stat reports 0o666.
func canonicalManifestModeDiffers(info os.FileInfo) bool {
	return info.Mode().Perm() != 0o666
}
