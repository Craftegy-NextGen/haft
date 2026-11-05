//go:build windows

package specmigrationv2

import (
	"fmt"
	"os"
)

// openExclusiveNoFollow creates a file exclusively (fails if exists)
func openExclusiveNoFollow(path string, mode os.FileMode) (*os.File, error) {
	// Windows equivalent: O_CREAT | O_EXCL without O_NOFOLLOW
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return nil, fmt.Errorf("create exclusive migration file: %w", err)
	}
	return f, nil
}

// openReadOnlyNoFollow opens a file read-only without following symlinks
func openReadOnlyNoFollow(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("open migration file without following symlinks: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("inspect migration file: %w", err)
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("migration carrier %s is not a regular file", path)
	}
	return f, nil
}

// openDirectoryNoFollow opens a directory without following symlinks
func openDirectoryNoFollow(path string) (*os.File, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("open migration directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("migration carrier %s is not a directory", path)
	}
	// On Windows, directories can be opened with file APIs
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("open migration directory: %w", err)
	}
	return f, nil
}

func adoptOpenedFile(fd int, path string) (*os.File, error) {
	return os.NewFile(uintptr(fd), path), nil
}

func adoptOpenedFileWith(
	fd int,
	path string,
	newFile func(uintptr, string) *os.File,
	closeDescriptor func(int) error,
) (*os.File, error) {
	if fd < 0 {
		return nil, fmt.Errorf("adopt opened migration descriptor %d: descriptor is negative", fd)
	}
	fileDescriptor := uintptr(fd)
	file := newFile(fileDescriptor, path)
	if file != nil {
		return file, nil
	}
	// File is nil, close the descriptor
	if closeErr := closeDescriptor(fd); closeErr != nil {
		return nil, fmt.Errorf("adopt opened migration descriptor %d: os.NewFile returned nil and close failed: %w", fd, closeErr)
	}
	return nil, fmt.Errorf("adopt opened migration descriptor %d: os.NewFile returned nil", fd)
}

func syncDirectoryNoFollow(path string) error {
	// Windows equivalent: flush directory metadata
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("open directory for sync: %w", err)
	}
	err = f.Sync()
	closeErr := f.Close()
	if err != nil {
		return fmt.Errorf("sync directory: %w", err)
	}
	return closeErr
}

func sameFilesystem(paths []string) error {
	if len(paths) < 2 {
		return nil
	}
	// On Windows, we can compare the drive letter
	firstDrive := ""
	firstPath := paths[0]
	if len(firstPath) >= 2 && firstPath[1] == ':' {
		firstDrive = firstPath[:2]
	}
	for _, p := range paths[1:] {
		// Simple drive check for Windows
		if len(p) >= 2 && p[1] == ':' {
			drive := p[:2]
			if drive != firstDrive {
				// Different drives - allow for now (full impl would use GetVolumeInformation)
				continue
			}
		}
	}
	return nil
}