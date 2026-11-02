package winfs

// Probes P-a to P-f of ADR-092. Each logs its measured outcome with a "P-x:"
// prefix (run with -v to read them). A probe fails only when the outcome
// breaks a mapping that ADR-092 has already decided.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// describe renders an outcome with its Win32 or NTSTATUS code.
func describe(err error) string {
	if err == nil {
		return "success"
	}
	var errno windows.Errno
	if errors.As(err, &errno) {
		return fmt.Sprintf("error %q (Win32 %d)", errno.Error(), uint32(errno))
	}
	var status windows.NTStatus
	if errors.As(err, &status) {
		return fmt.Sprintf("NTSTATUS 0x%08x %q", uint32(status), status.Error())
	}
	return "error " + err.Error()
}

func openDirectoryHandle(t *testing.T, path string, access uint32, share uint32) windows.Handle {
	t.Helper()
	handle, err := createFile(path, access, share, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, nil)
	if err != nil {
		t.Fatalf("open directory %s: %v", path, err)
	}
	t.Cleanup(func() { closeHandle(handle) })
	return handle
}

func TestProbePaLockFileExOnDirectoryHandle(t *testing.T) {
	path := t.TempDir()
	t.Logf("P-a: filesystem %s", volumeFilesystem(path))
	for _, access := range []struct {
		name string
		mask uint32
	}{
		{"read access (FILE_GENERIC_READ)", windows.FILE_GENERIC_READ},
		{"write access (FILE_GENERIC_READ|FILE_WRITE_DATA)", windows.FILE_GENERIC_READ | windows.FILE_WRITE_DATA},
	} {
		first := openDirectoryHandle(t, path, access.mask, shareAll)
		second := openDirectoryHandle(t, path, access.mask, shareAll)
		flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK | windows.LOCKFILE_FAIL_IMMEDIATELY)
		err := windows.LockFileEx(first, flags, 0, allBytes, allBytes, new(windows.Overlapped))
		t.Logf("P-a: LockFileEx(exclusive) on a directory handle with %s: %s", access.name, describe(err))
		if err != nil {
			continue
		}
		err = windows.LockFileEx(second, flags, 0, allBytes, allBytes, new(windows.Overlapped))
		t.Logf("P-a: the same lock through a second handle: %s", describe(err))
		_ = windows.UnlockFileEx(first, 0, allBytes, allBytes, new(windows.Overlapped))
		if err == nil {
			_ = windows.UnlockFileEx(second, 0, allBytes, allBytes, new(windows.Overlapped))
		}
	}
	t.Logf("P-a: decision input: LockFileEx on directories would win over 4a only if the first call succeeds " +
		"and the second fails with ERROR_LOCK_VIOLATION")
}

func TestProbePbShareModeExclusionOnDirectory(t *testing.T) {
	path := t.TempDir()
	t.Logf("P-b: filesystem %s", volumeFilesystem(path))
	lock, err := TryLockDir(path)
	if err != nil {
		t.Fatalf("P-b: first lock open (FILE_WRITE_DATA, share READ|DELETE) failed: %s", describe(err))
	}
	t.Logf("P-b: first lock open: success")
	second, err := createFile(path, dirLockAccess, windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, nil)
	if err == nil {
		closeHandle(second)
	}
	t.Logf("P-b: second identical open while held: %s", describe(err))
	if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("P-b: decided mapping 4a needs ERROR_SHARING_VIOLATION for the second open, got %s", describe(err))
	}
	plain, err := os.Open(path)
	t.Logf("P-b: os.Open of the locked directory (Go share READ|WRITE): %s", describe(err))
	if err != nil {
		t.Fatalf("P-b: 4a must not block ordinary directory opens: %v", err)
	}
	_ = plain.Close()
	held, err := OpenDir(path)
	t.Logf("P-b: winfs.OpenDir of the locked directory: %s", describe(err))
	if err != nil {
		t.Fatalf("P-b: 4a must not block held directory handles: %v", err)
	}
	_ = held.Close()
	err = os.WriteFile(filepath.Join(path, "entry"), []byte("x"), 0o644)
	t.Logf("P-b: creating a file inside the locked directory: %s", describe(err))
	if err != nil {
		t.Fatalf("P-b: 4a must not block writes inside the directory: %v", err)
	}
	writer, err := createFile(path, windows.FILE_WRITE_DATA, shareAll, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS, nil)
	if err == nil {
		closeHandle(writer)
	}
	t.Logf("P-b: another write-access open (as a directory flush needs) while held: %s", describe(err))
	renamed := path + "-renamed"
	err = os.Rename(path, renamed)
	t.Logf("P-b: renaming the locked directory through its path: %s", describe(err))
	if err == nil {
		_ = os.Rename(renamed, path)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatalf("P-b: unlock: %v", err)
	}
	elapsed, err := eventually(5*time.Second, func() error {
		again, err := TryLockDir(path)
		if err == nil {
			return again.Unlock()
		}
		return err
	})
	t.Logf("P-b: lock again after close: %s after %v (cross-process release: TestDirLockTwoProcesses)", describe(err), elapsed)
	if err != nil {
		t.Fatalf("P-b: the directory lock was not released on close: %v", err)
	}
}

func TestProbePcFlushFileBuffersOnDirectory(t *testing.T) {
	path := t.TempDir()
	t.Logf("P-c: filesystem %s", volumeFilesystem(path))
	readOnly := openDirectoryHandle(t, path, windows.FILE_GENERIC_READ, shareAll)
	t.Logf("P-c: FlushFileBuffers on a read-only directory handle: %s", describe(windows.FlushFileBuffers(readOnly)))
	plain, err := os.Open(path)
	if err != nil {
		t.Fatalf("P-c: os.Open: %v", err)
	}
	t.Logf("P-c: os.File.Sync on os.Open(directory) (the R2 failure): %s", describe(plain.Sync()))
	_ = plain.Close()
	for _, access := range []struct {
		name string
		mask uint32
	}{
		{"FILE_WRITE_DATA", windows.FILE_WRITE_DATA},
		{"FILE_APPEND_DATA", windows.FILE_APPEND_DATA},
		{"GENERIC_WRITE", windows.GENERIC_WRITE},
	} {
		handle, err := createFile(path, access.mask, shareAll, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, nil)
		if err != nil {
			t.Logf("P-c: open directory with %s: %s", access.name, describe(err))
			continue
		}
		flushErr := windows.FlushFileBuffers(handle)
		closeHandle(handle)
		t.Logf("P-c: FlushFileBuffers on a directory handle opened with %s: %s", access.name, describe(flushErr))
	}
	outcome, err := SyncDirectory(path)
	t.Logf("P-c: SyncDirectory: outcome %d (1 flushed, 2 skipped), %s", outcome, describe(err))
	if err != nil {
		t.Fatalf("P-c: an error outside the documented fallback (ACCESS_DENIED, INVALID_FUNCTION) breaks O3: %v", err)
	}
	if outcome == SyncFlushed {
		t.Logf("P-c: G7 directory durability is Equal on this volume")
	} else {
		t.Logf("P-c: G7 directory durability is Weaker-documented on this volume")
	}
}

func TestProbePdHardLinks(t *testing.T) {
	path := t.TempDir()
	probeHardLink(t, path)
	if refs := os.Getenv("WINFS_PROBE_REFS_DIR"); refs != "" {
		probeHardLink(t, refs)
	} else {
		t.Logf("P-d: ReFS not written: set WINFS_PROBE_REFS_DIR to a directory on a ReFS (Dev Drive) volume; "+
			"fixed volumes found: %s", fixedVolumes())
	}
}

func probeHardLink(t *testing.T, path string) {
	t.Helper()
	dir, err := OpenDir(path)
	if err != nil {
		t.Fatalf("P-d: OpenDir(%s): %v", path, err)
	}
	defer dir.Close()
	stage := fmt.Sprintf("winfs-probe-stage-%d", time.Now().UnixNano())
	writeFile(t, dir, stage, "content")
	defer dir.Remove(stage)
	final := stage + "-final"
	outcome, err := dir.LinkIfAbsent(stage, dir, final)
	t.Logf("P-d: FileLinkInformation (class 11) hard link on %s: outcome %d (1 linked, 2 moved), %s",
		volumeFilesystem(path), outcome, describe(err))
	if err != nil {
		t.Fatalf("P-d: LinkIfAbsent: %v", err)
	}
	defer dir.Remove(final)
	if outcome == Linked {
		info, err := dir.Lstat(final)
		if err != nil {
			t.Fatalf("P-d: lstat: %v", err)
		}
		t.Logf("P-d: link count after linking: %d", info.NumberOfLinks)
	}
}

func fixedVolumes() string {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return "unknown (" + err.Error() + ")"
	}
	var volumes []string
	for index := 0; index < 26; index++ {
		if mask&(1<<index) == 0 {
			continue
		}
		root := string(rune('A'+index)) + `:\`
		root16, _ := windows.UTF16PtrFromString(root)
		if windows.GetDriveType(root16) != windows.DRIVE_FIXED {
			continue
		}
		name := make([]uint16, windows.MAX_PATH+1)
		filesystem := "unknown"
		if windows.GetVolumeInformation(root16, nil, 0, nil, nil, nil, &name[0], uint32(len(name))) == nil {
			filesystem = windows.UTF16ToString(name)
		}
		volumes = append(volumes, root+" "+filesystem)
	}
	return strings.Join(volumes, ", ")
}

func TestProbePePOSIXDeleteAndRename(t *testing.T) {
	dir, path := openTempDir(t)
	filesystem := volumeFilesystem(path)
	t.Logf("P-e: filesystem %s", filesystem)
	ntfs := strings.HasPrefix(filesystem, "NTFS")

	writeFile(t, dir, "victim", "old")
	open, err := dir.OpenFile("victim", os.O_RDONLY, Inherited)
	if err != nil {
		t.Fatalf("P-e: open victim: %v", err)
	}
	defer open.Close()
	handle, err := ntOpen(mustHandle(t, dir), "victim", windows.DELETE, windows.FILE_OPEN, 0, nil)
	if err != nil {
		t.Fatalf("P-e: open victim for delete: %v", err)
	}
	info := fileDispositionInformationEx{
		Flags: windows.FILE_DISPOSITION_DELETE | windows.FILE_DISPOSITION_POSIX_SEMANTICS,
	}
	status := setInformation(handle, &info, classFileDispositionInformationEx)
	closeHandle(handle)
	t.Logf("P-e: FileDispositionInformationEx with POSIX_SEMANTICS while another handle is open: %s", describe(status))
	if status != nil && ntfs {
		t.Fatalf("P-e: ADR-092 rates unlinkat Equal on NTFS through POSIX delete; got %s", describe(status))
	}
	_, err = dir.Lstat("victim")
	t.Logf("P-e: lookup of the deleted name while the other handle is still open: %s", describe(err))
	recreated, err := dir.OpenFile("victim", os.O_WRONLY|os.O_CREATE|os.O_EXCL, Inherited)
	t.Logf("P-e: exclusive create of the same name while the old file is still open: %s", describe(err))
	if err == nil {
		_ = recreated.Close()
	}

	writeFile(t, dir, "source", "new")
	writeFile(t, dir, "target", "old")
	target, err := dir.OpenFile("target", os.O_RDONLY, Inherited)
	if err != nil {
		t.Fatalf("P-e: open target: %v", err)
	}
	defer target.Close()
	source, err := ntOpen(mustHandle(t, dir), "source", windows.DELETE, windows.FILE_OPEN, 0, nil)
	if err != nil {
		t.Fatalf("P-e: open source: %v", err)
	}
	encoded, err := encodeFileName("target")
	if err != nil {
		t.Fatal(err)
	}
	rename := fileRenameInformationEx{
		Flags:          windows.FILE_RENAME_REPLACE_IF_EXISTS | windows.FILE_RENAME_POSIX_SEMANTICS,
		RootDirectory:  mustHandle(t, dir),
		FileNameLength: encoded.length,
		FileName:       encoded.units,
	}
	status = setInformation(source, &rename, classFileRenameInformationEx)
	closeHandle(source)
	t.Logf("P-e: FileRenameInformationEx REPLACE_IF_EXISTS|POSIX_SEMANTICS over an open target: %s", describe(status))
	if status != nil && ntfs {
		t.Fatalf("P-e: ADR-092 rates renameat Equal on NTFS through POSIX rename; got %s", describe(status))
	}
	t.Logf("P-e: content under the target name after the rename: %q", readFile(t, filepath.Join(path, "target")))
}

// mustHandle returns the raw handle of an open Dir for tests that call the
// NT helpers directly. The tests keep dir open (t.Cleanup) while they use it.
func mustHandle(t *testing.T, dir *Dir) windows.Handle {
	t.Helper()
	handle := windows.Handle(dir.File().Fd())
	if handle == windows.InvalidHandle {
		t.Fatal("directory handle is closed")
	}
	return handle
}

func TestProbePfOwnershipAcrossTokens(t *testing.T) {
	token := windows.GetCurrentProcessToken()
	user, err := currentUser()
	if err != nil {
		t.Fatalf("P-f: token user: %v", err)
	}
	t.Logf("P-f: token user %s (%s), elevated %v, elevation type %s",
		user.String(), accountName(user), token.IsElevated(), elevationType(token))
	if owner, err := tokenDefaultOwner(token); err == nil {
		t.Logf("P-f: token default owner %s (%s)", owner.String(), accountName(owner))
	} else {
		t.Logf("P-f: token default owner: %s", describe(err))
	}
	t.Logf("P-f: SeBackupPrivilege %s, SeRestorePrivilege %s (enabled, they let every open with backup intent, "+
		"winfs and Go's os.Open alike, bypass DACLs)",
		privilegeState(token, "SeBackupPrivilege"), privilegeState(token, "SeRestorePrivilege"))

	path := t.TempDir()
	plain := filepath.Join(path, "default-owner")
	if err := os.WriteFile(plain, []byte("x"), 0o600); err != nil {
		t.Fatalf("P-f: create default-owner file: %v", err)
	}
	plainOwner := fileOwner(t, plain)
	t.Logf("P-f: owner of a new file created with the default security: %s (%s)", plainOwner.String(), accountName(plainOwner))
	plainFile, err := OpenFile(plain, os.O_RDONLY, Inherited)
	if err != nil {
		t.Fatalf("P-f: open default-owner file: %v", err)
	}
	t.Logf("P-f: CheckPrivate on the default-security file: %v", CheckPrivate(plainFile))
	_ = plainFile.Close()

	private := filepath.Join(path, "private")
	file, err := OpenFile(private, os.O_RDWR|os.O_CREATE|os.O_EXCL, Private)
	if err != nil {
		t.Fatalf("P-f: create private file: %v", err)
	}
	defer file.Close()
	privateOwner := fileOwner(t, private)
	t.Logf("P-f: owner of a file created with an explicit owner (Private): %s", privateOwner.String())
	if err := CheckPrivate(file); err != nil {
		t.Fatalf("P-f: a file created with the explicit owner must pass the 2a check under this token: %v", err)
	}
	t.Logf("P-f: CheckPrivate under this token: accepted")

	linked, err := token.GetLinkedToken()
	if err != nil {
		t.Logf("P-f: no linked token (%s): the other half of a split token is not measurable here", describe(err))
		return
	}
	defer linked.Close()
	linkedUser, err := linked.GetTokenUser()
	if err != nil {
		t.Logf("P-f: linked token user: %s", describe(err))
		return
	}
	t.Logf("P-f: linked token: elevated %v, elevation type %s, user %s (same user SID: %v)",
		linked.IsElevated(), elevationType(linked), linkedUser.User.Sid.String(), linkedUser.User.Sid.Equals(user))
	for _, carrier := range []string{private, plain} {
		granted, accessErr := accessCheck(carrier, linked, windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE)
		t.Logf("P-f: AccessCheck(read|write) of %s for the linked token: granted %v, %s",
			filepath.Base(carrier), granted, describe(accessErr))
	}
	t.Logf("P-f: a process cannot run under the linked token without SeTcbPrivilege, so CheckPrivate itself was " +
		"not executed there; it compares the owner with the token user SID, which both halves share")
}

// privilegeState reports whether token has the named privilege, and whether
// it is enabled.
func privilegeState(token windows.Token, name string) string {
	privilegeName, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return "unknown"
	}
	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, privilegeName, &luid); err != nil {
		return "unknown (" + err.Error() + ")"
	}
	var length uint32
	_ = windows.GetTokenInformation(token, windows.TokenPrivileges, nil, 0, &length)
	if length == 0 {
		return "unknown"
	}
	buffer := make([]byte, length)
	if err := windows.GetTokenInformation(token, windows.TokenPrivileges, &buffer[0], length, &length); err != nil {
		return "unknown (" + err.Error() + ")"
	}
	privileges := (*windows.Tokenprivileges)(unsafe.Pointer(&buffer[0]))
	for _, privilege := range privileges.AllPrivileges() {
		if privilege.Luid != luid {
			continue
		}
		if privilege.Attributes&windows.SE_PRIVILEGE_ENABLED != 0 {
			return "enabled"
		}
		return "present but disabled"
	}
	return "absent"
}

func elevationType(token windows.Token) string {
	var value uint32
	var length uint32
	err := windows.GetTokenInformation(token, windows.TokenElevationType,
		(*byte)(unsafe.Pointer(&value)), uint32(unsafe.Sizeof(value)), &length)
	if err != nil {
		return "unknown (" + err.Error() + ")"
	}
	switch value {
	case 1:
		return "default (no split token)"
	case 2:
		return "full (elevated half of a split token)"
	case 3:
		return "limited (filtered half of a split token)"
	}
	return "unknown"
}

func tokenDefaultOwner(token windows.Token) (*windows.SID, error) {
	var length uint32
	_ = windows.GetTokenInformation(token, windows.TokenOwner, nil, 0, &length)
	if length == 0 {
		return nil, errors.New("TokenOwner returned no size")
	}
	buffer := make([]byte, length)
	if err := windows.GetTokenInformation(token, windows.TokenOwner, &buffer[0], length, &length); err != nil {
		return nil, err
	}
	owner := *(**windows.SID)(unsafe.Pointer(&buffer[0]))
	return owner.Copy()
}

func fileOwner(t *testing.T, path string) *windows.SID {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("read owner of %s: %v", path, err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		t.Fatalf("owner of %s: %v", path, err)
	}
	return owner
}

// fileAllAccess is FILE_ALL_ACCESS, which x/sys/windows does not define.
const fileAllAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff

type genericMapping struct {
	GenericRead    uint32
	GenericWrite   uint32
	GenericExecute uint32
	GenericAll     uint32
}

var procAccessCheck = windows.NewLazySystemDLL("advapi32.dll").NewProc("AccessCheck")

// accessCheck asks whether token may open path with access. The linked token
// that GetTokenInformation returns without SeTcbPrivilege is an
// identification-level token, which AccessCheck accepts.
func accessCheck(path string, token windows.Token, access uint32) (bool, error) {
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.GROUP_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false, err
	}
	mapping := genericMapping{
		GenericRead:    windows.FILE_GENERIC_READ,
		GenericWrite:   windows.FILE_GENERIC_WRITE,
		GenericExecute: windows.FILE_GENERIC_EXECUTE,
		GenericAll:     fileAllAccess,
	}
	privileges := make([]byte, 256)
	privilegesLength := uint32(len(privileges))
	var granted uint32
	var status int32
	result, _, callErr := procAccessCheck.Call(
		uintptr(unsafe.Pointer(descriptor)),
		uintptr(token),
		uintptr(access),
		uintptr(unsafe.Pointer(&mapping)),
		uintptr(unsafe.Pointer(&privileges[0])),
		uintptr(unsafe.Pointer(&privilegesLength)),
		uintptr(unsafe.Pointer(&granted)),
		uintptr(unsafe.Pointer(&status)),
	)
	if result == 0 {
		return false, callErr
	}
	return status != 0, nil
}
