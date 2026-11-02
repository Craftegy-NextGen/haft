//go:build windows

package projectledger

import (
	"fmt"
	"os"
	"path/filepath"
)

// Windows has no equivalent of Unix's O_NOFOLLOW/openat(2) pair via the
// standard os package, so these anchors cannot get the same TOCTOU-proof
// "open exactly this inode, never a symlink, atomically" guarantee the
// Unix implementation gets from unix.Open/unix.Openat with O_NOFOLLOW.
// Instead: Lstat first to reject symlinks before opening, then open
// normally and rely on verifyAnchoredTopology's post-open Lstat +
// os.SameFile check (already present in ledger.go) to catch any swap
// that happened in between. This narrows, but does not eliminate, the
// race window on Windows.

func openAbsoluteDirectoryAnchor(path string, name string) (topologyAnchor, error) {
	return openNoFollowAnchor(path, name)
}

func openChildDirectoryAnchor(
	parent topologyAnchor,
	entry string,
	path string,
	name string,
) (topologyAnchor, error) {
	_ = entry
	_ = parent
	return openNoFollowAnchor(path, name)
}

func openChildRegularFileAnchor(
	parent topologyAnchor,
	entry string,
	path string,
	name string,
) (topologyAnchor, error) {
	_ = entry
	_ = parent
	return openNoFollowAnchor(path, name)
}

func openNoFollowAnchor(path string, name string) (topologyAnchor, error) {
	cleaned := filepath.Clean(path)
	lstatInfo, err := os.Lstat(cleaned)
	if err != nil {
		return topologyAnchor{}, fmt.Errorf("lstat no-follow %s: %w", name, err)
	}
	if lstatInfo.Mode()&os.ModeSymlink != 0 {
		return topologyAnchor{}, fmt.Errorf("no-follow %s rejects symlink: %s", name, cleaned)
	}
	file, err := os.Open(cleaned)
	if err != nil {
		return topologyAnchor{}, fmt.Errorf("open no-follow %s: %w", name, err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return topologyAnchor{}, fmt.Errorf("inspect anchored %s: %w", name, err)
	}
	valid := info.Mode().IsRegular()
	if !valid {
		_ = file.Close()
		return topologyAnchor{}, fmt.Errorf("anchored %s must be a real file, not a symlink", name)
	}
	return topologyAnchor{file: file, info: info, path: cleaned, name: name}, nil
}