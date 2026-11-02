package winfs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestIdentityEqualForSameFileDifferentForOthers(t *testing.T) {
	dir, path := openTempDir(t)
	writeFile(t, dir, "a", "x")
	writeFile(t, dir, "b", "x")
	first, err := dir.OpenFile("a", os.O_RDONLY, Inherited)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenFile(filepath.Join(path, "a"), os.O_RDONLY, Inherited)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	other, err := dir.OpenFile("b", os.O_RDONLY, Inherited)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()

	firstID := mustIdentity(t, first)
	if secondID := mustIdentity(t, second); firstID != secondID {
		t.Fatalf("two handles of one file: %v != %v", firstID, secondID)
	}
	otherID := mustIdentity(t, other)
	if firstID == otherID {
		t.Fatalf("two files share an identity: %v", firstID)
	}
	if !firstID.SameVolume(otherID) {
		t.Fatalf("files in one directory must be on the same volume: %v %v", firstID, otherID)
	}
	if firstID.String() == "" {
		t.Fatal("String must not be empty")
	}
	attached, err := dir.Lstat("a")
	if err != nil || attached.ID != firstID {
		t.Fatalf("Lstat identity %v, %v; want %v", attached.ID, err, firstID)
	}
	stat, err := Fstat(first)
	if err != nil || stat.ID != firstID || !stat.IsRegular() || stat.NumberOfLinks != 1 || stat.Size != 1 {
		t.Fatalf("Fstat = %+v, %v", stat, err)
	}
	pathID, err := PathIdentity(filepath.Join(path, "a"))
	if err != nil || pathID != firstID {
		t.Fatalf("PathIdentity = %v, %v; want %v", pathID, err, firstID)
	}
}

func TestPathIdentityFollowsLstatDoesNot(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	createJunction(t, filepath.Join(root, "junction"), target)
	targetID, err := PathIdentity(target)
	if err != nil {
		t.Fatal(err)
	}
	followed, err := PathIdentity(filepath.Join(root, "junction"))
	if err != nil || followed != targetID {
		t.Fatalf("PathIdentity(junction) = %v, %v; want the target %v", followed, err, targetID)
	}
	dir, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	link, err := dir.Lstat("junction")
	if err != nil {
		t.Fatal(err)
	}
	if link.ID == targetID || !link.IsNameSurrogate() || link.IsDir() {
		t.Fatalf("Lstat(junction) must describe the junction itself: %+v", link)
	}
}

func TestIdentityOfClosedFile(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "closed")
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if _, err := Identity(file); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("Identity(closed) = %v, want os.ErrClosed", err)
	}
	if _, err := PathIdentity(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("PathIdentity(missing) = %v, want os.ErrNotExist", err)
	}
}

func mustIdentity(t *testing.T, file *os.File) FileID {
	t.Helper()
	id, err := Identity(file)
	if err != nil {
		t.Fatalf("Identity(%s): %v", file.Name(), err)
	}
	return id
}
