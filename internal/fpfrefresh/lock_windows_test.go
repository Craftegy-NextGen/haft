//go:build windows

package fpfrefresh

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/m0n0x41d/haft/internal/winfs"
)

// writeInheritedFile creates a file that takes the security of its directory,
// which is never the owner-only descriptor of a lock carrier.
func writeInheritedFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireOperationLockCreatesPrivateContentFreeCarrier(t *testing.T) {
	t.Parallel()

	receiptPath := filepath.Join(t.TempDir(), "apply-receipt.json")
	release, err := acquireOperationLock(receiptPath)
	if err != nil {
		t.Fatalf("acquireOperationLock() error = %v", err)
	}
	carrierPath := receiptPath + ".operation.lock"
	if carrierNotPrivate(carrierPath) {
		t.Fatal("new operation lock carrier is not private")
	}
	release()
	release()

	content, err := os.ReadFile(carrierPath)
	if err != nil || len(content) != 0 {
		t.Fatalf("operation lock carrier after release = %q, %v; want empty", content, err)
	}
}

func TestAcquireOperationLockStaysHeldUntilReleased(t *testing.T) {
	t.Parallel()

	receiptPath := filepath.Join(t.TempDir(), "apply-receipt.json")
	release, err := acquireOperationLock(receiptPath)
	if err != nil {
		t.Fatalf("acquireOperationLock() error = %v", err)
	}
	if _, err := acquireOperationLock(receiptPath); !errors.Is(err, ErrReceiptBusy) {
		release()
		t.Fatalf("second acquire error = %v, want ErrReceiptBusy", err)
	}
	release()
	again, err := acquireOperationLock(receiptPath)
	if err != nil {
		t.Fatalf("acquire after release error = %v", err)
	}
	again()
}

func TestAcquireOperationLockRejectsCarrierThatIsNotPrivate(t *testing.T) {
	t.Parallel()

	receiptPath := filepath.Join(t.TempDir(), "apply-receipt.json")
	writeInheritedFile(t, receiptPath+".operation.lock")

	_, err := acquireOperationLock(receiptPath)
	if !errors.Is(err, ErrReceiptStale) || !errors.Is(err, winfs.ErrNotPrivate) {
		t.Fatalf("acquireOperationLock(inherited carrier) error = %v, want ErrReceiptStale over winfs.ErrNotPrivate", err)
	}
}

func TestAcquireOperationLockRejectsDirectoryAtCarrierPath(t *testing.T) {
	t.Parallel()

	receiptPath := filepath.Join(t.TempDir(), "apply-receipt.json")
	if err := os.Mkdir(receiptPath+".operation.lock", 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireOperationLock(receiptPath); err == nil {
		t.Fatal("acquireOperationLock(directory carrier) error = nil")
	}
}

func TestWithReceiptExclusiveLockHoldsLockDuringEffect(t *testing.T) {
	t.Parallel()

	receiptPath := filepath.Join(t.TempDir(), "apply-receipt.json")
	ran := false
	err := withReceiptExclusiveLock(receiptPath, func() error {
		ran = true
		nested := withReceiptExclusiveLock(receiptPath, func() error {
			t.Error("nested effect ran under a held receipt lock")
			return nil
		})
		if !errors.Is(nested, ErrReceiptBusy) {
			t.Errorf("nested lock error = %v, want ErrReceiptBusy", nested)
		}
		return nil
	})
	if err != nil || !ran {
		t.Fatalf("withReceiptExclusiveLock() = %v, ran = %v", err, ran)
	}
	if err := withReceiptExclusiveLock(receiptPath, func() error { return nil }); err != nil {
		t.Fatalf("lock after release error = %v", err)
	}
	if carrierNotPrivate(receiptPath + ".lock") {
		t.Fatal("receipt lock carrier is not private")
	}
}

func TestWithReceiptExclusiveLockRejectsCarrierThatIsNotPrivate(t *testing.T) {
	t.Parallel()

	receiptPath := filepath.Join(t.TempDir(), "apply-receipt.json")
	writeInheritedFile(t, receiptPath+".lock")

	err := withReceiptExclusiveLock(receiptPath, func() error {
		t.Error("effect ran with an inherited lock carrier")
		return nil
	})
	if !errors.Is(err, ErrReceiptInvalid) || !errors.Is(err, winfs.ErrNotPrivate) {
		t.Fatalf("withReceiptExclusiveLock(inherited carrier) error = %v, want ErrReceiptInvalid over winfs.ErrNotPrivate", err)
	}
}

func TestExecutableModeMissingFollowsPathExtension(t *testing.T) {
	t.Setenv("PATHEXT", ".COM;.EXE;.BAT;.CMD")
	directory := t.TempDir()
	for name, wantMissing := range map[string]bool{
		"tool.exe": false,
		"TOOL.EXE": false,
		"tool.cmd": false,
		"tool.sh":  true,
		"tool":     true,
	} {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte("x"), 0o700); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := executableModeMissing(path, info); got != wantMissing {
			t.Errorf("executableModeMissing(%s) = %v, want %v", name, got, wantMissing)
		}
	}
}

func TestStateDirectoryNotPrivateRejectsOnlyReadOnlyDirectory(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if stateDirectoryNotPrivate(info) {
		t.Fatalf("new directory %v reported as not private", info.Mode())
	}
}

func TestEffectsCacheDirectoryNotPrivateReadsOwnerAndDACL(t *testing.T) {
	t.Parallel()

	parent, err := winfs.OpenDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = parent.Close()
	}()
	if err := parent.Mkdir("private", winfs.Private); err != nil {
		t.Fatal(err)
	}
	if err := parent.Mkdir("inherited", winfs.Inherited); err != nil {
		t.Fatal(err)
	}
	for name, wantNotPrivate := range map[string]bool{"private": false, "inherited": true} {
		path := filepath.Join(parent.Name(), name)
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := effectsCacheDirectoryNotPrivate(path, info); got != wantNotPrivate {
			t.Errorf("effectsCacheDirectoryNotPrivate(%s) = %v, want %v", name, got, wantNotPrivate)
		}
	}
}
