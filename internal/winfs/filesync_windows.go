package winfs

import (
	"os"

	"golang.org/x/sys/windows"
)

// OpenFileForSync opens the absolute regular-file path so that Sync on the
// returned file flushes it (FlushFileBuffers). It is the file counterpart of
// OpenDirectoryForSync, for shared code that opens a file with os.Open only to
// call Sync and close it: Windows refuses FlushFileBuffers on the read-only
// handle that os.Open returns (ERROR_ACCESS_DENIED), while fsync(2) accepts a
// read-only descriptor. The handle has FILE_WRITE_DATA access and the share
// mode of every winfs handle (read, write, delete); like os.Open it follows a
// reparse point at path. Write access means a file with the read-only
// attribute, or one the caller may not write, cannot be opened for sync; every
// open and flush error reaches the caller.
func OpenFileForSync(path string) (*os.File, error) {
	if err := requireAbsolute(path); err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	handle, err := createFile(
		path,
		windows.FILE_WRITE_DATA|windows.SYNCHRONIZE,
		shareAll,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		nil,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}
