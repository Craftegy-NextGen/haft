package main

import (
	"errors"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/m0n0x41d/haft/internal/winfs"
)

// createFixtureDirectory is os.MkdirTemp with the owner-only security
// descriptor of winfs.Private (ADR-092 O2): the directory inherits nothing
// from the temporary folder, as a Unix 0700 directory is closed to other
// users. Go's directory mode bits say nothing about access on Windows.
func createFixtureDirectory(dir, pattern string) (string, error) {
	if dir == "" {
		dir = os.TempDir()
	}
	parent, err := winfs.OpenDir(dir)
	if err != nil {
		return "", err
	}
	defer parent.Close()
	prefix, suffix := pattern, ""
	if star := strings.LastIndex(pattern, "*"); star >= 0 {
		prefix, suffix = pattern[:star], pattern[star+1:]
	}
	for range 10000 {
		name := prefix + strconv.FormatUint(uint64(rand.Uint32()), 10) + suffix
		err := parent.Mkdir(name, winfs.Private)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, name), nil
	}
	return "", &os.PathError{Op: "mkdirtemp", Path: filepath.Join(dir, pattern), Err: fs.ErrExist}
}
