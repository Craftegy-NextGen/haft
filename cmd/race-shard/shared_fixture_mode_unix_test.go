//go:build !windows

package main

import "os"

// sharedFixtureRootNotPrivate is the Unix form of the privacy assertion in
// TestRaceSharedFixtureDirectoryIsPrivateAndRunnerOwned (ADR-093 E3, extracted
// verbatim): a private directory grants nothing to group or others. Its
// Windows sibling checks the owner and protected DACL instead (ADR-092 O2).
func sharedFixtureRootNotPrivate(info os.FileInfo, root string) bool {
	return info.Mode().Perm()&0o077 != 0
}
