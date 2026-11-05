package winfs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Dir is a held directory handle. Every operation with a name is relative to
// the directory that was opened, not to its path (G2), and never follows a
// symbolic link or junction at that name (G1). Like an open Unix directory
// descriptor it is shared for read, write and delete, so other processes may
// still rename or delete the directory; compare identities to detect that.
// Each operation holds a reference on the handle while it runs, so Close
// (which waits for running operations) can never let an operation act on a
// reused handle value; operations after Close fail with os.ErrClosed.
//
// A reparse point that is not a link, such as a deduplicated file or a cloud
// placeholder, is opened for data through the filter that owns its tag, not
// as its raw stub. Neither kind could be created on the development machine;
// the tests use a tag that no filter owns. WOF-compressed files hide their tag
// and read correctly either way (measured).
type Dir struct {
	file *os.File
}

// directoryAccess lets a held directory list entries, anchor relative opens
// and report its identity and security.
const directoryAccess = windows.FILE_LIST_DIRECTORY |
	windows.FILE_TRAVERSE |
	windows.FILE_READ_ATTRIBUTES |
	windows.FILE_READ_EA |
	windows.READ_CONTROL

// OpenDir opens an absolute directory path. Intermediate components resolve
// as usual; a symbolic link or junction at the final component is rejected
// with an error wrapping syscall.ELOOP (open(2) with O_NOFOLLOW|O_DIRECTORY).
func OpenDir(path string) (*Dir, error) {
	if err := requireAbsolute(path); err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	handle, err := createFile(
		path,
		directoryAccess|windows.SYNCHRONIZE,
		shareAll,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		nil,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	dir, err := adoptDir(handle, path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return dir, nil
}

// OpenFile opens an absolute file path with os.O_* flags, without following a
// symbolic link or junction at the final component. With os.O_CREATE the
// protection applies when the call creates the file.
func OpenFile(path string, flag int, protection Protection) (*os.File, error) {
	if err := requireAbsolute(path); err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	request, err := openRequestFor(flag)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	attributes, err := securityAttributes(protection, flag)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if request.options&windows.FILE_NON_DIRECTORY_FILE == 0 {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	if request.options&windows.FILE_WRITE_THROUGH != 0 {
		flags |= windows.FILE_FLAG_WRITE_THROUGH
	}
	handle, err := createFile(
		path,
		request.access|windows.SYNCHRONIZE,
		shareAll,
		request.win32Disposition(),
		flags,
		attributes,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file, err := adoptFile(handle, path, flag, request)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return file, nil
}

// Name returns the path the directory was opened with.
func (dir *Dir) Name() string {
	return dir.file.Name()
}

// File returns the *os.File that owns the handle, for Stat, ReadDir and
// identity checks. It stays owned by dir: close dir, not the file.
func (dir *Dir) File() *os.File {
	return dir.file
}

// Close releases the directory handle once running operations finish.
func (dir *Dir) Close() error {
	return dir.file.Close()
}

// Stat reports the held directory itself.
func (dir *Dir) Stat() (Info, error) {
	return Fstat(dir.file)
}

// OpenDir opens the child directory name without following a symbolic link or
// junction there (openat with O_DIRECTORY|O_NOFOLLOW).
func (dir *Dir) OpenDir(name string) (*Dir, error) {
	path := dir.join(name)
	var child *Dir
	err := dir.withRoot(name, func(root windows.Handle) error {
		handle, err := ntOpen(root, name, directoryAccess, windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE, nil)
		if err != nil {
			return err
		}
		child, err = adoptDir(handle, path)
		return err
	})
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return child, nil
}

// OpenFile opens name with os.O_* flags without following a symbolic link or
// junction there (openat with O_NOFOLLOW). os.O_CREATE|os.O_EXCL is an
// exclusive create (FILE_CREATE, G3). With os.O_CREATE the protection applies
// when the call creates the file.
func (dir *Dir) OpenFile(name string, flag int, protection Protection) (*os.File, error) {
	path := dir.join(name)
	request, err := openRequestFor(flag)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	descriptor, err := createDescriptor(protection, flag)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	var file *os.File
	err = dir.withRoot(name, func(root windows.Handle) error {
		handle, err := ntOpen(root, name, request.access, request.disposition, request.options, descriptor)
		if err != nil {
			return err
		}
		file, err = adoptFile(handle, path, flag, request)
		return err
	})
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return file, nil
}

// Mkdir creates the child directory name (mkdirat).
func (dir *Dir) Mkdir(name string, protection Protection) error {
	path := dir.join(name)
	descriptor, err := protection.descriptor(true)
	if err != nil {
		return &os.PathError{Op: "mkdir", Path: path, Err: err}
	}
	err = dir.withRoot(name, func(root windows.Handle) error {
		handle, err := ntOpen(
			root,
			name,
			windows.FILE_READ_ATTRIBUTES,
			windows.FILE_CREATE,
			windows.FILE_DIRECTORY_FILE,
			descriptor,
		)
		if err != nil {
			return err
		}
		closeHandle(handle)
		return nil
	})
	if err != nil {
		return &os.PathError{Op: "mkdir", Path: path, Err: err}
	}
	return nil
}

// Lstat reports the entry name without following a reparse point there
// (fstatat with AT_SYMLINK_NOFOLLOW). Compare Info.ID with Identity of a held
// file to check that the file is still attached under name.
func (dir *Dir) Lstat(name string) (Info, error) {
	path := dir.join(name)
	var info Info
	err := dir.withRoot(name, func(root windows.Handle) error {
		handle, err := ntOpen(root, name, windows.FILE_READ_ATTRIBUTES, windows.FILE_OPEN, 0, nil)
		if err != nil {
			return err
		}
		defer closeHandle(handle)
		info, err = handleInfo(handle)
		return err
	})
	if err != nil {
		return Info{}, &os.PathError{Op: "lstat", Path: path, Err: err}
	}
	return info, nil
}

func (dir *Dir) join(name string) string {
	return filepath.Join(dir.file.Name(), name)
}

// withRoot validates name and runs use with the directory handle held.
func (dir *Dir) withRoot(name string, use func(root windows.Handle) error) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	return withFileHandle(dir.file, use)
}

// adoptDir finishes a directory open and wraps the handle. The handle is
// consumed.
func adoptDir(handle windows.Handle, path string) (*Dir, error) {
	handle, err := finishDataOpen(handle, true, directoryAccess, windows.FILE_DIRECTORY_FILE)
	if err != nil {
		return nil, err
	}
	return &Dir{file: os.NewFile(uintptr(handle), path)}, nil
}

// adoptFile finishes a file open and wraps the handle. The handle is
// consumed.
func adoptFile(handle windows.Handle, path string, flag int, request openRequest) (*os.File, error) {
	handle, err := finishDataOpen(handle, false, request.access, request.options)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if flag&os.O_TRUNC != 0 {
		// Truncate after the open, as Go's Openat does: FILE_OVERWRITE would
		// replace a read-only file with a new one.
		if err := file.Truncate(0); err != nil {
			_ = file.Close()
			var pathErr *os.PathError
			if errors.As(err, &pathErr) {
				err = pathErr.Err
			}
			return nil, fmt.Errorf("truncate: %w", err)
		}
	}
	return file, nil
}

// openRequest is the NtCreateFile access, disposition and options for a set
// of os.O_* flags.
type openRequest struct {
	access      uint32
	disposition uint32
	options     uint32
}

const supportedOpenFlags = os.O_RDONLY | os.O_WRONLY | os.O_RDWR |
	os.O_CREATE | os.O_EXCL | os.O_TRUNC | os.O_APPEND | os.O_SYNC

func openRequestFor(flag int) (openRequest, error) {
	if unsupported := flag &^ supportedOpenFlags; unsupported != 0 {
		return openRequest{}, fmt.Errorf("winfs: unsupported open flags %#x: %w", unsupported, fs.ErrInvalid)
	}
	// READ_CONTROL lets CheckPrivate read the security descriptor of any handle.
	request := openRequest{
		access:      windows.READ_CONTROL | windows.FILE_READ_ATTRIBUTES | windows.FILE_READ_EA,
		disposition: windows.FILE_OPEN,
	}
	switch flag & (os.O_RDONLY | os.O_WRONLY | os.O_RDWR) {
	case os.O_RDONLY:
		request.access |= windows.FILE_GENERIC_READ
	case os.O_WRONLY:
		request.access |= windows.FILE_GENERIC_WRITE
		request.options |= windows.FILE_NON_DIRECTORY_FILE
	case os.O_RDWR:
		request.access |= windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE
		request.options |= windows.FILE_NON_DIRECTORY_FILE
	default:
		return openRequest{}, fmt.Errorf("winfs: invalid access mode in flags %#x: %w", flag, fs.ErrInvalid)
	}
	if flag&os.O_APPEND != 0 {
		request.access |= windows.FILE_APPEND_DATA
		if flag&os.O_TRUNC == 0 {
			request.access &^= windows.FILE_WRITE_DATA
		}
	}
	switch {
	case flag&(os.O_CREATE|os.O_EXCL) == os.O_CREATE|os.O_EXCL:
		request.disposition = windows.FILE_CREATE
	case flag&os.O_CREATE != 0:
		request.disposition = windows.FILE_OPEN_IF
	}
	if flag&os.O_SYNC == os.O_SYNC {
		request.options |= windows.FILE_WRITE_THROUGH
	}
	return request, nil
}

func (request openRequest) win32Disposition() uint32 {
	switch request.disposition {
	case windows.FILE_CREATE:
		return windows.CREATE_NEW
	case windows.FILE_OPEN_IF:
		return windows.OPEN_ALWAYS
	}
	return windows.OPEN_EXISTING
}

// createDescriptor returns the security descriptor for a file create, or nil
// when the flags cannot create a file.
func createDescriptor(protection Protection, flag int) (*windows.SECURITY_DESCRIPTOR, error) {
	descriptor, err := protection.descriptor(false)
	if err != nil || flag&os.O_CREATE == 0 {
		return nil, err
	}
	return descriptor, nil
}

func securityAttributes(protection Protection, flag int) (*windows.SecurityAttributes, error) {
	descriptor, err := createDescriptor(protection, flag)
	if err != nil || descriptor == nil {
		return nil, err
	}
	attributes := &windows.SecurityAttributes{SecurityDescriptor: descriptor}
	attributes.Length = uint32(unsafe.Sizeof(*attributes))
	return attributes, nil
}
