package winfs

import (
	"errors"
	"os"
	"sync/atomic"

	"golang.org/x/sys/windows"
)

// LinkOutcome reports how LinkIfAbsent installed the new name.
type LinkOutcome int

const (
	// Linked means the new name is a second hard link; the old name remains.
	Linked LinkOutcome = iota + 1
	// Moved means the volume has no hard links, so the old name was renamed to
	// the new name without replacement; the old name is gone.
	Moved
)

// classicDeletes counts deletes that fell back to delete-on-close.
var classicDeletes atomic.Uint64

// ClassicDeleteFallbacks returns how many Remove calls in this process fell
// back to the classic delete-on-close disposition because the volume or the
// Windows version has no POSIX delete. After such a delete the name stays
// visible, and blocks a new entry of that name, until the last handle closes.
func ClassicDeleteFallbacks() uint64 {
	return classicDeletes.Load()
}

// Remove deletes the entry name: a file, a symbolic link or junction itself,
// or an empty directory (unlinkat). The file's own permissions and read-only
// attribute do not matter when the caller may delete children of dir, as on
// Unix. It uses POSIX semantics, so the name is gone at once even while other
// handles are open; see ClassicDeleteFallbacks for the fallback.
func (dir *Dir) Remove(name string) error {
	err := dir.withRoot(name, func(root windows.Handle) error {
		handle, err := openForDelete(root, name)
		if err != nil {
			return err
		}
		defer closeHandle(handle)
		return deleteHandle(handle)
	})
	if err != nil {
		return &os.PathError{Op: "remove", Path: dir.join(name), Err: err}
	}
	return nil
}

// openForDelete opens name for DELETE and FILE_READ_ATTRIBUTES (to see the
// read-only attribute), retrying with DELETE alone, which the parent's
// FILE_DELETE_CHILD grants even when the file's DACL denies the caller.
func openForDelete(root windows.Handle, name string) (windows.Handle, error) {
	handle, err := ntOpenEntry(root, name, windows.DELETE|windows.FILE_READ_ATTRIBUTES, 0)
	if retryDeleteOnly(err) {
		handle, err = ntOpenEntry(root, name, windows.DELETE, 0)
	}
	return handle, err
}

// retryDeleteOnly selects the DELETE-only second open of Remove.
func retryDeleteOnly(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED)
}

// Rename moves oldName in dir to newName in newDir, replacing an existing
// target (renameat). Both names are single components; a symbolic link at
// oldName is renamed itself.
func (dir *Dir) Rename(oldName string, newDir *Dir, newName string) error {
	return dir.rename(oldName, newDir, newName, true)
}

// RenameNoReplace is Rename that fails with an error satisfying
// errors.Is(err, fs.ErrExist) when newName exists (renameat2 RENAME_NOREPLACE).
func (dir *Dir) RenameNoReplace(oldName string, newDir *Dir, newName string) error {
	return dir.rename(oldName, newDir, newName, false)
}

// LinkIfAbsent installs oldName in dir as newName in newDir only if newName is
// absent (linkat). An existing newName yields an error satisfying
// errors.Is(err, fs.ErrExist). On a volume without hard links it renames
// without replacement instead and reports Moved, which leaves the same file
// under newName.
func (dir *Dir) LinkIfAbsent(oldName string, newDir *Dir, newName string) (LinkOutcome, error) {
	var outcome LinkOutcome
	err := dir.withEntryPair(oldName, newDir, newName, windows.FILE_WRITE_ATTRIBUTES,
		func(source windows.Handle, target windows.Handle) error {
			err := linkHandle(source, target, newName)
			if err == nil {
				outcome = Linked
				return nil
			}
			if !hardLinksUnsupported(err) {
				return ntError(err)
			}
			if err := moveInsteadOfLink(source, target, newName); err != nil {
				return err
			}
			outcome = Moved
			return nil
		})
	if err != nil {
		return 0, dir.linkError("link", oldName, newDir, newName, err)
	}
	return outcome, nil
}

// hardLinksUnsupported selects the LinkIfAbsent fallback: the statuses with
// which a filesystem without hard links rejects FileLinkInformation.
func hardLinksUnsupported(err error) bool {
	return errors.Is(err, windows.STATUS_NOT_SUPPORTED) || errors.Is(err, windows.STATUS_INVALID_DEVICE_REQUEST)
}

// moveInsteadOfLink is the LinkIfAbsent fallback for volumes without hard
// links. It reopens the same file object for DELETE through the open handle
// (no second name lookup) and renames it without replacement.
func moveInsteadOfLink(source windows.Handle, target windows.Handle, name string) error {
	movable, err := ntOpenEntry(source, "", windows.DELETE, windows.FILE_WRITE_THROUGH)
	if err != nil {
		return err
	}
	defer closeHandle(movable)
	return renameHandle(movable, target, name, false)
}

func (dir *Dir) rename(oldName string, newDir *Dir, newName string, replace bool) error {
	err := dir.withEntryPair(oldName, newDir, newName, windows.DELETE,
		func(source windows.Handle, target windows.Handle) error {
			return renameHandle(source, target, newName, replace)
		})
	if err != nil {
		return dir.linkError("rename", oldName, newDir, newName, err)
	}
	return nil
}

// withEntryPair validates both names, holds both directory handles, opens
// oldName for the given access and runs use. The source is opened
// write-through, so the rename or link is committed to the log before it
// returns; callers still sync the directories as on Unix.
func (dir *Dir) withEntryPair(
	oldName string,
	newDir *Dir,
	newName string,
	access uint32,
	use func(source windows.Handle, target windows.Handle) error,
) error {
	if err := ValidateName(newName); err != nil {
		return err
	}
	return dir.withRoot(oldName, func(root windows.Handle) error {
		return withFileHandle(newDir.file, func(target windows.Handle) error {
			source, err := ntOpenEntry(root, oldName, access, windows.FILE_WRITE_THROUGH)
			if err != nil {
				return err
			}
			defer closeHandle(source)
			return use(source, target)
		})
	})
}

func (dir *Dir) linkError(op string, oldName string, newDir *Dir, newName string, err error) error {
	return &os.LinkError{Op: op, Old: dir.join(oldName), New: newDir.join(newName), Err: err}
}

// deleteHandle marks an open handle for deletion. It tries POSIX semantics
// with FILE_DISPOSITION_IGNORE_READONLY_ATTRIBUTE (Windows 10 1809 and
// later), then POSIX semantics after clearing the read-only attribute itself
// (1607 to 1803), then the classic delete-on-close disposition (older
// systems, filesystems without POSIX delete).
func deleteHandle(handle windows.Handle) error {
	err := deletePOSIX(handle, true)
	if !unsupportedInformationClass(err) {
		return ntError(err)
	}
	if err := clearReadOnly(handle); err != nil {
		return err
	}
	err = deletePOSIX(handle, false)
	if !unsupportedInformationClass(err) {
		return ntError(err)
	}
	return deleteHandleClassic(handle)
}

// deletePOSIX sets FileDispositionInformationEx and returns the raw NTSTATUS.
func deletePOSIX(handle windows.Handle, ignoreReadOnly bool) error {
	flags := uint32(windows.FILE_DISPOSITION_DELETE |
		windows.FILE_DISPOSITION_FORCE_IMAGE_SECTION_CHECK |
		windows.FILE_DISPOSITION_POSIX_SEMANTICS)
	if ignoreReadOnly {
		flags |= windows.FILE_DISPOSITION_IGNORE_READONLY_ATTRIBUTE
	}
	info := fileDispositionInformationEx{Flags: flags}
	return setInformation(handle, &info, classFileDispositionInformationEx)
}

// deleteHandleClassic deletes on last close: the name stays visible, and
// blocks a new file of the same name, while other handles are open. Like Go's
// deleteatFallback it first clears the read-only attribute.
func deleteHandleClassic(handle windows.Handle) error {
	classicDeletes.Add(1)
	if err := clearReadOnly(handle); err != nil {
		return err
	}
	info := fileDispositionInformation{DeleteFile: true}
	return ntError(setInformation(handle, &info, classFileDispositionInformation))
}

// clearReadOnly removes FILE_ATTRIBUTE_READONLY, which blocks a delete that
// cannot ignore it; unlink(2) ignores the file's own mode. It reopens the file
// through the handle (no name lookup) with FILE_WRITE_ATTRIBUTES. When the
// handle cannot read attributes (the DELETE-only open), it leaves the file
// alone and the delete reports the outcome.
func clearReadOnly(handle windows.Handle) error {
	info, err := attributeTag(handle)
	if err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_READONLY == 0 {
		return nil
	}
	writable, err := ntOpenEntry(handle, "", windows.FILE_WRITE_ATTRIBUTES, 0)
	if err != nil {
		return err
	}
	defer closeHandle(writable)
	attributes := info.FileAttributes &^ windows.FILE_ATTRIBUTE_READONLY
	if attributes == 0 {
		// Zero means "leave unchanged" in FILE_BASIC_INFORMATION.
		attributes = windows.FILE_ATTRIBUTE_NORMAL
	}
	basic := fileBasicInformation{FileAttributes: attributes}
	return ntError(setInformation(writable, &basic, classFileBasicInformation))
}

// renameHandle renames an open handle to name relative to root, with POSIX
// semantics where supported (an open target is replaced at once) and the
// classic class as fallback.
func renameHandle(source windows.Handle, root windows.Handle, name string, replace bool) error {
	encoded, err := encodeFileName(name)
	if err != nil {
		return err
	}
	flags := uint32(windows.FILE_RENAME_POSIX_SEMANTICS)
	if replace {
		flags |= windows.FILE_RENAME_REPLACE_IF_EXISTS
	}
	extended := fileRenameInformationEx{
		Flags:          flags,
		RootDirectory:  root,
		FileNameLength: encoded.length,
		FileName:       encoded.units,
	}
	err = setInformation(source, &extended, classFileRenameInformationEx)
	if !unsupportedInformationClass(err) {
		return ntError(err)
	}
	classic := fileRenameInformation{
		ReplaceIfExists: replace,
		RootDirectory:   root,
		FileNameLength:  encoded.length,
		FileName:        encoded.units,
	}
	return ntError(setInformation(source, &classic, classFileRenameInformation))
}

// linkHandle adds name relative to root as a hard link to source, never
// replacing an existing entry. It returns the raw NTSTATUS.
func linkHandle(source windows.Handle, root windows.Handle, name string) error {
	encoded, err := encodeFileName(name)
	if err != nil {
		return err
	}
	info := fileLinkInformation{
		ReplaceIfExists: false,
		RootDirectory:   root,
		FileNameLength:  encoded.length,
		FileName:        encoded.units,
	}
	return setInformation(source, &info, classFileLinkInformation)
}

// RenameNoReplace renames oldPath to newPath with MoveFileEx, without
// MOVEFILE_REPLACE_EXISTING and without MOVEFILE_COPY_ALLOWED (a cross-volume
// move fails, it never copies), and with MOVEFILE_WRITE_THROUGH. An existing
// newPath yields an error satisfying errors.Is(err, fs.ErrExist).
func RenameNoReplace(oldPath string, newPath string) error {
	from, err := windows.UTF16PtrFromString(extendedPath(oldPath))
	if err != nil {
		return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: err}
	}
	to, err := windows.UTF16PtrFromString(extendedPath(newPath))
	if err != nil {
		return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: err}
	}
	if err := windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: err}
	}
	return nil
}
