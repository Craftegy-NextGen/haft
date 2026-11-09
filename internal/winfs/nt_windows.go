package winfs

import (
	"errors"
	"fmt"
	"io/fs"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// NtSetInformationFile classes. x/sys/windows v0.46.0 defines FileLinkInformation
// as 72, which is the number of FileLinkInformationEx; the classic class used here
// is 11. FileRenameInformationEx is not defined there at all.
const (
	classFileBasicInformation         = 4
	classFileRenameInformation        = 10
	classFileLinkInformation          = 11
	classFileDispositionInformation   = 13
	classFileDispositionInformationEx = 64
	classFileRenameInformationEx      = 65
)

// reparseTagNameSurrogate marks reparse tags that name another file (symbolic
// links, junctions); see IsReparseTagNameSurrogate in winnt.h.
const reparseTagNameSurrogate = 0x20000000

// shareAll is the share mode of every handle winfs holds, so that other
// processes may read, write, rename and delete entries as on Unix.
const shareAll = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE

// errDeletePending reports STATUS_DELETE_PENDING: the name, or the directory
// it is looked up in, is being deleted. On Unix the name would already be
// gone (ENOENT), so it satisfies errors.Is(err, fs.ErrNotExist).
var errDeletePending = fmt.Errorf("delete pending: %w", windows.ERROR_FILE_NOT_FOUND)

// fileIDInfo is FILE_ID_INFO.
type fileIDInfo struct {
	VolumeSerialNumber uint64
	FileID             [16]byte
}

// fileAttributeTagInfo is FILE_ATTRIBUTE_TAG_INFO.
type fileAttributeTagInfo struct {
	FileAttributes uint32
	ReparseTag     uint32
}

// fileBasicInformation is FILE_BASIC_INFORMATION. Zero times and zero
// attributes mean "leave unchanged".
type fileBasicInformation struct {
	CreationTime   int64
	LastAccessTime int64
	LastWriteTime  int64
	ChangeTime     int64
	FileAttributes uint32
	_              uint32
}

// fileRenameInformation is FILE_RENAME_INFORMATION for FileRenameInformation.
// A component name is at most 255 UTF-16 units, so MAX_PATH is enough.
type fileRenameInformation struct {
	ReplaceIfExists bool
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [windows.MAX_PATH]uint16
}

// fileRenameInformationEx is FILE_RENAME_INFORMATION for FileRenameInformationEx
// (the first member is a ULONG Flags instead of a BOOLEAN).
type fileRenameInformationEx struct {
	Flags          uint32
	RootDirectory  windows.Handle
	FileNameLength uint32
	FileName       [windows.MAX_PATH]uint16
}

// fileLinkInformation is FILE_LINK_INFORMATION for FileLinkInformation.
type fileLinkInformation struct {
	ReplaceIfExists bool
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [windows.MAX_PATH]uint16
}

// fileDispositionInformationEx is FILE_DISPOSITION_INFORMATION_EX.
type fileDispositionInformationEx struct {
	Flags uint32
}

// fileDispositionInformation is FILE_DISPOSITION_INFORMATION.
type fileDispositionInformation struct {
	DeleteFile bool
}

// ntCreate is NtCreateFile relative to root with the share mode of every
// winfs handle. It adds no access or option bits; the callers below choose
// them. The handle is never inheritable. An empty name reopens root itself
// without a name lookup.
func ntCreate(
	root windows.Handle,
	name string,
	access uint32,
	disposition uint32,
	options uint32,
	descriptor *windows.SECURITY_DESCRIPTOR,
) (windows.Handle, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return windows.InvalidHandle, err
	}
	attributes := windows.OBJECT_ATTRIBUTES{
		RootDirectory:      root,
		ObjectName:         objectName,
		SecurityDescriptor: descriptor,
	}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	var handle windows.Handle
	err = windows.NtCreateFile(
		&handle,
		access,
		&attributes,
		&windows.IO_STATUS_BLOCK{},
		nil,
		windows.FILE_ATTRIBUTE_NORMAL,
		shareAll,
		disposition,
		options,
		0,
		0,
	)
	runtime.KeepAlive(objectName)
	runtime.KeepAlive(descriptor)
	if err != nil {
		return windows.InvalidHandle, ntError(err)
	}
	return handle, nil
}

// ntOpen opens name relative to root without following a reparse point at
// name (FILE_OPEN_REPARSE_POINT) and for synchronous I/O, so the handle can
// back an *os.File.
func ntOpen(
	root windows.Handle,
	name string,
	access uint32,
	disposition uint32,
	options uint32,
	descriptor *windows.SECURITY_DESCRIPTOR,
) (windows.Handle, error) {
	return ntCreate(
		root,
		name,
		access|windows.SYNCHRONIZE,
		disposition,
		options|
			windows.FILE_SYNCHRONOUS_IO_NONALERT|
			windows.FILE_OPEN_FOR_BACKUP_INTENT|
			windows.FILE_OPEN_REPARSE_POINT,
		descriptor,
	)
}

// ntOpenEntry opens an existing name relative to root, without following a
// reparse point, for a handle that only feeds NtSetInformationFile (delete,
// rename, link). It requests neither SYNCHRONIZE nor synchronous I/O, as Go's
// Deleteat does: the parent's FILE_DELETE_CHILD grants DELETE alone, so an
// entry whose own DACL denies the caller can still be removed or renamed, as
// with unlink(2) and rename(2).
func ntOpenEntry(root windows.Handle, name string, access uint32, options uint32) (windows.Handle, error) {
	return ntCreate(
		root,
		name,
		access,
		windows.FILE_OPEN,
		options|windows.FILE_OPEN_FOR_BACKUP_INTENT|windows.FILE_OPEN_REPARSE_POINT,
		nil,
	)
}

// informationStruct lists the fixed-layout structs passed to the kernel. They
// are the only types the two helpers below reinterpret as bytes.
type informationStruct interface {
	fileIDInfo | fileAttributeTagInfo | fileBasicInformation |
		fileRenameInformation | fileRenameInformationEx | fileLinkInformation |
		fileDispositionInformation | fileDispositionInformationEx
}

// setInformation calls NtSetInformationFile and returns the raw NTSTATUS so
// callers can choose a fallback; convert with ntError before returning it.
func setInformation[T informationStruct](handle windows.Handle, info *T, class uint32) error {
	return windows.NtSetInformationFile(
		handle,
		&windows.IO_STATUS_BLOCK{},
		(*byte)(unsafe.Pointer(info)), // #nosec G103 -- T is a fixed-layout FILE_*_INFORMATION struct.
		uint32(unsafe.Sizeof(*info)),  // #nosec G115 -- every informationStruct is smaller than 1 KiB.
		class,
	)
}

// queryInformation calls GetFileInformationByHandleEx for one fixed-size class.
func queryInformation[T informationStruct](handle windows.Handle, class uint32, info *T) error {
	return windows.GetFileInformationByHandleEx(
		handle,
		class,
		(*byte)(unsafe.Pointer(info)), // #nosec G103 -- T is a fixed-layout FILE_*_INFO struct.
		uint32(unsafe.Sizeof(*info)),  // #nosec G115 -- every informationStruct is smaller than 1 KiB.
	)
}

// ntError converts an NTSTATUS to the errno the stdlib would report, so
// errors.Is(err, fs.ErrExist / fs.ErrNotExist) keeps working for callers.
func ntError(err error) error {
	var status windows.NTStatus
	if !errors.As(err, &status) {
		return err
	}
	switch status {
	case windows.STATUS_REPARSE_POINT_ENCOUNTERED:
		return syscall.ELOOP
	case windows.STATUS_NOT_A_DIRECTORY:
		return syscall.ENOTDIR
	case windows.STATUS_FILE_IS_A_DIRECTORY:
		return syscall.EISDIR
	case windows.STATUS_DELETE_PENDING:
		// NTSTATUS maps it to ERROR_ACCESS_DENIED, which callers would read
		// as a permission problem.
		return errDeletePending
	}
	return status.Errno()
}

// unsupportedInformationClass reports the statuses with which a filesystem or
// an older Windows rejects an information class or one of its flags. Only
// these select a weaker fallback; every other status is a real result.
func unsupportedInformationClass(err error) bool {
	switch err {
	case windows.STATUS_INVALID_INFO_CLASS,
		windows.STATUS_INVALID_PARAMETER,
		windows.STATUS_NOT_SUPPORTED,
		windows.STATUS_INVALID_DEVICE_REQUEST:
		return true
	}
	return false
}

// fileName is a component name encoded for the FileName array of the rename
// and link structs: UTF-16 without the terminating NUL, plus its byte length.
type fileName struct {
	units  [windows.MAX_PATH]uint16
	length uint32
}

func encodeFileName(name string) (fileName, error) {
	encoded, err := windows.UTF16FromString(name)
	if err != nil {
		return fileName{}, err
	}
	if len(encoded) > windows.MAX_PATH {
		return fileName{}, fmt.Errorf("winfs: name longer than %d UTF-16 units: %w", windows.MAX_PATH-1, fs.ErrInvalid)
	}
	var result fileName
	copy(result.units[:], encoded[:len(encoded)-1])
	result.length = uint32(2 * (len(encoded) - 1)) // #nosec G115 -- bounded by MAX_PATH above.
	return result, nil
}

// attributeTag reads FILE_ATTRIBUTE_TAG_INFO for an open handle.
func attributeTag(handle windows.Handle) (fileAttributeTagInfo, error) {
	var info fileAttributeTagInfo
	if err := queryInformation(handle, windows.FileAttributeTagInfo, &info); err != nil {
		return fileAttributeTagInfo{}, fmt.Errorf("query attribute tag: %w", err)
	}
	return info, nil
}

func (info fileAttributeTagInfo) isNameSurrogate() bool {
	return info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 &&
		info.ReparseTag&reparseTagNameSurrogate != 0
}

func (info fileAttributeTagInfo) isReparsePoint() bool {
	return info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

func (info fileAttributeTagInfo) isDirectory() bool {
	return info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
}

// check rejects a symbolic link or junction (G1) and, when asked, anything
// that is not a directory.
func (info fileAttributeTagInfo) check(wantDirectory bool) error {
	if info.isNameSurrogate() {
		return syscall.ELOOP
	}
	if wantDirectory && !info.isDirectory() {
		return syscall.ENOTDIR
	}
	return nil
}

// finishDataOpen completes an open whose handle will carry data or anchor
// relative opens. The handle was opened with FILE_OPEN_REPARSE_POINT, so
// nothing was followed. A symbolic link or junction is now rejected (ELOOP).
// Any other reparse point (deduplicated file, cloud placeholder) is reopened
// through the handle without FILE_OPEN_REPARSE_POINT, so the filter that owns
// the tag serves the data instead of exposing the raw stub; a tag that no
// filter handles fails with ERROR_CANT_ACCESS_FILE. The reopened handle must
// be the same file. The given handle is consumed: returned or closed.
func finishDataOpen(handle windows.Handle, wantDirectory bool, access uint32, options uint32) (windows.Handle, error) {
	info, err := attributeTag(handle)
	if err == nil {
		err = info.check(wantDirectory)
	}
	if err != nil {
		closeHandle(handle)
		return windows.InvalidHandle, err
	}
	if !info.isReparsePoint() {
		return handle, nil
	}
	defer closeHandle(handle)
	return reopenThroughFilter(handle, access, options)
}

// reopenThroughFilter reopens the file behind handle with reparse processing
// enabled and fails unless the result is the same file.
func reopenThroughFilter(handle windows.Handle, access uint32, options uint32) (windows.Handle, error) {
	reopened, err := ntCreate(
		handle,
		"",
		access|windows.SYNCHRONIZE,
		windows.FILE_OPEN,
		options|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_OPEN_FOR_BACKUP_INTENT,
		nil,
	)
	if err != nil {
		return windows.InvalidHandle, err
	}
	if err := requireSameFile(handle, reopened); err != nil {
		closeHandle(reopened)
		return windows.InvalidHandle, err
	}
	return reopened, nil
}

func requireSameFile(first windows.Handle, second windows.Handle) error {
	firstID, err := handleFileID(first)
	if err != nil {
		return err
	}
	secondID, err := handleFileID(second)
	if err != nil {
		return err
	}
	if firstID != secondID {
		return fmt.Errorf("winfs: the reparse point resolved to another file: %w", syscall.ELOOP)
	}
	return nil
}
