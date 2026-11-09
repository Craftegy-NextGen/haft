//go:build !windows

package main

import "os"

// createFixtureDirectory makes the runner-owned temporary directory. On Unix
// os.MkdirTemp already creates it with mode 0700; the Windows sibling gives it
// the owner-only security descriptor that mode stands for.
func createFixtureDirectory(dir, pattern string) (string, error) {
	return os.MkdirTemp(dir, pattern)
}
