//go:build !windows

package initfs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

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
	if !info.Mode().IsRegular() ||
		info.Mode().Perm() != 0o600 ||
		info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("manifest lock is not a safe regular 0600 carrier: %v", info.Mode())
	}
}

func canonicalManifestModeDiffers(info os.FileInfo) bool {
	return info.Mode().Perm() != 0o644
}
