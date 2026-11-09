//go:build !windows

package projectledgermigration

// reserveSnapshotPartialPath checks that the partial snapshot path is free
// before VACUUM INTO creates it. Outside Windows it is exactly
// requireSnapshotPathAbsent: SQLite creates the file, and the later
// os.Chmod(path, 0o600) makes it private.
func reserveSnapshotPartialPath(path string) error {
	return requireSnapshotPathAbsent(path)
}
