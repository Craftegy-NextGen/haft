//go:build windows

package onboardingfs

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/m0n0x41d/haft/internal/winfs"
)

// Windows-only tests of the twin (ADR-093 E4). Junctions need no privilege, so
// they cover the no-follow boundary even where the symbolic-link contract
// test cannot create its fixture.

func createOnboardingJunction(t *testing.T, link string, target string) {
	t.Helper()
	output, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Fatalf("create junction %s: %v: %s", link, err, output)
	}
}

func TestWindowsMemoryDeferralRejectsJunctionedStateDirectory(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	createOnboardingJunction(t, filepath.Join(root, DirectoryName), outside)
	deferral := memoryDeferralFixture(
		t,
		"qnt_a11ce002",
		"c",
		time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC),
	)
	if _, err := Install(root, deferral); err == nil {
		t.Fatal("install followed a junctioned onboarding state directory")
	}
	if _, err := Read(root); err == nil {
		t.Fatal("read followed a junctioned onboarding state directory")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("writer escaped through the junction: %v, %v", entries, err)
	}
}

func TestWindowsMemoryDeferralRejectsJunctionedCarrier(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, DirectoryName), 0o755); err != nil {
		t.Fatal(err)
	}
	createOnboardingJunction(t, filepath.Join(root, DirectoryName, FileName), t.TempDir())
	if _, err := Read(root); err == nil {
		t.Fatal("junction carrier was accepted")
	}
}

// The carrier and the lock are created like Unix 0600 files: explicit owner
// and protected owner-only DACL (ADR-092 O2, 2a).
func TestWindowsMemoryDeferralCarrierAndLockArePrivate(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, DirectoryName), 0o755); err != nil {
		t.Fatal(err)
	}
	deferral := memoryDeferralFixture(
		t,
		"qnt_a11ce003",
		"d",
		time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC),
	)
	result, err := Install(root, deferral)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.(Created); !ok {
		t.Fatalf("install = %T, want Created", result)
	}
	for _, name := range []string{FileName, lockFileName} {
		file, err := os.Open(filepath.Join(root, DirectoryName, name))
		if err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
		err = winfs.CheckPrivate(file)
		_ = file.Close()
		if err != nil {
			t.Fatalf("%s is not private: %v", name, err)
		}
	}
}
