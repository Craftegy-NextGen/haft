//go:build windows

package agenthostrestart

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Windows-only tests of the twin (ADR-093 E4). Junctions need no privilege, so
// they cover the no-follow boundary even where the symbolic-link contract
// tests cannot create their fixtures.

func createRestartJunction(t *testing.T, link string, target string) {
	t.Helper()
	output, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Fatalf("create junction %s: %v: %s", link, err, output)
	}
}

func TestWindowsStoreRejectsJunctionedRestartDirectory(t *testing.T) {
	root := newRestartProject(t)
	outside := t.TempDir()
	createRestartJunction(t, filepath.Join(root, ".haft", RestartDirectoryName), outside)
	store, err := NewStore(root)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	checkpoint := newPreparedFixture(t, root, "restart-junction", digestOf('2'))
	err = store.Prepare(checkpoint)
	if err == nil || !strings.Contains(err.Error(), "non-symlink directory") {
		t.Fatalf("Prepare junction error = %v", err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("Load followed a junctioned restart directory")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("store wrote through the junction: %v, %v", entries, err)
	}
}

func TestWindowsStoreRejectsJunctionedHaftDirectoryAndProjectRoot(t *testing.T) {
	outside := t.TempDir()
	root := t.TempDir()
	createRestartJunction(t, filepath.Join(root, ".haft"), outside)
	if _, err := NewStore(root); err == nil || !strings.Contains(err.Error(), "non-symlink directory") {
		t.Fatalf("NewStore accepted a junctioned .haft directory: %v", err)
	}
	junctionRoot := filepath.Join(t.TempDir(), "root")
	createRestartJunction(t, junctionRoot, newRestartProject(t))
	if _, err := NewStore(junctionRoot); err == nil || !strings.Contains(err.Error(), "non-symlink directory") {
		t.Fatalf("NewStore accepted a junctioned project root: %v", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("store wrote through the junction: %v, %v", entries, err)
	}
}

func TestWindowsStoreCreatesPrivateRestartTree(t *testing.T) {
	root := newRestartProject(t)
	store, err := NewStore(root)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	prepared := newPreparedFixture(t, root, "restart-private-tree", digestOf('3'))
	if err := store.Prepare(prepared); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	assertPrivateDirectoryMode(t, store.directoryPath)
	assertPrivateDirectoryMode(t, store.attemptsDirectoryPath)
	for _, name := range []string{lockFileName, CheckpointFileName} {
		assertPrivateRegularMode(t, filepath.Join(store.directoryPath, name))
	}
	assertPrivateRegularMode(t, attemptMarkerPath(t, store, prepared.DesiredHaftBinaryDigest()))
}

func TestWindowsReceiptFilesAreCreatedPrivateAndReplacedWithoutStages(t *testing.T) {
	root := newRestartProject(t)
	store, err := NewStore(root)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	prepared := newPreparedFixture(t, root, "restart-receipt-private", digestOf('4'))
	if err := store.Prepare(prepared); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	path := store.restartReceiptPath(prepared.restartID, liveMCPReceiptSuffix)
	first := []byte("{\"first\":true}\n")
	if err := createPrivateRestartFile(path, first); err != nil {
		t.Fatalf("createPrivateRestartFile: %v", err)
	}
	assertPrivateRegularMode(t, path)
	if err := createPrivateRestartFile(path, first); !os.IsExist(err) {
		t.Fatalf("duplicate create error = %v, want exists", err)
	}
	second := []byte("{\"second\":true}\n")
	if err := replacePrivateRestartFileCAS(path, []byte("stale\n"), second); err != ErrConcurrentUpdate {
		t.Fatalf("stale CAS error = %v", err)
	}
	if err := replacePrivateRestartFileCAS(path, first, second); err != nil {
		t.Fatalf("replacePrivateRestartFileCAS: %v", err)
	}
	assertPrivateRegularMode(t, path)
	content, err := os.ReadFile(path)
	if err != nil || string(content) != string(second) {
		t.Fatalf("replaced receipt = %q, %v", content, err)
	}
	entries, err := os.ReadDir(store.directoryPath)
	if err != nil {
		t.Fatalf("read restart directory: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), restartReceiptStagePrefix) {
			t.Fatalf("receipt stage remains: %s", entry.Name())
		}
	}
}

func TestWindowsExecutableNeedsAnExecutableExtension(t *testing.T) {
	directory := t.TempDir()
	plain := filepath.Join(directory, "supervisor")
	runnable := filepath.Join(directory, "supervisor.exe")
	for _, path := range []string{plain, runnable} {
		if err := os.WriteFile(path, []byte("fixture"), 0o700); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	if err := ensureExecutablePath(plain); err == nil {
		t.Fatal("a file without an executable extension was accepted")
	}
	if err := ensureExecutablePath(runnable); err != nil {
		t.Fatalf("a .exe file was rejected: %v", err)
	}
	if err := ensureExecutablePath(directory); err == nil {
		t.Fatal("a directory was accepted as an executable")
	}
}
