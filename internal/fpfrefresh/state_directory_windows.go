//go:build windows

package fpfrefresh

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/m0n0x41d/haft/internal/winfs"
)

// stateDirectoryNotPrivate is the Windows meaning of the Unix predicate
// `info.Mode().Perm()&0o077 != 0` in acquireRepositoryVerificationOperationLock
// (ADR-092 fact 3 and O2; ADR-091 R-b).
//
// On Windows, Go derives the permission bits of an os.FileInfo from the
// read-only attribute alone: 0o555 when it is set, 0o777 otherwise. The
// state directory is created by createStateDirectory with an owner-only
// descriptor, which Go still reports as 0o777, so the predicate rejects
// exactly what cannot be the image of 0o700: a read-only directory.
//
// The call site holds no handle (os.Lstat), so winfs.CheckPrivate cannot run
// here. The owner and DACL are checked where the directory is created, in
// createStateDirectory. The lock files that guard the refresh are private by
// construction (apply_lock_windows.go and receipt_lock_windows.go).
func stateDirectoryNotPrivate(info os.FileInfo) bool {
	return info.Mode().Perm()&0o200 == 0
}

// createStateDirectory is the Windows meaning of os.MkdirAll(path, 0o700)
// (ADR-092 O2): the missing parents are created as os.MkdirAll creates them
// (they take the security of their own parent), and the state directory
// itself is created with winfs.Private, an explicit owner and a protected
// DACL that allows only the token user, SYSTEM and Administrators. A directory
// that already exists is accepted unchanged, as os.MkdirAll accepts it; one
// this call creates is verified with winfs.CheckPrivate before it is used.
func createStateDirectory(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return &os.PathError{Op: "mkdir", Path: path, Err: err}
	}
	parent := filepath.Dir(absolute)
	if parent != absolute {
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return err
		}
	}
	directory, err := winfs.OpenDir(parent)
	if err != nil {
		return err
	}
	defer func() {
		_ = directory.Close()
	}()
	name := filepath.Base(absolute)
	err = directory.Mkdir(name, winfs.Private)
	if errors.Is(err, fs.ErrExist) {
		info, statErr := os.Stat(absolute)
		if statErr == nil && info.IsDir() {
			return nil
		}
		return &os.PathError{Op: "mkdir", Path: path, Err: syscall.ENOTDIR}
	}
	if err != nil {
		return err
	}
	created, err := directory.OpenDir(name)
	if err != nil {
		return err
	}
	defer func() {
		_ = created.Close()
	}()
	if err := winfs.CheckPrivate(created.File()); err != nil {
		return fmt.Errorf("check private FPF refresh state directory: %w", err)
	}
	return nil
}
