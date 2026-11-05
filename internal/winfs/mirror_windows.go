package winfs

// Primitives for the Windows twins of Unix descriptor code (ADR-091 R-c, the
// projectledger anchors): adopting a held directory kept as an *os.File, a
// sync-capable reopen of a held directory, and a path-based lstat(2).

import (
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

// Lstat reports the entry at the absolute path without following a symbolic
// link or junction at its final component, like lstat(2); intermediate
// components resolve as usual. Compare Info.ID with the Identity of a held
// file to check that the path still names it.
func Lstat(path string) (Info, error) {
	if err := requireAbsolute(path); err != nil {
		return Info{}, &os.PathError{Op: "lstat", Path: path, Err: err}
	}
	handle, err := createFile(
		path,
		windows.FILE_READ_ATTRIBUTES,
		shareAll,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		nil,
	)
	if err != nil {
		return Info{}, &os.PathError{Op: "lstat", Path: path, Err: err}
	}
	defer closeHandle(handle)
	info, err := handleInfo(handle)
	if err != nil {
		return Info{}, &os.PathError{Op: "lstat", Path: path, Err: err}
	}
	return info, nil
}

// DirFromFile returns a Dir for file, an open directory, so that a caller that
// keeps a held directory as an *os.File (a struct shared with Unix code that
// stores the descriptor of openat(2)) can run handle-relative operations on
// it. The Dir and file share one handle: closing either closes it, so only the
// owner of file closes it. The handle must be a directory that is not a
// symbolic link or junction (ENOTDIR, ELOOP); it needs the directory access of
// a Dir, which every directory handle that winfs opens has.
func DirFromFile(file *os.File) (*Dir, error) {
	info, err := Fstat(file)
	if err != nil {
		return nil, err
	}
	switch {
	case info.IsNameSurrogate():
		return nil, &os.PathError{Op: "open", Path: file.Name(), Err: syscall.ELOOP}
	case !info.IsDir():
		return nil, &os.PathError{Op: "open", Path: file.Name(), Err: syscall.ENOTDIR}
	}
	return &Dir{file: file}, nil
}

// OpenForSync opens the held directory again through its own handle, without
// a name lookup, with the write access that FlushFileBuffers needs (probe
// P-c), so Sync on the returned file is fsync on the held directory even after
// its path was renamed or replaced. It is the handle-relative counterpart of
// OpenDirectoryForSync for code that passes a directory *os.File to a Sync
// call: every open and flush error reaches the caller, with no SyncSkipped
// fallback. The returned file is independent of dir; close it.
func (dir *Dir) OpenForSync() (*os.File, error) {
	var file *os.File
	err := withFileHandle(dir.file, func(root windows.Handle) error {
		handle, err := ntOpen(root, "", windows.FILE_WRITE_DATA, windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE, nil)
		if err != nil {
			return err
		}
		file = os.NewFile(uintptr(handle), dir.file.Name())
		return nil
	})
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: dir.Name(), Err: err}
	}
	return file, nil
}
