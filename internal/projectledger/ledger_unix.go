//go:build darwin || linux

package projectledger

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func openAbsoluteDirectoryAnchor(path string, name string) (topologyAnchor, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return topologyAnchor{}, fmt.Errorf("open no-follow %s: %w", name, err)
	}
	return anchorOpenedFile(fd, path, name, true)
}

func openChildDirectoryAnchor(
	parent topologyAnchor,
	entry string,
	path string,
	name string,
) (topologyAnchor, error) {
	fd, err := unix.Openat(int(parent.file.Fd()), entry, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return topologyAnchor{}, fmt.Errorf("open anchored no-follow %s: %w", name, err)
	}
	return anchorOpenedFile(fd, path, name, true)
}

func openChildRegularFileAnchor(
	parent topologyAnchor,
	entry string,
	path string,
	name string,
) (topologyAnchor, error) {
	fd, err := unix.Openat(int(parent.file.Fd()), entry, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		if err == unix.ELOOP {
			return topologyAnchor{}, fmt.Errorf("anchored %s must be a real regular file, not a symlink", name)
		}
		return topologyAnchor{}, fmt.Errorf("open anchored no-follow %s: %w", name, err)
	}
	return anchorOpenedFile(fd, path, name, false)
}