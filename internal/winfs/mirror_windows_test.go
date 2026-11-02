package winfs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// Lstat is lstat(2): the final component is reported itself, intermediate
// junctions resolve, and the identity matches the held directory.
func TestLstatDoesNotFollowTheFinalComponent(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(filepath.Join(target, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	createJunction(t, filepath.Join(root, "junction"), target)
	info, err := Lstat(filepath.Join(root, "junction"))
	if err != nil {
		t.Fatalf("Lstat(junction): %v", err)
	}
	if !info.IsNameSurrogate() || info.IsDir() {
		t.Fatalf("Lstat(junction) = %+v, want the junction itself", info)
	}
	held, err := OpenDir(filepath.Join(target, "child"))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	heldInfo, err := held.Stat()
	if err != nil {
		t.Fatal(err)
	}
	direct, err := Lstat(filepath.Join(target, "child"))
	if err != nil || direct.ID != heldInfo.ID || !direct.IsDir() {
		t.Fatalf("Lstat(child) = %+v, %v; want the held directory %v", direct, err, heldInfo.ID)
	}
	through, err := Lstat(filepath.Join(root, "junction", "child"))
	if err != nil || through.ID != heldInfo.ID {
		t.Fatalf("Lstat through an intermediate junction = %+v, %v; want %v", through, err, heldInfo.ID)
	}
	if _, err := Lstat(filepath.Join(root, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Lstat(missing) = %v, want fs.ErrNotExist", err)
	}
	if _, err := Lstat("relative"); !errors.Is(err, ErrRelativePath) {
		t.Fatalf("Lstat(relative) = %v, want ErrRelativePath", err)
	}
}

// DirFromFile shares the handle of a held directory: relative operations
// address the directory that was opened, and closing the file closes the Dir.
func TestDirFromFileSharesTheHeldHandle(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "work")
	if err := os.Mkdir(original, 0o755); err != nil {
		t.Fatal(err)
	}
	held, err := OpenDir(original)
	if err != nil {
		t.Fatal(err)
	}
	file := held.File()
	dir, err := DirFromFile(file)
	if err != nil {
		t.Fatalf("DirFromFile: %v", err)
	}
	if err := os.Rename(original, filepath.Join(root, "moved")); err != nil {
		t.Fatalf("rename held directory: %v", err)
	}
	if err := os.Mkdir(original, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "entry", "bound")
	if got := readFile(t, filepath.Join(root, "moved", "entry")); got != "bound" {
		t.Fatalf("entry landed elsewhere: %q", got)
	}
	if _, err := os.Stat(filepath.Join(original, "entry")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("new directory at the old path must stay empty: %v", err)
	}
	heldID, err := Identity(file)
	if err != nil {
		t.Fatal(err)
	}
	sharedID, err := Identity(dir.File())
	if err != nil || sharedID != heldID {
		t.Fatalf("shared identity = %v, %v; want %v", sharedID, err, heldID)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := dir.Lstat("entry"); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("Lstat after the owner closed the file = %v, want os.ErrClosed", err)
	}
}

func TestDirFromFileRejectsFilesAndClosedHandles(t *testing.T) {
	dir, path := openTempDir(t)
	writeFile(t, dir, "file", "x")
	regular, err := OpenFile(filepath.Join(path, "file"), os.O_RDONLY, Inherited)
	if err != nil {
		t.Fatal(err)
	}
	defer regular.Close()
	if _, err := DirFromFile(regular); !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("DirFromFile(regular file) = %v, want ENOTDIR", err)
	}
	if _, err := DirFromFile(nil); err == nil {
		t.Fatal("DirFromFile(nil) was accepted")
	}
	closed, err := OpenDir(path)
	if err != nil {
		t.Fatal(err)
	}
	file := closed.File()
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := DirFromFile(file); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("DirFromFile(closed) = %v, want os.ErrClosed", err)
	}
}

// A junction opened through os.Open (which follows it) is still accepted: the
// handle is the target directory. The no-follow guarantee belongs to the open
// that produced the handle, which is why callers use winfs opens.
func TestDirFromFileTakesTheHandleNotThePath(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	createJunction(t, filepath.Join(root, "junction"), target)
	followed, err := os.Open(filepath.Join(root, "junction"))
	if err != nil {
		t.Fatal(err)
	}
	defer followed.Close()
	dir, err := DirFromFile(followed)
	if err != nil {
		t.Fatalf("DirFromFile(followed junction handle): %v", err)
	}
	info, err := dir.Stat()
	if err != nil || !info.IsDir() {
		t.Fatalf("Stat = %+v, %v; want the target directory", info, err)
	}
}

// OpenForSync flushes the held directory through a second handle that stays
// bound to it after the path is replaced (G2, G7).
func TestOpenForSyncFlushesTheHeldDirectory(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "work")
	if err := os.Mkdir(original, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := OpenDir(original)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if err := os.Rename(original, filepath.Join(root, "moved")); err != nil {
		t.Fatalf("rename held directory: %v", err)
	}
	if err := os.Mkdir(original, 0o755); err != nil {
		t.Fatal(err)
	}
	syncFile, err := dir.OpenForSync()
	if err != nil {
		t.Fatalf("OpenForSync: %v", err)
	}
	defer syncFile.Close()
	if err := syncFile.Sync(); err != nil {
		t.Fatalf("Sync through OpenForSync: %v", err)
	}
	heldID, err := Identity(dir.File())
	if err != nil {
		t.Fatal(err)
	}
	syncID, err := Identity(syncFile)
	if err != nil || syncID != heldID {
		t.Fatalf("sync handle identity = %v, %v; want the held %v", syncID, err, heldID)
	}
	if err := dir.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := dir.OpenForSync(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("OpenForSync on a closed Dir = %v, want os.ErrClosed", err)
	}
}
