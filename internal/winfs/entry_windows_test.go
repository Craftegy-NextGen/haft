package winfs

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/sys/windows"
)

func TestRemoveWithOpenHandleUsesPOSIXSemantics(t *testing.T) {
	dir, _ := openTempDir(t)
	writeFile(t, dir, "victim", "still readable")
	open, err := dir.OpenFile("victim", os.O_RDONLY, Inherited)
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()
	if err := dir.Remove("victim"); err != nil {
		t.Fatalf("Remove with an open handle: %v", err)
	}
	if _, err := dir.Lstat("victim"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("name must be gone at once, Lstat = %v", err)
	}
	replacement, err := dir.OpenFile("victim", os.O_WRONLY|os.O_CREATE|os.O_EXCL, Inherited)
	if err != nil {
		t.Fatalf("the name must be free for a new file at once: %v", err)
	}
	_ = replacement.Close()
	content, err := io.ReadAll(open)
	if err != nil || string(content) != "still readable" {
		t.Fatalf("the open handle must keep the old data: %q, %v", content, err)
	}
}

func TestRemoveEntryKinds(t *testing.T) {
	dir, path := openTempDir(t)
	if err := dir.Remove("missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Remove(missing) = %v, want fs.ErrNotExist", err)
	}
	if err := dir.Mkdir("empty", Inherited); err != nil {
		t.Fatal(err)
	}
	if err := dir.Remove("empty"); err != nil {
		t.Fatalf("Remove(empty directory): %v", err)
	}
	if err := dir.Mkdir("full", Inherited); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "full", "x"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := dir.Remove("full"); err == nil {
		t.Fatal("Remove(non-empty directory) must fail")
	}
	target := filepath.Join(path, "full")
	createJunction(t, filepath.Join(path, "junction"), target)
	if err := dir.Remove("junction"); err != nil {
		t.Fatalf("Remove(junction): %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "x")); err != nil {
		t.Fatalf("removing a junction must not touch its target: %v", err)
	}
	writeFile(t, dir, "readonly", "x")
	if err := os.Chmod(filepath.Join(path, "readonly"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := dir.Remove("readonly"); err != nil {
		t.Fatalf("Remove(read-only file) like unlink: %v", err)
	}
}

// The third step of deleteHandle, for systems without POSIX delete, runs
// directly here: it clears the read-only attribute and is counted.
func TestDeleteHandleClassicClearsReadOnlyAndCounts(t *testing.T) {
	dir, path := openTempDir(t)
	writeFile(t, dir, "victim", "x")
	setOnlyReadOnly(t, filepath.Join(path, "victim"))
	before := ClassicDeleteFallbacks()
	handle, err := openForDelete(mustHandle(t, dir), "victim")
	if err != nil {
		t.Fatal(err)
	}
	if err := deleteHandleClassic(handle); err != nil {
		closeHandle(handle)
		t.Fatalf("classic disposition on a read-only file: %v", err)
	}
	closeHandle(handle)
	if _, err := dir.Lstat("victim"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the name must be gone after the last close: %v", err)
	}
	if got := ClassicDeleteFallbacks() - before; got != 1 {
		t.Fatalf("classic deletes counted %d, want 1", got)
	}
}

// The second step of deleteHandle, for Windows 10 1607 to 1803 (POSIX delete
// without FILE_DISPOSITION_IGNORE_READONLY_ATTRIBUTE), runs directly here.
func TestPOSIXDeleteWithoutIgnoreReadOnly(t *testing.T) {
	dir, path := openTempDir(t)
	writeFile(t, dir, "victim", "x")
	setOnlyReadOnly(t, filepath.Join(path, "victim"))
	handle, err := openForDelete(mustHandle(t, dir), "victim")
	if err != nil {
		t.Fatal(err)
	}
	defer closeHandle(handle)
	if status := deletePOSIX(handle, false); status != windows.STATUS_CANNOT_DELETE {
		t.Fatalf("POSIX delete of a read-only file without the ignore flag = %v, want STATUS_CANNOT_DELETE", status)
	}
	if err := clearReadOnly(handle); err != nil {
		t.Fatalf("clearReadOnly: %v", err)
	}
	if status := deletePOSIX(handle, false); status != nil {
		t.Fatalf("POSIX delete after clearing read-only = %v", status)
	}
	if _, err := dir.Lstat("victim"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("POSIX delete must remove the name at once: %v", err)
	}
}

// setOnlyReadOnly leaves FILE_ATTRIBUTE_READONLY as the only attribute, so
// clearing it must write FILE_ATTRIBUTE_NORMAL, not the "unchanged" zero.
func setOnlyReadOnly(t *testing.T, path string) {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetFileAttributes(name, windows.FILE_ATTRIBUTE_READONLY); err != nil {
		t.Fatal(err)
	}
}

// Unix unlink and rename need write access to the directory only; the file's
// own permissions do not matter. The parent's FILE_DELETE_CHILD carries that
// on Windows, which works only because the entry opens ask for DELETE alone.
func TestRemoveAndRenameIgnoreTheFilesOwnDACL(t *testing.T) {
	dir, path := openTempDir(t)
	writeFile(t, dir, "locked", "x")
	writeFile(t, dir, "moving", "y")
	denyAllAccess(t, filepath.Join(path, "locked"))
	denyAllAccess(t, filepath.Join(path, "moving"))
	withoutBackupPrivileges(t, func() {
		if _, err := os.ReadFile(filepath.Join(path, "locked")); !errors.Is(err, fs.ErrPermission) {
			t.Fatalf("precondition: the file itself must deny the caller, ReadFile = %v", err)
		}
		// The old open (DELETE plus SYNCHRONIZE and synchronous I/O) is refused:
		// FILE_DELETE_CHILD on the parent grants DELETE only.
		if handle, err := ntOpen(mustHandle(t, dir), "locked", windows.DELETE, windows.FILE_OPEN, 0, nil); err == nil {
			closeHandle(handle)
			t.Fatal("precondition: an open with SYNCHRONIZE must be denied")
		}
		if err := dir.Rename("moving", dir, "moved"); err != nil {
			t.Fatalf("Rename of a file whose DACL denies the caller: %v", err)
		}
		if err := dir.Remove("locked"); err != nil {
			t.Fatalf("Remove of a file whose DACL denies the caller: %v", err)
		}
		if err := dir.Remove("moved"); err != nil {
			t.Fatalf("Remove of the renamed file: %v", err)
		}
	})
	for _, name := range []string{"locked", "moving", "moved"} {
		if _, err := os.Lstat(filepath.Join(path, name)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%s must be gone: %v", name, err)
		}
	}
}

// withoutBackupPrivileges runs body on a locked OS thread that impersonates a
// copy of this process's token with SeBackupPrivilege and SeRestorePrivilege
// disabled. Opens with backup intent (every winfs NT open, and Go's os.Open)
// let those privileges bypass every DACL, and this machine's elevated token
// has both enabled, so a DACL test needs them off.
func withoutBackupPrivileges(t *testing.T, body func()) {
	t.Helper()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.ImpersonateSelf(windows.SecurityImpersonation); err != nil {
		t.Fatalf("ImpersonateSelf: %v", err)
	}
	defer func() { _ = windows.RevertToSelf() }()
	var token windows.Token
	err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, true, &token)
	if err != nil {
		t.Fatalf("OpenThreadToken: %v", err)
	}
	defer token.Close()
	for _, name := range []string{"SeBackupPrivilege", "SeRestorePrivilege"} {
		privilegeName, err := windows.UTF16PtrFromString(name)
		if err != nil {
			t.Fatal(err)
		}
		privileges := windows.Tokenprivileges{PrivilegeCount: 1}
		if err := windows.LookupPrivilegeValue(nil, privilegeName, &privileges.Privileges[0].Luid); err != nil {
			t.Fatalf("LookupPrivilegeValue(%s): %v", name, err)
		}
		if err := windows.AdjustTokenPrivileges(token, false, &privileges, 0, nil, nil); err != nil {
			t.Fatalf("disable %s: %v", name, err)
		}
	}
	body()
}

// denyAllAccess gives path a protected DACL that denies the current user
// everything. The owner keeps WRITE_DAC, so cleanup can restore access.
func denyAllAccess(t *testing.T, path string) {
	t.Helper()
	user, err := currentUser()
	if err != nil {
		t.Fatal(err)
	}
	setDACL(t, path, "D:P(D;;FA;;;"+user.String()+")")
	t.Cleanup(func() {
		if _, err := os.Lstat(path); err == nil {
			setDACL(t, path, "D:(A;;FA;;;"+user.String()+")")
		}
	})
}

func setDACL(t *testing.T, path string, sddl string) {
	t.Helper()
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
	if err != nil {
		t.Fatalf("set DACL %s on %s: %v", sddl, path, err)
	}
}

// Inside a held directory that was deleted, Windows answers
// STATUS_DELETE_PENDING; Unix answers ENOENT, and so does winfs.
func TestOperationsInDeletedHeldDirectory(t *testing.T) {
	parent, _ := openTempDir(t)
	if err := parent.Mkdir("gone", Inherited); err != nil {
		t.Fatal(err)
	}
	held, err := parent.OpenDir("gone")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := parent.Remove("gone"); err != nil {
		t.Fatalf("remove the held directory: %v", err)
	}
	if _, err := held.OpenFile("child", os.O_WRONLY|os.O_CREATE|os.O_EXCL, Inherited); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("create in a deleted directory = %v, want fs.ErrNotExist", err)
	}
	if _, err := held.Lstat("child"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("lstat in a deleted directory = %v, want fs.ErrNotExist", err)
	}
	if err := held.Mkdir("sub", Inherited); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("mkdir in a deleted directory = %v, want fs.ErrNotExist", err)
	}
	if errors.Is(ntError(windows.STATUS_DELETE_PENDING), fs.ErrPermission) {
		t.Fatal("STATUS_DELETE_PENDING must not read as a permission error")
	}
}

// The statuses that select each weaker fallback, and those that must not.
func TestFallbackSelectors(t *testing.T) {
	classUnsupported := []error{
		windows.STATUS_INVALID_INFO_CLASS,
		windows.STATUS_INVALID_PARAMETER,
		windows.STATUS_NOT_SUPPORTED,
		windows.STATUS_INVALID_DEVICE_REQUEST,
	}
	realResults := []error{
		nil,
		windows.STATUS_OBJECT_NAME_COLLISION,
		windows.STATUS_ACCESS_DENIED,
		windows.STATUS_SHARING_VIOLATION,
		windows.STATUS_CANNOT_DELETE,
		windows.STATUS_DIRECTORY_NOT_EMPTY,
		windows.STATUS_NOT_SAME_DEVICE,
		windows.ERROR_INVALID_PARAMETER,
	}
	for _, status := range classUnsupported {
		if !unsupportedInformationClass(status) {
			t.Errorf("unsupportedInformationClass(%v) = false, want the rename/delete fallback", status)
		}
	}
	for _, status := range realResults {
		if unsupportedInformationClass(status) {
			t.Errorf("unsupportedInformationClass(%v) = true, must be returned as a result", status)
		}
	}
	for _, status := range []error{windows.STATUS_NOT_SUPPORTED, windows.STATUS_INVALID_DEVICE_REQUEST} {
		if !hardLinksUnsupported(status) {
			t.Errorf("hardLinksUnsupported(%v) = false, want the move fallback", status)
		}
	}
	for _, status := range []error{
		nil,
		windows.STATUS_OBJECT_NAME_COLLISION,
		windows.STATUS_ACCESS_DENIED,
		windows.STATUS_INVALID_PARAMETER,
		windows.STATUS_INVALID_INFO_CLASS,
		windows.STATUS_NOT_SAME_DEVICE,
	} {
		if hardLinksUnsupported(status) {
			t.Errorf("hardLinksUnsupported(%v) = true, must not move instead of linking", status)
		}
	}
	if !retryDeleteOnly(ntError(windows.STATUS_ACCESS_DENIED)) {
		t.Error("retryDeleteOnly(access denied) = false, want the DELETE-only retry")
	}
	for _, err := range []error{
		nil,
		ntError(windows.STATUS_OBJECT_NAME_NOT_FOUND),
		ntError(windows.STATUS_SHARING_VIOLATION),
		ntError(windows.STATUS_DELETE_PENDING),
	} {
		if retryDeleteOnly(err) {
			t.Errorf("retryDeleteOnly(%v) = true, want no retry", err)
		}
	}
}

func TestRenameReplacesOpenTarget(t *testing.T) {
	dir, path := openTempDir(t)
	writeFile(t, dir, "source", "new")
	writeFile(t, dir, "target", "old")
	target, err := dir.OpenFile("target", os.O_RDONLY, Inherited)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	source, err := dir.OpenFile("source", os.O_RDONLY, Inherited)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if err := dir.Rename("source", dir, "target"); err != nil {
		t.Fatalf("Rename over an open target: %v", err)
	}
	if got := readFile(t, filepath.Join(path, "target")); got != "new" {
		t.Fatalf("target content = %q", got)
	}
	if _, err := dir.Lstat("source"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("source name must be gone: %v", err)
	}
	moved, err := Identity(source)
	if err != nil {
		t.Fatal(err)
	}
	attached, err := dir.Lstat("target")
	if err != nil || attached.ID != moved {
		t.Fatalf("target must now be the source file: %v vs %v (%v)", attached.ID, moved, err)
	}
}

func TestRenameAcrossHeldDirectories(t *testing.T) {
	dir, path := openTempDir(t)
	if err := dir.Mkdir("other", Inherited); err != nil {
		t.Fatal(err)
	}
	other, err := dir.OpenDir("other")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	writeFile(t, dir, "file", "moved")
	if err := dir.RenameNoReplace("file", other, "file"); err != nil {
		t.Fatalf("RenameNoReplace across directories: %v", err)
	}
	if got := readFile(t, filepath.Join(path, "other", "file")); got != "moved" {
		t.Fatalf("content = %q", got)
	}
}

func TestRenameNoReplaceKeepsExistingTarget(t *testing.T) {
	dir, path := openTempDir(t)
	writeFile(t, dir, "source", "new")
	writeFile(t, dir, "target", "old")
	err := dir.RenameNoReplace("source", dir, "target")
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("RenameNoReplace over an existing target = %v, want fs.ErrExist", err)
	}
	var linkErr *os.LinkError
	if !errors.As(err, &linkErr) || linkErr.Old != filepath.Join(path, "source") || linkErr.New != filepath.Join(path, "target") {
		t.Fatalf("error %v must be an *os.LinkError naming both entries", err)
	}
	if got := readFile(t, filepath.Join(path, "target")); got != "old" {
		t.Fatalf("target content = %q", got)
	}
	if got := readFile(t, filepath.Join(path, "source")); got != "new" {
		t.Fatalf("source content = %q", got)
	}
}

func TestLinkIfAbsent(t *testing.T) {
	dir, path := openTempDir(t)
	writeFile(t, dir, "stage", "payload")
	outcome, err := dir.LinkIfAbsent("stage", dir, "final")
	if err != nil || outcome != Linked {
		t.Fatalf("LinkIfAbsent = %d, %v; want Linked", outcome, err)
	}
	stage, err := dir.Lstat("stage")
	if err != nil {
		t.Fatal(err)
	}
	final, err := dir.Lstat("final")
	if err != nil {
		t.Fatal(err)
	}
	if stage.ID != final.ID || final.NumberOfLinks != 2 {
		t.Fatalf("final must be a second link: stage %+v final %+v", stage, final)
	}
	writeFile(t, dir, "other", "different")
	_, err = dir.LinkIfAbsent("other", dir, "final")
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("LinkIfAbsent over an existing name = %v, want fs.ErrExist", err)
	}
	if got := readFile(t, filepath.Join(path, "final")); got != "payload" {
		t.Fatalf("existing name was replaced: %q", got)
	}
	if _, err := dir.LinkIfAbsent("missing", dir, "x"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("LinkIfAbsent(missing) = %v, want fs.ErrNotExist", err)
	}
}

// The no-hard-link fallback cannot be triggered on NTFS, so its body runs
// directly here.
func TestMoveInsteadOfLink(t *testing.T) {
	dir, path := openTempDir(t)
	writeFile(t, dir, "stage", "payload")
	writeFile(t, dir, "taken", "keep")
	root := mustHandle(t, dir)
	source, err := ntOpen(root, "stage", windows.FILE_WRITE_ATTRIBUTES, windows.FILE_OPEN, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeHandle(source)
	if err := moveInsteadOfLink(source, root, "taken"); !errors.Is(ntError(err), fs.ErrExist) {
		t.Fatalf("fallback over an existing name = %v, want fs.ErrExist", err)
	}
	if err := moveInsteadOfLink(source, root, "final"); err != nil {
		t.Fatalf("fallback move: %v", err)
	}
	if got := readFile(t, filepath.Join(path, "final")); got != "payload" {
		t.Fatalf("final content = %q", got)
	}
	if _, err := dir.Lstat("stage"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the fallback moves the old name away: %v", err)
	}
}

func TestPathRenameNoReplace(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	if err := os.WriteFile(source, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RenameNoReplace(source, target); err != nil {
		t.Fatalf("RenameNoReplace: %v", err)
	}
	if err := os.WriteFile(source, []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := RenameNoReplace(source, target)
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("RenameNoReplace over an existing target = %v, want fs.ErrExist", err)
	}
	var linkErr *os.LinkError
	if !errors.As(err, &linkErr) {
		t.Fatalf("error %T must be *os.LinkError", err)
	}
	if got := readFile(t, target); got != "new" {
		t.Fatalf("target content = %q", got)
	}
	if err := RenameNoReplace(filepath.Join(root, "missing"), filepath.Join(root, "x")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("RenameNoReplace(missing) = %v, want fs.ErrNotExist", err)
	}
}
