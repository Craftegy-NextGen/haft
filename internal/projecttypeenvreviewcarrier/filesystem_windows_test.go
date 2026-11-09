//go:build windows

package projecttypeenvreviewcarrier

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/m0n0x41d/haft/internal/winfs"
	"golang.org/x/sys/windows"
)

// Windows-only tests of the twin (ADR-093 E4). Junctions need no privilege, so
// they cover the no-follow boundary even where the symbolic-link contract
// tests cannot create their fixtures.

func createReviewJunction(t *testing.T, link string, target string) {
	t.Helper()
	output, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Fatalf("create junction %s: %v: %s", link, err, output)
	}
}

// installedCarrierNotPrivate is the Windows sibling of the Unix 0600 check
// extracted from TestInstallCreatesRereadsAndExactlyReusesCarrier
// (filesystem_unix_test.go). Go reports 0666 for every writable file on
// Windows, so privacy is the explicit owner and protected owner-only DACL of
// winfs.Private (ADR-092 O2, 2a). The shared failure message then prints the
// Go mode; TestWindowsInstalledCarrierAndLockArePrivate reports the DACL cause.
func installedCarrierNotPrivate(info os.FileInfo, root string) bool {
	if !info.Mode().IsRegular() {
		return true
	}
	file, err := os.Open(filepath.Join(root, DirectoryName, FileName))
	if err != nil {
		return true
	}
	defer file.Close()
	return winfs.CheckPrivate(file) != nil
}

// The Windows counterpart of the 0600 assertion in
// TestInstallCreatesRereadsAndExactlyReusesCarrier, with the cause reported:
// the carrier and the lock carry the explicit owner and protected owner-only
// DACL of winfs.Private (ADR-092 O2, 2a).
func TestWindowsInstalledCarrierAndLockArePrivate(t *testing.T) {
	root := newProjectRoot(t)
	proposed := mustCarrier(t, []byte("{\"review\":\"private\"}\n"))
	if _, err := Install(root, proposed); err != nil {
		t.Fatalf("install carrier: %v", err)
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

func TestWindowsBoundaryRejectsJunctionedHaftDirectory(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	createReviewJunction(t, filepath.Join(root, DirectoryName), outside)
	proposed := mustCarrier(t, []byte("{\"review\":\"must-not-escape\"}\n"))
	if _, err := Install(root, proposed); err == nil {
		t.Fatal("installation followed a junctioned .haft directory")
	}
	if _, err := Read(root); err == nil {
		t.Fatal("read followed a junctioned .haft directory")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("writer escaped through the junction: %v, %v", entries, err)
	}
}

func TestWindowsBoundaryRejectsJunctionedCarrierAndLock(t *testing.T) {
	for _, name := range []string{FileName, lockFileName} {
		t.Run(name, func(t *testing.T) {
			root := newProjectRoot(t)
			outside := t.TempDir()
			createReviewJunction(t, filepath.Join(root, DirectoryName, name), outside)
			proposed := mustCarrier(t, []byte("{\"review\":\"must-not-follow\"}\n"))
			if _, err := Install(root, proposed); err == nil {
				t.Fatalf("installation accepted a junction at %s", name)
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatalf("writer escaped through the junction: %v, %v", entries, err)
			}
		})
	}
}

// Counterpart of TestHeldDirectoryRejectsAttachedPathSwap (kept for Linux and
// macOS in filesystem_unix_test.go, ADR-093 E3), which renames the held .haft
// directory while the mutation lock inside it is open. NTFS refuses to rename
// a directory while an entry below it is open, whatever the share mode
// (measured: ERROR_ACCESS_DENIED), so on Windows that swap cannot happen
// while a mutation holds the directory. This test shows the refusal, and that
// the identity check still catches a swap where Windows allows one: a read
// capability over a project without a lock file holds only directory handles.
func TestWindowsHeldDirectoryRefusesOrDetectsPathSwap(t *testing.T) {
	root := newProjectRoot(t)
	mutation, err := openHeldDirectoryForMutation(root)
	if err != nil {
		t.Fatalf("open held directory: %v", err)
	}
	heldPath := filepath.Join(root, ".haft-held")
	err = os.Rename(filepath.Join(root, DirectoryName), heldPath)
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("rename of the held .haft directory = %v, want ERROR_ACCESS_DENIED", err)
	}
	if err := mutation.lockExclusive(); err != nil {
		t.Fatalf("lock after a refused swap: %v", err)
	}
	mutation.unlock()
	if err := mutation.Close(); err != nil {
		t.Fatal(err)
	}

	readRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(readRoot, DirectoryName), 0o755); err != nil {
		t.Fatal(err)
	}
	carrier := mustCarrier(t, []byte("{\"review\":\"held\"}\n"))
	if err := os.WriteFile(filepath.Join(readRoot, DirectoryName, FileName), carrier.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := openHeldDirectoryForRead(readRoot)
	if err != nil {
		t.Fatalf("open read capability: %v", err)
	}
	defer reader.Close()
	if reader.lock != nil {
		t.Fatal("read capability opened a lock that does not exist")
	}
	if err := os.Rename(filepath.Join(readRoot, DirectoryName), filepath.Join(readRoot, ".haft-held")); err != nil {
		t.Fatalf("move held .haft without open entries: %v", err)
	}
	outside := t.TempDir()
	createReviewJunction(t, filepath.Join(readRoot, DirectoryName), outside)
	if err := reader.lockShared(); err == nil {
		t.Fatal("held directory accepted a swapped .haft path")
	}
	if _, err := reader.readTarget(); err == nil {
		t.Fatal("read through a swapped .haft path")
	}
	if _, err := os.Stat(filepath.Join(outside, FileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reader touched the swapped path: %v", err)
	}
}
