package winfs

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// FileID is the identity of an open file from FILE_ID_INFO: the 64-bit volume
// serial number and the 128-bit file ID (ADR-092 O5). It is the Windows
// counterpart of the Unix (st_dev, st_ino) pair.
type FileID struct {
	VolumeSerial uint64
	ID           [16]byte
}

// SameVolume reports whether both files live on the same volume, the Windows
// counterpart of comparing st_dev (sameFilesystem).
func (id FileID) SameVolume(other FileID) bool {
	return id.VolumeSerial == other.VolumeSerial
}

// String formats the identity as volume serial and file ID in hex.
func (id FileID) String() string {
	return fmt.Sprintf("%016x:%x", id.VolumeSerial, id.ID)
}

// Info is what an Lstat-style query reports about one directory entry or open
// handle, without following a final reparse point.
type Info struct {
	ID            FileID
	Attributes    uint32 // FILE_ATTRIBUTE_* flags
	ReparseTag    uint32 // meaningful only with FILE_ATTRIBUTE_REPARSE_POINT
	NumberOfLinks uint32
	Size          int64
}

// IsDir reports a directory that is not a symbolic link or junction.
func (info Info) IsDir() bool {
	return info.Attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 && !info.IsNameSurrogate()
}

// IsRegular reports a plain file: not a directory and not a reparse point.
func (info Info) IsRegular() bool {
	return info.Attributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) == 0
}

// IsNameSurrogate reports a symbolic link or junction.
func (info Info) IsNameSurrogate() bool {
	tag := fileAttributeTagInfo{FileAttributes: info.Attributes, ReparseTag: info.ReparseTag}
	return tag.isNameSurrogate()
}

// Identity returns the FILE_ID_INFO identity of an open file or directory.
func Identity(file *os.File) (FileID, error) {
	var id FileID
	err := withFileHandle(file, func(handle windows.Handle) error {
		var err error
		id, err = handleFileID(handle)
		return err
	})
	if err != nil {
		return FileID{}, &os.PathError{Op: "identity", Path: nameOf(file), Err: err}
	}
	return id, nil
}

// PathIdentity returns the identity of the file at path, following reparse
// points like stat(2). Use Dir.Lstat for a no-follow identity.
func PathIdentity(path string) (FileID, error) {
	handle, err := createFile(
		path,
		windows.FILE_READ_ATTRIBUTES,
		shareAll,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		nil,
	)
	if err != nil {
		return FileID{}, &os.PathError{Op: "identity", Path: path, Err: err}
	}
	defer closeHandle(handle)
	id, err := handleFileID(handle)
	if err != nil {
		return FileID{}, &os.PathError{Op: "identity", Path: path, Err: err}
	}
	return id, nil
}

// Fstat reports the Info of an open file or directory.
func Fstat(file *os.File) (Info, error) {
	var info Info
	err := withFileHandle(file, func(handle windows.Handle) error {
		var err error
		info, err = handleInfo(handle)
		return err
	})
	if err != nil {
		return Info{}, &os.PathError{Op: "fstat", Path: nameOf(file), Err: err}
	}
	return info, nil
}

// handleFileID queries FILE_ID_INFO and fails closed when the volume cannot
// answer it or answers with an all-zero file ID.
func handleFileID(handle windows.Handle) (FileID, error) {
	var info fileIDInfo
	if err := queryInformation(handle, windows.FileIdInfo, &info); err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) ||
			errors.Is(err, windows.ERROR_INVALID_FUNCTION) ||
			errors.Is(err, windows.ERROR_NOT_SUPPORTED) {
			return FileID{}, fmt.Errorf("%w: %w", ErrUnsupportedFilesystem, err)
		}
		return FileID{}, fmt.Errorf("query file ID: %w", err)
	}
	if info.FileID == ([16]byte{}) {
		return FileID{}, fmt.Errorf("%w: the volume reports a zero file ID", ErrUnsupportedFilesystem)
	}
	return FileID{VolumeSerial: info.VolumeSerialNumber, ID: info.FileID}, nil
}

func handleInfo(handle windows.Handle) (Info, error) {
	id, err := handleFileID(handle)
	if err != nil {
		return Info{}, err
	}
	tag, err := attributeTag(handle)
	if err != nil {
		return Info{}, err
	}
	var data windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &data); err != nil {
		return Info{}, fmt.Errorf("query file information: %w", err)
	}
	return Info{
		ID:            id,
		Attributes:    tag.FileAttributes,
		ReparseTag:    tag.ReparseTag,
		NumberOfLinks: data.NumberOfLinks,
		Size:          int64(data.FileSizeHigh)<<32 | int64(data.FileSizeLow),
	}, nil
}

// withFileHandle runs use with the handle behind file while holding a
// reference on the *os.File (SyscallConn().Control, the role of os.Root's
// incref). During the call neither a concurrent Close nor the finalizer can
// close the handle, so its value cannot be reused by another open; Close
// waits until use returns. A file that is already closed yields os.ErrClosed
// and use does not run.
func withFileHandle(file *os.File, use func(windows.Handle) error) error {
	if file == nil {
		return os.ErrInvalid
	}
	conn, err := file.SyscallConn()
	if err != nil {
		return err
	}
	var useErr error
	controlErr := conn.Control(func(fd uintptr) {
		useErr = use(windows.Handle(fd))
	})
	if controlErr != nil {
		// Control fails only for a closed file (poll.ErrFileClosing).
		return os.ErrClosed
	}
	return useErr
}

// nameOf is file.Name() that tolerates a nil file in error messages.
func nameOf(file *os.File) string {
	if file == nil {
		return ""
	}
	return file.Name()
}

// closeHandle closes a handle winfs opened for one operation; a failure there
// cannot change the operation's result.
func closeHandle(handle windows.Handle) {
	_ = windows.CloseHandle(handle)
}
