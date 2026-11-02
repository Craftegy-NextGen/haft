//go:build windows

package projectledger

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"github.com/m0n0x41d/haft/internal/winfs"
)

// The Windows counterparts of the three anchor opens in ledger_unix.go
// (ADR-091 R-a, ADR-092 O1 option 1a). They give the same guarantees through
// internal/winfs:
//
//   - unix.Open(path, O_RDONLY|O_DIRECTORY|O_NOFOLLOW): winfs.OpenDir. Like
//     open(2), intermediate components resolve; a symbolic link or junction at
//     the final component fails with ELOOP and a non-directory with ENOTDIR
//     (G1).
//   - unix.Openat(parentFD, entry, O_RDONLY|O_DIRECTORY|O_NOFOLLOW): Dir.OpenDir
//     relative to the parent anchor's own handle (winfs.DirFromFile on the
//     anchor's *os.File), never by path, so a swapped path component cannot
//     redirect the open (G1, G2).
//   - unix.Openat(parentFD, entry, O_RDONLY|O_NOFOLLOW) and its ELOOP check:
//     Dir.OpenFile with os.O_RDONLY relative to the parent handle; winfs
//     reports a symbolic link or junction as syscall.ELOOP, so the same
//     errors.Is check selects the same message.
//   - anchorOpenedFile(fd, ...) in ledger.go adopts a Unix descriptor; winfs
//     already returns the *os.File that owns the handle, so anchorOpenedHandle
//     applies the same checks and messages to it instead of adopting the
//     handle a second time. It also reads the FILE_ID_INFO identity once, so a
//     volume that cannot report it fails closed (ADR-092 O5).
//
// The open errors are unwrapped from winfs's *os.PathError to the bare errno,
// as unix.Open and unix.Openat return them, so the messages keep the Unix
// shape ("open no-follow <name>: <errno text>"); errors.Is still matches
// fs.ErrNotExist and the other errno classes.
//
// Anchor identity checks stay in the shared ledger.go, unchanged (operator
// decision, WS10): verifyAnchoredTopology and verifySQLiteMainDatabaseIdentity
// compare the anchor's file.Stat() with os.Lstat(path) through os.SameFile.
// On Windows the identity is therefore the volume serial plus the 64-bit file
// index of BY_HANDLE_FILE_INFORMATION: the full identity on NTFS, and
// Weaker-documented on ReFS, whose FILE_ID_INFO identity has 128 bits
// (ADR-092 O5; unmeasured, no ReFS volume was available).
//
// NTFS refuses to rename a directory while an entry below it is open, whatever
// the share mode (measured: ERROR_ACCESS_DENIED). While a ledger handle holds
// its anchors, no anchored directory can therefore be swapped; only the
// anchored files can be renamed (the identity carrier always, the database
// before SQLite connects, as SQLite opens it without FILE_SHARE_DELETE), and
// such a swap is detected by the shared identity checks.

func openAbsoluteDirectoryAnchor(path string, name string) (topologyAnchor, error) {
	directory, err := winfs.OpenDir(path)
	if err != nil {
		return topologyAnchor{}, fmt.Errorf("open no-follow %s: %w", name, bareErrno(err))
	}
	return anchorOpenedHandle(directory.File(), path, name, true)
}

func openChildDirectoryAnchor(
	parent topologyAnchor,
	entry string,
	path string,
	name string,
) (topologyAnchor, error) {
	held, err := heldParentDirectory(parent)
	if err != nil {
		return topologyAnchor{}, fmt.Errorf("open anchored no-follow %s: %w", name, err)
	}
	directory, err := held.OpenDir(entry)
	if err != nil {
		return topologyAnchor{}, fmt.Errorf("open anchored no-follow %s: %w", name, bareErrno(err))
	}
	return anchorOpenedHandle(directory.File(), path, name, true)
}

func openChildRegularFileAnchor(
	parent topologyAnchor,
	entry string,
	path string,
	name string,
) (topologyAnchor, error) {
	held, err := heldParentDirectory(parent)
	if err != nil {
		return topologyAnchor{}, fmt.Errorf("open anchored no-follow %s: %w", name, err)
	}
	file, err := held.OpenFile(entry, os.O_RDONLY, winfs.Inherited)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return topologyAnchor{}, fmt.Errorf("anchored %s must be a real regular file, not a symlink", name)
		}
		return topologyAnchor{}, fmt.Errorf("open anchored no-follow %s: %w", name, bareErrno(err))
	}
	return anchorOpenedHandle(file, path, name, false)
}

// heldParentDirectory returns the parent anchor's directory handle as a
// winfs.Dir. The Dir shares the anchor's handle; the anchor stays its only
// owner, so the Dir is never closed here.
func heldParentDirectory(parent topologyAnchor) (*winfs.Dir, error) {
	if parent.file == nil {
		return nil, fmt.Errorf("parent anchor %s is unavailable", parent.name)
	}
	return winfs.DirFromFile(parent.file)
}

// anchorOpenedHandle is anchorOpenedFile for a file that winfs opened: the
// same kind check and messages, plus the fail-closed FILE_ID_INFO read. It
// consumes file: on failure it closes it.
func anchorOpenedHandle(file *os.File, path string, name string, directory bool) (topologyAnchor, error) {
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return topologyAnchor{}, fmt.Errorf("inspect anchored %s: %w", name, err)
	}
	valid := info.Mode().IsRegular()
	if directory {
		valid = info.IsDir()
	}
	if !valid {
		_ = file.Close()
		kind := "regular file"
		if directory {
			kind = "directory"
		}
		return topologyAnchor{}, fmt.Errorf("anchored %s must be a real %s, not a symlink", name, kind)
	}
	if _, err := winfs.Identity(file); err != nil {
		_ = file.Close()
		return topologyAnchor{}, fmt.Errorf("inspect anchored %s identity: %w", name, err)
	}
	return topologyAnchor{file: file, info: info, path: path, name: name}, nil
}

// bareErrno returns the error inside winfs's *os.PathError, the form in which
// unix.Open and unix.Openat report a failure.
func bareErrno(err error) error {
	var pathError *os.PathError
	if errors.As(err, &pathError) {
		return pathError.Err
	}
	return err
}
