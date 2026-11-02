package winfs

import (
	"errors"
	"os"
	"sync/atomic"

	"golang.org/x/sys/windows"
)

// SyncOutcome reports how a directory sync was carried out.
type SyncOutcome int

const (
	// SyncFlushed means FlushFileBuffers succeeded on the directory.
	SyncFlushed SyncOutcome = iota + 1
	// SyncSkipped is the documented fallback of ADR-092 O3: the directory could
	// not be opened for write or flushed, with ERROR_ACCESS_DENIED or
	// ERROR_INVALID_FUNCTION, so nothing was flushed. NTFS still journals the
	// entry, but the newest create or rename can roll back on power loss.
	SyncSkipped
)

// skippedDirectorySyncs counts SyncSkipped results in this process.
var skippedDirectorySyncs atomic.Uint64

// SkippedDirectorySyncs returns how many directory syncs in this process took
// the SyncSkipped fallback. It makes the weaker path observable to callers,
// diagnostics and tests.
func SkippedDirectorySyncs() uint64 {
	return skippedDirectorySyncs.Load()
}

// SyncDirectory is fsync on a directory path (G7). It follows reparse points
// at path like open(2) without O_NOFOLLOW, opens the directory for write with
// FILE_FLAG_BACKUP_SEMANTICS and calls FlushFileBuffers. Only
// ERROR_ACCESS_DENIED and ERROR_INVALID_FUNCTION return SyncSkipped with a nil
// error; every other failure is returned.
func SyncDirectory(path string) (SyncOutcome, error) {
	open := func() (windows.Handle, error) {
		return createFile(
			path,
			windows.FILE_WRITE_DATA|windows.SYNCHRONIZE,
			shareAll,
			windows.OPEN_EXISTING,
			windows.FILE_FLAG_BACKUP_SEMANTICS,
			nil,
		)
	}
	outcome, err := syncDirectory(open)
	if err != nil {
		return 0, &os.PathError{Op: "sync", Path: path, Err: err}
	}
	return outcome, nil
}

// OpenDirectoryForSync opens the absolute directory path so that Sync on the
// returned file flushes it (FlushFileBuffers, probe P-c). It is the Windows
// counterpart of os.Open for shared code that opens a directory, calls Sync
// and closes it: os.Open returns a read-only handle, whose flush fails with
// ERROR_ACCESS_DENIED. The handle has FILE_WRITE_DATA access,
// FILE_FLAG_BACKUP_SEMANTICS and the share mode of every winfs handle (read,
// write, delete); like os.Open it follows a reparse point at path.
//
// Unlike SyncDirectory there is no SyncSkipped fallback: every open and flush
// error reaches the caller, as on Unix.
func OpenDirectoryForSync(path string) (*os.File, error) {
	if err := requireAbsolute(path); err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	handle, err := createFile(
		path,
		windows.FILE_WRITE_DATA|windows.SYNCHRONIZE,
		shareAll,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		nil,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}

// Sync is fsync on the held directory. It reopens the directory through its
// own handle (no name lookup) with write access, then behaves as
// SyncDirectory.
func (dir *Dir) Sync() (SyncOutcome, error) {
	var outcome SyncOutcome
	err := withFileHandle(dir.file, func(root windows.Handle) error {
		open := func() (windows.Handle, error) {
			return ntOpen(root, "", windows.FILE_WRITE_DATA, windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE, nil)
		}
		var err error
		outcome, err = syncDirectory(open)
		return err
	})
	if err != nil {
		return 0, &os.PathError{Op: "sync", Path: dir.Name(), Err: err}
	}
	return outcome, nil
}

func syncDirectory(open func() (windows.Handle, error)) (SyncOutcome, error) {
	handle, err := open()
	if err != nil {
		return syncResult(err)
	}
	defer closeHandle(handle)
	return syncResult(windows.FlushFileBuffers(handle))
}

// syncResult applies the documented fallback: only ERROR_ACCESS_DENIED and
// ERROR_INVALID_FUNCTION become SyncSkipped.
func syncResult(err error) (SyncOutcome, error) {
	if err == nil {
		return SyncFlushed, nil
	}
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_INVALID_FUNCTION) {
		skippedDirectorySyncs.Add(1)
		return SyncSkipped, nil
	}
	return 0, err
}
