//go:build windows

package projectledger

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/m0n0x41d/haft/internal/winfs"
	"golang.org/x/sys/windows"
)

// createAnchorJunction makes link a junction to target; junctions need no
// privilege.
func createAnchorJunction(t *testing.T, link string, target string) {
	t.Helper()
	output, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Fatalf("create junction %s: %v: %s", link, err, output)
	}
}

// createAnchorSymlinkOrSkip creates a symbolic link, or skips when the process
// lacks SeCreateSymbolicLinkPrivilege and Developer Mode is off.
func createAnchorSymlinkOrSkip(t *testing.T, target string, link string) {
	t.Helper()
	err := os.Symlink(target, link)
	if errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
		t.Skipf("creating a symbolic link needs SeCreateSymbolicLinkPrivilege or Developer Mode: %v", err)
	}
	if err != nil {
		t.Fatalf("create symbolic link %s: %v", link, err)
	}
}

func openTestRootAnchor(t *testing.T, path string) topologyAnchor {
	t.Helper()
	anchor, err := openAbsoluteDirectoryAnchor(path, "test root")
	if err != nil {
		t.Fatalf("open root anchor %s: %v", path, err)
	}
	t.Cleanup(func() { _ = anchor.file.Close() })
	return anchor
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func anchorIdentity(t *testing.T, anchor topologyAnchor) winfs.FileID {
	t.Helper()
	id, err := winfs.Identity(anchor.file)
	if err != nil {
		t.Fatalf("identity of %s: %v", anchor.name, err)
	}
	return id
}

// The phase-1 sibling rejected every directory anchor; a real directory and a
// real file now anchor at each level and pass the shared topology check.
func TestWindowsAnchorsAcceptRealDirectoriesAndFiles(t *testing.T) {
	root := canonicalTempDirectory(t)
	mustMkdir(t, filepath.Join(root, "child"))
	mustWrite(t, filepath.Join(root, "child", "file"), "content")
	rootAnchor := openTestRootAnchor(t, root)
	if !rootAnchor.info.IsDir() {
		t.Fatalf("root anchor info = %v, want a directory", rootAnchor.info.Mode())
	}
	childPath := filepath.Join(root, "child")
	child, err := openChildDirectoryAnchor(rootAnchor, "child", childPath, "test child")
	if err != nil {
		t.Fatalf("open child directory anchor: %v", err)
	}
	defer child.file.Close()
	filePath := filepath.Join(childPath, "file")
	file, err := openChildRegularFileAnchor(child, "file", filePath, "test file")
	if err != nil {
		t.Fatalf("open child file anchor: %v", err)
	}
	defer file.file.Close()
	if !file.info.Mode().IsRegular() {
		t.Fatalf("file anchor info = %v, want a regular file", file.info.Mode())
	}
	content, err := readAnchoredContent(file)
	if err != nil || string(content) != "content" {
		t.Fatalf("anchored content = %q, %v", content, err)
	}
	if err := verifyAnchoredTopology([]topologyAnchor{rootAnchor, child, file}); err != nil {
		t.Fatalf("verify fresh anchors: %v", err)
	}
}

// open(O_DIRECTORY|O_NOFOLLOW) of the absolute root: a junction or symbolic
// link at the final component is ELOOP, a file is ENOTDIR, a missing path is
// fs.ErrNotExist, all with the Unix message shape.
func TestWindowsAbsoluteAnchorRejectsLinksAndNonDirectories(t *testing.T) {
	base := canonicalTempDirectory(t)
	target := filepath.Join(base, "target")
	mustMkdir(t, target)
	junction := filepath.Join(base, "junction")
	createAnchorJunction(t, junction, target)
	_, err := openAbsoluteDirectoryAnchor(junction, "project root")
	if !errors.Is(err, syscall.ELOOP) || !strings.HasPrefix(err.Error(), "open no-follow project root: ") {
		t.Fatalf("junction root anchor = %v, want ELOOP with the Unix message", err)
	}
	regular := filepath.Join(base, "regular")
	mustWrite(t, regular, "x")
	if _, err := openAbsoluteDirectoryAnchor(regular, "project root"); !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("file root anchor = %v, want ENOTDIR", err)
	}
	if _, err := openAbsoluteDirectoryAnchor(filepath.Join(base, "missing"), "project root"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing root anchor = %v, want fs.ErrNotExist", err)
	}
	link := filepath.Join(base, "dir-link")
	createAnchorSymlinkOrSkip(t, target, link)
	if _, err := openAbsoluteDirectoryAnchor(link, "project root"); !errors.Is(err, syscall.ELOOP) {
		t.Fatalf("symlink root anchor = %v, want ELOOP", err)
	}
}

// openat(O_DIRECTORY|O_NOFOLLOW) of a child directory: a junction is ELOOP
// and a file is ENOTDIR; the target outside is never anchored.
func TestWindowsChildDirectoryAnchorRejectsJunctionAndFile(t *testing.T) {
	root := canonicalTempDirectory(t)
	outside := canonicalTempDirectory(t)
	createAnchorJunction(t, filepath.Join(root, ".haft"), outside)
	mustWrite(t, filepath.Join(root, "regular"), "x")
	rootAnchor := openTestRootAnchor(t, root)
	_, err := openChildDirectoryAnchor(rootAnchor, ".haft", filepath.Join(root, ".haft"), "project .haft directory")
	if !errors.Is(err, syscall.ELOOP) ||
		!strings.HasPrefix(err.Error(), "open anchored no-follow project .haft directory: ") {
		t.Fatalf("junction child anchor = %v, want ELOOP with the Unix message", err)
	}
	if _, err := openChildDirectoryAnchor(rootAnchor, "regular", filepath.Join(root, "regular"), "child"); !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("file child directory anchor = %v, want ENOTDIR", err)
	}
	if _, err := openChildDirectoryAnchor(rootAnchor, "missing", filepath.Join(root, "missing"), "child"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing child directory anchor = %v, want fs.ErrNotExist", err)
	}
}

func TestWindowsChildDirectoryAnchorRejectsSymlink(t *testing.T) {
	root := canonicalTempDirectory(t)
	outside := canonicalTempDirectory(t)
	createAnchorSymlinkOrSkip(t, outside, filepath.Join(root, ".haft"))
	rootAnchor := openTestRootAnchor(t, root)
	_, err := openChildDirectoryAnchor(rootAnchor, ".haft", filepath.Join(root, ".haft"), "project .haft directory")
	if !errors.Is(err, syscall.ELOOP) {
		t.Fatalf("symlink child anchor = %v, want ELOOP", err)
	}
}

// openat(O_NOFOLLOW) of the identity carrier or database: a directory is not
// a regular file, and a junction or symbolic link gets the ELOOP message of
// ledger_unix.go.
func TestWindowsChildFileAnchorRejectsDirectoryAndLinks(t *testing.T) {
	root := canonicalTempDirectory(t)
	outside := canonicalTempDirectory(t)
	outsideFile := filepath.Join(outside, "project.yaml")
	mustWrite(t, outsideFile, "id: qnt_00000000\n")
	mustMkdir(t, filepath.Join(root, "directory.yaml"))
	createAnchorJunction(t, filepath.Join(root, "junction.yaml"), outside)
	rootAnchor := openTestRootAnchor(t, root)

	_, err := openChildRegularFileAnchor(rootAnchor, "directory.yaml", filepath.Join(root, "directory.yaml"), "project identity carrier")
	if err == nil || err.Error() != "anchored project identity carrier must be a real regular file, not a symlink" {
		t.Fatalf("directory file anchor = %v", err)
	}
	_, err = openChildRegularFileAnchor(rootAnchor, "junction.yaml", filepath.Join(root, "junction.yaml"), "project identity carrier")
	if err == nil || err.Error() != "anchored project identity carrier must be a real regular file, not a symlink" {
		t.Fatalf("junction file anchor = %v", err)
	}
	if _, err := openChildRegularFileAnchor(rootAnchor, "missing.yaml", filepath.Join(root, "missing.yaml"), "carrier"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file anchor = %v, want fs.ErrNotExist", err)
	}
	createAnchorSymlinkOrSkip(t, outsideFile, filepath.Join(root, "link.yaml"))
	_, err = openChildRegularFileAnchor(rootAnchor, "link.yaml", filepath.Join(root, "link.yaml"), "project identity carrier")
	if err == nil || err.Error() != "anchored project identity carrier must be a real regular file, not a symlink" {
		t.Fatalf("symlink file anchor = %v", err)
	}
}

// G2: a child open goes through the parent anchor's handle, so after the
// parent's path is renamed and replaced the child comes from the anchored
// directory, never from the directory now at the path; the shared topology
// check reports the swap.
func TestWindowsChildAnchorsUseTheParentHandleAfterPathSwap(t *testing.T) {
	base := canonicalTempDirectory(t)
	parentPath := filepath.Join(base, "parent")
	mustMkdir(t, parentPath)
	mustMkdir(t, filepath.Join(parentPath, "child"))
	mustWrite(t, filepath.Join(parentPath, "carrier"), "anchored")
	parent := openTestRootAnchor(t, parentPath)
	parentID := anchorIdentity(t, parent)

	movedPath := filepath.Join(base, "moved")
	if err := os.Rename(parentPath, movedPath); err != nil {
		t.Fatalf("rename anchored parent: %v", err)
	}
	mustMkdir(t, parentPath)
	mustMkdir(t, filepath.Join(parentPath, "child"))
	mustWrite(t, filepath.Join(parentPath, "carrier"), "replacement")

	child, err := openChildDirectoryAnchor(parent, "child", filepath.Join(parentPath, "child"), "child")
	if err != nil {
		t.Fatalf("open child through the held parent: %v", err)
	}
	defer child.file.Close()
	wantChild, err := winfs.PathIdentity(filepath.Join(movedPath, "child"))
	if err != nil {
		t.Fatal(err)
	}
	if got := anchorIdentity(t, child); got != wantChild {
		t.Fatalf("child anchor identity = %v, want the anchored parent's child %v", got, wantChild)
	}
	carrier, err := openChildRegularFileAnchor(parent, "carrier", filepath.Join(parentPath, "carrier"), "carrier")
	if err != nil {
		t.Fatalf("open file through the held parent: %v", err)
	}
	defer carrier.file.Close()
	content, err := readAnchoredContent(carrier)
	if err != nil || string(content) != "anchored" {
		t.Fatalf("file through the held parent = %q, %v; want the anchored bytes", content, err)
	}
	if got := anchorIdentity(t, parent); got != parentID {
		t.Fatalf("parent anchor identity changed: %v, want %v", got, parentID)
	}
	err = verifyAnchoredTopology([]topologyAnchor{parent})
	if err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("topology check after the swap = %v, want identity changed", err)
	}
}

// A parent anchor that is missing or closed cannot anchor a child.
func TestWindowsChildAnchorRequiresAnOpenParent(t *testing.T) {
	root := canonicalTempDirectory(t)
	mustMkdir(t, filepath.Join(root, "child"))
	if _, err := openChildDirectoryAnchor(topologyAnchor{name: "absent parent"}, "child", filepath.Join(root, "child"), "child"); err == nil {
		t.Fatal("child anchored without a parent handle")
	}
	parent, err := openAbsoluteDirectoryAnchor(root, "closed parent")
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.file.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = openChildDirectoryAnchor(parent, "child", filepath.Join(root, "child"), "child")
	if !errors.Is(err, os.ErrClosed) {
		t.Fatalf("child of a closed parent = %v, want os.ErrClosed", err)
	}
}

// End to end through loadIdentityAnchored: a real project loads, and a
// junction at the project root or at .haft is rejected before any carrier is
// read through it.
func TestWindowsLoadIdentityRejectsJunctionsAtEachLevel(t *testing.T) {
	root := canonicalTempDirectory(t)
	writeProjectIdentity(t, root, "qnt_a1b2c3d4")
	identity, anchors, err := loadIdentityAnchored(root)
	if err != nil {
		t.Fatalf("load real project identity: %v", err)
	}
	if identity.ProjectID().String() != "qnt_a1b2c3d4" {
		t.Fatalf("project id = %s", identity.ProjectID())
	}
	if err := verifyAnchoredTopology(anchors); err != nil {
		t.Fatalf("verify real project anchors: %v", err)
	}
	if err := closeAnchors(anchors); err != nil {
		t.Fatal(err)
	}

	junctionRoot := filepath.Join(canonicalTempDirectory(t), "root-junction")
	createAnchorJunction(t, junctionRoot, root)
	if _, err := LoadIdentity(junctionRoot); err == nil {
		t.Fatal("a junction project root was admitted")
	}

	foreign := canonicalTempDirectory(t)
	writeProjectIdentity(t, foreign, "qnt_f0f0f0f0")
	swapped := canonicalTempDirectory(t)
	createAnchorJunction(t, filepath.Join(swapped, ".haft"), filepath.Join(foreign, ".haft"))
	_, err = LoadIdentity(swapped)
	if !errors.Is(err, syscall.ELOOP) {
		t.Fatalf("junction .haft identity load = %v, want ELOOP", err)
	}
}

// The anchor survives as an identity: its FILE_ID_INFO and the os.SameFile
// view used by ledger.go agree with the entry, and replacing the entry with an
// equal-content file is reported.
func TestWindowsFileAnchorSurvivesAsIdentity(t *testing.T) {
	root := canonicalTempDirectory(t)
	writeProjectIdentity(t, root, "qnt_b1b2c3d4")
	_, anchors, err := loadIdentityAnchored(root)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAnchors(anchors)
	carrier := anchors[len(anchors)-1]
	carrierPath := filepath.Join(root, ".haft", "project.yaml")
	if carrier.path != carrierPath {
		t.Fatalf("carrier anchor path = %s, want %s", carrier.path, carrierPath)
	}
	pathID, err := winfs.PathIdentity(carrierPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := anchorIdentity(t, carrier); got != pathID {
		t.Fatalf("anchor identity = %v, path identity = %v", got, pathID)
	}
	content, err := os.ReadFile(carrierPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(carrierPath, carrierPath+".displaced"); err != nil {
		t.Fatalf("displace anchored carrier: %v", err)
	}
	if err := os.WriteFile(carrierPath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	err = verifyAnchoredTopology(anchors)
	if err == nil || !strings.Contains(err.Error(), "project identity carrier identity changed") {
		t.Fatalf("topology check after an equal-content swap = %v", err)
	}
}
